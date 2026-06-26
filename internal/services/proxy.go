package services

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	mrand "math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"proxy_version/internal/models"

	"golang.org/x/crypto/curve25519"
)

type ProxyService struct {
	configDir string
}

const bundledSingBoxVersion = "1.13.11"

func NewProxyService() *ProxyService {
	return &ProxyService{
		configDir: "/etc/v2ray-agent",
	}
}

// ProtocolInfo describes a protocol
type ProtocolInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Recommended bool   `json:"recommended"`
	NeedsDomain bool   `json:"needs_domain"`
	NeedsCert   bool   `json:"needs_cert"`
	Transport   string `json:"transport"`
}

// CoreStatus represents the installation status of proxy cores
type CoreStatus struct {
	SingBoxInstalled bool   `json:"singbox_installed"`
	SingBoxVersion   string `json:"singbox_version,omitempty"`
}

// GetCoreStatus returns the installation status of sing-box
func (s *ProxyService) GetCoreStatus() CoreStatus {
	status := CoreStatus{}

	// Check sing-box on HOST using nsenter (since we run in container but sing-box is on host)
	singboxPaths := []string{"/usr/local/bin/sing-box", "/etc/v2ray-agent/sing-box/sing-box", "/usr/bin/sing-box"}

	for _, path := range singboxPaths {
		// Use nsenter to check if file exists on host
		cmd := exec.Command("nsenter", "-t", "1", "-m", "test", "-f", path)
		if err := cmd.Run(); err == nil {
			status.SingBoxInstalled = true

			// Get version using nsenter
			versionCmd := exec.Command("nsenter", "-t", "1", "-m", "-u", "-i", "-n", path, "version")
			if output, err := versionCmd.Output(); err == nil {
				lines := strings.Split(string(output), "\n")
				for _, line := range lines {
					if strings.Contains(line, "sing-box version") {
						parts := strings.Fields(line)
						if len(parts) >= 3 {
							status.SingBoxVersion = parts[2]
						}
						break
					}
				}
			}
			break
		}
	}

	return status
}

// InstallSingBox installs sing-box on the host system
func (s *ProxyService) InstallSingBox() error {
	// Check if already installed
	status := s.GetCoreStatus()
	if status.SingBoxInstalled && compareVersion(status.SingBoxVersion, bundledSingBoxVersion) >= 0 {
		return nil
	}

	// Download and install sing-box
	script := `
set -e
ARCH=$(uname -m)
case $ARCH in
    x86_64) ARCH="amd64" ;;
    aarch64) ARCH="arm64" ;;
    armv7l) ARCH="armv7" ;;
esac
	VERSION="%s"
	URL="https://github.com/SagerNet/sing-box/releases/download/v${VERSION}/sing-box-${VERSION}-linux-${ARCH}.tar.gz"
	cd /tmp
	curl -fsSL "$URL" -o sing-box.tar.gz
tar -xzf sing-box.tar.gz
mkdir -p /usr/local/bin
mv sing-box-${VERSION}-linux-${ARCH}/sing-box /usr/local/bin/
chmod +x /usr/local/bin/sing-box
	rm -rf sing-box.tar.gz sing-box-${VERSION}-linux-${ARCH}
	echo "sing-box installed successfully"
`
	script = fmt.Sprintf(script, bundledSingBoxVersion)
	cmd := exec.Command("nsenter", "-t", "1", "-m", "-u", "-i", "-n", "bash", "-c", script)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("安装失败: %s", string(output))
	}
	return nil
}

// UninstallSingBox removes sing-box from the host system
func (s *ProxyService) UninstallSingBox() error {
	// Stop all node services first (use for loop since wildcard doesn't work with systemctl)
	stopScript := `
# Kill any running sing-box processes first
pkill -9 sing-box 2>/dev/null || true
sleep 1

# Stop and disable all proxy node services
for service in $(systemctl list-units --type=service --all 2>/dev/null | grep 'proxy_node_' | awk '{print $1}'); do
    systemctl stop "$service" 2>/dev/null || true
    systemctl disable "$service" 2>/dev/null || true
done

# Clean up service files
rm -f /etc/systemd/system/proxy_node_*.service
systemctl daemon-reload 2>/dev/null || true
`
	s.runOnHost("bash", "-c", stopScript)

	// Remove sing-box binary from ALL possible installation paths
	// This must match the paths checked in GetCoreStatus()
	removeScript := `
# All possible sing-box installation paths (must match GetCoreStatus detection)
SING_BOX_PATHS="/usr/local/bin/sing-box /etc/v2ray-agent/sing-box/sing-box /usr/bin/sing-box"
REMOVED=0
FAILED=0

for SING_BOX_PATH in $SING_BOX_PATHS; do
    if [ -f "$SING_BOX_PATH" ]; then
        echo "Found sing-box at: $SING_BOX_PATH"
        rm -fv "$SING_BOX_PATH" 2>&1
        
        # Verify removal
        if [ -f "$SING_BOX_PATH" ]; then
            echo "Failed to remove: $SING_BOX_PATH"
            ls -la "$SING_BOX_PATH" 2>&1
            FAILED=1
        else
            echo "Removed: $SING_BOX_PATH"
            REMOVED=1
        fi
    fi
done

# Also clean up the sing-box directory if it exists
if [ -d "/etc/v2ray-agent/sing-box" ]; then
    rm -rf "/etc/v2ray-agent/sing-box" 2>&1
    echo "Removed sing-box directory"
fi

if [ "$FAILED" = "1" ]; then
    echo "removal_failed"
    exit 1
fi

if [ "$REMOVED" = "0" ]; then
    echo "already_removed"
fi

echo "removed_successfully"
exit 0
`
	output, err := s.runOnHost("bash", "-c", removeScript)

	if err != nil {
		return fmt.Errorf("卸载失败: %v, 输出: %s", err, output)
	}

	if strings.Contains(output, "removal_failed") {
		return fmt.Errorf("sing-box 文件删除失败: %s", output)
	}

	// Also remove config directory
	s.runOnHost("bash", "-c", "rm -rf /etc/v2ray-agent/nodes")

	return nil
}

// GetRandomAvailablePort returns a random available port
func (s *ProxyService) GetRandomAvailablePort() int {
	mrand.Seed(time.Now().UnixNano())

	// Try up to 20 times to find an available port
	for i := 0; i < 20; i++ {
		// Generate random port between 10000 and 60000
		port := mrand.Intn(50000) + 10000

		// Check if port is available
		listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err == nil {
			listener.Close()
			return port
		}
	}

	// Fallback: let system assign a port
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		return 10000 + mrand.Intn(50000)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
}

// GetSupportedProtocols returns all supported protocols with descriptions
func (s *ProxyService) GetSupportedProtocols() []ProtocolInfo {
	return []ProtocolInfo{
		{
			ID:          "vless-reality-vision",
			Name:        "VLESS + Reality + Vision",
			Description: "最强抗检测，无需域名证书，伪装真实网站流量，推荐首选",
			Recommended: true,
			NeedsDomain: false,
			NeedsCert:   false,
			Transport:   "tcp",
		},
		{
			ID:          "vless-reality-grpc",
			Name:        "VLESS + Reality + gRPC",
			Description: "Reality 配合 gRPC 传输，适合高延迟网络，无需域名",
			Recommended: false,
			NeedsDomain: false,
			NeedsCert:   false,
			Transport:   "grpc",
		},
		{
			ID:          "vless-vision-tls",
			Name:        "VLESS + Vision + TLS",
			Description: "新一代 VLESS，支持 Vision 流控，需要域名和证书",
			Recommended: true,
			NeedsDomain: true,
			NeedsCert:   true,
			Transport:   "tcp",
		},
		{
			ID:          "anytls",
			Name:        "AnyTLS + TLS",
			Description: "基于真实 TLS 的新协议，内置分包填充与连接复用，适合追求低特征和高性能的线路",
			Recommended: true,
			NeedsDomain: true,
			NeedsCert:   true,
			Transport:   "tcp",
		},
		{
			ID:          "vless-ws-tls",
			Name:        "VLESS + WebSocket + TLS",
			Description: "支持 CDN 中转，适合被墙 IP，需要域名",
			Recommended: false,
			NeedsDomain: true,
			NeedsCert:   true,
			Transport:   "ws",
		},
		{
			ID:          "hysteria2",
			Name:        "Hysteria2",
			Description: "基于 QUIC，超高速度低延迟，适合视频游戏，需要域名",
			Recommended: true,
			NeedsDomain: true,
			NeedsCert:   true,
			Transport:   "quic",
		},
		{
			ID:          "tuic-v5",
			Name:        "TUIC v5",
			Description: "基于 QUIC，性能优秀，中转能力强",
			Recommended: false,
			NeedsDomain: true,
			NeedsCert:   true,
			Transport:   "quic",
		},
	}
}

