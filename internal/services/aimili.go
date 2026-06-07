package services

import (
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
)

type AimiliCountry struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type AimiliStatus struct {
	Installed        bool            `json:"installed"`
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

func (s *AimiliService) runOnHost(command string, args ...string) (string, error) {
	fullArgs := append([]string{"-t", "1", "-m", "-u", "-i", "-n", command}, args...)
	output, err := exec.Command("nsenter", fullArgs...).CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("%s: %s", err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
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
	script := `set -e
curl -fsSL https://raw.githubusercontent.com/baoweise-bot/aimili-vpngate/main/install.sh -o /tmp/aimilivpn-install.sh
bash /tmp/aimilivpn-install.sh
rm -f /tmp/aimilivpn-install.sh`
	if output, err := s.runOnHost("bash", "-c", script); err != nil {
		return fmt.Errorf("安装 Aimili VPN 失败: %s", strings.TrimSpace(output))
	}
	return s.Configure("")
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
	if err := s.applyCompatibilityPatch(); err != nil {
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

func (s *AimiliService) applyCompatibilityPatch() error {
	script := `from pathlib import Path

path = Path("/opt/aimilivpn/vpngate_manager.py")
text = path.read_text(encoding="utf-8")
marker = "# proxy_version fixed-region compatibility patch"
if marker not in text:
    old_test = '''            to_test = [n for n in current_nodes if not n.get("active")]
            to_test_ids = [n["id"] for n in to_test]'''
    new_test = '''            to_test = [n for n in current_nodes if not n.get("active")]
            # proxy_version fixed-region compatibility patch
            routing_cfg = load_ui_config()
            # proxy_version full availability scan patch
            full_scan = Path("/opt/aimilivpn/vpngate_data/proxy_version_full_scan").exists()
            if not full_scan and routing_cfg.get("routing_mode") == "fixed_region" and routing_cfg.get("force_country"):
                target_country = routing_cfg.get("force_country")
                to_test = [
                    n for n in to_test
                    if n.get("country") == target_country
                    or vpn_utils.COUNTRY_TRANSLATIONS.get(n.get("country", ""), n.get("country", "")) == target_country
                ]
            to_test_ids = [n["id"] for n in to_test]'''
    old_retry = '''        def bg_fetch_and_switch():
            try:
                maintain_valid_nodes(force=False)
                auto_switch_node()
            except Exception as e:
                print(f"[自动切换后台补齐] 获取并测试节点失败: {e}", flush=True)

        threading.Thread(target=bg_fetch_and_switch, daemon=True).start()'''
    new_retry = '''        # proxy_version: collector_loop and manual refresh own retries.
        # Avoid recursive refresh threads when a selected region has no live nodes.'''
    if old_test not in text or old_retry not in text:
        raise RuntimeError("Aimili 源码版本不匹配，无法应用固定地区兼容补丁")
    text = text.replace(old_test, new_test, 1).replace(old_retry, new_retry, 1)

status_marker = "# proxy_version no-candidate status patch"
if status_marker not in text:
    old_status = '''                        if available_candidates:
                            auto_switch_node()

        valid_nodes_count = len([n for n in merged if n.get("probe_status") == "available"])
        message = f"Fetched {len(candidates)} nodes. Tested first 10 nodes."'''
    new_status = '''                        if available_candidates:
                            auto_switch_node()
                        elif routing_mode == "fixed_region" and target_country:
                            # proxy_version no-candidate status patch
                            message = f"没有可用的【{target_country}】节点"
                            set_state(
                                active_openvpn_node_id="",
                                is_connecting=False,
                                last_check_message=message,
                            )

        valid_nodes_count = len([n for n in merged if n.get("probe_status") == "available"])
        message = read_json(STATE_FILE, {}).get("last_check_message", "")
        if "没有可用" not in message:
            message = f"Fetched {len(candidates)} nodes. Tested {len(to_test_ids)} nodes."'''
    if old_status not in text:
        raise RuntimeError("Aimili 源码版本不匹配，无法应用无可用节点状态补丁")
    text = text.replace(old_status, new_status, 1)

scan_marker = "# proxy_version full availability scan patch"
if scan_marker not in text:
    old_filter = '''            if routing_cfg.get("routing_mode") == "fixed_region" and routing_cfg.get("force_country"):
                target_country = routing_cfg.get("force_country")'''
    new_filter = '''            # proxy_version full availability scan patch
            full_scan = Path("/opt/aimilivpn/vpngate_data/proxy_version_full_scan").exists()
            if not full_scan and routing_cfg.get("routing_mode") == "fixed_region" and routing_cfg.get("force_country"):
                target_country = routing_cfg.get("force_country")'''
    if old_filter not in text:
        raise RuntimeError("Aimili 源码版本不匹配，无法应用全量可用地区检测补丁")
    text = text.replace(old_filter, new_filter, 1)


chain_marker = "# proxy_version missing intermediate certificate patch"
if chain_marker not in text:
    old_decode = '''                    config_text = decode_config(encoded)
                    node = row_to_node(row, config_text)'''
    new_decode = '''                    config_text = decode_config(encoded)
                    # proxy_version missing intermediate certificate patch
                    chain_path = Path("/opt/aimilivpn/vpngate_data/certs/letsencrypt-current-chain.pem")
                    is_isrg_x1_config = "MIIFazCCA1OgAwIBAgIRAIIQz7DSQONZRGPgu2OCiw" in config_text
                    if is_isrg_x1_config and chain_path.exists() and "proxy_version-letsencrypt-chain" not in config_text:
                        chain = chain_path.read_text(encoding="utf-8")
                        config_text = config_text.replace(
                            "</ca>",
                            "# proxy_version-letsencrypt-chain\\n" + chain + "\\n</ca>",
                            1,
                        )
                    node = row_to_node(row, config_text)'''
    if old_decode not in text:
        raise RuntimeError("Aimili 源码版本不匹配，无法应用证书链兼容补丁")
    text = text.replace(old_decode, new_decode, 1)

path.write_text(text, encoding="utf-8")
`
	if output, err := s.runOnHost("python3", "-c", script); err != nil {
		return fmt.Errorf("应用 Aimili 固定地区兼容补丁失败: %s", strings.TrimSpace(output))
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
	if err := s.applyCompatibilityPatch(); err != nil {
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
