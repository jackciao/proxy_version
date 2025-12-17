package services

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type DetectorService struct{}

func NewDetectorService() *DetectorService {
	return &DetectorService{}
}

type SystemStatus struct {
	OS               string `json:"os"`
	Arch             string `json:"arch"`
	Hostname         string `json:"hostname"`
	XrayInstalled    bool   `json:"xray_installed"`
	XrayVersion      string `json:"xray_version,omitempty"`
	SingBoxInstalled bool   `json:"singbox_installed"`
	SingBoxVersion   string `json:"singbox_version,omitempty"`
}

type ReverseProxyResult struct {
	NginxInstalled      bool   `json:"nginx_installed"`
	NginxVersion        string `json:"nginx_version,omitempty"`
	NginxPath           string `json:"nginx_path,omitempty"`
	NginxConfigPath     string `json:"nginx_config_path,omitempty"`
	OpenRestyInstalled  bool   `json:"openresty_installed"`
	OpenRestyVersion    string `json:"openresty_version,omitempty"`
	OpenRestyPath       string `json:"openresty_path,omitempty"`
	OpenRestyConfigPath string `json:"openresty_config_path,omitempty"`
	OpenRestyContainer  string `json:"openresty_container,omitempty"`
	PanelDetected       string `json:"panel_detected,omitempty"`
	PanelType           string `json:"panel_type,omitempty"`
	Recommendation      string `json:"recommendation"`
	CanUseExisting      bool   `json:"can_use_existing"`
	ProxyType           string `json:"proxy_type,omitempty"`
}

