package services

import (
	"bytes"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

const (
	aimiliInstallDir    = "/opt/aimilivpn"
	aimiliConfigPath    = aimiliInstallDir + "/vpngate_data/ui_auth.json"
	aimiliNodesPath     = aimiliInstallDir + "/vpngate_data/nodes.json"
	aimiliStatePath     = aimiliInstallDir + "/vpngate_data/state.json"
	aimiliCountriesPath = aimiliInstallDir + "/vpngate_data/proxy_version_available_countries.json"
	aimiliBundleVersion = "2db62f9b9ec490d4d29a2c047f18e1d6ea8ab29e-proxy-version-3"
)

//go:embed aimili_bundle/*
var aimiliBundle embed.FS

type AimiliCountry struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type AimiliStatus struct {
	Installed        bool            `json:"installed"`
	BundleCurrent    bool            `json:"bundle_current"`
	Installing       bool            `json:"installing"`
	InstallStartedAt string          `json:"install_started_at,omitempty"`
	InstallError     string          `json:"install_error,omitempty"`
	Refreshing       bool            `json:"refreshing"`
	RefreshStartedAt string          `json:"refresh_started_at,omitempty"`
	RefreshError     string          `json:"refresh_error,omitempty"`
	LastFetchAt      int64           `json:"last_fetch_at,omitempty"`
	LastFetchMessage string          `json:"last_fetch_message,omitempty"`
	LastCheckMessage string          `json:"last_check_message,omitempty"`
	ValidNodes       int             `json:"valid_nodes"`
	Running          bool            `json:"running"`
	ProxyReady       bool            `json:"proxy_ready"`
	Connected        bool            `json:"connected"`
	Ready            bool            `json:"ready"`
	ProxyHost        string          `json:"proxy_host"`
	ProxyPort        int             `json:"proxy_port"`
	RoutingMode      string          `json:"routing_mode"`
	Country          string          `json:"country"`
	ActiveCountry    string          `json:"active_country,omitempty"`
	ActiveIP         string          `json:"active_ip,omitempty"`
	Countries        []AimiliCountry `json:"countries"`
	Error            string          `json:"error,omitempty"`
}

type AimiliService struct{}

func NewAimiliService() *AimiliService {
	return &AimiliService{}
}

func (s *AimiliService) IsBundleCurrent() bool {
	output, err := s.runOnHost("cat", aimiliInstallDir+"/.proxy_version_bundle")
	return err == nil && strings.TrimSpace(output) == aimiliBundleVersion
}

func (s *AimiliService) runOnHost(command string, args ...string) (string, error) {
	fullArgs := append([]string{"-t", "1", "-m", "-u", "-i", "-n", command}, args...)
	output, err := exec.Command("nsenter", fullArgs...).CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("%s: %s", err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func (s *AimiliService) runOnHostInput(input []byte, command string, args ...string) (string, error) {
	fullArgs := append([]string{"-t", "1", "-m", "-u", "-i", "-n", command}, args...)
	cmd := exec.Command("nsenter", fullArgs...)
	cmd.Stdin = bytes.NewReader(input)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("%s: %s", err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func (s *AimiliService) writeHostFile(path string, data []byte, mode string) error {
	script := fmt.Sprintf(
		"set -e; mkdir -p $(dirname %s); tmp=%s.tmp.$$; cat > \"$tmp\"; chmod %s \"$tmp\"; mv \"$tmp\" %s",
		path, path, mode, path,
	)
	_, err := s.runOnHostInput(data, "sh", "-c", script)
	return err
}

func (s *AimiliService) readHostJSON(path string, target interface{}) error {
	output, err := s.runOnHost("cat", path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(output), target); err != nil {
		return fmt.Errorf("解析 Aimili 配置失败: %v", err)
	}
	return nil
}

func (s *AimiliService) writeHostJSON(path string, value interface{}) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	script := fmt.Sprintf(
		"mkdir -p %s/vpngate_data && echo %s | base64 -d > %s.tmp && chmod 600 %s.tmp && mv %s.tmp %s",
		aimiliInstallDir, encoded, path, path, path, path,
	)
	_, err = s.runOnHost("sh", "-c", script)
	return err
}

func (s *AimiliService) GetStatus() AimiliStatus {
	status := AimiliStatus{
		ProxyHost:   "127.0.0.1",
		ProxyPort:   7928,
		RoutingMode: "auto",
		Countries:   []AimiliCountry{},
	}

	if _, err := s.runOnHost("test", "-f", aimiliInstallDir+"/vpngate_manager.py"); err != nil {
		return status
	}
	status.Installed = true
	status.BundleCurrent = s.IsBundleCurrent()
	if _, err := s.runOnHost("systemctl", "is-active", "--quiet", "aimilivpn.service"); err == nil {
		status.Running = true
	}

	config := map[string]interface{}{}
	if err := s.readHostJSON(aimiliConfigPath, &config); err == nil {
		status.ProxyPort = interfaceInt(config["proxy_port"], status.ProxyPort)
		status.RoutingMode = interfaceString(config["routing_mode"], "auto")
		status.Country = interfaceString(config["force_country"], "")
	}

	if status.Running {
		command := fmt.Sprintf("ss -H -ltn 'sport = :%d' | grep -q LISTEN", status.ProxyPort)
		if _, err := s.runOnHost("sh", "-c", command); err == nil {
			status.ProxyReady = true
		}
	}

	var state map[string]interface{}
	_ = s.readHostJSON(aimiliStatePath, &state)
	status.LastFetchAt = interfaceInt64(state["last_fetch_at"])
	status.LastFetchMessage = interfaceString(state["last_fetch_message"], "")
	status.LastCheckMessage = interfaceString(state["last_check_message"], "")
	status.ValidNodes = interfaceInt(state["valid_nodes"], 0)
	status.Connected = interfaceBool(state["proxy_ok"])
	if status.Connected {
		status.ActiveIP = interfaceString(state["proxy_ip"], "")
	}

	var cachedCountries []AimiliCountry
	if err := s.readHostJSON(aimiliCountriesPath, &cachedCountries); err == nil {
		status.Countries = cachedCountries
	}

	var nodes []map[string]interface{}
	if err := s.readHostJSON(aimiliNodesPath, &nodes); err == nil {
		liveCountries := aimiliCountriesFromNodes(nodes)
		if len(status.Countries) == 0 && len(liveCountries) > 0 {
			status.Countries = liveCountries
		}
		activeID := interfaceString(state["active_openvpn_node_id"], "")
		for _, node := range nodes {
			if activeID != "" && interfaceString(node["id"], "") == activeID {
				status.Connected = true
				status.ActiveCountry = interfaceString(node["country"], "")
				if status.ActiveIP == "" {
					status.ActiveIP = interfaceString(node["ip"], interfaceString(node["remote_host"], ""))
				}
				break
			}
		}
	}

	status.Ready = status.Running && status.ProxyReady && status.Connected
	if status.Installed && !status.Running {
		status.Error = "Aimili VPN 服务未运行"
	} else if status.Running && !status.Connected {
		if status.RoutingMode == "fixed_region" && status.Country != "" && strings.Contains(status.LastCheckMessage, "没有可用") {
			status.Error = fmt.Sprintf("%s当前没有可用节点，请刷新地区列表、改选其他地区或使用自动选区", status.Country)
		} else if status.LastCheckMessage != "" {
			status.Error = status.LastCheckMessage
		} else {
			status.Error = "Aimili VPN 正在获取并连接可用节点"
		}
	} else if status.Running && !status.ProxyReady {
		status.Error = "Aimili VPN 本地代理端口未就绪"
	}
	return status
}

func (s *AimiliService) Install() error {
	config := map[string]interface{}{}
	hasConfig := s.readHostJSON(aimiliConfigPath, &config) == nil
	if err := s.installHostDependencies(); err != nil {
		return err
	}
	if err := s.configureOpenVPNAppArmor(); err != nil {
		return err
	}
	if err := s.deployBundledSource(); err != nil {
		return err
	}
	if !hasConfig {
		return s.Configure("")
	}
	if err := s.ensureCertificateChain(); err != nil {
		return err
	}
	if _, err := s.runOnHost("systemctl", "restart", "aimilivpn.service"); err != nil {
		return fmt.Errorf("重启 Aimili VPN 失败: %v", err)
	}
	return nil
}

func (s *AimiliService) installHostDependencies() error {
	script := `set -e
missing=0
for command in openvpn python3 ip iptables pkill curl openssl systemctl; do
  command -v "$command" >/dev/null 2>&1 || missing=1
done
[ "$missing" -eq 0 ] && exit 0
. /etc/os-release
case "${ID:-}" in
  ubuntu|debian)
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -q
    apt-get install -y openvpn curl ca-certificates iptables iproute2 psmisc python3 openssl
    ;;
  alpine)
    apk add --no-cache openvpn curl ca-certificates iptables iproute2 psmisc python3 openssl
    ;;
  centos|rhel|rocky|almalinux|fedora|ol|amzn)
    manager=yum
    command -v dnf >/dev/null 2>&1 && manager=dnf
    "$manager" install -y openvpn curl ca-certificates iptables iproute psmisc python3 openssl ||
      "$manager" install -y openvpn curl ca-certificates iptables iproute2 psmisc python3 openssl
    ;;
  *)
    echo "不支持的宿主机系统: ${ID:-unknown}" >&2
    exit 1
    ;;
esac`
	if output, err := s.runOnHost("bash", "-c", script); err != nil {
		return fmt.Errorf("安装 Aimili VPN 宿主机依赖失败: %s", strings.TrimSpace(output))
	}
	return nil
}

func (s *AimiliService) configureOpenVPNAppArmor() error {
	script := `set -e
profile=""
local_rule=""
if [ -f /etc/apparmor.d/openvpn ]; then
  profile=/etc/apparmor.d/openvpn
  local_rule=/etc/apparmor.d/local/openvpn
elif [ -f /etc/apparmor.d/usr.sbin.openvpn ]; then
  profile=/etc/apparmor.d/usr.sbin.openvpn
  local_rule=/etc/apparmor.d/local/usr.sbin.openvpn
fi
[ -z "$profile" ] && exit 0
command -v apparmor_parser >/dev/null 2>&1 || exit 0
mkdir -p /etc/apparmor.d/local
cat > "$local_rule" <<'EOF'
# Managed by proxy_version Aimili VPN
/opt/aimilivpn/** r,
EOF
apparmor_parser -r "$profile"`
	if output, err := s.runOnHost("bash", "-c", script); err != nil {
		return fmt.Errorf("配置 OpenVPN AppArmor 访问规则失败: %s", strings.TrimSpace(output))
	}
	return nil
}

func (s *AimiliService) deployBundledSource() error {
	files := []struct {
		source string
		target string
		mode   string
	}{
		{"aimili_bundle/vpngate_manager.py", aimiliInstallDir + "/vpngate_manager.py", "0755"},
		{"aimili_bundle/proxy_server.py", aimiliInstallDir + "/proxy_server.py", "0644"},
		{"aimili_bundle/vpn_utils.py", aimiliInstallDir + "/vpn_utils.py", "0644"},
		{"aimili_bundle/LICENSE", aimiliInstallDir + "/LICENSE", "0644"},
	}
	for _, file := range files {
		data, err := aimiliBundle.ReadFile(file.source)
		if err != nil {
			return fmt.Errorf("读取内置 Aimili 文件失败: %v", err)
		}
		if err := s.writeHostFile(file.target, data, file.mode); err != nil {
			return fmt.Errorf("部署内置 Aimili 文件失败: %v", err)
		}
	}
	if err := s.writeHostFile(aimiliInstallDir+"/.proxy_version_bundle", []byte(aimiliBundleVersion+"\n"), "0644"); err != nil {
		return fmt.Errorf("写入 Aimili 版本标记失败: %v", err)
	}

	unit := `[Unit]
Description=AimiliVPN OpenVPN Manager with HTTP/SOCKS5 Proxy
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/opt/aimilivpn
ExecStart=/usr/bin/python3 /opt/aimilivpn/vpngate_manager.py
Restart=always
RestartSec=5
EnvironmentFile=-/etc/default/aimilivpn

[Install]
WantedBy=multi-user.target
`
	if err := s.writeHostFile("/etc/systemd/system/aimilivpn.service", []byte(unit), "0644"); err != nil {
		return fmt.Errorf("部署 Aimili systemd 服务失败: %v", err)
	}
	if output, err := s.runOnHost("bash", "-c", "mkdir -p /opt/aimilivpn/vpngate_data && systemctl daemon-reload && systemctl enable aimilivpn.service"); err != nil {
		return fmt.Errorf("启用 Aimili VPN 服务失败: %s", strings.TrimSpace(output))
	}
	return nil
}

func (s *AimiliService) ensureCertificateChain() error {
	script := `set -e
cert_dir=/opt/aimilivpn/vpngate_data/certs
chain_file=$cert_dir/letsencrypt-current-chain.pem
mkdir -p "$cert_dir"
if [ ! -s "$chain_file" ]; then
  curl -fsSL http://yr1.i.lencr.org/ -o "$cert_dir/yr1.der"
  curl -fsSL http://yr.i.lencr.org/ -o "$cert_dir/yr.der"
  openssl x509 -inform DER -in "$cert_dir/yr1.der" -out "$cert_dir/yr1.pem"
  openssl x509 -inform DER -in "$cert_dir/yr.der" -out "$cert_dir/yr.pem"
  cat "$cert_dir/yr1.pem" "$cert_dir/yr.pem" > "$chain_file"
  chmod 600 "$chain_file"
fi`
	if output, err := s.runOnHost("bash", "-c", script); err != nil {
		return fmt.Errorf("准备 Aimili VPN 证书链失败: %s", strings.TrimSpace(output))
	}
	return nil
}

func (s *AimiliService) RefreshCountries() error {
	if _, err := s.runOnHost("test", "-f", aimiliConfigPath); err != nil {
		return fmt.Errorf("Aimili VPN 未安装")
	}
	if err := s.ensureCertificateChain(); err != nil {
		return err
	}
	if err := s.deployBundledSource(); err != nil {
		return err
	}
	script := `import http.cookiejar
import json
import os
import time
import urllib.request

config_path = "/opt/aimilivpn/vpngate_data/ui_auth.json"
with open(config_path, encoding="utf-8") as handle:
    config = json.load(handle)
port = int(config.get("port", 8787))
secret = str(config.get("secret_path", "")).strip("/")
base = "http://127.0.0.1:%d/%s/api" % (port, secret)
opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

def post(path, payload, timeout=15):
    request = urllib.request.Request(
        base + path,
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with opener.open(request, timeout=timeout) as response:
        result = json.loads(response.read().decode("utf-8") or "{}")
    if not result.get("ok"):
        raise RuntimeError(result.get("error") or "Aimili API request failed")
    return result

post("/login", {"username": config.get("username", ""), "password": config.get("password", "")})
marker = "/opt/aimilivpn/vpngate_data/proxy_version_full_scan"
state_path = "/opt/aimilivpn/vpngate_data/state.json"
wait_deadline = time.time() + 300
while time.time() < wait_deadline:
    try:
        with open(state_path, encoding="utf-8") as handle:
            existing_state = json.load(handle)
    except (FileNotFoundError, json.JSONDecodeError):
        existing_state = {}
    if not existing_state.get("is_connecting", False):
        break
    time.sleep(2)
else:
    raise TimeoutError("等待现有 Aimili 节点检测结束超时")
baseline = float(existing_state.get("last_check_at") or 0)
open(marker, "w", encoding="utf-8").close()
try:
    post("/refresh_nodes", {})
    deadline = time.time() + 300
    while time.time() < deadline:
        time.sleep(2)
        try:
            with open(state_path, encoding="utf-8") as handle:
                state = json.load(handle)
        except (FileNotFoundError, json.JSONDecodeError):
            continue
        if float(state.get("last_check_at") or 0) > baseline and not state.get("is_connecting", False):
            break
    else:
        raise TimeoutError("Aimili 全量可用性检测超时")
finally:
    try:
        os.unlink(marker)
    except FileNotFoundError:
        pass

nodes_path = "/opt/aimilivpn/vpngate_data/nodes.json"
cache_path = "/opt/aimilivpn/vpngate_data/proxy_version_available_countries.json"
with open(nodes_path, encoding="utf-8") as handle:
    nodes = json.load(handle)
counts = {}
for node in nodes:
    if node.get("probe_status") != "available":
        continue
    country = str(node.get("country") or "").strip()
    if country:
        counts[country] = counts.get(country, 0) + 1
countries = [{"name": name, "count": counts[name]} for name in sorted(counts)]
with open(cache_path, "w", encoding="utf-8") as handle:
    json.dump(countries, handle, ensure_ascii=False, indent=2)
`
	if output, err := s.runOnHost("python3", "-c", script); err != nil {
		return fmt.Errorf("触发 Aimili 节点刷新失败: %s", strings.TrimSpace(output))
	}
	return nil
}

func (s *AimiliService) Configure(country string) error {
	if _, err := s.runOnHost("test", "-f", aimiliInstallDir+"/vpngate_manager.py"); err != nil {
		return fmt.Errorf("请先安装 Aimili VPN")
	}
	if err := s.ensureCertificateChain(); err != nil {
		return err
	}
	if err := s.deployBundledSource(); err != nil {
		return err
	}
	country = strings.TrimSpace(country)
	if len([]rune(country)) > 80 {
		return fmt.Errorf("国家或地区名称过长")
	}
	if country != "" {
		var available []AimiliCountry
		if err := s.readHostJSON(aimiliCountriesPath, &available); err != nil {
			return fmt.Errorf("请先刷新可用地区列表")
		}
		found := false
		for _, item := range available {
			if item.Name == country {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%s当前不在可用地区列表中，请刷新列表或选择自动选区", country)
		}
	}

	config := map[string]interface{}{}
	_ = s.readHostJSON(aimiliConfigPath, &config)
	config["host"] = "127.0.0.1"
	if interfaceInt(config["port"], 0) == 0 {
		config["port"] = 8787
	}
	if interfaceInt(config["proxy_port"], 0) == 0 {
		config["proxy_port"] = 7928
	}
	config["connection_enabled"] = true
	if country == "" {
		config["routing_mode"] = "auto"
		config["force_country"] = ""
	} else {
		config["routing_mode"] = "fixed_region"
		config["force_country"] = country
	}
	if err := s.writeHostJSON(aimiliConfigPath, config); err != nil {
		return err
	}
	if _, err := s.runOnHost("systemctl", "restart", "aimilivpn.service"); err != nil {
		return fmt.Errorf("重启 Aimili VPN 失败: %v", err)
	}
	return nil
}

func (s *AimiliService) ValidateReady() error {
	status := s.GetStatus()
	if !status.Installed {
		return fmt.Errorf("Aimili VPN 未安装，请先在系统设置中安装")
	}
	if !status.Ready {
		if status.Error != "" {
			return fmt.Errorf("%s", status.Error)
		}
		return fmt.Errorf("Aimili VPN 尚未就绪")
	}
	return nil
}

func (s *AimiliService) GenerateSingBoxOutbound() (map[string]interface{}, error) {
	status := s.GetStatus()
	if !status.Ready {
		if status.Error != "" {
			return nil, fmt.Errorf("%s", status.Error)
		}
		return nil, fmt.Errorf("Aimili VPN 尚未就绪")
	}
	return buildAimiliOutbound(status.ProxyHost, status.ProxyPort), nil
}

func buildAimiliOutbound(host string, port int) map[string]interface{} {
	return map[string]interface{}{
		"type":        "socks",
		"tag":         "aimili-out",
		"server":      host,
		"server_port": port,
		"version":     "5",
	}
}

func aimiliCountriesFromNodes(nodes []map[string]interface{}) []AimiliCountry {
	counts := map[string]int{}
	for _, node := range nodes {
		if interfaceString(node["probe_status"], "") != "available" {
			continue
		}
		country := strings.TrimSpace(interfaceString(node["country"], ""))
		if country != "" {
			counts[country]++
		}
	}
	countries := make([]AimiliCountry, 0, len(counts))
	for name, count := range counts {
		countries = append(countries, AimiliCountry{Name: name, Count: count})
	}
	sort.Slice(countries, func(i, j int) bool { return countries[i].Name < countries[j].Name })
	return countries
}

func interfaceString(value interface{}, fallback string) string {
	if text, ok := value.(string); ok && text != "" {
		return text
	}
	return fallback
}

func interfaceBool(value interface{}) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case float64:
		return typed != 0
	case int:
		return typed != 0
	case string:
		parsed, _ := strconv.ParseBool(typed)
		return parsed
	}
	return false
}

func interfaceInt64(value interface{}) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case int64:
		return typed
	case int:
		return int64(typed)
	case json.Number:
		parsed, _ := strconv.ParseInt(typed.String(), 10, 64)
		return parsed
	}
	return 0
}

func interfaceInt(value interface{}, fallback int) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case json.Number:
		if parsed, err := strconv.Atoi(typed.String()); err == nil {
			return parsed
		}
	case string:
		if parsed, err := strconv.Atoi(typed); err == nil {
			return parsed
		}
	}
	return fallback
}