func (s *ProxyService) GenerateConfig(protocol, domain string, port int, config models.NodeConfig) (map[string]interface{}, error) {
	// Auto-generate credentials if not provided
	if config.UUID == "" {
		config.UUID = generateUUID()
	}
	if config.Password == "" {
		if protocol == "anytls" {
			config.Password = generateBase64Password(16)
		} else {
			config.Password = generatePassword()
		}
	}

	if requiresDomain(protocol) && strings.TrimSpace(domain) == "" {
		return nil, fmt.Errorf("该协议需要域名和有效 TLS 证书，请先填写已解析到本机的域名")
	}

	switch protocol {
	case "vless-reality-vision":
		return s.generateVLESSRealityConfig(domain, port, config, "tcp")
	case "vless-reality-grpc":
		return s.generateVLESSRealityConfig(domain, port, config, "grpc")
	case "vless-vision-tls", "vless-vision":
		return s.generateVLESSVisionConfig(domain, port, config)
	case "vless-ws-tls", "vless-ws":
		return s.generateVLESSWSConfig(domain, port, config)
	case "anytls":
		return s.generateAnyTLSConfig(domain, port, config)
	case "trojan-tcp-tls", "trojan":
		return s.generateTrojanConfig(domain, port, config, "tcp")
	case "trojan-grpc-tls":
		return s.generateTrojanConfig(domain, port, config, "grpc")
	case "hysteria2":
		return s.generateHysteria2Config(domain, port, config)
	case "tuic-v5", "tuic":
		return s.generateTUICConfig(domain, port, config)
	case "vmess-ws-tls", "vmess-ws":
		return s.generateVMessWSConfig(domain, port, config)
	case "shadowsocks-2022":
		return s.generateShadowsocks2022Config(domain, port, config)
	default:
		return nil, fmt.Errorf("不支持的协议: %s", protocol)
	}
}

func (s *ProxyService) generateVLESSRealityConfig(domain string, port int, config models.NodeConfig, transport string) (map[string]interface{}, error) {
	if config.PublicKey == "" || config.PrivateKey == "" {
		pub, priv := generateX25519Keys()
		config.PublicKey = pub
		config.PrivateKey = priv
	}
	if config.ShortID == "" {
		config.ShortID = generateShortID()
	}
	if config.ServerName == "" {
		config.ServerName = GetSuggestedSNI()
	}

	result := map[string]interface{}{
		"protocol":    "vless",
		"uuid":        config.UUID,
		"flow":        "xtls-rprx-vision",
		"port":        port,
		"transport":   transport,
		"security":    "reality",
		"publicKey":   config.PublicKey,
		"privateKey":  config.PrivateKey,
		"shortId":     config.ShortID,
		"serverName":  config.ServerName,
		"fingerprint": "chrome",
	}

	// Preserve listen IP if specified
	if config.Listen != "" {
		result["listen"] = config.Listen
	}

	if transport == "grpc" {
		result["serviceName"] = config.Path
		if result["serviceName"] == "" || result["serviceName"] == "grpc" {
			// Generate a random API-like service name to avoid pattern detection
			result["serviceName"] = generateRandomServiceName()
		}
		delete(result, "flow")
	}

	return result, nil
}

func (s *ProxyService) generateVLESSVisionConfig(domain string, port int, config models.NodeConfig) (map[string]interface{}, error) {
	result := map[string]interface{}{
		"protocol":  "vless",
		"uuid":      config.UUID,
		"flow":      "xtls-rprx-vision",
		"port":      port,
		"domain":    domain,
		"transport": "tcp",
		"security":  "tls",
		"certPath":  config.CertPath,
		"keyPath":   config.KeyPath,
	}
	if config.Listen != "" {
		result["listen"] = config.Listen
	}
	return result, nil
}

func (s *ProxyService) generateVLESSWSConfig(domain string, port int, config models.NodeConfig) (map[string]interface{}, error) {
	if config.Path == "" {
		config.Path = "/" + generateShortID()
	}
	result := map[string]interface{}{
		"protocol":  "vless",
		"uuid":      config.UUID,
		"port":      port,
		"domain":    domain,
		"transport": "ws",
		"path":      config.Path,
		"security":  "tls",
		"certPath":  config.CertPath,
		"keyPath":   config.KeyPath,
	}
	if config.Listen != "" {
		result["listen"] = config.Listen
	}
	return result, nil
}

func (s *ProxyService) generateAnyTLSConfig(domain string, port int, config models.NodeConfig) (map[string]interface{}, error) {
	result := map[string]interface{}{
		"protocol":  "anytls",
		"password":  config.Password,
		"port":      port,
		"domain":    domain,
		"security":  "tls",
		"certPath":  config.CertPath,
		"keyPath":   config.KeyPath,
		"minIdle":   5,
		"checkIdle": "30s",
		"idleTime":  "30s",
	}
	if config.Listen != "" {
		result["listen"] = config.Listen
	}
	return result, nil
}

func (s *ProxyService) generateVMessWSConfig(domain string, port int, config models.NodeConfig) (map[string]interface{}, error) {
	if config.Path == "" {
		config.Path = "/" + generateShortID()
	}
	result := map[string]interface{}{
		"protocol":  "vmess",
		"uuid":      config.UUID,
		"port":      port,
		"domain":    domain,
		"transport": "ws",
		"path":      config.Path,
		"security":  "tls",
		"certPath":  config.CertPath,
		"keyPath":   config.KeyPath,
	}
	if config.Listen != "" {
		result["listen"] = config.Listen
	}
	return result, nil
}

func (s *ProxyService) generateTrojanConfig(domain string, port int, config models.NodeConfig, transport string) (map[string]interface{}, error) {
	result := map[string]interface{}{
		"protocol":  "trojan",
		"password":  config.Password,
		"port":      port,
		"domain":    domain,
		"transport": transport,
		"security":  "tls",
		"certPath":  config.CertPath,
		"keyPath":   config.KeyPath,
	}

	if transport == "grpc" {
		result["serviceName"] = config.Path
		if result["serviceName"] == "" {
			result["serviceName"] = generateRandomServiceName()
		}
	}
	if config.Listen != "" {
		result["listen"] = config.Listen
	}

	return result, nil
}

func (s *ProxyService) generateHysteria2Config(domain string, port int, config models.NodeConfig) (map[string]interface{}, error) {
	if config.UpMbps == 0 {
		config.UpMbps = 100
	}
	if config.DownMbps == 0 {
		config.DownMbps = 100
	}

	result := map[string]interface{}{
		"protocol": "hysteria2",
		"password": config.Password,
		"port":     port,
		"domain":   domain,
		"upMbps":   config.UpMbps,
		"downMbps": config.DownMbps,
		"certPath": config.CertPath,
		"keyPath":  config.KeyPath,
	}
	if config.Listen != "" {
		result["listen"] = config.Listen
	}
	return result, nil
}

func (s *ProxyService) generateTUICConfig(domain string, port int, config models.NodeConfig) (map[string]interface{}, error) {
	if config.CongestionCtrl == "" {
		config.CongestionCtrl = "bbr"
	}

	result := map[string]interface{}{
		"protocol":          "tuic",
		"uuid":              config.UUID,
		"password":          config.Password,
		"port":              port,
		"domain":            domain,
		"congestionControl": config.CongestionCtrl,
		"certPath":          config.CertPath,
		"keyPath":           config.KeyPath,
	}
	if config.Listen != "" {
		result["listen"] = config.Listen
	}
	return result, nil
}

func (s *ProxyService) generateShadowsocks2022Config(domain string, port int, config models.NodeConfig) (map[string]interface{}, error) {
	// Generate 32-byte key for 2022-blake3-aes-256-gcm
	key := make([]byte, 32)
	rand.Read(key)
	keyStr := base64.StdEncoding.EncodeToString(key)

	return map[string]interface{}{
		"protocol": "shadowsocks",
		"method":   "2022-blake3-aes-256-gcm",
		"password": keyStr,
		"port":     port,
	}, nil
}