func (d *DetectorService) GetSystemStatus() SystemStatus {
	hostname, _ := os.Hostname()

	status := SystemStatus{
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Hostname: hostname,
	}

	// Check Xray
	if xrayPath := d.findExecutable("xray", []string{"/etc/v2ray-agent/xray/xray", "/usr/local/bin/xray", "/usr/bin/xray"}); xrayPath != "" {
		status.XrayInstalled = true
		if version, err := exec.Command(xrayPath, "version").Output(); err == nil {
			lines := strings.Split(string(version), "\n")
			if len(lines) > 0 {
				parts := strings.Fields(lines[0])
				if len(parts) >= 2 {
					status.XrayVersion = parts[1]
				}
			}
		}
	}

	// Check sing-box
	if singboxPath := d.findExecutable("sing-box", []string{"/etc/v2ray-agent/sing-box/sing-box", "/usr/local/bin/sing-box", "/usr/bin/sing-box"}); singboxPath != "" {
		status.SingBoxInstalled = true
		if version, err := exec.Command(singboxPath, "version").Output(); err == nil {
			lines := strings.Split(string(version), "\n")
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
	}

	return status
}

func (d *DetectorService) DetectReverseProxy() ReverseProxyResult {
	result := ReverseProxyResult{}

	// ==== Detect 1Panel ====
	if _, err := os.Stat("/opt/1panel"); err == nil {
		result.PanelDetected = "1Panel"
		result.PanelType = "1panel"

		// 1Panel runs OpenResty in Docker container
		// Check for running 1Panel-openresty container
		if containerName := d.find1PanelOpenRestyContainer(); containerName != "" {
			result.OpenRestyInstalled = true
			result.OpenRestyContainer = containerName
			result.OpenRestyPath = "docker:" + containerName

			// Get version from container
			if version, err := exec.Command("docker", "exec", containerName, "openresty", "-v").CombinedOutput(); err == nil {
				d.parseOpenRestyVersion(&result, string(version))
			}

			// 1Panel OpenResty config path (mapped from container)
			result.OpenRestyConfigPath = "/opt/1panel/apps/openresty/" + strings.TrimPrefix(containerName, "1Panel-openresty-") + "/conf"
			
			// Try to find actual config path
			if entries, err := filepath.Glob("/opt/1panel/apps/openresty/*/conf"); err == nil && len(entries) > 0 {
				result.OpenRestyConfigPath = entries[0]
			}
		}
	}

	// ==== Detect aapanel/宝塔 ====
	if _, err := os.Stat("/www/server/panel"); err == nil {
		result.PanelDetected = "aapanel/宝塔面板"
		result.PanelType = "aapanel"

		// aapanel installs Nginx directly on host
		nginxPath := "/www/server/nginx/sbin/nginx"
		if _, err := os.Stat(nginxPath); err == nil {
			result.NginxInstalled = true
			result.NginxPath = nginxPath

			if version, err := exec.Command(nginxPath, "-v").CombinedOutput(); err == nil {
				d.parseNginxVersion(&result, string(version))
			}
			result.NginxConfigPath = "/www/server/nginx/conf"
		}
	}

	// ==== Detect standard OpenResty (if not found via panel) ====
	if !result.OpenRestyInstalled {
		// Check for OpenResty Docker containers (generic)
		if containerName := d.findOpenRestyContainer(); containerName != "" {
			result.OpenRestyInstalled = true
			result.OpenRestyContainer = containerName
			result.OpenRestyPath = "docker:" + containerName

			if version, err := exec.Command("docker", "exec", containerName, "openresty", "-v").CombinedOutput(); err == nil {
				d.parseOpenRestyVersion(&result, string(version))
			}
		}

		// Check for host-installed OpenResty
		if !result.OpenRestyInstalled {
			standardOpenRestyPaths := []string{
				"/usr/local/openresty/bin/openresty",
				"/usr/local/openresty/nginx/sbin/nginx",
				"/usr/bin/openresty",
			}

			for _, orPath := range standardOpenRestyPaths {
				if _, err := os.Stat(orPath); err == nil {
					result.OpenRestyInstalled = true
					result.OpenRestyPath = orPath

					if version, err := exec.Command(orPath, "-v").CombinedOutput(); err == nil {
						d.parseOpenRestyVersion(&result, string(version))
					}

					if _, err := os.Stat("/usr/local/openresty/nginx/conf/nginx.conf"); err == nil {
						result.OpenRestyConfigPath = "/usr/local/openresty/nginx/conf"
					}
					break
				}
			}
		}
	}

	// ==== Detect standard Nginx (if not found via panel) ====
	if !result.NginxInstalled {
		standardNginxPaths := []string{
			"/usr/sbin/nginx",
			"/usr/bin/nginx",
			"/usr/local/nginx/sbin/nginx",
		}

		for _, nginxPath := range standardNginxPaths {
			if _, err := os.Stat(nginxPath); err == nil {
				result.NginxInstalled = true
				result.NginxPath = nginxPath

				if version, err := exec.Command(nginxPath, "-v").CombinedOutput(); err == nil {
					d.parseNginxVersion(&result, string(version))
				}

				configPaths := []string{"/etc/nginx", "/usr/local/nginx/conf"}
				for _, cp := range configPaths {
					if _, err := os.Stat(filepath.Join(cp, "nginx.conf")); err == nil {
						result.NginxConfigPath = cp
						break
					}
				}
				break
			}
		}
	}

	// ==== Generate recommendation ====
	if result.OpenRestyInstalled {
		result.ProxyType = "openresty"
		result.CanUseExisting = true
		if result.OpenRestyContainer != "" {
			result.Recommendation = "检测到 " + result.PanelDetected + " 的 OpenResty (容器: " + result.OpenRestyContainer + ")，将使用现有配置"
		} else if result.PanelDetected != "" {
			result.Recommendation = "检测到 " + result.PanelDetected + " 的 OpenResty，将使用现有配置"
		} else {
			result.Recommendation = "检测到 OpenResty，建议使用现有反向代理"
		}
	} else if result.NginxInstalled {
		result.ProxyType = "nginx"
		result.CanUseExisting = true
		if result.PanelDetected != "" {
			result.Recommendation = "检测到 " + result.PanelDetected + " 的 Nginx，将使用现有配置"
		} else {
			result.Recommendation = "检测到 Nginx，建议使用现有反向代理"
		}
	} else {
		result.ProxyType = "none"
		result.CanUseExisting = false
		if result.PanelDetected != "" {
			result.Recommendation = "检测到 " + result.PanelDetected + "，但未找到反向代理，请在面板中安装 OpenResty 或 Nginx"
		} else {
			result.Recommendation = "未检测到反向代理，创建节点时将自动安装 Nginx"
		}
	}

	return result
}

// find1PanelOpenRestyContainer finds 1Panel's OpenResty container
func (d *DetectorService) find1PanelOpenRestyContainer() string {
	output, err := exec.Command("docker", "ps", "--format", "{{.Names}}", "--filter", "name=1Panel-openresty").Output()
	if err != nil {
		return ""
	}
	
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "1Panel-openresty") {
			return line
		}
	}
	return ""
}

