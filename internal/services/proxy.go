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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"proxy_version/internal/models"
)

type ProxyService struct {
	configDir string
}

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
	if status.SingBoxInstalled {
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
VERSION="1.10.4"
URL="https://github.com/SagerNet/sing-box/releases/download/v${VERSION}/sing-box-${VERSION}-linux-${ARCH}.tar.gz"
cd /tmp
wget -q "$URL" -O sing-box.tar.gz
tar -xzf sing-box.tar.gz
mv sing-box-${VERSION}-linux-${ARCH}/sing-box /usr/local/bin/
chmod +x /usr/local/bin/sing-box
rm -rf sing-box.tar.gz sing-box-${VERSION}-linux-${ARCH}
echo "sing-box installed successfully"
`
	cmd := exec.Command("nsenter", "-t", "1", "-m", "-u", "-i", "-n", "bash", "-c", script)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("安装失败: %s", string(output))
	}
	return nil
}

// UninstallSingBox removes sing-box from the host system
func (s *ProxyService) UninstallSingBox() error {
	script := `
rm -f /usr/local/bin/sing-box
systemctl stop 'proxy_node_*' 2>/dev/null || true
echo "sing-box uninstalled"
`
	cmd := exec.Command("nsenter", "-t", "1", "-m", "-u", "-i", "-n", "bash", "-c", script)
	cmd.CombinedOutput()
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
		config.Password = generatePassword()
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
		config.ServerName = "www.microsoft.com"
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
	return map[string]interface{}{
		"protocol":  "vless",
		"uuid":      config.UUID,
		"flow":      "xtls-rprx-vision",
		"port":      port,
		"domain":    domain,
		"transport": "tcp",
		"security":  "tls",
		"certPath":  config.CertPath,
		"keyPath":   config.KeyPath,
	}, nil
}

func (s *ProxyService) generateVLESSWSConfig(domain string, port int, config models.NodeConfig) (map[string]interface{}, error) {
	if config.Path == "" {
		config.Path = "/" + generateShortID()
	}
	return map[string]interface{}{
		"protocol":  "vless",
		"uuid":      config.UUID,
		"port":      port,
		"domain":    domain,
		"transport": "ws",
		"path":      config.Path,
		"security":  "tls",
		"certPath":  config.CertPath,
		"keyPath":   config.KeyPath,
	}, nil
}

func (s *ProxyService) generateVMessWSConfig(domain string, port int, config models.NodeConfig) (map[string]interface{}, error) {
	if config.Path == "" {
		config.Path = "/" + generateShortID()
	}
	return map[string]interface{}{
		"protocol":  "vmess",
		"uuid":      config.UUID,
		"port":      port,
		"domain":    domain,
		"transport": "ws",
		"path":      config.Path,
		"security":  "tls",
		"certPath":  config.CertPath,
		"keyPath":   config.KeyPath,
	}, nil
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
			result["serviceName"] = "trojan-grpc"
		}
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

	return map[string]interface{}{
		"protocol":  "hysteria2",
		"password":  config.Password,
		"port":      port,
		"domain":    domain,
		"upMbps":    config.UpMbps,
		"downMbps":  config.DownMbps,
		"certPath":  config.CertPath,
		"keyPath":   config.KeyPath,
	}, nil
}

func (s *ProxyService) generateTUICConfig(domain string, port int, config models.NodeConfig) (map[string]interface{}, error) {
	if config.CongestionCtrl == "" {
		config.CongestionCtrl = "bbr"
	}

	return map[string]interface{}{
		"protocol":          "tuic",
		"uuid":              config.UUID,
		"password":          config.Password,
		"port":              port,
		"domain":            domain,
		"congestionControl": config.CongestionCtrl,
		"certPath":          config.CertPath,
		"keyPath":           config.KeyPath,
	}, nil
}

func (s *ProxyService) generateShadowsocks2022Config(domain string, port int, config models.NodeConfig) (map[string]interface{}, error) {
	// Generate 32-byte key for 2022-blake3-aes-256-gcm
	key := make([]byte, 32)
	rand.Read(key)
	keyStr := hex.EncodeToString(key)

	return map[string]interface{}{
		"protocol": "shadowsocks",
		"method":   "2022-blake3-aes-256-gcm",
		"password": keyStr,
		"port":     port,
	}, nil
}

func (s *ProxyService) StartNode(nodeID int64, protocol, configJSON string, warpEnabled bool, db interface{}) error {
	var config map[string]interface{}
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		return fmt.Errorf("配置解析失败: %v", err)
	}

	// Auto-install sing-box if not present
	status := s.GetCoreStatus()
	if !status.SingBoxInstalled {
		if err := s.InstallSingBox(); err != nil {
			return fmt.Errorf("自动安装 sing-box 失败: %v", err)
		}
	}

	// Generate sing-box compatible config
	singboxConfig, err := s.generateSingBoxConfig(config, warpEnabled, db)
	if err != nil {
		return fmt.Errorf("生成配置失败: %v", err)
	}
	
	configData, _ := json.MarshalIndent(singboxConfig, "", "  ")
	
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
	serviceContent := fmt.Sprintf(`[Unit]
Description=Proxy Node %d
After=network.target

[Service]
Type=simple
Environment=ENABLE_DEPRECATED_WIREGUARD_OUTBOUND=true
ExecStart=/usr/local/bin/sing-box run -c %s
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`, nodeID, configPath)

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

// generateSingBoxConfig generates a sing-box compatible configuration
func (s *ProxyService) generateSingBoxConfig(config map[string]interface{}, warpEnabled bool, db interface{}) (map[string]interface{}, error) {
	// Debug log
	fmt.Printf("generateSingBoxConfig called with warpEnabled=%v, db type=%T\n", warpEnabled, db)
	
	port := 443
	if p, ok := config["port"].(float64); ok {
		port = int(p)
	}
	
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
		
		// Get listen IP from config, default to all interfaces
		listenIP := "::"
		if li, ok := config["listen"].(string); ok && li != "" {
			// Check if this is a public IP that needs to be mapped to local IP (NAT)
			detector := NewDetectorService()
			if localIP := detector.GetLocalIPForPublic(li); localIP != "" {
				listenIP = localIP
			} else {
				listenIP = li
			}
		}
		
		inbound = map[string]interface{}{
			"type":        "vless",
			"tag":         "vless-in",
			"listen":      listenIP,
			"listen_port": port,
			"users": []map[string]interface{}{
				{"uuid": uuid, "flow": userFlow},
			},
			// Enable multiplex for better performance
			"multiplex": map[string]interface{}{
				"enabled": true,
				"padding": true,
				"brutal": map[string]interface{}{
					"enabled":   true,
					"up_mbps":   100,
					"down_mbps": 100,
				},
			},
		}
		
		// Add transport config
		if transport == "ws" {
			path := "/vless-ws"
			if p, ok := config["path"].(string); ok && p != "" {
				path = p
			}
			inbound["transport"] = map[string]interface{}{
				"type": "ws",
				"path": path,
				"max_early_data": 2048,
				"early_data_header_name": "Sec-WebSocket-Protocol",
			}
		} else if transport == "grpc" {
			serviceName := "grpc"
			if sn, ok := config["serviceName"].(string); ok && sn != "" {
				serviceName = sn
			}
			inbound["transport"] = map[string]interface{}{
				"type": "grpc",
				"service_name": serviceName,
			}
		}
		
		if security == "reality" {
			privateKey := ""
			serverName := "www.microsoft.com"
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
			// Regular TLS for VLESS
			domain := ""
			if d, ok := config["domain"].(string); ok {
				domain = d
			}
			certPath := ""
			keyPath := ""
			if cp, ok := config["certPath"].(string); ok {
				certPath = cp
			}
			if kp, ok := config["keyPath"].(string); ok {
				keyPath = kp
			}
			
		tlsConfig := map[string]interface{}{
				"enabled": true,
			}
			
			// Use domain-based certificate paths or explicitly provided paths
			if certPath != "" && keyPath != "" {
				tlsConfig["certificate_path"] = certPath
				tlsConfig["key_path"] = keyPath
			} else if domain != "" {
				// Use domain-based certificate from /etc/v2ray-agent/tls/
				tlsConfig["certificate_path"] = fmt.Sprintf("/etc/v2ray-agent/tls/%s.crt", domain)
				tlsConfig["key_path"] = fmt.Sprintf("/etc/v2ray-agent/tls/%s.key", domain)
			} else {
				// Fallback to self-signed (will fail if not exists)
				tlsConfig["certificate_path"] = "/etc/v2ray-agent/tls/selfsigned.crt"
				tlsConfig["key_path"] = "/etc/v2ray-agent/tls/selfsigned.key"
			}
			
			if domain != "" {
				tlsConfig["server_name"] = domain
			}
			
			inbound["tls"] = tlsConfig
		}
		
	case "trojan":
		password := ""
		if p, ok := config["password"].(string); ok {
			password = p
		}
		inbound = map[string]interface{}{
			"type":        "trojan",
			"tag":         "trojan-in",
			"listen":      "::",
			"listen_port": port,
			"users": []map[string]interface{}{
				{"password": password},
			},
		}
		
	case "hysteria2":
		password := ""
		if p, ok := config["password"].(string); ok {
			password = p
		}
		domain := ""
		if d, ok := config["domain"].(string); ok {
			domain = d
		}
		certPath := ""
		keyPath := ""
		if cp, ok := config["certPath"].(string); ok {
			certPath = cp
		}
		if kp, ok := config["keyPath"].(string); ok {
			keyPath = kp
		}
		
		tlsConfig := map[string]interface{}{
			"enabled": true,
		}
		
		// Use domain-based certificate paths or explicitly provided paths
		if certPath != "" && keyPath != "" {
			tlsConfig["certificate_path"] = certPath
			tlsConfig["key_path"] = keyPath
		} else if domain != "" {
			// Use domain-based certificate from /etc/v2ray-agent/tls/
			tlsConfig["certificate_path"] = fmt.Sprintf("/etc/v2ray-agent/tls/%s.crt", domain)
			tlsConfig["key_path"] = fmt.Sprintf("/etc/v2ray-agent/tls/%s.key", domain)
		} else {
			// Fallback to self-signed (will fail if not exists)
			tlsConfig["certificate_path"] = "/etc/v2ray-agent/tls/selfsigned.crt"
			tlsConfig["key_path"] = "/etc/v2ray-agent/tls/selfsigned.key"
		}
		
		if domain != "" {
			tlsConfig["server_name"] = domain
		}
		
		tlsConfig["alpn"] = []string{"h3"}
		
		inbound = map[string]interface{}{
			"type":        "hysteria2",
			"tag":         "hy2-in",
			"listen":      "::",
			"listen_port": port,
			"users": []map[string]interface{}{
				{"password": password},
			},
			"up_mbps":   100,
			"down_mbps": 100,
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
			"listen":      "::",
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
		domain := ""
		if d, ok := config["domain"].(string); ok {
			domain = d
		}
		certPath := ""
		keyPath := ""
		if cp, ok := config["certPath"].(string); ok {
			certPath = cp
		}
		if kp, ok := config["keyPath"].(string); ok {
			keyPath = kp
		}
		
		tlsConfig := map[string]interface{}{
			"enabled": true,
			"alpn":    []string{"h3"},
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
		
		inbound = map[string]interface{}{
			"type":               "tuic",
			"tag":                "tuic-in",
			"listen":             "::",
			"listen_port":        port,
			"users": []map[string]interface{}{
				{"uuid": uuid, "password": password},
			},
			"congestion_control": "bbr",
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
	
	// Add WARP outbound if enabled
	finalOutbound := "direct"
	if warpEnabled && db != nil {
		fmt.Printf("WARP enabled, attempting to get outbound config\n")
		if sqlDB, ok := db.(*sql.DB); ok {
			fmt.Printf("DB type assertion successful\n")
			warpService := NewWarpService(sqlDB)
			warpOutbound, err := warpService.GenerateSingBoxOutbound()
			if err != nil {
				fmt.Printf("WARP outbound generation failed: %v\n", err)
			} else if warpOutbound != nil {
				fmt.Printf("WARP outbound generated successfully, setting final to warp-out\n")
				outbounds = append(outbounds, warpOutbound)
				finalOutbound = "warp-out"
			}
		} else {
			fmt.Printf("DB type assertion failed, db type is %T\n", db)
		}
	} else {
		fmt.Printf("WARP not enabled or db is nil (warpEnabled=%v, db=%v)\n", warpEnabled, db != nil)
	}
	
	fmt.Printf("Final outbound: %s, total outbounds: %d\n", finalOutbound, len(outbounds))
	
	singboxConfig["outbounds"] = outbounds
	
	// Add route - direct all traffic to final outbound
	singboxConfig["route"] = map[string]interface{}{
		"final": finalOutbound,
	}
	
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

func generateX25519Keys() (string, string) {
	// Use sing-box to generate a proper Reality keypair
	singboxPaths := []string{"/etc/v2ray-agent/sing-box/sing-box", "/usr/local/bin/sing-box", "/usr/bin/sing-box"}
	
	for _, path := range singboxPaths {
		if _, err := os.Stat(path); err == nil {
			output, err := exec.Command(path, "generate", "reality-keypair").Output()
			if err == nil {
				lines := strings.Split(string(output), "\n")
				var privateKey, publicKey string
				for _, line := range lines {
					if strings.HasPrefix(line, "PrivateKey:") {
						privateKey = strings.TrimSpace(strings.TrimPrefix(line, "PrivateKey:"))
					} else if strings.HasPrefix(line, "PublicKey:") {
						publicKey = strings.TrimSpace(strings.TrimPrefix(line, "PublicKey:"))
					}
				}
				if privateKey != "" && publicKey != "" {
					return publicKey, privateKey
				}
			}
		}
	}
	
	// Fallback: generate random keys (not recommended, may not work)
	priv := make([]byte, 32)
	rand.Read(priv)
	pub := make([]byte, 32)
	rand.Read(pub)
	return base64.RawURLEncoding.EncodeToString(pub), base64.RawURLEncoding.EncodeToString(priv)
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

func (s *ProxyService) generateClientConfig(server string, port int, config map[string]interface{}) map[string]interface{} {
	// Generate a basic client-side config for reference
	return map[string]interface{}{
		"server":   server,
		"port":     port,
		"protocol": config["protocol"],
		"settings": config,
	}
}