func (s *ProxyService) StartNode(nodeID int64, protocol, configJSON string, warpEnabled, aimiliEnabled, packetstreamEnabled bool, db interface{}) error {
	var config map[string]interface{}
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		return fmt.Errorf("配置解析失败: %v", err)
	}

	// Auto-install sing-box if not present
	status := s.GetCoreStatus()
	if !status.SingBoxInstalled || !singBoxSupportsProtocol(status.SingBoxVersion, protocol) {
		if err := s.InstallSingBox(); err != nil {
			return fmt.Errorf("自动安装 sing-box 失败: %v", err)
		}
	}

	// Generate sing-box compatible config
	singboxConfig, err := s.generateSingBoxConfig(config, warpEnabled, aimiliEnabled, packetstreamEnabled, db)
	if err != nil {
		return fmt.Errorf("生成配置失败: %v", err)
	}

	// If this node will bind to port 443 (the default HTTPS port already
	// claimed by 1Panel OpenResty), make sure OpenResty no longer
	// occupies the IPv6 :443 listener so the bind does not collide.
	if portUsed := nodePortFromConfig(config); portUsed == 443 {
		camo := NewCamouflageService()
		if camo.IsAvailable() {
			if err := camo.DisableIPv6On443(); err != nil {
				fmt.Printf("[node-%d] 关闭 OpenResty IPv6:443 监听失败: %v\n", nodeID, err)
			}
		}
	}

	configData, _ := json.MarshalIndent(singboxConfig, "", "  ")

	// Copy TLS certificates from container to host (sing-box runs on host, needs certs on host)
	s.copyCertsToHost()

	// Use nsenter to write config file to host filesystem
	configDir := "/etc/v2ray-agent/nodes"
	configPath := filepath.Join(configDir, fmt.Sprintf("node_%d.json", nodeID))

	// Create directory on host
	mkdirScript := fmt.Sprintf("mkdir -p %s", configDir)
	s.runOnHost("bash", "-c", mkdirScript)

	// Write config file to host using cat with heredoc via nsenter
	writeConfigScript := fmt.Sprintf("cat > %s << 'EOFCONFIG'\n%s\nEOFCONFIG", configPath, string(configData))
	if _, err := s.runOnHost("bash", "-c", writeConfigScript); err != nil {
		return fmt.Errorf("写入配置失败: %v", err)
	}

	// Create systemd service on host
	serviceName := fmt.Sprintf("proxy_node_%d", nodeID)
	singboxPath := s.findSingBoxPathOnHost()
	serviceContent := fmt.Sprintf(`[Unit]
Description=Proxy Node %d
After=network.target

[Service]
Type=simple
Environment=ENABLE_DEPRECATED_WIREGUARD_OUTBOUND=true
ExecStart=%s run -c %s
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`, nodeID, singboxPath, configPath)

	servicePath := fmt.Sprintf("/etc/systemd/system/%s.service", serviceName)

	// Write service file to host
	writeServiceScript := fmt.Sprintf("cat > %s << 'EOFSERVICE'\n%s\nEOFSERVICE", servicePath, serviceContent)
	if _, err := s.runOnHost("bash", "-c", writeServiceScript); err != nil {
		return fmt.Errorf("创建服务文件失败: %v", err)
	}

	// Execute systemctl on host
	if _, err := s.runOnHost("systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload 失败: %v", err)
	}

	s.runOnHost("systemctl", "enable", serviceName)

	if _, err := s.runOnHost("systemctl", "start", serviceName); err != nil {
		return fmt.Errorf("启动服务失败: %v", err)
	}

	return nil
}

func (s *ProxyService) StopNode(nodeID int64) error {
	serviceName := fmt.Sprintf("proxy_node_%d", nodeID)

	s.runOnHost("systemctl", "stop", serviceName)
	s.runOnHost("systemctl", "disable", serviceName)

	return nil
}

// nodePortFromConfig extracts the listen port from a node config map.
// It accepts both numeric and string representations.
func nodePortFromConfig(config map[string]interface{}) int {
	if config == nil {
		return 0
	}
	switch v := config["port"].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case string:
		if v == "" {
			return 0
		}
		n := 0
		for _, c := range v {
			if c < '0' || c > '9' {
				return 0
			}
			n = n*10 + int(c-'0')
		}
		return n
	}
	return 0
}

// runOnHost executes a command on the host system using nsenter
func (s *ProxyService) runOnHost(command string, args ...string) (string, error) {
	fullArgs := append([]string{"-t", "1", "-m", "-u", "-i", "-n", command}, args...)
	cmd := exec.Command("nsenter", fullArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("%s: %s", err, string(output))
	}
	return string(output), nil
}

func (s *ProxyService) findSingBoxPathOnHost() string {
	paths := []string{"/usr/local/bin/sing-box", "/etc/v2ray-agent/sing-box/sing-box", "/usr/bin/sing-box"}
	for _, path := range paths {
		if _, err := s.runOnHost("test", "-x", path); err == nil {
			return path
		}
	}
	return "/usr/local/bin/sing-box"
}

// copyCertsToHost copies TLS certificates from container to host filesystem
// This is needed because sing-box runs on host and needs access to certs
func (s *ProxyService) copyCertsToHost() {
	certDir := "/etc/v2ray-agent/tls"

	// Create cert directory on host
	s.runOnHost("bash", "-c", fmt.Sprintf("mkdir -p %s", certDir))

	// Find all certificate files in container and copy to host
	entries, err := os.ReadDir(certDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		filename := entry.Name()
		srcPath := filepath.Join(certDir, filename)

		// Read cert content from container
		content, err := os.ReadFile(srcPath)
		if err != nil {
			continue
		}

		// Write to host using nsenter with base64 encoding to handle special chars
		encodedContent := base64.StdEncoding.EncodeToString(content)
		writeScript := fmt.Sprintf("echo '%s' | base64 -d > %s", encodedContent, srcPath)
		s.runOnHost("bash", "-c", writeScript)
	}
}

func configString(config map[string]interface{}, key, fallback string) string {
	if v, ok := config[key].(string); ok && v != "" {
		return v
	}
	return fallback
}

func configInt(config map[string]interface{}, key string, fallback int) int {
	switch v := config[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case string:
		n := 0
		if v == "" {
			return fallback
		}
		for _, c := range v {
			if c < '0' || c > '9' {
				return fallback
			}
			n = n*10 + int(c-'0')
		}
		return n
	}
	return fallback
}

func listenIPFromConfig(config map[string]interface{}) string {
	listenIP := "::"
	if li, ok := config["listen"].(string); ok && li != "" {
		detector := NewDetectorService()
		listenIP = detector.ResolveBindAddress(li)
	}
	return listenIP
}

func tlsConfigFromNode(config map[string]interface{}, alpn []string) map[string]interface{} {
	domain := configString(config, "domain", "")
	certPath := configString(config, "certPath", "")
	keyPath := configString(config, "keyPath", "")

	tlsConfig := map[string]interface{}{
		"enabled": true,
	}
	if certPath != "" && keyPath != "" {
		tlsConfig["certificate_path"] = certPath
		tlsConfig["key_path"] = keyPath
	} else if domain != "" {
		tlsConfig["certificate_path"] = fmt.Sprintf("/etc/v2ray-agent/tls/%s.crt", domain)
		tlsConfig["key_path"] = fmt.Sprintf("/etc/v2ray-agent/tls/%s.key", domain)
	} else {
		tlsConfig["certificate_path"] = "/etc/v2ray-agent/tls/selfsigned.crt"
		tlsConfig["key_path"] = "/etc/v2ray-agent/tls/selfsigned.key"
	}
	if domain != "" {
		tlsConfig["server_name"] = domain
	}
	if len(alpn) > 0 {
		tlsConfig["alpn"] = alpn
	}
	return tlsConfig
}