// findOpenRestyContainer finds any OpenResty container
func (d *DetectorService) findOpenRestyContainer() string {
	output, err := exec.Command("docker", "ps", "--format", "{{.Names}}\t{{.Image}}").Output()
	if err != nil {
		return ""
	}
	
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		parts := strings.Split(line, "\t")
		if len(parts) >= 2 {
			if strings.Contains(strings.ToLower(parts[1]), "openresty") {
				return parts[0]
			}
		}
	}
	return ""
}

func (d *DetectorService) parseNginxVersion(result *ReverseProxyResult, output string) {
	if idx := strings.Index(output, "nginx/"); idx != -1 {
		versionStr := output[idx+6:]
		if endIdx := strings.IndexAny(versionStr, " \n"); endIdx != -1 {
			result.NginxVersion = versionStr[:endIdx]
		} else {
			result.NginxVersion = strings.TrimSpace(versionStr)
		}
	}
}

func (d *DetectorService) parseOpenRestyVersion(result *ReverseProxyResult, output string) {
	if idx := strings.Index(output, "openresty/"); idx != -1 {
		versionStr := output[idx+10:]
		if endIdx := strings.IndexAny(versionStr, " \n"); endIdx != -1 {
			result.OpenRestyVersion = versionStr[:endIdx]
		} else {
			result.OpenRestyVersion = strings.TrimSpace(versionStr)
		}
	} else if idx := strings.Index(output, "nginx/"); idx != -1 {
		versionStr := output[idx+6:]
		if endIdx := strings.IndexAny(versionStr, " \n"); endIdx != -1 {
			result.OpenRestyVersion = versionStr[:endIdx]
		} else {
			result.OpenRestyVersion = strings.TrimSpace(versionStr)
		}
	}
}

func (d *DetectorService) findExecutable(name string, paths []string) string {
	if path, err := exec.LookPath(name); err == nil {
		return path
	}

	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	return ""
}

func (d *DetectorService) GetProxyExecutable() (string, string) {
	result := d.DetectReverseProxy()

	if result.OpenRestyInstalled && result.OpenRestyPath != "" {
		return result.OpenRestyPath, "openresty"
	}

	if result.NginxInstalled && result.NginxPath != "" {
		return result.NginxPath, "nginx"
	}

	return "", "none"
}

func (d *DetectorService) GetProxyConfigPath() string {
	result := d.DetectReverseProxy()

	if result.OpenRestyInstalled && result.OpenRestyConfigPath != "" {
		return result.OpenRestyConfigPath
	}

	if result.NginxInstalled && result.NginxConfigPath != "" {
		return result.NginxConfigPath
	}

	return "/etc/nginx"
}

// GetOpenRestyContainer returns the OpenResty container name if running in Docker
func (d *DetectorService) GetOpenRestyContainer() string {
	result := d.DetectReverseProxy()
	return result.OpenRestyContainer
}