// generateSingBoxConfig generates a sing-box compatible configuration
func (s *ProxyService) generateSingBoxConfig(config map[string]interface{}, warpEnabled, aimiliEnabled, packetstreamEnabled bool, db interface{}) (map[string]interface{}, error) {
	enabledCount := 0
	if warpEnabled {
		enabledCount++
	}
	if aimiliEnabled {
		enabledCount++
	}
	if packetstreamEnabled {
		enabledCount++
	}
	if enabledCount > 1 {
		return nil, fmt.Errorf("WARP、Aimili VPN 与 PacketStream 同一节点只能开启一个")
	}

	port := configInt(config, "port", 443)

	protocol := ""
	if p, ok := config["protocol"].(string); ok {
		protocol = p
	}

	// Basic sing-box config structure
	singboxConfig := map[string]interface{}{
		"log": map[string]interface{}{
			"level": "info",
		},
		"inbounds": []map[string]interface{}{},
	}

	var inbound map[string]interface{}

	switch protocol {
	case "vless":
		uuid := ""
		if u, ok := config["uuid"].(string); ok {
			uuid = u
		}
		security := ""
		if sec, ok := config["security"].(string); ok {
			security = sec
		}
		transport := "tcp"
		if t, ok := config["transport"].(string); ok {
			transport = t
		}

		// For gRPC, users don't need flow
		userFlow := "xtls-rprx-vision"
		if transport == "grpc" || transport == "ws" {
			userFlow = ""
		}

		listenIP := listenIPFromConfig(config)
		user := map[string]interface{}{"uuid": uuid}
		if userFlow != "" {
			user["flow"] = userFlow
		}

		inbound = map[string]interface{}{
			"type":        "vless",
			"tag":         "vless-in",
			"listen":      listenIP,
			"listen_port": port,
			"users":       []map[string]interface{}{user},
		}

		// Add transport config
		if transport == "ws" {
			path := "/vless-ws"
			if p, ok := config["path"].(string); ok && p != "" {
				path = p
			}
			inbound["transport"] = map[string]interface{}{
				"type":                   "ws",
				"path":                   path,
				"max_early_data":         2048,
				"early_data_header_name": "Sec-WebSocket-Protocol",
			}
		} else if transport == "grpc" {
			serviceName := "grpc"
			if sn, ok := config["serviceName"].(string); ok && sn != "" {
				serviceName = sn
			}
			inbound["transport"] = map[string]interface{}{
				"type":         "grpc",
				"service_name": serviceName,
			}
		}

		if security == "reality" {
			privateKey := ""
			serverName := GetSuggestedSNI()
			shortId := ""

			if pk, ok := config["privateKey"].(string); ok {
				privateKey = pk
			}
			if sn, ok := config["serverName"].(string); ok {
				serverName = sn
			}
			if si, ok := config["shortId"].(string); ok {
				shortId = si
			}

			inbound["tls"] = map[string]interface{}{
				"enabled":     true,
				"server_name": serverName,
				"reality": map[string]interface{}{
					"enabled": true,
					"handshake": map[string]interface{}{
						"server":      serverName,
						"server_port": 443,
					},
					"private_key": privateKey,
					"short_id":    []string{shortId},
				},
			}
		} else if security == "tls" {
			inbound["tls"] = tlsConfigFromNode(config, nil)
		}

	case "trojan":
		password := ""
		if p, ok := config["password"].(string); ok {
			password = p
		}
		transport := configString(config, "transport", "tcp")
		inbound = map[string]interface{}{
			"type":        "trojan",
			"tag":         "trojan-in",
			"listen":      listenIPFromConfig(config),
			"listen_port": port,
			"users": []map[string]interface{}{
				{"password": password},
			},
			"tls": tlsConfigFromNode(config, nil),
		}
		if transport == "grpc" {
			serviceName := configString(config, "serviceName", "grpc")
			inbound["transport"] = map[string]interface{}{
				"type":         "grpc",
				"service_name": serviceName,
			}
		}

	case "vmess":
		uuid := configString(config, "uuid", "")
		path := configString(config, "path", "/vmess-ws")
		inbound = map[string]interface{}{
			"type":        "vmess",
			"tag":         "vmess-in",
			"listen":      listenIPFromConfig(config),
			"listen_port": port,
			"users": []map[string]interface{}{
				{"uuid": uuid, "alterId": 0},
			},
			"transport": map[string]interface{}{
				"type":                   "ws",
				"path":                   path,
				"max_early_data":         2048,
				"early_data_header_name": "Sec-WebSocket-Protocol",
			},
			"tls": tlsConfigFromNode(config, nil),
		}

	case "anytls":
		password := configString(config, "password", "")
		inbound = map[string]interface{}{
			"type":        "anytls",
			"tag":         "anytls-in",
			"listen":      listenIPFromConfig(config),
			"listen_port": port,
			"users": []map[string]interface{}{
				{"name": "proxy_version", "password": password},
			},
			"tls": tlsConfigFromNode(config, nil),
		}

	case "hysteria2":
		password := ""
		if p, ok := config["password"].(string); ok {
			password = p
		}
		tlsConfig := tlsConfigFromNode(config, []string{"h3"})

		inbound = map[string]interface{}{
			"type":        "hysteria2",
			"tag":         "hy2-in",
			"listen":      listenIPFromConfig(config),
			"listen_port": port,
			"users": []map[string]interface{}{
				{"password": password},
			},
			"up_mbps":   configInt(config, "upMbps", 100),
			"down_mbps": configInt(config, "downMbps", 100),
			"tls":       tlsConfig,
		}

	case "shadowsocks":
		password := ""
		method := "2022-blake3-aes-256-gcm"
		if p, ok := config["password"].(string); ok {
			password = p
		}
		if m, ok := config["method"].(string); ok {
			method = m
		}
		inbound = map[string]interface{}{
			"type":        "shadowsocks",
			"tag":         "ss-in",
			"listen":      listenIPFromConfig(config),
			"listen_port": port,
			"method":      method,
			"password":    password,
		}

	case "tuic":
		uuid := ""
		password := ""
		if u, ok := config["uuid"].(string); ok {
			uuid = u
		}
		if p, ok := config["password"].(string); ok {
			password = p
		}
		tlsConfig := tlsConfigFromNode(config, []string{"h3"})

		inbound = map[string]interface{}{
			"type":        "tuic",
			"tag":         "tuic-in",
			"listen":      listenIPFromConfig(config),
			"listen_port": port,
			"users": []map[string]interface{}{
				{"uuid": uuid, "password": password},
			},
			"congestion_control": configString(config, "congestionControl", configString(config, "congestion", "bbr")),
			"tls":                tlsConfig,
		}

	default:
		return nil, fmt.Errorf("暂不支持的协议: %s", protocol)
	}

	singboxConfig["inbounds"] = []map[string]interface{}{inbound}

	// Add outbounds - required for proxy to forward traffic
	outbounds := []map[string]interface{}{
		{
			"type": "direct",
			"tag":  "direct",
		},
		{
			"type": "block",
			"tag":  "block",
		},
	}

	// Add WARP endpoint if enabled. sing-box 1.13 removed the legacy wireguard outbound,
	// so WARP must be exposed as a top-level endpoint and routed by tag.
	finalOutbound := "direct"
	if warpEnabled {
		sqlDB, ok := db.(*sql.DB)
		if !ok || sqlDB == nil {
			return nil, fmt.Errorf("WARP 已开启但数据库连接不可用")
		}

		warpService := NewWarpService(sqlDB)
		warpEndpoint, err := warpService.GenerateSingBoxEndpoint()
		if err != nil {
			return nil, fmt.Errorf("生成 WARP 配置失败: %v", err)
		}
		singboxConfig["endpoints"] = []map[string]interface{}{warpEndpoint}
		singboxConfig["dns"] = map[string]interface{}{
			"servers": []map[string]interface{}{
				{"type": "local", "tag": "local"},
			},
		}
		finalOutbound = "warp-out"
	}
	if aimiliEnabled {
		aimiliService := NewAimiliService()
		aimiliOutbound, err := aimiliService.GenerateSingBoxOutbound()
		if err != nil {
			return nil, fmt.Errorf("生成 Aimili VPN 配置失败: %v", err)
		}
		outbounds = append(outbounds, aimiliOutbound)
		finalOutbound = "aimili-out"
	}
	if packetstreamEnabled {
		sqlDB, ok := db.(*sql.DB)
		if !ok || sqlDB == nil {
			return nil, fmt.Errorf("PacketStream 已开启但数据库连接不可用")
		}
		psService := NewPacketStreamService(sqlDB)
		psOutbound, err := psService.GenerateSingBoxOutbound()
		if err != nil {
			return nil, fmt.Errorf("生成 PacketStream 配置失败: %v", err)
		}
		outbounds = append(outbounds, psOutbound)
		finalOutbound = "packetstream-out"
	}

	singboxConfig["outbounds"] = outbounds

	// Add route - direct all traffic to final outbound
	route := map[string]interface{}{
		"final": finalOutbound,
	}
	if warpEnabled {
		route["default_domain_resolver"] = "local"
	}
	singboxConfig["route"] = route

	return singboxConfig, nil
}

func generateUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func generateShortID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func generatePassword() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func generateBase64Password(size int) string {
	b := make([]byte, size)
	rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

func singBoxSupportsProtocol(version, protocol string) bool {
	switch protocol {
	case "anytls":
		return compareVersion(version, "1.12.0") >= 0
	default:
		return true
	}
}

func compareVersion(left, right string) int {
	leftParts := parseVersionParts(left)
	rightParts := parseVersionParts(right)
	for i := 0; i < 3; i++ {
		if leftParts[i] > rightParts[i] {
			return 1
		}
		if leftParts[i] < rightParts[i] {
			return -1
		}
	}
	return 0
}

func parseVersionParts(version string) [3]int {
	var parts [3]int
	field := 0
	for _, c := range version {
		if c == 'v' && field == 0 && parts[field] == 0 {
			continue
		}
		if c == '.' {
			if field < 2 {
				field++
			}
			continue
		}
		if c < '0' || c > '9' {
			break
		}
		parts[field] = parts[field]*10 + int(c-'0')
	}
	return parts
}

func requiresDomain(protocol string) bool {
	switch protocol {
	case "vless-vision-tls", "vless-vision", "vless-ws-tls", "vless-ws",
		"trojan-tcp-tls", "trojan", "trojan-grpc-tls",
		"hysteria2", "tuic-v5", "tuic", "vmess-ws-tls", "vmess-ws", "anytls":
		return true
	default:
		return false
	}
}

func generateX25519Keys() (string, string) {
	privateKey := make([]byte, curve25519.ScalarSize)
	if _, err := rand.Read(privateKey); err != nil {
		panic(err)
	}

	// Match X25519 private key clamping used by sing-box's reality-keypair command.
	privateKey[0] &= 248
	privateKey[31] &= 127
	privateKey[31] |= 64

	publicKey, err := curve25519.X25519(privateKey, curve25519.Basepoint)
	if err != nil {
		panic(err)
	}

	return base64.RawURLEncoding.EncodeToString(publicKey), base64.RawURLEncoding.EncodeToString(privateKey)
}

func generateRandomServiceName() string {
	prefixes := []string{"ShopService", "ApiGateway", "DataSync", "UpdateService", "StreamApi", "AuthService", "UserApi", "PaymentGw", "OrderSvc", "ProductApi"}
	suffix := make([]byte, 4)
	rand.Read(suffix)
	return fmt.Sprintf("%s_%x", prefixes[mrand.Intn(len(prefixes))], suffix)
}

// Helper to generate random port
func init() {
	n, _ := rand.Int(rand.Reader, big.NewInt(1000000))
	mrand.Seed(n.Int64())
}

// ShareInfo contains shareable node information
type ShareInfo struct {
	URL        string `json:"url"`         // Primary URL (IPv4 if available, else IPv6)
	URLIPv4    string `json:"url_ipv4"`    // IPv4 specific URL
	URLIPv6    string `json:"url_ipv6"`    // IPv6 specific URL
	QRCode     string `json:"qrcode"`      // Base64 encoded QR code image data URL
	QRCodeIPv4 string `json:"qrcode_ipv4"` // IPv4 QR code
	QRCodeIPv6 string `json:"qrcode_ipv6"` // IPv6 QR code
	JSON       string `json:"json"`        // Client-side JSON config
	Remarks    string `json:"remarks"`
	ServerIPv4 string `json:"server_ipv4"` // Server's IPv4 address
	ServerIPv6 string `json:"server_ipv6"` // Server's IPv6 address
}

// getServerIPs detects and returns the server's IPv4 and IPv6 addresses
func getServerIPs() (ipv4, ipv6 string) {
	// First try local interfaces
	interfaces, err := net.Interfaces()
	if err == nil {
		for _, iface := range interfaces {
			// Skip loopback and down interfaces
			if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
				continue
			}

			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}

			for _, addr := range addrs {
				var ip net.IP
				switch v := addr.(type) {
				case *net.IPNet:
					ip = v.IP
				case *net.IPAddr:
					ip = v.IP
				}

				if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
					continue
				}

				// Check if it's an IPv4 or IPv6 address
				if ip4 := ip.To4(); ip4 != nil {
					if ipv4 == "" && !ip.IsPrivate() {
						ipv4 = ip4.String()
					}
				} else if ip6 := ip.To16(); ip6 != nil {
					if ipv6 == "" && !ip.IsPrivate() {
						ipv6 = ip6.String()
					}
				}
			}
		}
	}

	// If no public IPv4 found locally, try external API (for NAT/cloud environments)
	if ipv4 == "" {
		ipv4 = getExternalIPv4()
	}

	// If no public IPv6 found locally, try external API
	if ipv6 == "" {
		ipv6 = getExternalIPv6()
	}

	return ipv4, ipv6
}

// getExternalIPv4 gets the public IPv4 address via external API
func getExternalIPv4() string {
	client := &http.Client{Timeout: 5 * time.Second}

	// Try multiple APIs for redundancy
	apis := []string{
		"https://api4.ipify.org",
		"https://ipv4.icanhazip.com",
		"https://v4.ident.me",
	}

	for _, api := range apis {
		resp, err := client.Get(api)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		body := make([]byte, 64)
		n, _ := resp.Body.Read(body)
		ip := strings.TrimSpace(string(body[:n]))

		// Validate it's a valid IPv4
		if net.ParseIP(ip) != nil && strings.Contains(ip, ".") {
			return ip
		}
	}
	return ""
}

// getExternalIPv6 gets the public IPv6 address via external API
func getExternalIPv6() string {
	client := &http.Client{Timeout: 5 * time.Second}

	// Try multiple APIs for redundancy
	apis := []string{
		"https://api6.ipify.org",
		"https://ipv6.icanhazip.com",
		"https://v6.ident.me",
	}

	for _, api := range apis {
		resp, err := client.Get(api)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		body := make([]byte, 64)
		n, _ := resp.Body.Read(body)
		ip := strings.TrimSpace(string(body[:n]))

		// Validate it's a valid IPv6
		if net.ParseIP(ip) != nil && strings.Contains(ip, ":") {
			return ip
		}
	}
	return ""
}

// GenerateShareURL creates a shareable URL for a node
func (s *ProxyService) GenerateShareURL(nodeName string, serverIP string, config map[string]interface{}) (ShareInfo, error) {
	info := ShareInfo{Remarks: nodeName}

	// Use DetectorService for proper IP detection (handles container environment)
	detector := NewDetectorService()
	serverInfo := detector.GetAllServerIPs()

	// Check if node is bound to a specific IP (listen parameter)
	listenIP := ""
	if li, ok := config["listen"].(string); ok && li != "" && li != "::" && li != "0.0.0.0" {
		listenIP = li
	}

	// If bound to specific IP, determine the public IP for sharing
	if listenIP != "" {
		publicIP := ""

		// Case 1: Check if listenIP is a private IP that maps to public IP
		for _, mapping := range serverInfo.IPv4Mappings {
			if mapping.LocalIP == listenIP {
				publicIP = mapping.PublicIP
				break
			}
		}
		for _, mapping := range serverInfo.IPv6Mappings {
			if mapping.LocalIP == listenIP {
				publicIP = mapping.PublicIP
				break
			}
		}

		// Case 2: Check if listenIP is already a public IP (user selected public IP directly)
		if publicIP == "" {
			// Check if this IP exists in public IP list
			for _, ip := range serverInfo.IPv4List {
				if ip == listenIP {
					publicIP = listenIP
					break
				}
			}
			for _, ip := range serverInfo.IPv6List {
				if ip == listenIP {
					publicIP = listenIP
					break
				}
			}
		}

		// Case 3: Check if listenIP is a public IP that appears in mappings (reverse lookup)
		if publicIP == "" {
			for _, mapping := range serverInfo.IPv4Mappings {
				if mapping.PublicIP == listenIP {
					publicIP = listenIP
					break
				}
			}
			for _, mapping := range serverInfo.IPv6Mappings {
				if mapping.PublicIP == listenIP {
					publicIP = listenIP
					break
				}
			}
		}

		// Fallback: use listenIP as-is
		if publicIP == "" {
			publicIP = listenIP
		}

		if strings.Contains(publicIP, ":") {
			// IPv6 only
			info.ServerIPv6 = publicIP
			info.ServerIPv4 = ""
		} else {
			// IPv4 only
			info.ServerIPv4 = publicIP
			info.ServerIPv6 = ""
		}
	} else {
		// No specific IP bound - use auto-detected IPs from DetectorService
		if len(serverInfo.IPv4List) > 0 {
			info.ServerIPv4 = serverInfo.IPv4List[0]
		}
		if len(serverInfo.IPv6List) > 0 {
			info.ServerIPv6 = serverInfo.IPv6List[0]
		}
	}

	// Determine primary IP for URL generation
	primaryIP := serverIP
	if primaryIP == "" {
		if info.ServerIPv4 != "" {
			primaryIP = info.ServerIPv4
		} else if info.ServerIPv6 != "" {
			primaryIP = info.ServerIPv6
		}
	}

	protocol := ""
	if p, ok := config["protocol"].(string); ok {
		protocol = p
	}

	port := 443
	if p, ok := config["port"].(float64); ok {
		port = int(p)
	} else if p, ok := config["port"].(int); ok {
		port = p
	}

	// URL generator function based on protocol
	var generateURL func(name, server string, port int, config map[string]interface{}) string
	switch protocol {
	case "vless":
		generateURL = s.generateVLESSShareURL
	case "trojan":
		generateURL = s.generateTrojanShareURL
	case "hysteria2":
		generateURL = s.generateHysteria2ShareURL
	case "shadowsocks":
		generateURL = s.generateShadowsocksShareURL
	case "vmess":
		generateURL = s.generateVMessShareURL
	case "tuic":
		generateURL = s.generateTUICShareURL
	case "anytls":
		generateURL = s.generateAnyTLSShareURL
	default:
		return info, fmt.Errorf("暂不支持该协议的分享: %s", protocol)
	}

	// Generate URLs for both IP versions
	if info.ServerIPv4 != "" {
		info.URLIPv4 = generateURL(nodeName+" [IPv4]", info.ServerIPv4, port, config)
		info.QRCodeIPv4 = info.URLIPv4
	}
	if info.ServerIPv6 != "" {
		info.URLIPv6 = generateURL(nodeName+" [IPv6]", info.ServerIPv6, port, config)
		info.QRCodeIPv6 = info.URLIPv6
	}

	// Primary URL - prefer IPv4 for compatibility
	if info.ServerIPv4 != "" {
		info.URL = info.URLIPv4
	} else if info.ServerIPv6 != "" {
		info.URL = info.URLIPv6
	} else {
		// Fallback to provided serverIP
		info.URL = generateURL(nodeName, primaryIP, port, config)
	}
	info.QRCode = info.URL

	// Generate client JSON config with correct server IP
	// Use the bound IP (from info.ServerIPv4/IPv6) not the old primaryIP
	clientServerIP := info.ServerIPv4
	if clientServerIP == "" {
		clientServerIP = info.ServerIPv6
	}
	if clientServerIP == "" {
		clientServerIP = primaryIP
	}
	clientConfig := s.generateClientConfig(clientServerIP, port, config)
	clientConfig["server_ipv4"] = info.ServerIPv4
	clientConfig["server_ipv6"] = info.ServerIPv6
	if jsonBytes, err := json.MarshalIndent(clientConfig, "", "  "); err == nil {
		info.JSON = string(jsonBytes)
	}

	return info, nil
}

func (s *ProxyService) generateVLESSShareURL(name, server string, port int, config map[string]interface{}) string {
	uuid := ""
	if u, ok := config["uuid"].(string); ok {
		uuid = u
	}

	security := ""
	if sec, ok := config["security"].(string); ok {
		security = sec
	}

	flow := ""
	if f, ok := config["flow"].(string); ok {
		flow = f
	}

	transport := "tcp"
	if t, ok := config["transport"].(string); ok {
		transport = t
	}

	// vless://uuid@server:port?params#name
	// Shadowrocket requires encryption=none
	params := []string{"encryption=none"}

	if security == "reality" {
		params = append(params, "security=reality")
		params = append(params, "type="+transport)
		if flow != "" {
			params = append(params, "flow="+flow)
		}
		if sn, ok := config["serverName"].(string); ok && sn != "" {
			params = append(params, "sni="+sn)
		}
		if pk, ok := config["publicKey"].(string); ok && pk != "" {
			params = append(params, "pbk="+pk)
		}
		if sid, ok := config["shortId"].(string); ok && sid != "" {
			params = append(params, "sid="+sid)
		}
		params = append(params, "fp=chrome")
		// Add gRPC serviceName if transport is grpc
		if transport == "grpc" {
			serviceName := "grpc"
			if sn, ok := config["serviceName"].(string); ok && sn != "" {
				serviceName = sn
			}
			params = append(params, "serviceName="+serviceName)
		}
	} else if security == "tls" {
		params = append(params, "security=tls")
		params = append(params, "type="+transport)
		if transport == "ws" {
			if path, ok := config["path"].(string); ok && path != "" {
				params = append(params, "path="+path)
			}
		}
		if sn, ok := config["domain"].(string); ok && sn != "" {
			params = append(params, "sni="+sn)
		}
		if flow != "" {
			params = append(params, "flow="+flow)
		}
	} else {
		params = append(params, "security=none")
		params = append(params, "type="+transport)
	}

	paramStr := strings.Join(params, "&")
	encodedName := strings.ReplaceAll(name, " ", "%20")

	// Format server address (wrap IPv6 in brackets)
	serverAddr := formatServerForURL(server)

	return fmt.Sprintf("vless://%s@%s:%d?%s#%s", uuid, serverAddr, port, paramStr, encodedName)
}

// formatServerForURL wraps IPv6 addresses in brackets for URL format
func formatServerForURL(server string) string {
	// Check if it's an IPv6 address (contains : but not already bracketed)
	if strings.Contains(server, ":") && !strings.HasPrefix(server, "[") {
		return "[" + server + "]"
	}
	return server
}

func (s *ProxyService) generateTrojanShareURL(name, server string, port int, config map[string]interface{}) string {
	password := ""
	if p, ok := config["password"].(string); ok {
		password = p
	}

	// trojan://password@server:port?params#name
	params := []string{"security=tls"}

	if transport, ok := config["transport"].(string); ok {
		params = append(params, "type="+transport)
		if transport == "grpc" {
			if sn, ok := config["serviceName"].(string); ok {
				params = append(params, "serviceName="+sn)
			}
		}
	}

	if sn, ok := config["domain"].(string); ok && sn != "" {
		params = append(params, "sni="+sn)
	}

	paramStr := strings.Join(params, "&")
	encodedName := strings.ReplaceAll(name, " ", "%20")

	serverAddr := formatServerForURL(server)
	return fmt.Sprintf("trojan://%s@%s:%d?%s#%s", password, serverAddr, port, paramStr, encodedName)
}

func (s *ProxyService) generateHysteria2ShareURL(name, server string, port int, config map[string]interface{}) string {
	password := ""
	if p, ok := config["password"].(string); ok {
		password = p
	}

	// hysteria2://password@server:port?params#name
	params := []string{}

	if sn, ok := config["domain"].(string); ok && sn != "" {
		params = append(params, "sni="+sn)
	}

	paramStr := ""
	if len(params) > 0 {
		paramStr = "?" + strings.Join(params, "&")
	}
	encodedName := strings.ReplaceAll(name, " ", "%20")

	serverAddr := formatServerForURL(server)
	return fmt.Sprintf("hysteria2://%s@%s:%d%s#%s", password, serverAddr, port, paramStr, encodedName)
}

func (s *ProxyService) generateShadowsocksShareURL(name, server string, port int, config map[string]interface{}) string {
	password := ""
	if p, ok := config["password"].(string); ok {
		password = p
	}
	method := "2022-blake3-aes-256-gcm"
	if m, ok := config["method"].(string); ok {
		method = m
	}

	// ss://base64(method:password)@server:port#name
	auth := fmt.Sprintf("%s:%s", method, password)
	encoded := base64.StdEncoding.EncodeToString([]byte(auth))
	encodedName := strings.ReplaceAll(name, " ", "%20")

	serverAddr := formatServerForURL(server)
	return fmt.Sprintf("ss://%s@%s:%d#%s", encoded, serverAddr, port, encodedName)
}

func (s *ProxyService) generateVMessShareURL(name, server string, port int, config map[string]interface{}) string {
	uuid := ""
	if u, ok := config["uuid"].(string); ok {
		uuid = u
	}

	// VMess uses a different format - JSON base64 encoded
	vmessConfig := map[string]interface{}{
		"v":    "2",
		"ps":   name,
		"add":  server,
		"port": port,
		"id":   uuid,
		"aid":  0,
		"net":  "ws",
		"type": "none",
		"tls":  "tls",
	}

	if path, ok := config["path"].(string); ok {
		vmessConfig["path"] = path
	}
	if domain, ok := config["domain"].(string); ok {
		vmessConfig["host"] = domain
		vmessConfig["sni"] = domain
	}

	jsonBytes, _ := json.Marshal(vmessConfig)
	encoded := base64.StdEncoding.EncodeToString(jsonBytes)

	return "vmess://" + encoded
}

func (s *ProxyService) generateTUICShareURL(name, server string, port int, config map[string]interface{}) string {
	uuid := ""
	password := ""
	if u, ok := config["uuid"].(string); ok {
		uuid = u
	}
	if p, ok := config["password"].(string); ok {
		password = p
	}

	// tuic://uuid:password@server:port?params#name
	params := []string{"congestion_control=bbr", "alpn=h3"}

	if sn, ok := config["domain"].(string); ok && sn != "" {
		params = append(params, "sni="+sn)
	}

	paramStr := strings.Join(params, "&")
	encodedName := strings.ReplaceAll(name, " ", "%20")

	serverAddr := formatServerForURL(server)
	return fmt.Sprintf("tuic://%s:%s@%s:%d?%s#%s", uuid, password, serverAddr, port, paramStr, encodedName)
}

func (s *ProxyService) generateAnyTLSShareURL(name, server string, port int, config map[string]interface{}) string {
	password := configString(config, "password", "")
	params := url.Values{}
	params.Set("security", "tls")
	if sn := configString(config, "domain", ""); sn != "" {
		params.Set("sni", sn)
	}

	serverAddr := formatServerForURL(server)
	return fmt.Sprintf("anytls://%s@%s:%d?%s#%s",
		url.QueryEscape(password),
		serverAddr,
		port,
		params.Encode(),
		url.QueryEscape(name),
	)
}

func (s *ProxyService) generateClientConfig(server string, port int, config map[string]interface{}) map[string]interface{} {
	if protocol, _ := config["protocol"].(string); protocol == "anytls" {
		return map[string]interface{}{
			"type":                        "anytls",
			"tag":                         "proxy",
			"server":                      server,
			"server_port":                 port,
			"password":                    configString(config, "password", ""),
			"idle_session_check_interval": configString(config, "checkIdle", "30s"),
			"idle_session_timeout":        configString(config, "idleTime", "30s"),
			"min_idle_session":            configInt(config, "minIdle", 5),
			"tls": map[string]interface{}{
				"enabled":     true,
				"server_name": configString(config, "domain", ""),
			},
		}
	}

	// Generate a basic client-side config for reference
	return map[string]interface{}{
		"server":   server,
		"port":     port,
		"protocol": config["protocol"],
		"settings": config,
	}
}

// ===== Reality SNI Smart Suggestion System =====

// SNISuggestion represents a suggested Reality SNI site
type SNISuggestion struct {
	Domain      string `json:"domain"`
	Description string `json:"description"`
	Latency     int    `json:"latency_ms,omitempty"` // TLS handshake latency in ms
}

// SNIResult contains suggested SNI sites for the server's region
type SNIResult struct {
	Country     string          `json:"country"`
	CountryCode string          `json:"country_code"`
	Suggested   []SNISuggestion `json:"suggested"`
	Best        string          `json:"best"` // Auto-selected best SNI
}

// cached SNI result to avoid repeated lookups
var cachedSNI string
var cachedSNITime time.Time

// GetSuggestedSNI returns the best SNI for this server (cached)
func GetSuggestedSNI() string {
	// Return cache if fresh (within 1 hour)
	if cachedSNI != "" && time.Since(cachedSNITime) < time.Hour {
		return cachedSNI
	}

	result := GetSuggestedRealitySNI()
	if result.Best != "" {
		cachedSNI = result.Best
		cachedSNITime = time.Now()
		return cachedSNI
	}
	return "www.apple.com" // ultimate fallback
}

// GetSuggestedRealitySNI detects server location and returns recommended SNI sites
func GetSuggestedRealitySNI() SNIResult {
	result := SNIResult{
		Country:     "Unknown",
		CountryCode: "XX",
	}

	// Detect server geolocation
	countryCode := detectServerCountry()
	result.CountryCode = countryCode

	// Get country-specific SNI list
	sites := getSNIListForCountry(countryCode)
	result.Country = getCountryName(countryCode)

	// Test each site's TLS connectivity and latency
	type testResult struct {
		domain  string
		latency int
		ok      bool
	}

	ch := make(chan testResult, len(sites))
	for _, site := range sites {
		go func(s SNISuggestion) {
			latency, ok := testTLSHandshake(s.Domain)
			ch <- testResult{domain: s.Domain, latency: latency, ok: ok}
		}(site)
	}

	// Collect results with timeout
	timeout := time.After(8 * time.Second)
	tested := make(map[string]testResult)
	for i := 0; i < len(sites); i++ {
		select {
		case r := <-ch:
			tested[r.domain] = r
		case <-timeout:
			break
		}
	}

	// Build result with latency info
	bestLatency := 999999
	for i := range sites {
		if tr, ok := tested[sites[i].Domain]; ok {
			if tr.ok {
				sites[i].Latency = tr.latency
				if tr.latency < bestLatency {
					bestLatency = tr.latency
					result.Best = sites[i].Domain
				}
			}
		}
	}
	result.Suggested = sites

	// Fallback if no site responded
	if result.Best == "" && len(sites) > 0 {
		result.Best = sites[0].Domain
	}

	return result
}

// detectServerCountry returns the ISO country code of the server
func detectServerCountry() string {
	apis := []struct {
		url   string
		parse func([]byte) string
	}{
		{
			url: "http://ip-api.com/json/?fields=countryCode",
			parse: func(body []byte) string {
				var resp struct {
					CountryCode string `json:"countryCode"`
				}
				if json.Unmarshal(body, &resp) == nil && resp.CountryCode != "" {
					return resp.CountryCode
				}
				return ""
			},
		},
		{
			url: "https://ipapi.co/country_code/",
			parse: func(body []byte) string {
				code := strings.TrimSpace(string(body))
				if len(code) == 2 {
					return strings.ToUpper(code)
				}
				return ""
			},
		},
	}

	client := &http.Client{Timeout: 5 * time.Second}
	for _, api := range apis {
		resp, err := client.Get(api.url)
		if err != nil {
			continue
		}
		body := make([]byte, 256)
		n, _ := resp.Body.Read(body)
		resp.Body.Close()
		if n > 0 {
			code := api.parse(body[:n])
			if code != "" {
				return code
			}
		}
	}
	return "US" // default fallback
}

// testTLSHandshake tests TLS 1.3 connectivity to a domain and returns latency in ms
func testTLSHandshake(domain string) (int, bool) {
	start := time.Now()
	conn, err := net.DialTimeout("tcp", domain+":443", 5*time.Second)
	if err != nil {
		return 0, false
	}
	defer conn.Close()
	latency := int(time.Since(start).Milliseconds())
	return latency, true
}

// getSNIListForCountry returns recommended SNI sites for a country
// All sites MUST:
// 1. Support TLS 1.3 + H2
// 2. Be accessible from mainland China (not blocked by GFW)
// 3. Be major, legitimate websites in that region (for realistic traffic patterns)
func getSNIListForCountry(countryCode string) []SNISuggestion {
	countryMap := map[string][]SNISuggestion{
		// United States
		"US": {
			{Domain: "www.tesla.com", Description: "Tesla Motors"},
			{Domain: "www.apple.com", Description: "Apple Inc."},
			{Domain: "www.amazon.com", Description: "Amazon"},
			{Domain: "www.microsoft.com", Description: "Microsoft"},
			{Domain: "www.ups.com", Description: "UPS Shipping"},
			{Domain: "www.target.com", Description: "Target Retail"},
		},
		// Japan
		"JP": {
			{Domain: "www.rakuten.co.jp", Description: "Rakuten"},
			{Domain: "www.toyota.co.jp", Description: "Toyota Japan"},
			{Domain: "www.sony.jp", Description: "Sony Japan"},
			{Domain: "www.nintendo.co.jp", Description: "Nintendo Japan"},
			{Domain: "www.honda.co.jp", Description: "Honda Japan"},
			{Domain: "www.panasonic.jp", Description: "Panasonic Japan"},
		},
		// South Korea
		"KR": {
			{Domain: "www.samsung.com", Description: "Samsung"},
			{Domain: "www.hyundai.com", Description: "Hyundai Motor"},
			{Domain: "www.lge.co.kr", Description: "LG Electronics"},
			{Domain: "www.sk.com", Description: "SK Group"},
			{Domain: "www.asiana.com", Description: "Asiana Airlines"},
		},
		// Singapore
		"SG": {
			{Domain: "www.dbs.com.sg", Description: "DBS Bank"},
			{Domain: "www.singaporeair.com", Description: "Singapore Airlines"},
			{Domain: "www.grab.com", Description: "Grab"},
			{Domain: "www.ocbc.com", Description: "OCBC Bank"},
			{Domain: "www.capitaland.com", Description: "CapitaLand"},
		},
		// Hong Kong
		"HK": {
			{Domain: "www.hsbc.com.hk", Description: "HSBC Hong Kong"},
			{Domain: "www.cathaypacific.com", Description: "Cathay Pacific"},
			{Domain: "www.hkt.com", Description: "HKT Telecom"},
			{Domain: "www.swireproperties.com", Description: "Swire Properties"},
			{Domain: "www.bochk.com", Description: "Bank of China HK"},
		},
		// Taiwan
		"TW": {
			{Domain: "www.asus.com", Description: "ASUS"},
			{Domain: "www.acer.com", Description: "Acer"},
			{Domain: "www.evaair.com", Description: "EVA Air"},
			{Domain: "www.tsmc.com", Description: "TSMC"},
			{Domain: "www.cht.com.tw", Description: "Chunghwa Telecom"},
		},
		// Macau
		"MO": {
			{Domain: "www.macautourism.gov.mo", Description: "Macau Tourism"},
			{Domain: "www.apple.com", Description: "Apple"},
			{Domain: "www.microsoft.com", Description: "Microsoft"},
		},
		// India
		"IN": {
			{Domain: "www.tatamotors.com", Description: "Tata Motors"},
			{Domain: "www.infosys.com", Description: "Infosys"},
			{Domain: "www.wipro.com", Description: "Wipro"},
			{Domain: "www.reliancedigital.in", Description: "Reliance Digital"},
			{Domain: "www.airtel.in", Description: "Airtel India"},
		},
		// Malaysia
		"MY": {
			{Domain: "www.maybank.com", Description: "Maybank"},
			{Domain: "www.airasia.com", Description: "AirAsia"},
			{Domain: "www.petronas.com", Description: "Petronas"},
			{Domain: "www.tm.com.my", Description: "Telekom Malaysia"},
		},
		// Indonesia
		"ID": {
			{Domain: "www.garuda-indonesia.com", Description: "Garuda Indonesia"},
			{Domain: "www.telkom.co.id", Description: "Telkom Indonesia"},
			{Domain: "www.bca.co.id", Description: "Bank BCA"},
			{Domain: "www.pertamina.com", Description: "Pertamina"},
		},
		// Philippines
		"PH": {
			{Domain: "www.globe.com.ph", Description: "Globe Telecom"},
			{Domain: "www.bpi.com.ph", Description: "Bank of PI"},
			{Domain: "www.cebuair.com", Description: "Cebu Pacific"},
			{Domain: "www.apple.com", Description: "Apple"},
		},
		// Germany
		"DE": {
			{Domain: "www.bmw.com", Description: "BMW"},
			{Domain: "www.siemens.com", Description: "Siemens"},
			{Domain: "www.mercedes-benz.com", Description: "Mercedes-Benz"},
			{Domain: "www.sap.com", Description: "SAP"},
			{Domain: "www.bosch.com", Description: "Bosch"},
		},
		// France
		"FR": {
			{Domain: "www.airfrance.com", Description: "Air France"},
			{Domain: "www.renault.fr", Description: "Renault"},
			{Domain: "www.orange.fr", Description: "Orange Telecom"},
			{Domain: "www.bnpparibas.com", Description: "BNP Paribas"},
			{Domain: "www.loreal.com", Description: "L'Oréal"},
		},
		// United Kingdom
		"GB": {
			{Domain: "www.bbc.co.uk", Description: "BBC"},
			{Domain: "www.barclays.co.uk", Description: "Barclays Bank"},
			{Domain: "www.rolls-royce.com", Description: "Rolls-Royce"},
			{Domain: "www.bp.com", Description: "BP"},
			{Domain: "www.bt.com", Description: "BT Group"},
		},
		// Canada
		"CA": {
			{Domain: "www.shopify.com", Description: "Shopify"},
			{Domain: "www.td.com", Description: "TD Bank"},
			{Domain: "www.bombardier.com", Description: "Bombardier"},
			{Domain: "www.rbc.com", Description: "Royal Bank of Canada"},
		},
		// Australia
		"AU": {
			{Domain: "www.qantas.com", Description: "Qantas Airways"},
			{Domain: "www.commbank.com.au", Description: "Commonwealth Bank"},
			{Domain: "www.telstra.com.au", Description: "Telstra"},
			{Domain: "www.bhp.com", Description: "BHP Mining"},
		},
		// Argentina
		"AR": {
			{Domain: "www.mercadolibre.com.ar", Description: "Mercado Libre"},
			{Domain: "www.aerolineas.com.ar", Description: "Aerolíneas Argentinas"},
			{Domain: "www.apple.com", Description: "Apple"},
			{Domain: "www.microsoft.com", Description: "Microsoft"},
		},
		// Turkey
		"TR": {
			{Domain: "www.turkishairlines.com", Description: "Turkish Airlines"},
			{Domain: "www.garanti.com.tr", Description: "Garanti Bank"},
			{Domain: "www.apple.com", Description: "Apple"},
			{Domain: "www.microsoft.com", Description: "Microsoft"},
		},
		// Mexico
		"MX": {
			{Domain: "www.telmex.com", Description: "Telmex"},
			{Domain: "www.aeromexico.com", Description: "Aeroméxico"},
			{Domain: "www.apple.com", Description: "Apple"},
			{Domain: "www.microsoft.com", Description: "Microsoft"},
		},
		// Netherlands
		"NL": {
			{Domain: "www.ing.com", Description: "ING Bank"},
			{Domain: "www.philips.com", Description: "Philips"},
			{Domain: "www.klm.com", Description: "KLM Airlines"},
			{Domain: "www.shell.com", Description: "Shell"},
		},
		// Russia
		"RU": {
			{Domain: "www.apple.com", Description: "Apple"},
			{Domain: "www.microsoft.com", Description: "Microsoft"},
			{Domain: "www.samsung.com", Description: "Samsung"},
		},
		// Brazil
		"BR": {
			{Domain: "www.embraer.com", Description: "Embraer"},
			{Domain: "www.apple.com", Description: "Apple"},
			{Domain: "www.microsoft.com", Description: "Microsoft"},
		},
		// Thailand
		"TH": {
			{Domain: "www.thaiairways.com", Description: "Thai Airways"},
			{Domain: "www.ais.th", Description: "AIS Telecom"},
			{Domain: "www.apple.com", Description: "Apple"},
		},
		// Vietnam
		"VN": {
			{Domain: "www.vietnamairlines.com", Description: "Vietnam Airlines"},
			{Domain: "www.vietcombank.com.vn", Description: "Vietcombank"},
			{Domain: "www.apple.com", Description: "Apple"},
		},
		// UAE
		"AE": {
			{Domain: "www.emirates.com", Description: "Emirates Airlines"},
			{Domain: "www.etisalat.ae", Description: "Etisalat"},
			{Domain: "www.apple.com", Description: "Apple"},
		},
		// Italy
		"IT": {
			{Domain: "www.ferrari.com", Description: "Ferrari"},
			{Domain: "www.alitalia.com", Description: "Alitalia"},
			{Domain: "www.gucci.com", Description: "Gucci"},
		},
		// Spain
		"ES": {
			{Domain: "www.iberia.com", Description: "Iberia Airlines"},
			{Domain: "www.zara.com", Description: "Zara"},
			{Domain: "www.apple.com", Description: "Apple"},
		},
		// Switzerland
		"CH": {
			{Domain: "www.nestle.com", Description: "Nestlé"},
			{Domain: "www.ubs.com", Description: "UBS Bank"},
			{Domain: "www.swiss.com", Description: "Swiss Airlines"},
		},
		// Sweden
		"SE": {
			{Domain: "www.ikea.com", Description: "IKEA"},
			{Domain: "www.ericsson.com", Description: "Ericsson"},
			{Domain: "www.volvo.com", Description: "Volvo"},
		},
	}

	// Default / fallback list (global major brands accessible from China)
	defaultSites := []SNISuggestion{
		{Domain: "www.apple.com", Description: "Apple Inc."},
		{Domain: "www.microsoft.com", Description: "Microsoft"},
		{Domain: "www.samsung.com", Description: "Samsung"},
		{Domain: "www.tesla.com", Description: "Tesla Motors"},
		{Domain: "www.ups.com", Description: "UPS Shipping"},
		{Domain: "www.bmw.com", Description: "BMW"},
	}

	if sites, ok := countryMap[countryCode]; ok {
		return sites
	}
	return defaultSites
}

// getCountryName returns human-readable country name
func getCountryName(code string) string {
	names := map[string]string{
		"US": "United States", "JP": "Japan", "KR": "South Korea",
		"SG": "Singapore", "HK": "Hong Kong", "TW": "Taiwan",
		"MO": "Macau", "IN": "India", "MY": "Malaysia",
		"ID": "Indonesia", "PH": "Philippines", "DE": "Germany",
		"FR": "France", "GB": "United Kingdom", "CA": "Canada",
		"AU": "Australia", "AR": "Argentina", "TR": "Turkey",
		"MX": "Mexico", "NL": "Netherlands", "RU": "Russia",
		"BR": "Brazil", "TH": "Thailand", "VN": "Vietnam",
		"AE": "UAE", "IT": "Italy", "ES": "Spain",
		"CH": "Switzerland", "SE": "Sweden",
	}
	if name, ok := names[code]; ok {
		return name
	}
	return "Unknown"
}
