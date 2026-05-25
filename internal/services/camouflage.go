package services

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CamouflageService manages the StreamVault disguise site via 1Panel OpenResty
type CamouflageService struct {
	containerName string // 1Panel OpenResty container name
	hostBasePath  string // e.g. /opt/1panel/apps/openresty/openresty
	hostConfDir   string // host directory included as OpenResty conf.d
	hostWWWDir    string // host directory mounted to /www
	hostSSLDir    string // host directory mounted to OpenResty ssl dir
	hostLogDir    string // host directory for OpenResty logs
}

type dockerMount struct {
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
}

type mediaItem struct {
	Title  string
	Year   string
	Rating string
	Poster string
}

type tvMazeShow struct {
	Name      string `json:"name"`
	Premiered string `json:"premiered"`
	Rating    struct {
		Average *float64 `json:"average"`
	} `json:"rating"`
	Image *struct {
		Medium   string `json:"medium"`
		Original string `json:"original"`
	} `json:"image"`
}

// CamouflageStatus represents the deployment status
type CamouflageStatus struct {
	Deployed      bool   `json:"deployed"`
	Domain        string `json:"domain"`
	URL           string `json:"url,omitempty"`
	ContainerName string `json:"container_name,omitempty"`
	Error         string `json:"error,omitempty"`
}

// NewCamouflageService creates a new service, auto-detecting 1Panel OpenResty
func NewCamouflageService() *CamouflageService {
	s := &CamouflageService{}
	s.detect()
	return s
}

// detect finds the 1Panel OpenResty container and its host base path
func (s *CamouflageService) detect() {
	detector := NewDetectorService()

	// Find 1Panel OpenResty container
	s.containerName = detector.find1PanelOpenRestyContainer()
	if s.containerName == "" {
		// Try generic OpenResty container
		s.containerName = detector.findOpenRestyContainer()
	}

	// Find the host base path for OpenResty
	s.hostBasePath = s.findOpenRestyBaseDir()
	s.detectOpenRestyMounts()
}

// detectOpenRestyMounts locates the host directories mounted into OpenResty.
func (s *CamouflageService) detectOpenRestyMounts() {
	if s.hostBasePath != "" {
		s.hostWWWDir = filepath.Join(s.hostBasePath, "www")
		s.hostConfDir = filepath.Join(s.hostBasePath, "conf", "conf.d")
		if vhostDir := filepath.Join(s.hostBasePath, "conf", "vhost"); pathExists(vhostDir) {
			s.hostConfDir = vhostDir
		}
		s.hostSSLDir = filepath.Join(s.hostBasePath, "conf", "ssl")
		s.hostLogDir = filepath.Join(s.hostBasePath, "logs")
		if logDir := filepath.Join(s.hostBasePath, "log"); pathExists(logDir) {
			s.hostLogDir = logDir
		}
	}

	if s.containerName != "" {
		cmd := exec.Command("docker", "inspect", s.containerName, "--format", "{{json .Mounts}}")
		if output, err := cmd.Output(); err == nil {
			var mounts []dockerMount
			if json.Unmarshal(output, &mounts) == nil {
				for _, mount := range mounts {
					switch filepath.Clean(mount.Destination) {
					case "/www":
						s.hostWWWDir = mount.Source
					case "/usr/local/openresty/nginx/conf/conf.d":
						s.hostConfDir = mount.Source
					case "/usr/local/openresty/nginx/conf/ssl":
						s.hostSSLDir = mount.Source
					case "/var/log/nginx", "/usr/local/openresty/nginx/logs":
						s.hostLogDir = mount.Source
					}
				}
			}
		}
	}

	if s.hostConfDir == "" && pathExists("/opt/1panel/www/conf.d") {
		s.hostConfDir = "/opt/1panel/www/conf.d"
	}
	if s.hostWWWDir == "" && pathExists("/opt/1panel/www") {
		s.hostWWWDir = "/opt/1panel/www"
	}
}

func (s *CamouflageService) findOpenRestyBaseDir() string {
	// Try standard 1Panel paths
	patterns := []string{
		"/opt/1panel/apps/openresty/openresty",
		"/opt/1panel/apps/openresty/*/",
	}

	for _, pattern := range patterns {
		if matches, err := filepath.Glob(pattern); err == nil {
			for _, match := range matches {
				// 1Panel usually uses vhost for site configs
				vhostPath := filepath.Join(match, "conf", "vhost")
				if _, err := os.Stat(vhostPath); err == nil {
					return match
				}
				confPath := filepath.Join(match, "conf", "conf.d")
				if _, err := os.Stat(confPath); err == nil {
					return match
				}
			}
		}
	}

	// Direct check
	if _, err := os.Stat("/opt/1panel/apps/openresty/openresty/conf"); err == nil {
		return "/opt/1panel/apps/openresty/openresty"
	}

	return ""
}

// IsAvailable checks if OpenResty is available for deployment
func (s *CamouflageService) IsAvailable() bool {
	return s.containerName != "" && s.hostBasePath != "" && s.hostConfDir != "" && s.hostWWWDir != "" && s.hostSSLDir != ""
}

// DeployCamouflage deploys the StreamVault disguise site for a domain
func (s *CamouflageService) DeployCamouflage(domain, certPath, keyPath string) error {
	if err := validateCamouflageDomain(domain); err != nil {
		return err
	}
	if !s.IsAvailable() {
		return fmt.Errorf("1Panel OpenResty 未检测到，无法部署伪装站")
	}

	// Step 1: Create site directory structure
	if err := s.createSiteDir(domain); err != nil {
		return fmt.Errorf("创建站点目录失败: %v", err)
	}

	// Step 2: Deploy static HTML files
	if err := s.deploySiteHTML(domain); err != nil {
		return fmt.Errorf("部署页面文件失败: %v", err)
	}

	// Step 3: Copy certificates to OpenResty accessible path
	if err := s.copyCertsToOpenResty(domain, certPath, keyPath); err != nil {
		return fmt.Errorf("复制证书失败: %v", err)
	}

	// Step 4: Create Nginx/OpenResty configuration
	if err := s.createNginxConfig(domain); err != nil {
		return fmt.Errorf("创建站点配置失败: %v", err)
	}

	// Step 5: Make sure OpenResty no longer occupies IPv6 :443 so that
	// VLESS/Reality nodes bound to [::]:443 can start without conflict.
	if err := s.DisableIPv6On443(); err != nil {
		// Non-fatal: log via returned error message but continue
		// since the camouflage site itself does not need IPv6.
		fmt.Printf("[camouflage] 关闭 OpenResty IPv6:443 监听失败: %v\n", err)
	}

	// Step 6: Test and reload OpenResty
	if err := s.reloadOpenResty(); err != nil {
		// Try to roll back config on failure
		s.removeNginxConfig(domain)
		return fmt.Errorf("重载 OpenResty 失败: %v", err)
	}

	return nil
}

// DisableIPv6On443 walks every loaded OpenResty configuration file and
// comments out any "listen [::]:443 ..." (or quic) directive that is
// currently active. The IPv4 :443 listener is preserved so HTTPS keeps
// working, but releases [::]:443 for other services (e.g. sing-box).
//
// The helper is idempotent: previously commented entries are left alone
// and the OpenResty service is reloaded only when files actually change.
func (s *CamouflageService) DisableIPv6On443() error {
	if s.containerName == "" {
		return nil
	}

	// Find candidate config files inside the container.
	listOut, err := exec.Command("docker", "exec", s.containerName, "sh", "-c",
		`grep -RIl --include='*.conf' -E '^[[:space:]]*listen[[:space:]]+\[::\]:443([[:space:]]|;)' /usr/local/openresty/nginx/conf 2>/dev/null || true`).
		CombinedOutput()
	if err != nil {
		return fmt.Errorf("扫描 OpenResty 配置失败: %v: %s", err, string(listOut))
	}

	files := strings.Split(strings.TrimSpace(string(listOut)), "\n")
	changed := false
	for _, f := range files {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		// Comment out any uncommented "listen [::]:443 ..." line.
		// Use sed -E for portable extended regex.
		script := `sed -i -E 's@^([[:space:]]*)(listen[[:space:]]+\[::\]:443[^;]*;)@\1# disabled by proxy_version: \2@' ` + f
		if out, err := exec.Command("docker", "exec", s.containerName, "sh", "-c", script).CombinedOutput(); err != nil {
			return fmt.Errorf("修改 %s 失败: %v: %s", f, err, string(out))
		}
		changed = true
	}

	if !changed {
		return nil
	}

	// Validate config first so we don't break OpenResty.
	if _, err := s.execOpenResty("-t"); err != nil {
		return fmt.Errorf("修改后配置测试失败: %v", err)
	}

	// SIGHUP / "-s reload" does not always close existing listen sockets,
	// so restart the container to be sure [::]:443 is released.
	if out, err := exec.Command("docker", "restart", s.containerName).CombinedOutput(); err != nil {
		// Fall back to a normal reload, which at least applies textual changes.
		if _, rerr := s.execOpenResty("-s", "reload"); rerr != nil {
			return fmt.Errorf("重启容器失败 (%s): %v; 退化重载也失败: %v",
				strings.TrimSpace(string(out)), err, rerr)
		}
	}
	return nil
}

// createSiteDir creates the site directory structure
func (s *CamouflageService) createSiteDir(domain string) error {
	dirs := []string{
		s.siteRootHostPath(domain),
		filepath.Join(s.siteRootHostPath(domain), ".well-known", "acme-challenge"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	return nil
}

// deploySiteHTML writes the private cloud drive camouflage HTML files to every
// site root that may be served by 1Panel/OpenResty for this domain.
func (s *CamouflageService) deploySiteHTML(domain string) error {
	for _, siteRoot := range s.siteRootHostPaths(domain) {
		if err := os.MkdirAll(siteRoot, 0755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(siteRoot, "index.html"), []byte(streamVaultIndexHTML), 0644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(siteRoot, "login.html"), []byte(streamVaultLoginHTML), 0644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(siteRoot, "drive.html"), []byte(streamVaultDriveHTML), 0644); err != nil {
			return err
		}
	}

	return nil
}

// copyCertsToOpenResty copies SSL certificates to a path accessible by OpenResty container
func (s *CamouflageService) copyCertsToOpenResty(domain, certPath, keyPath string) error {
	// Create SSL directory in OpenResty's conf
	sslDir := s.sslHostDir(domain)
	if err := os.MkdirAll(sslDir, 0755); err != nil {
		return err
	}

	// Copy cert file
	certData, err := os.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("读取证书文件失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sslDir, "fullchain.pem"), certData, 0644); err != nil {
		return err
	}

	// Copy key file
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("读取密钥文件失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sslDir, "privkey.pem"), keyData, 0600); err != nil {
		return err
	}

	return nil
}

func (s *CamouflageService) getConfDir() string {
	if s.hostConfDir != "" {
		return s.hostConfDir
	}
	vhostDir := filepath.Join(s.hostBasePath, "conf", "vhost")
	if pathExists(vhostDir) {
		return vhostDir
	}
	return filepath.Join(s.hostBasePath, "conf", "conf.d")
}

// createNginxConfig generates and writes the OpenResty site configuration
func (s *CamouflageService) createNginxConfig(domain string) error {
	confDir := s.getConfDir()
	if err := os.MkdirAll(confDir, 0755); err != nil {
		return err
	}

	certHostPath := filepath.Join(s.sslHostDir(domain), "fullchain.pem")
	keyHostPath := filepath.Join(s.sslHostDir(domain), "privkey.pem")
	siteRootHostPath := s.siteRootHostPath(domain)
	logHostDir := s.hostLogDir
	if logHostDir == "" {
		logHostDir = filepath.Join(s.hostBasePath, "logs")
	}

	certContainerPath := s.containerPathFor(certHostPath, fmt.Sprintf("/usr/local/openresty/nginx/conf/ssl/%s/fullchain.pem", domain))
	keyContainerPath := s.containerPathFor(keyHostPath, fmt.Sprintf("/usr/local/openresty/nginx/conf/ssl/%s/privkey.pem", domain))
	siteRootContainerPath := s.containerPathFor(siteRootHostPath, fmt.Sprintf("/www/sites/%s/index", domain))
	logContainerDir := s.containerPathFor(logHostDir, "/usr/local/openresty/nginx/logs")
	panelBackendURL := s.panelBackendURL()
	nativeConfigUpdated, err := s.updateExistingNginxConfigs(domain, panelBackendURL, certContainerPath, keyContainerPath)
	if err != nil {
		return err
	}
	if nativeConfigUpdated {
		s.removeNginxConfig(domain)
		return nil
	}

	config := fmt.Sprintf(`# Private Drive Camouflage Site - Auto-generated by proxy_version
# Domain: %s

server {
    listen 80;
    server_name %s;

    # ACME challenge for certificate renewal
    location ^~ /.well-known/acme-challenge/ {
        default_type "text/plain";
        root %s;
    }

    # Redirect all HTTP to HTTPS
    location / {
        return 301 https://$host$request_uri;
    }
}

server {
    listen 443 ssl;
    http2 on;
    server_name %s;

    # SSL Configuration
    ssl_certificate %s;
    ssl_certificate_key %s;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384:ECDHE-ECDSA-CHACHA20-POLY1305:ECDHE-RSA-CHACHA20-POLY1305;
    ssl_prefer_server_ciphers off;
    ssl_session_timeout 1d;
    ssl_session_cache shared:SSL:10m;
    ssl_session_tickets off;

    # HSTS
    add_header Strict-Transport-Security "max-age=63072000" always;

    # Site root
    root %s;
    index index.html;

    # Gotee drive upload limits
    client_max_body_size 20g;
    client_body_timeout 3600s;

    # Security headers
    add_header X-Content-Type-Options "nosniff" always;
    add_header X-Frame-Options "SAMEORIGIN" always;
    add_header X-XSS-Protection "1; mode=block" always;
    add_header Referrer-Policy "strict-origin-when-cross-origin" always;

    # Panel API proxy for the private drive login.
    location ^~ /api/ {
        proxy_pass %s/api/;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_connect_timeout 5s;
        proxy_send_timeout 3600s;
        proxy_read_timeout 3600s;
        proxy_request_buffering off;
    }

    # camouflage no-cache html: disable browser cache so updates take effect immediately.
    location ~* \.(html|htm)$ {
        add_header Cache-Control "no-store, no-cache, must-revalidate, max-age=0" always;
        add_header Pragma "no-cache" always;
        add_header Expires "0" always;
        try_files $uri $uri/ =404;
    }

    # Main location
    location / {
        try_files $uri $uri/ =404;
    }

    # Cache static assets
    location ~* \.(css|js|png|jpg|jpeg|gif|ico|svg|woff|woff2|ttf|eot)$ {
        expires 30d;
        add_header Cache-Control "public, immutable";
    }

    # Deny hidden files
    location ~ /\. {
        deny all;
        access_log off;
        log_not_found off;
    }

    # Custom error pages
    error_page 404 /index.html;

    access_log %s/%s.access.log;
    error_log %s/%s.error.log;
}
`, domain, domain, siteRootContainerPath, domain, certContainerPath, keyContainerPath, siteRootContainerPath, panelBackendURL, logContainerDir, domain, logContainerDir, domain)

	confPath := filepath.Join(confDir, fmt.Sprintf("streamvault_%s.conf", strings.ReplaceAll(domain, ".", "_")))
	return os.WriteFile(confPath, []byte(config), 0644)
}

// removeNginxConfig removes the site configuration (for rollback)
func (s *CamouflageService) removeNginxConfig(domain string) {
	confPath := filepath.Join(s.getConfDir(), fmt.Sprintf("streamvault_%s.conf", strings.ReplaceAll(domain, ".", "_")))
	os.Remove(confPath)
}

func (s *CamouflageService) panelBackendURL() string {
	modeOutput, modeErr := exec.Command("docker", "inspect", "-f", "{{.HostConfig.NetworkMode}}", s.containerName).CombinedOutput()
	if modeErr == nil && strings.TrimSpace(string(modeOutput)) == "host" {
		return "http://127.0.0.1:8080"
	}

	output, err := exec.Command("docker", "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.Gateway}} {{end}}", s.containerName).CombinedOutput()
	if err == nil {
		for _, gateway := range strings.Fields(string(output)) {
			if gateway != "" && gateway != "<no value>" {
				return "http://" + gateway + ":8080"
			}
		}
	}
	return "http://172.17.0.1:8080"
}

// reloadOpenResty tests and reloads the OpenResty configuration
func (s *CamouflageService) reloadOpenResty() error {
	if _, err := s.execOpenResty("-t"); err != nil {
		return fmt.Errorf("配置测试失败: %v", err)
	}
	if _, err := s.execOpenResty("-s", "reload"); err != nil {
		return fmt.Errorf("重载失败: %v", err)
	}
	return nil
}

func (s *CamouflageService) execOpenResty(args ...string) (string, error) {
	var lastOutput string
	for _, binary := range []string{"openresty", "nginx"} {
		cmdArgs := append([]string{"exec", s.containerName, binary}, args...)
		output, err := exec.Command("docker", cmdArgs...).CombinedOutput()
		if err == nil {
			return string(output), nil
		}
		lastOutput = string(output)
	}
	message := strings.TrimSpace(lastOutput)
	if message == "" {
		message = "openresty/nginx 命令不可用"
	}
	return lastOutput, fmt.Errorf("%s", message)
}

// GetStatus returns the camouflage deployment status for a domain
func (s *CamouflageService) GetStatus(domain string) CamouflageStatus {
	status := CamouflageStatus{
		Domain:        domain,
		ContainerName: s.containerName,
	}

	if err := validateCamouflageDomain(domain); err != nil {
		status.Error = err.Error()
		return status
	}

	if !s.IsAvailable() {
		status.Error = "1Panel OpenResty 未检测到"
		return status
	}

	// Check if config file exists
	confPath := filepath.Join(s.getConfDir(), fmt.Sprintf("streamvault_%s.conf", strings.ReplaceAll(domain, ".", "_")))
	if _, err := os.Stat(confPath); err == nil {
		// Check if HTML exists
		indexPath := filepath.Join(s.siteRootHostPath(domain), "index.html")
		if _, err := os.Stat(indexPath); err == nil {
			status.Deployed = true
			status.URL = fmt.Sprintf("https://%s", domain)
		}
	}

	return status
}

// CreateWebrootDir creates the webroot directory for ACME HTTP-01 challenge
func (s *CamouflageService) CreateWebrootDir(domain string) (string, error) {
	if err := validateCamouflageDomain(domain); err != nil {
		return "", err
	}
	if s.hostWWWDir == "" {
		return "", fmt.Errorf("OpenResty Web 目录未找到")
	}

	webrootDir := s.siteRootHostPath(domain)
	challengeDir := filepath.Join(webrootDir, ".well-known", "acme-challenge")
	if err := os.MkdirAll(challengeDir, 0755); err != nil {
		return "", err
	}

	return webrootDir, nil
}

// EnsureHTTPServerBlock creates a temporary HTTP server block for ACME validation
func (s *CamouflageService) EnsureHTTPServerBlock(domain string) error {
	if err := validateCamouflageDomain(domain); err != nil {
		return err
	}
	if !s.IsAvailable() {
		return fmt.Errorf("OpenResty 不可用")
	}

	confDir := s.getConfDir()
	confPath := filepath.Join(confDir, fmt.Sprintf("acme_temp_%s.conf", strings.ReplaceAll(domain, ".", "_")))

	// Check if a full site config already exists (no need for temp)
	fullConfPath := filepath.Join(confDir, fmt.Sprintf("streamvault_%s.conf", strings.ReplaceAll(domain, ".", "_")))
	if _, err := os.Stat(fullConfPath); err == nil {
		return nil // Full config already exists, ACME challenge is supported
	}

	webrootHostPath := s.siteRootHostPath(domain)
	webrootContainerPath := s.containerPathFor(webrootHostPath, fmt.Sprintf("/www/sites/%s/index", domain))

	config := fmt.Sprintf(`# Temporary ACME validation config - auto-generated
server {
    listen 80;
    server_name %s;

    location ^~ /.well-known/acme-challenge/ {
        default_type "text/plain";
        root %s;
    }

    location / {
        return 444;
    }
}
`, domain, webrootContainerPath)

	if err := os.MkdirAll(confDir, 0755); err != nil {
		return err
	}

	if err := os.WriteFile(confPath, []byte(config), 0644); err != nil {
		return err
	}

	return s.reloadOpenResty()
}

// RemoveTempHTTPConfig removes the temporary ACME validation config
func (s *CamouflageService) RemoveTempHTTPConfig(domain string) {
	confPath := filepath.Join(s.getConfDir(), fmt.Sprintf("acme_temp_%s.conf", strings.ReplaceAll(domain, ".", "_")))
	if _, err := os.Stat(confPath); err == nil {
		os.Remove(confPath)
		s.reloadOpenResty()
	}
}

func (s *CamouflageService) siteRootHostPath(domain string) string {
	return filepath.Join(s.hostWWWDir, "sites", domain, "index")
}

func (s *CamouflageService) siteRootHostPaths(domain string) []string {
	paths := []string{s.siteRootHostPath(domain)}

	for _, candidate := range []string{
		filepath.Join("/opt/1panel/www/sites", domain, "index"),
		filepath.Join(s.hostBasePath, "www", "sites", domain, "index"),
	} {
		if candidate != "" && pathExists(candidate) {
			paths = append(paths, candidate)
		}
	}

	if confDir := s.getConfDir(); confDir != "" {
		if matches, err := filepath.Glob(filepath.Join(confDir, "*.conf")); err == nil {
			for _, match := range matches {
				data, err := os.ReadFile(match)
				if err != nil || !nginxConfigMatchesDomain(string(data), domain) {
					continue
				}
				for _, root := range nginxRootPaths(string(data)) {
					if hostRoot := s.hostPathForContainerPath(root); hostRoot != "" {
						paths = append(paths, hostRoot)
					}
				}
			}
		}
	}

	return uniqueExistingOrPrimaryPaths(paths)
}

func (s *CamouflageService) sslHostDir(domain string) string {
	if s.hostWWWDir != "" {
		return filepath.Join(s.hostWWWDir, "sites", domain, "ssl")
	}
	return filepath.Join(s.hostSSLDir, domain)
}

func (s *CamouflageService) updateExistingNginxConfigs(domain, panelBackendURL, certContainerPath, keyContainerPath string) (bool, error) {
	confDir := s.getConfDir()
	if confDir == "" {
		return false, nil
	}
	matches, err := filepath.Glob(filepath.Join(confDir, "*.conf"))
	if err != nil {
		return false, err
	}

	foundNativeConfig := false
	generatedName := fmt.Sprintf("streamvault_%s.conf", strings.ReplaceAll(domain, ".", "_"))
	for _, match := range matches {
		if filepath.Base(match) == generatedName {
			continue
		}
		data, err := os.ReadFile(match)
		if err != nil {
			return false, err
		}
		content := string(data)
		if !nginxConfigMatchesDomain(content, domain) {
			continue
		}
		foundNativeConfig = true

		updated := ensurePanelAPILocation(content, panelBackendURL)
		updated = ensureNoCacheForHTML(updated)
		updated = ensureDriveUploadLimits(updated)
		updated = ensureSSLCertificatePaths(updated, certContainerPath, keyContainerPath)
		if updated == content {
			continue
		}
		if err := os.WriteFile(match, []byte(updated), 0644); err != nil {
			return false, err
		}
	}
	return foundNativeConfig, nil
}

func nginxConfigMatchesDomain(config, domain string) bool {
	for _, line := range strings.Split(config, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || !strings.HasPrefix(line, "server_name") {
			continue
		}
		line = strings.TrimSuffix(strings.TrimPrefix(line, "server_name"), ";")
		for _, name := range strings.Fields(line) {
			if name == domain {
				return true
			}
		}
	}
	return false
}

func nginxRootPaths(config string) []string {
	var roots []string
	for _, line := range strings.Split(config, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || !strings.HasPrefix(line, "root ") {
			continue
		}
		root := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "root "), ";"))
		if root != "" {
			roots = append(roots, root)
		}
	}
	return roots
}

func (s *CamouflageService) hostPathForContainerPath(containerPath string) string {
	if containerPath == "" {
		return ""
	}
	if strings.HasPrefix(containerPath, "/www/") && s.hostWWWDir != "" {
		return filepath.Join(s.hostWWWDir, strings.TrimPrefix(containerPath, "/www/"))
	}
	if strings.HasPrefix(containerPath, "/usr/local/openresty/nginx/conf/") && s.hostBasePath != "" {
		return filepath.Join(s.hostBasePath, "conf", strings.TrimPrefix(containerPath, "/usr/local/openresty/nginx/conf/"))
	}
	if filepath.IsAbs(containerPath) && pathExists(containerPath) {
		return containerPath
	}
	return ""
}

func uniqueExistingOrPrimaryPaths(paths []string) []string {
	seen := make(map[string]bool)
	unique := make([]string, 0, len(paths))
	for i, p := range paths {
		if p == "" {
			continue
		}
		p = filepath.Clean(p)
		if seen[p] {
			continue
		}
		if i > 0 && !pathExists(p) {
			continue
		}
		seen[p] = true
		unique = append(unique, p)
	}
	return unique
}

func ensureSSLCertificatePaths(config, certPath, keyPath string) string {
	lines := strings.Split(config, "\n")
	changed := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		if strings.HasPrefix(trimmed, "ssl_certificate_key ") {
			lines[i] = indent + "ssl_certificate_key " + keyPath + ";"
			changed = true
			continue
		}
		if strings.HasPrefix(trimmed, "ssl_certificate ") {
			lines[i] = indent + "ssl_certificate " + certPath + ";"
			changed = true
		}
	}
	if !changed {
		return config
	}
	return strings.Join(lines, "\n")
}

func ensureDriveUploadLimits(config string) string {
	if strings.Contains(config, "# Gotee drive upload limits") {
		return config
	}
	limits := `
    # Gotee drive upload limits
    client_max_body_size 20g;
    client_body_timeout 3600s;
`
	if idx := strings.Index(config, "    # Site root"); idx != -1 {
		return config[:idx] + limits + config[idx:]
	}
	if idx := strings.Index(config, "    root "); idx != -1 {
		return config[:idx] + limits + config[idx:]
	}
	idx := strings.LastIndex(config, "\n}")
	if idx == -1 {
		return config
	}
	return config[:idx] + limits + config[idx:]
}

func ensureNoCacheForHTML(config string) string {
	if strings.Contains(config, "# camouflage no-cache html") {
		return config
	}
	location := `
    # camouflage no-cache html: disable browser cache so updates take effect immediately.
    location ~* \.(html|htm)$ {
        add_header Cache-Control "no-store, no-cache, must-revalidate, max-age=0" always;
        add_header Pragma "no-cache" always;
        add_header Expires "0" always;
        try_files $uri $uri/ =404;
    }
`
	if idx := strings.Index(config, "    # Panel API proxy"); idx != -1 {
		return config[:idx] + location + config[idx:]
	}
	if idx := strings.Index(config, "    location ^~ /api/"); idx != -1 {
		return config[:idx] + location + config[idx:]
	}
	idx := strings.LastIndex(config, "\n}")
	if idx == -1 {
		return config
	}
	return config[:idx] + location + config[idx:]
}

func ensurePanelAPIUploadProxyOptions(config string) string {
	if !strings.Contains(config, "location ^~ /api/") {
		return config
	}
	updated := strings.ReplaceAll(config, "        proxy_read_timeout 60s;", "        proxy_read_timeout 3600s;")
	if strings.Contains(updated, "proxy_request_buffering off;") {
		return updated
	}
	needle := "        proxy_connect_timeout 5s;\n"
	if strings.Contains(updated, needle) {
		return strings.Replace(updated, needle, needle+"        proxy_send_timeout 3600s;\n        proxy_read_timeout 3600s;\n        proxy_request_buffering off;\n", 1)
	}
	return updated
}

func ensurePanelAPILocation(config, panelBackendURL string) string {
	config = disableSiteProxyIncludes(config)
	if strings.Contains(config, "location ^~ /api/") {
		return ensurePanelAPIUploadProxyOptions(config)
	}

	location := fmt.Sprintf(`
    # Panel API proxy for the private drive login.
    location ^~ /api/ {
        proxy_pass %s/api/;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_connect_timeout 5s;
        proxy_send_timeout 3600s;
        proxy_read_timeout 3600s;
        proxy_request_buffering off;
    }
`, panelBackendURL)

	if idx := strings.Index(config, "    include /www/sites/"); idx != -1 {
		return config[:idx] + location + config[idx:]
	}

	idx := strings.LastIndex(config, "\n}")
	if idx == -1 {
		return config
	}
	return config[:idx] + location + config[idx:]
}

func disableSiteProxyIncludes(config string) string {
	lines := strings.Split(config, "\n")
	changed := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "include /www/sites/") && strings.Contains(trimmed, "/proxy/*.conf") {
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[i] = indent + "# disabled by proxy_version private drive: " + trimmed
			changed = true
		}
	}
	if !changed {
		return config
	}
	return strings.Join(lines, "\n")
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (s *CamouflageService) containerPathFor(hostPath, fallback string) string {
	if s.containerName == "" {
		return fallback
	}

	hostPath = filepath.Clean(hostPath)
	cmd := exec.Command("docker", "inspect", s.containerName, "--format", "{{json .Mounts}}")
	output, err := cmd.Output()
	if err == nil {
		var mounts []dockerMount
		if json.Unmarshal(output, &mounts) == nil {
			bestSource := ""
			bestDestination := ""
			for _, mount := range mounts {
				source := filepath.Clean(mount.Source)
				if hostPath == source || strings.HasPrefix(hostPath, source+string(os.PathSeparator)) {
					if len(source) > len(bestSource) {
						bestSource = source
						bestDestination = mount.Destination
					}
				}
			}
			if bestSource != "" {
				rel, err := filepath.Rel(bestSource, hostPath)
				if err == nil {
					if rel == "." {
						return filepath.ToSlash(bestDestination)
					}
					return filepath.ToSlash(filepath.Join(bestDestination, rel))
				}
			}
		}
	}

	confDir := filepath.Join(s.hostBasePath, "conf")
	if rel, err := filepath.Rel(confDir, hostPath); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(filepath.Join("/usr/local/openresty/nginx/conf", rel))
	}
	wwwDir := filepath.Join(s.hostBasePath, "www")
	if rel, err := filepath.Rel(wwwDir, hostPath); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(filepath.Join("/www", rel))
	}
	return fallback
}

func fetchPublicMediaItems() ([]mediaItem, error) {
	client := &http.Client{Timeout: 12 * time.Second}
	request, err := http.NewRequest(http.MethodGet, "https://api.tvmaze.com/shows?page=0", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (compatible; StreamVault/1.0)")
	request.Header.Set("Accept", "application/json")

	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("影视数据源返回状态码 %d", response.StatusCode)
	}

	var shows []tvMazeShow
	if err := json.NewDecoder(response.Body).Decode(&shows); err != nil {
		return nil, err
	}

	items := make([]mediaItem, 0, 12)
	for _, show := range shows {
		if show.Name == "" || show.Image == nil || show.Rating.Average == nil {
			continue
		}
		poster := show.Image.Original
		if poster == "" {
			poster = show.Image.Medium
		}
		if poster == "" {
			continue
		}
		year := ""
		if len(show.Premiered) >= 4 {
			year = show.Premiered[:4]
		}
		items = append(items, mediaItem{
			Title:  show.Name,
			Year:   year,
			Rating: fmt.Sprintf("%.1f", *show.Rating.Average),
			Poster: poster,
		})
		if len(items) >= 12 {
			break
		}
	}
	return items, nil
}

func injectMediaItems(page string, items []mediaItem) string {
	cards := renderMediaCards(items)
	if cards == "" {
		return page
	}

	page = strings.Replace(page, `<div class="content-grid" id="content-grid"></div>`, `<div class="content-grid" id="content-grid">`+cards+`</div>`, 1)
	start := strings.Index(page, "    <script>\n    // Generate mock content cards")
	end := strings.Index(page, "    </script>\n</body>")
	if start != -1 && end != -1 && end > start {
		page = page[:start] + page[end+len("    </script>\n"):]
	}
	return page
}

func renderMediaCards(items []mediaItem) string {
	var builder strings.Builder
	for _, item := range items {
		if item.Title == "" || item.Poster == "" {
			continue
		}
		builder.WriteString(`<div class="content-card" onclick="window.location.href='/login.html'">`)
		builder.WriteString(`<div class="content-thumb"><img src="`)
		builder.WriteString(html.EscapeString(item.Poster))
		builder.WriteString(`" alt="`)
		builder.WriteString(html.EscapeString(item.Title))
		builder.WriteString(` poster" loading="lazy" referrerpolicy="no-referrer" style="width:100%;height:100%;object-fit:cover;display:block"></div>`)
		builder.WriteString(`<div class="info"><div class="title">`)
		builder.WriteString(html.EscapeString(item.Title))
		builder.WriteString(`</div><div class="meta"><span class="rating">★ `)
		builder.WriteString(html.EscapeString(item.Rating))
		builder.WriteString(`</span>`)
		if item.Year != "" {
			builder.WriteString(` &middot; `)
			builder.WriteString(html.EscapeString(item.Year))
		}
		builder.WriteString(`</div></div></div>`)
	}
	return builder.String()
}

func validateCamouflageDomain(domain string) error {
	if domain == "" {
		return fmt.Errorf("域名不能为空")
	}
	if strings.ContainsAny(domain, "/\\") || strings.Contains(domain, "..") || strings.ContainsRune(domain, 0) {
		return fmt.Errorf("域名包含非法字符")
	}
	return nil
}

// ============================================================
// StreamVault HTML Templates
// ============================================================

const streamVaultIndexHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Gotee 网盘 - 安全的个人云存储</title>
    <meta name="description" content="Gotee 网盘，安全、稳定、可长期保存的私人网盘。">
    <style>
        *{box-sizing:border-box;margin:0;padding:0}body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI','PingFang SC','Microsoft YaHei',sans-serif;color:#1f2937;background:#f5fbff;min-height:100vh}a{text-decoration:none;color:inherit}.topbar{height:76px;background:#fff;display:flex;align-items:center;padding:0 9vw;box-shadow:0 1px 0 rgba(15,23,42,.06);position:sticky;top:0;z-index:20}.brand{display:flex;align-items:center;gap:12px;font-size:26px;font-weight:700;color:#23509b}.mark{width:42px;height:42px;border-radius:8px;background:#2457a8;color:white;display:grid;place-items:center;font-size:20px;font-weight:800;letter-spacing:.5px}.hero{min-height:calc(100vh - 76px);display:grid;grid-template-columns:minmax(0,1.1fr) 420px;gap:64px;align-items:center;padding:58px 12vw 70px;background:linear-gradient(110deg,#fdfbff 0%,#f1fbff 46%,#f4fbff 100%);overflow:hidden}.hero-copy{text-align:center}.hero h1{font-size:42px;line-height:1.18;margin-bottom:28px;color:#172235}.badges{display:flex;justify-content:center;gap:28px;flex-wrap:wrap;color:#667386;font-size:16px;margin-bottom:42px}.badges span{display:flex;align-items:center;gap:8px}.badge-icon{width:18px;height:18px;border:2px solid #2f7df6;border-radius:5px;display:inline-block}.scene{height:360px;position:relative}.cloud{position:absolute;left:8%;bottom:8px;width:310px;height:92px;border-radius:46px;background:#fff;box-shadow:0 20px 35px rgba(58,126,194,.14)}.folder{position:absolute;left:25%;bottom:48px;width:230px;height:116px;border-radius:14px;background:#2393ef;box-shadow:inset 0 -18px 0 #1d58d2}.folder:before{content:'';position:absolute;left:20px;top:-28px;width:85px;height:34px;border-radius:8px 8px 0 0;background:#2861d5}.doc{position:absolute;background:#fff;border-radius:8px;box-shadow:0 18px 35px rgba(55,102,150,.12)}.doc.one{left:16%;top:42px;width:126px;height:84px}.doc.two{left:36%;top:110px;width:112px;height:150px}.doc.three{right:14%;top:92px;width:148px;height:102px;transform:rotate(12deg)}.line{height:6px;background:#b8ddfb;border-radius:8px;margin:16px 22px}.bubble{position:absolute;right:28%;top:64px;width:118px;height:64px;border-radius:8px;background:#1677ff}.bubble:after{content:'';position:absolute;left:0;bottom:-18px;border-top:20px solid #1677ff;border-right:28px solid transparent}.login-panel{height:520px;background:#fff;border:1px solid #d9e3f2;border-radius:8px;box-shadow:0 18px 40px rgba(43,84,130,.08);display:flex;flex-direction:column;align-items:center;justify-content:center;padding:48px 34px}.avatar{width:110px;height:110px;border-radius:50%;background:linear-gradient(135deg,#2465d9,#2ad1c8);display:grid;place-items:center;color:#fff;font-size:44px;font-weight:800;margin-bottom:26px;letter-spacing:1px}.login-panel h2{font-size:24px;margin-bottom:8px}.login-panel p{color:#8b95a5;margin-bottom:34px}.primary{width:260px;height:52px;border-radius:4px;background:#2f7df6;color:#fff;display:grid;place-items:center;font-size:20px;font-weight:700}.secondary{margin-top:22px;color:#2f5597}.features{background:#fff;padding:56px 12vw;display:grid;grid-template-columns:repeat(4,1fr);gap:22px}.feature{border:1px solid #e5edf7;border-radius:8px;padding:24px;background:#fff}.feature strong{display:block;font-size:18px;margin-bottom:10px;color:#172235}.feature p{color:#6b7280;line-height:1.7;font-size:14px}@media(max-width:960px){.topbar{padding:0 22px}.hero{grid-template-columns:1fr;padding:44px 22px}.login-panel{height:auto}.features{grid-template-columns:1fr;padding:34px 22px}.hero h1{font-size:32px}.scene{height:280px}}
    </style>
</head>
<body>
    <header class="topbar">
        <a class="brand" href="/"><span class="mark">G</span><span>Gotee 网盘</span></a>
    </header>
    <main class="hero">
        <section class="hero-copy">
            <h1>Gotee 网盘，专属于你的私人云空间</h1>
            <div class="badges"><span><i class="badge-icon"></i>安全隐私</span><span><i class="badge-icon"></i>长期空间</span><span><i class="badge-icon"></i>超大文件</span><span><i class="badge-icon"></i>多端同步</span></div>
            <div class="scene" aria-hidden="true"><div class="cloud"></div><div class="folder"></div><div class="doc one"><div class="line"></div><div class="line"></div><div class="line"></div></div><div class="doc two"><div class="line"></div><div class="line"></div><div class="line"></div><div class="line"></div></div><div class="doc three"><div class="line"></div><div class="line"></div></div><div class="bubble"></div></div>
        </section>
        <aside class="login-panel">
            <div class="avatar">G</div>
            <h2>私人网盘</h2>
            <p>使用本项目面板账号密码登录</p>
            <a href="/login.html" class="primary">进入 Gotee 网盘</a>
            <a href="#features" class="secondary">了解空间能力</a>
        </aside>
    </main>
    <section class="features" id="features">
        <article class="feature"><strong>文件管理</strong><p>上传、下载、重命名、删除、搜索与文件夹归档，适合日常资料存放。</p></article>
        <article class="feature"><strong>隐私访问</strong><p>登录凭据与本项目面板保持一致，未登录无法进入功能页。</p></article>
        <article class="feature"><strong>多类型预览</strong><p>支持文档、表格、图片、PDF 和常见格式视频在线查看。</p></article>
        <article class="feature"><strong>轻量实用</strong><p>聚焦私人网盘最常用的核心工作流，简洁顺手。</p></article>
    </section>
</body>
</html>`
const streamVaultLoginHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>登录 - Gotee 网盘</title>
    <meta name="description" content="登录 Gotee 私人网盘。">
    <style>
        *{box-sizing:border-box;margin:0;padding:0}body{min-height:100vh;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI','PingFang SC','Microsoft YaHei',sans-serif;background:linear-gradient(112deg,#f8fbff,#eef8ff);color:#172235;display:grid;place-items:center}.back{position:fixed;left:28px;top:26px;color:#607086}.login{width:min(920px,calc(100vw - 32px));display:grid;grid-template-columns:1fr 380px;background:#fff;border:1px solid #dbe6f5;border-radius:10px;box-shadow:0 24px 70px rgba(30,82,140,.12);overflow:hidden}.visual{padding:52px;background:linear-gradient(180deg,#f7fbff,#eef7ff);display:flex;flex-direction:column;justify-content:space-between}.brand{display:flex;align-items:center;gap:12px;color:#23509b;font-size:24px;font-weight:800}.mark{width:42px;height:42px;border-radius:8px;background:#2457a8;color:#fff;display:grid;place-items:center;font-size:20px;letter-spacing:.5px}.visual h1{font-size:34px;line-height:1.25;margin-top:70px}.visual p{color:#687386;line-height:1.8;margin-top:18px}.mini-grid{display:grid;grid-template-columns:repeat(3,1fr);gap:14px;margin-top:52px}.mini{height:76px;border-radius:8px;background:#fff;border:1px solid #e2edf8;display:grid;place-items:center;color:#2f7df6;font-weight:700}.form{padding:52px 42px}.form h2{font-size:28px;margin-bottom:10px}.hint{color:#6b7280;font-size:14px;line-height:1.7;margin-bottom:28px}.hint strong{color:#2f7df6}.group{margin-bottom:18px}.group label{display:block;font-size:14px;color:#4b5563;margin-bottom:8px}.input{width:100%;height:46px;border:1px solid #d9e3f2;border-radius:6px;padding:0 14px;font-size:16px;outline:none}.input:focus{border-color:#2f7df6;box-shadow:0 0 0 3px rgba(47,125,246,.12)}.row{display:flex;align-items:center;justify-content:space-between;margin:12px 0 24px;font-size:14px;color:#6b7280}.submit{width:100%;height:50px;border:0;border-radius:5px;background:#2f7df6;color:#fff;font-size:18px;font-weight:800;cursor:pointer}.submit:disabled{opacity:.65;cursor:not-allowed}.msg{display:none;margin-top:16px;padding:12px;border-radius:6px;font-size:14px;line-height:1.6}.msg.error{display:block;background:#fff1f2;color:#be123c;border:1px solid #fecdd3}.msg.info{display:block;background:#eff6ff;color:#1d4ed8;border:1px solid #bfdbfe}.footer{margin-top:26px;text-align:center;color:#8a95a5;font-size:13px}@media(max-width:760px){.login{grid-template-columns:1fr}.visual{display:none}.form{padding:36px 24px}.back{position:absolute}}
    </style>
</head>
<body>
    <a class="back" href="/">返回首页</a>
    <main class="login">
        <section class="visual">
            <div><div class="brand"><span class="mark">G</span><span>Gotee 网盘</span></div><h1>安全进入你的私人文件空间</h1><p>登录使用本项目面板中设置的用户名和密码。登录成功后，可进入文件管理、相册、上传与下载等功能。</p></div>
            <div class="mini-grid"><div class="mini">文件</div><div class="mini">相册</div><div class="mini">分享</div></div>
        </section>
        <section class="form">
            <h2>账号登录</h2>
            <p class="hint">请使用 <strong>本项目面板账号密码</strong> 登录 Gotee 网盘。该页面会调用同一套认证接口。</p>
            <form id="login-form">
                <div class="group"><label for="username">用户名</label><input id="username" class="input" autocomplete="username" required></div>
                <div class="group"><label for="password">密码</label><input id="password" class="input" type="password" autocomplete="current-password" required></div>
                <div class="row"><label><input type="checkbox" checked> 保持登录</label><a href="#" id="help">忘记密码？</a></div>
                <button class="submit" id="submit" type="submit">进入网盘</button>
                <div id="message" class="msg"></div>
            </form>
            <p class="footer">仅限私人服务器授权用户访问</p>
        </section>
    </main>
    <script>
    const form = document.getElementById('login-form');
    const message = document.getElementById('message');
    const submit = document.getElementById('submit');
    const apiBases = ['/api'];
    if (location.protocol === 'http:') apiBases.push(location.protocol + '//' + location.hostname + ':8080/api');
    function show(text, type){ message.textContent = text; message.className = 'msg ' + type; }
    async function apiLogin(username, password){
        let lastError;
        for (const base of apiBases) {
            try {
                const response = await fetch(base + '/auth/login', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({username, password})});
                const data = await response.json().catch(() => ({}));
                if (!response.ok) throw new Error(data.error || '登录失败');
                return data;
            } catch (err) { lastError = err; }
        }
        throw lastError || new Error('无法连接认证服务');
    }
    form.addEventListener('submit', async function(event){
        event.preventDefault();
        submit.disabled = true;
        show('正在验证面板账号...', 'info');
        try {
            const data = await apiLogin(document.getElementById('username').value.trim(), document.getElementById('password').value);
            localStorage.setItem('cloud_token', data.token);
            localStorage.setItem('cloud_user', JSON.stringify(data.user || {}));
            location.href = '/drive.html';
        } catch (err) {
            show(err.message || '用户名或密码错误', 'error');
            submit.disabled = false;
        }
    });
    document.getElementById('help').addEventListener('click', function(event){event.preventDefault();show('请在本项目面板中重置或修改账号密码。', 'info')});
    </script>
</body>
</html>`

const streamVaultDriveHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>网盘 - Gotee 网盘</title>
    <meta name="description" content="Gotee 私人网盘文件管理。">
    <style>
        *{box-sizing:border-box;margin:0;padding:0}body{height:100vh;overflow:hidden;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI','PingFang SC','Microsoft YaHei',sans-serif;color:#1f2937;background:#f6f8fb}.app{height:100vh;display:grid;grid-template-columns:88px 300px minmax(0,1fr) 250px;background:#fff}.rail{background:#f1f6fc;border-right:1px solid #e4edf8;display:flex;flex-direction:column;align-items:center;padding:16px 8px;gap:12px}.logo{width:46px;height:46px;border-radius:50%;background:linear-gradient(135deg,#2f7df6,#22c8b8);color:#fff;display:grid;place-items:center;font-weight:900;font-size:18px}.rail-btn{width:68px;height:62px;border:0;border-radius:10px;background:transparent;color:#718096;font-size:13px;cursor:pointer;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:4px}.rail-btn:hover,.rail-btn.active{background:#fff;color:#2563eb;box-shadow:0 8px 20px rgba(30,82,140,.1)}.rail-btn span{font-size:18px}.user{margin-top:auto;width:100%;text-align:center;padding:12px 4px;border-top:1px solid #e4edf8;color:#172235;cursor:pointer}.avatar-sm{width:38px;height:38px;border-radius:50%;background:linear-gradient(135deg,#2f7df6,#22c8b8);color:#fff;display:grid;place-items:center;font-weight:800;margin:0 auto 6px}.uname{font-size:12px;font-weight:700;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.user small{font-size:11px;color:#8b95a5}.left{background:linear-gradient(180deg,#eef6ff,#f8fbff);padding:22px 18px;border-right:1px solid #e4edf8;overflow:auto}.tabs{font-size:20px;font-weight:800;margin-bottom:20px}.quota{background:#fff;border:2px solid #2f7df6;border-radius:8px;padding:18px;margin-bottom:16px}.quota h3{font-size:17px;margin-bottom:8px}.quota-text{color:#6b7280;font-size:13px}.bar{height:12px;background:#e5e7eb;border-radius:12px;overflow:hidden;margin:14px 0 12px}.bar div{height:100%;background:linear-gradient(90deg,#2f7df6,#14b8a6);transition:width .25s}.bar.over div{background:linear-gradient(90deg,#ef4444,#f97316)}.legend{display:flex;gap:9px;flex-wrap:wrap;color:#8b95a5;font-size:12px}.dot{width:9px;height:9px;border-radius:50%;display:inline-block;margin-right:4px}.quota-edit{margin-top:14px;display:flex;align-items:center;gap:8px;font-size:13px;color:#4b5563}.quota-edit input{width:78px;height:32px;border:1px solid #d9e3f2;border-radius:6px;padding:0 9px}.quota-edit button{height:32px;border:0;border-radius:6px;background:#2f7df6;color:#fff;padding:0 12px;cursor:pointer}.quick{background:#fff;border-radius:8px;padding:18px}.quick h3{font-size:16px;margin-bottom:16px}.quick-grid{display:grid;grid-template-columns:repeat(3,1fr);gap:12px 8px}.quick-item{text-align:center;color:#607086;font-size:13px;cursor:pointer;padding:6px 4px;border-radius:8px}.quick-item:hover,.quick-item.active{background:#f1f5fb;color:#2563eb;font-weight:700}.quick-icon{width:38px;height:38px;margin:0 auto 6px;border-radius:12px;display:grid;place-items:center;color:#fff;font-weight:800}.main{min-width:0;display:flex;flex-direction:column}.top{height:70px;display:flex;align-items:center;gap:14px;padding:0 24px;border-bottom:1px solid #eef2f7}.search{flex:1;max-width:560px;height:42px;border:0;border-radius:22px;background:#f1f3f8;padding:0 18px;font-size:15px;outline:none}.mobile-title{display:none;font-weight:800;color:#23509b}.tools{min-height:72px;display:flex;align-items:center;justify-content:space-between;gap:12px;padding:14px 24px}.tool-left{display:flex;gap:10px;align-items:center;flex-wrap:wrap}.btn{height:40px;border:1px solid #d1d7e0;border-radius:6px;background:#fff;padding:0 16px;font-size:14px;cursor:pointer;display:inline-flex;align-items:center;justify-content:center;gap:6px;transition:all .15s;white-space:nowrap}.btn:hover{border-color:#2f7df6;color:#2f7df6}.btn.primary{background:#2f7df6;color:#fff;border-color:#2f7df6}.btn.danger{background:#ef4444;color:#fff;border-color:#ef4444}.btn:disabled{opacity:.55;cursor:not-allowed}.view{display:flex}.view button{width:40px;height:40px;border:1px solid #d8e1ef;background:#fff;color:#6b7280;cursor:pointer}.view button:first-child{border-radius:6px 0 0 6px}.view button:last-child{border-radius:0 6px 6px 0;border-left:0}.view button.active{color:#2f7df6;background:#eef5ff;border-color:#2f7df6}.crumb{padding:0 24px 12px;color:#4b5563;font-size:14px;display:flex;align-items:center;flex-wrap:wrap;gap:2px}.crumb .cr{cursor:pointer;color:#2f7df6;padding:4px 6px;border-radius:4px}.crumb .current{color:#1f2937;font-weight:700;cursor:default}.sep{color:#9aa3af}.content{flex:1;overflow:auto;padding:0 24px 24px}.table{width:100%;border-collapse:collapse;table-layout:fixed}.table th{text-align:left;font-weight:500;color:#6b7280;height:42px;border-bottom:1px solid #e5e7eb;font-size:13px}.table td{height:60px;border-bottom:1px solid #eef2f7;color:#6b7280;font-size:13px}.table tbody tr{cursor:pointer}.table tbody tr:hover,.table tr.selected{background:#f3f8ff}.name{display:flex;align-items:center;gap:12px;color:#1f2937;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;font-size:14px}.star{color:#f59e0b;margin-left:4px}.file-icon{width:34px;height:34px;border-radius:8px;background:#3b82f6;color:#fff;display:grid;place-items:center;font-weight:800;flex:0 0 auto;font-size:12px}.folder{background:#ffbd17}.image{background:#22c55e}.video{background:#8b5cf6}.pdf{background:#ef4444}.audio{background:#f43f5e}.archive{background:#f59e0b}.doc{background:#3b82f6}.row-action{width:32px;height:32px;border:0;background:transparent;border-radius:8px;cursor:pointer;color:#6b7280;font-size:18px}.row-action:hover,.row-action.open{background:#eef5ff;color:#2f7df6}.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(145px,1fr));gap:14px}.card{border:1px solid #e5edf7;border-radius:8px;padding:16px;min-height:132px;cursor:pointer;text-align:center;background:#fff}.card:hover{border-color:#2f7df6;box-shadow:0 8px 22px rgba(30,82,140,.08)}.card .file-icon{margin:0 auto 12px;width:48px;height:48px}.cname{font-size:13px;color:#1f2937;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.csub{color:#9aa3af;margin-top:6px;font-size:12px}.album{display:grid;grid-template-columns:repeat(auto-fill,minmax(170px,1fr));gap:14px}.photo{position:relative;aspect-ratio:1/1;border-radius:8px;overflow:hidden;cursor:pointer;background:#f1f5fb}.photo img,.photo video{width:100%;height:100%;object-fit:cover;display:block;background:#0f172a}.photo .fallback{position:absolute;inset:0;display:grid;place-items:center;background:linear-gradient(135deg,#2563eb,#14b8a6);color:#fff;font-weight:800}.pmeta{position:absolute;left:0;right:0;bottom:0;padding:9px 10px;background:linear-gradient(0deg,rgba(0,0,0,.64),transparent);color:#fff;font-size:12px}.side{border-left:1px solid #e4edf8;padding:24px 18px;color:#718096;font-size:13px;line-height:1.8;overflow:auto}.side h3{color:#172235;margin-bottom:16px;font-size:15px}.empty{height:260px;display:grid;place-items:center;color:#94a3b8;text-align:center;font-size:14px}.empty .sub{margin-top:8px;color:#cbd5e1;font-size:12px}.hidden{display:none!important}.menu{position:fixed;background:#fff;border:1px solid #e5edf7;border-radius:8px;box-shadow:0 18px 40px rgba(15,23,42,.16);padding:6px;min-width:158px;z-index:900}.menu button{display:block;width:100%;text-align:left;padding:8px 12px;border:0;background:transparent;cursor:pointer;font-size:13px;border-radius:6px;color:#1f2937}.menu button:hover{background:#f1f5fb}.menu button.danger{color:#ef4444}.menu .sep{height:1px;background:#eef2f7;margin:4px 0}.modal{position:fixed;inset:0;background:rgba(15,23,42,.78);display:none;align-items:center;justify-content:center;z-index:1000;padding:30px}.modal.open{display:flex}.modal-box{background:#fff;border-radius:10px;max-width:1080px;width:100%;max-height:calc(100vh - 60px);display:flex;flex-direction:column;overflow:hidden;box-shadow:0 30px 80px rgba(0,0,0,.42)}.modal-head{display:flex;align-items:center;padding:13px 18px;border-bottom:1px solid #eef2f7;gap:10px}.modal-head h4{flex:1;font-size:15px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.modal-head .meta{color:#9aa3af;font-size:13px}.close{border:0;background:transparent;font-size:22px;cursor:pointer;color:#6b7280;padding:3px 8px;border-radius:6px}.close:hover{background:#f1f5fb}.modal-body{flex:1;overflow:auto;background:#f8fafd}.modal-body.center{display:grid;place-items:center;padding:18px}.modal-body img{max-width:100%;max-height:calc(100vh - 200px);object-fit:contain}.modal-body video{width:min(960px,100%);max-height:calc(100vh - 210px);background:#000;border-radius:6px}.modal-body audio{width:min(720px,90%)}.modal-body iframe{width:100%;height:calc(100vh - 175px);border:0;background:#fff}.modal-body pre{padding:22px 26px;margin:0;font-family:'SF Mono',Menlo,Consolas,monospace;font-size:13px;line-height:1.65;white-space:pre-wrap;word-break:break-word}.csv-table{width:100%;border-collapse:collapse;font-size:13px}.csv-table th,.csv-table td{border:1px solid #e5e7eb;padding:8px 12px;text-align:left}.csv-table th{background:#f1f5fb;position:sticky;top:0}.queue-btn{position:relative;min-width:116px}.queue-spin{width:15px;height:15px;border:2px solid #c7d2fe;border-top-color:#2f7df6;border-radius:50%;display:none}.queue-btn.busy .queue-spin{display:inline-block;animation:spin .8s linear infinite}.queue-btn.busy{border-color:#2f7df6;color:#2f7df6;box-shadow:0 0 0 3px rgba(47,125,246,.1)}.queue-count{min-width:20px;height:20px;border-radius:10px;background:#eef5ff;color:#2563eb;display:inline-flex;align-items:center;justify-content:center;font-size:12px;font-weight:800;padding:0 6px}.queue-btn.busy .queue-count{background:#2f7df6;color:#fff}.queue-box{max-width:980px}.queue-body{padding:0;background:#fff}.queue-table{width:100%;border-collapse:collapse;table-layout:fixed}.queue-table th{height:40px;text-align:left;color:#718096;font-weight:500;font-size:13px;border-bottom:1px solid #e5e7eb;background:#f8fafd}.queue-table td{height:54px;border-bottom:1px solid #eef2f7;color:#1f2937;font-size:13px;padding-right:12px;vertical-align:middle}.queue-name{display:block;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;padding-left:16px}.queue-muted{color:#94a3b8}.queue-progress{height:18px;border:1px solid #c7d2fe;background:#f4f6fb;border-radius:2px;position:relative;overflow:hidden}.queue-progress span{position:absolute;left:0;top:0;bottom:0;background:#4b6fe2;transition:width .15s}.queue-progress b{position:absolute;inset:0;display:flex;align-items:center;justify-content:center;color:#1f2937;font-size:12px;font-weight:700}.queue-progress.active b{color:#fff;text-shadow:0 1px 2px rgba(0,0,0,.24)}.status-pill{display:inline-flex;align-items:center;gap:6px;color:#1f2937}.status-pill.uploading:before{content:"";width:14px;height:14px;border:2px solid #c7d2fe;border-top-color:#2f7df6;border-radius:50%;animation:spin .8s linear infinite}.status-pill.done{color:#2563eb}.status-pill.failed{color:#ef4444}.queue-empty{height:180px}.queue-summary-strong{color:#1f2937;font-weight:700}@keyframes spin{to{transform:rotate(360deg)}}.toast{position:fixed;left:50%;bottom:28px;transform:translateX(-50%);background:#1f2937;color:#fff;padding:11px 20px;border-radius:22px;font-size:14px;box-shadow:0 12px 30px rgba(0,0,0,.25);opacity:0;transition:opacity .2s;pointer-events:none;z-index:2000}.toast.show{opacity:1}@media(max-width:1180px){.app{grid-template-columns:78px minmax(0,1fr)}.left,.side{display:none}.top,.tools,.crumb,.content{padding-left:16px;padding-right:16px}.search{max-width:100%}}@media(max-width:720px){body{overflow:auto}.app{height:100vh;grid-template-columns:1fr;grid-template-rows:auto minmax(0,1fr)}.rail{height:auto;min-height:68px;flex-direction:row;justify-content:space-between;align-items:center;padding:8px 12px;border-right:0;border-bottom:1px solid #e4edf8}.logo{width:40px;height:40px}.rail-btn{width:58px;height:48px;font-size:12px}.rail-btn span{font-size:16px}.user{margin-top:0;width:auto;border-top:0;padding:4px 0}.avatar-sm{width:30px;height:30px;margin-bottom:0}.uname,.user small{display:none}.top{height:auto;min-height:58px;gap:10px;flex-wrap:wrap;padding-top:10px;padding-bottom:10px}.mobile-title{display:block}.search{order:2;flex-basis:100%;height:40px}.tools{align-items:flex-start;min-height:auto}.tool-left{width:100%;display:grid;grid-template-columns:1fr 1fr}.tool-left .btn{padding:0 10px}.view{display:none}.crumb{font-size:13px;padding-bottom:10px}.content{padding-bottom:16px}.table,.table thead,.table tbody,.table tr,.table td{display:block;width:100%}.table thead{display:none}.table tr{position:relative;border:1px solid #e5edf7;border-radius:8px;margin-bottom:10px;padding:10px;background:#fff}.table td{height:auto;border-bottom:0;padding:3px 44px 3px 0}.table td:nth-child(2),.table td:nth-child(3),.table td:nth-child(4){font-size:12px;color:#94a3b8}.table td:nth-child(5){position:absolute;right:8px;top:12px;padding:0}.grid{grid-template-columns:repeat(2,minmax(0,1fr));gap:10px}.album{grid-template-columns:repeat(2,minmax(0,1fr));gap:10px}.modal{padding:12px}.modal-box{max-height:calc(100vh - 24px)}.modal-head{padding:10px 12px;flex-wrap:wrap}.modal-head .meta{display:none}.modal-head .btn{height:34px;padding:0 10px}.queue-box{width:100%;max-width:100%}.queue-body{max-height:72vh}.queue-table,.queue-table thead,.queue-table tbody,.queue-table tr,.queue-table td{display:block;width:100%}.queue-table thead{display:none}.queue-table tr{position:relative;border-bottom:1px solid #e5edf7;padding:10px 12px}.queue-table td{height:auto;border:0;padding:3px 0;color:#1f2937}.queue-table td:nth-child(1){padding-right:0}.queue-name{padding-left:0;font-weight:700}.queue-table td:nth-child(2),.queue-table td:nth-child(3),.queue-table td:nth-child(5),.queue-table td:nth-child(6){font-size:12px;color:#718096}.queue-table td:nth-child(4){padding:8px 0}.toast{left:12px;right:12px;transform:none;text-align:center}}
    </style>
</head>
<body>
    <div class="app">
        <aside class="rail">
            <div class="logo">G</div>
            <button class="rail-btn active" data-pane="storage"><span>▤</span>存储</button>
            <button class="rail-btn" data-pane="album"><span>▦</span>相册</button>
            <button class="rail-btn" data-pane="video"><span>▷</span>视频</button>
            <div class="user" id="user-area" title="点击退出登录"><div class="avatar-sm" id="user-avatar">G</div><div class="uname" id="user-name">用户</div><small>点击退出</small></div>
        </aside>
        <aside class="left">
            <div class="tabs">网盘</div>
            <section class="quota">
                <h3>全部文件</h3>
                <div class="quota-text" id="quota-text">已用 0 B / 0 B</div>
                <div class="bar" id="quota-bar"><div style="width:0%"></div></div>
                <div class="legend"><span><i class="dot" style="background:#f43f5e"></i>音频</span><span><i class="dot" style="background:#22c55e"></i>文档</span><span><i class="dot" style="background:#3b82f6"></i>图片</span><span><i class="dot" style="background:#8b5cf6"></i>视频</span></div>
                <div class="quota-edit"><span>总空间</span><input id="quota-input" type="number" min="1" max="102400" step="1"><span>GB</span><button id="quota-save">保存</button></div>
            </section>
            <section class="quick"><h3>常用</h3><div class="quick-grid"><div class="quick-item" data-section="recent"><div class="quick-icon" style="background:#60a5fa">上</div>最近上传</div><div class="quick-item" data-section="starred"><div class="quick-icon" style="background:#fbbf24">星</div>星标文件</div><div class="quick-item" data-section="trash"><div class="quick-icon" style="background:#94a3b8">回</div>回收站</div></div></section>
        </aside>
        <main class="main">
            <div class="top"><div class="mobile-title">Gotee 网盘</div><input id="search" class="search" placeholder="搜索文件、文件夹..."></div>
            <div class="tools"><div class="tool-left"><button id="upload-btn" class="btn primary">上传</button><button id="queue-btn" class="btn queue-btn" title="上传队列"><span class="queue-spin"></span><span>上传队列</span><b id="queue-count" class="queue-count">0</b></button><button id="folder-btn" class="btn">新建文件夹</button><input id="file-input" class="hidden" type="file" multiple><button id="trash-clear" class="btn danger hidden">清空回收站</button></div><div class="view"><button id="list-view" class="active" title="列表">☰</button><button id="grid-view" title="网格">▦</button></div></div>
            <div class="crumb" id="crumb"></div>
            <section id="content" class="content"></section>
        </main>
        <aside class="side"><h3>文件属性</h3><div id="props">选中文件 / 文件夹，查看名称、大小、类型和修改时间。</div></aside>
    </div>
    <div class="modal" id="modal"><div class="modal-box"><div class="modal-head"><h4 id="modal-title">预览</h4><span class="meta" id="modal-meta"></span><button class="btn" id="modal-download">下载</button><button class="close" id="modal-close" title="关闭">×</button></div><div class="modal-body" id="modal-body"></div></div></div>
    <div class="modal" id="move-modal"><div class="modal-box" style="max-width:520px"><div class="modal-head"><h4>移动到</h4><button class="close" id="move-close" title="关闭">×</button></div><div class="modal-body" id="move-body" style="padding:14px 18px;max-height:60vh"></div></div></div>
    <div class="modal" id="queue-modal"><div class="modal-box queue-box"><div class="modal-head"><h4>上传队列</h4><span class="meta" id="queue-summary">暂无上传任务</span><button class="close" id="queue-close" title="关闭">×</button></div><div class="modal-body queue-body"><table class="queue-table"><thead><tr><th style="width:34%">名称</th><th style="width:13%">文件大小</th><th style="width:14%">状态</th><th style="width:20%">上传进度</th><th style="width:11%">已上传</th><th style="width:8%">上传速度</th></tr></thead><tbody id="queue-rows"></tbody></table><div id="queue-empty" class="empty queue-empty">暂无上传任务</div></div></div></div>
    <div class="toast" id="toast"></div>
    <script>
    var token = localStorage.getItem('cloud_token');
    var user = JSON.parse(localStorage.getItem('cloud_user') || '{}');
    var apiBases = ['/api'];
    if (location.protocol === 'http:') apiBases.push(location.protocol + '//' + location.hostname + ':8080/api');
    var apiBase = apiBases[0];
    var files = [];
    var quotaGB = 20;
    var usedServerBytes = 0;
    var cwd = [];
    var view = 'list';
    var pane = 'storage';
    var section = 'all';
    var query = '';
    var selectedId = null;
    var activeObjectURLs = [];
    var uploadTasks = [];
    var uploadTaskSeq = 0;

    function authHeaders(extra){ var h = extra || {}; h.Authorization = 'Bearer ' + token; return h; }
    async function api(path, opts){
        opts = opts || {};
        opts.headers = opts.headers || {};
        opts.headers.Authorization = 'Bearer ' + token;
        var lastErr;
        for (var i=0;i<apiBases.length;i++){
            try {
                var res = await fetch(apiBases[i] + path, opts);
                if (res.status === 401) { localStorage.removeItem('cloud_token'); location.href = '/login.html'; return Promise.reject(new Error('登录已过期')); }
                var data = null;
                var text = await res.text();
                if (text) { try { data = JSON.parse(text); } catch(e) { data = {raw:text}; } }
                if (!res.ok) throw new Error((data && data.error) || '请求失败');
                apiBase = apiBases[i];
                return data || {};
            } catch(err) { lastErr = err; }
        }
        throw lastErr || new Error('无法连接网盘服务');
    }
    function escapeHTML(s){ return String(s==null?'':s).replace(/[&<>"']/g, function(c){ return ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'})[c]; }); }
    function toast(msg){ var t=document.getElementById('toast'); t.textContent=msg; t.classList.add('show'); clearTimeout(toast._t); toast._t=setTimeout(function(){t.classList.remove('show');},1900); }
    function fmtBytes(n){ n=Number(n)||0; if(n<=0) return '0 B'; var u=['B','KB','MB','GB','TB']; var i=0; while(n>=1024&&i<u.length-1){n/=1024;i++;} return n.toFixed(n>=10||i===0?0:1)+' '+u[i]; }
    function dateOnly(v){ return v ? String(v).slice(0,10) : '-'; }
    function quotaBytes(){ return (Number(quotaGB)||0)*1024*1024*1024; }
    function usedBytes(){ return Number(usedServerBytes)||0; }
    function currentParent(){ return cwd.length ? cwd[cwd.length-1].id : ''; }
    function findItem(id){ return files.filter(function(f){return f.id===id;})[0] || null; }
    function fileCategory(mime,name){ mime=mime||''; var lower=(name||'').toLowerCase(); if(mime.indexOf('image/')===0)return'image'; if(mime.indexOf('video/')===0)return'video'; if(mime.indexOf('audio/')===0)return'audio'; if(mime==='application/pdf'||lower.endsWith('.pdf'))return'pdf'; if(/\.(zip|rar|7z|tar|gz|tgz|bz2)$/.test(lower))return'archive'; return'doc'; }
    function iconLabel(cat){ if(cat==='image')return'图'; if(cat==='video')return'视'; if(cat==='audio')return'音'; if(cat==='pdf')return'PDF'; if(cat==='archive')return'包'; return'文'; }
    function typeLabel(f){ if(f.folder)return'文件夹'; var cat=fileCategory(f.mime,f.name); if(cat==='image')return'图片'; if(cat==='video')return'视频'; if(cat==='audio')return'音频'; if(cat==='pdf')return'PDF'; if(cat==='archive')return'压缩包'; var ext=(f.name.split('.').pop()||'').toUpperCase(); return ext && ext!==f.name.toUpperCase() ? ext+' 文件' : '文件'; }
    function setUser(){ var uname=user.username||'用户'; document.getElementById('user-name').textContent=uname; document.getElementById('user-avatar').textContent=(uname.charAt(0)||'G').toUpperCase(); }
    async function verify(){ if(!token){location.href='/login.html';return;} await api('/auth/me'); }
    async function loadState(){ var data=await api('/drive/state'); files=(data.items||[]).map(function(f){ f.folder=!!f.folder; f.starred=!!f.starred; f.trashed=!!f.trashed; f.parent=f.parent||''; f.modified=dateOnly(f.updated_at||f.created_at); f.uploadedAt=Date.parse(f.created_at||f.updated_at||'')||0; return f; }); quotaGB=Number(data.quota_gb)||20; usedServerBytes=Number(data.used_bytes)||0; renderAll(); }
    function renderQuota(){ var used=usedBytes(); var total=quotaBytes(); var ratio=total>0?Math.min(100,used/total*100):0; document.getElementById('quota-text').textContent='已用 '+fmtBytes(used)+' / '+fmtBytes(total); var bar=document.getElementById('quota-bar'); bar.classList.toggle('over',used>total); bar.firstElementChild.style.width=ratio.toFixed(1)+'%'; document.getElementById('quota-input').value=quotaGB; }
    function renderCrumb(){ var crumb=document.getElementById('crumb'); if(section==='recent'){crumb.innerHTML='<span class="cr current">最近上传</span>';return;} if(section==='starred'){crumb.innerHTML='<span class="cr current">星标文件</span>';return;} if(section==='trash'){crumb.innerHTML='<span class="cr current">回收站</span>';return;} if(pane==='album'){crumb.innerHTML='<span class="cr current">相册</span>';return;} if(pane==='video'){crumb.innerHTML='<span class="cr current">视频</span>';return;} var html='<span class="cr" data-i="-1">根目录</span>'; for(var i=0;i<cwd.length;i++) html+='<span class="sep">/</span><span class="cr '+(i===cwd.length-1?'current':'')+'" data-i="'+i+'">'+escapeHTML(cwd[i].name)+'</span>'; crumb.innerHTML=html; crumb.querySelectorAll('.cr').forEach(function(el){ el.onclick=function(){ var idx=parseInt(el.getAttribute('data-i'),10); cwd=idx===-1?[]:cwd.slice(0,idx+1); renderAll(); }; }); }
    function currentList(){ var list; if(section==='recent') list=files.filter(function(f){return !f.folder&&!f.trashed;}).slice().sort(function(a,b){return b.uploadedAt-a.uploadedAt;}).slice(0,80); else if(section==='starred') list=files.filter(function(f){return f.starred&&!f.trashed;}); else if(section==='trash') list=files.filter(function(f){return f.trashed;}); else list=files.filter(function(f){return !f.trashed&&(f.parent||'')===currentParent();}); if(query){ var q=query.toLowerCase(); list=list.filter(function(f){return f.name.toLowerCase().indexOf(q)!==-1;}); } list.sort(function(a,b){ if(!!a.folder!==!!b.folder)return a.folder?-1:1; return a.name.localeCompare(b.name,'zh'); }); return list; }
    function renderList(){ var content=document.getElementById('content'); var data=currentList(); document.getElementById('trash-clear').classList.toggle('hidden',section!=='trash'||!files.some(function(f){return f.trashed;})); if(!data.length){ var msg=section==='trash'?'回收站为空':(query?'没有匹配的文件':'此文件夹为空，点击上传按钮开始添加文件'); content.innerHTML='<div class="empty"><div>'+msg+'</div></div>'; return; } if(view==='grid'){ var cards=''; data.forEach(function(f){ var cat=f.folder?'folder':fileCategory(f.mime,f.name); cards+='<div class="card" data-id="'+f.id+'"><div class="file-icon '+cat+'">'+(f.folder?'夹':iconLabel(cat))+'</div><div class="cname">'+escapeHTML(f.name)+(f.starred?' <span class="star">★</span>':'')+'</div><div class="csub">'+(f.folder?'文件夹':fmtBytes(f.size))+'</div></div>'; }); content.innerHTML='<div class="grid">'+cards+'</div>'; } else { var folderCount=data.filter(function(f){return f.folder;}).length; var rows=''; data.forEach(function(f){ var cat=f.folder?'folder':fileCategory(f.mime,f.name); rows+='<tr data-id="'+f.id+'" class="'+(f.id===selectedId?'selected':'')+'"><td><div class="name"><span class="file-icon '+cat+'">'+(f.folder?'夹':iconLabel(cat))+'</span>'+escapeHTML(f.name)+(f.starred?'<span class="star">★</span>':'')+'</div></td><td>'+(f.folder?'-':fmtBytes(f.size))+'</td><td>'+typeLabel(f)+'</td><td>'+dateOnly(f.updated_at||f.created_at)+'</td><td style="text-align:right;padding-right:6px"><button class="row-action" data-menu="1" data-id="'+f.id+'" title="更多操作">⋯</button></td></tr>'; }); content.innerHTML='<table class="table"><thead><tr><th style="width:46%">文件名 <span style="color:#9aa3af">'+folderCount+' 个文件夹，'+(data.length-folderCount)+' 个文件</span></th><th style="width:14%">大小</th><th style="width:14%">类型</th><th style="width:18%">修改时间</th><th style="width:8%;text-align:right;padding-right:6px">操作</th></tr></thead><tbody>'+rows+'</tbody></table>'; } bindContentEvents(); }
    function renderMedia(kind){ var content=document.getElementById('content'); document.getElementById('trash-clear').classList.add('hidden'); var data=files.filter(function(f){return !f.folder&&!f.trashed&&fileCategory(f.mime,f.name)===kind;}); data.sort(function(a,b){return b.uploadedAt-a.uploadedAt;}); if(query){ var q=query.toLowerCase(); data=data.filter(function(f){return f.name.toLowerCase().indexOf(q)!==-1;}); } if(!data.length){ content.innerHTML='<div class="empty"><div>'+(kind==='image'?'相册暂无照片':'视频列表暂无内容')+'</div><div class="sub">上传对应文件后即可在此查看</div></div>'; return; } var html='<div class="album">'; data.forEach(function(f){ html+='<div class="photo '+kind+'-tile" data-id="'+f.id+'">'+(kind==='image'?'<img alt="'+escapeHTML(f.name)+'" loading="lazy">':'<video preload="metadata" muted playsinline></video><div class="fallback">'+escapeHTML((f.name.split('.').pop()||'').toUpperCase())+'</div>')+'<div class="pmeta">'+escapeHTML(f.name)+' · '+fmtBytes(f.size)+'</div></div>'; }); html+='</div>'; content.innerHTML=html; content.querySelectorAll('.photo').forEach(function(node){ var id=node.getAttribute('data-id'); node.onclick=function(){openPreview(id);}; fetchBlob(id,false).then(function(blob){ if(!blob)return; var url=trackObjectURL(URL.createObjectURL(blob)); var el=node.querySelector(kind==='image'?'img':'video'); if(el)el.src=url; }).catch(function(){}); }); }
    function renderAll(){ renderQuota(); renderCrumb(); closeRowMenu(); if(pane==='album') renderMedia('image'); else if(pane==='video') renderMedia('video'); else renderList(); updateProps(); document.querySelectorAll('.rail-btn').forEach(function(b){b.classList.toggle('active',b.getAttribute('data-pane')===pane);}); document.querySelectorAll('.quick-item').forEach(function(q){q.classList.toggle('active',q.getAttribute('data-section')===section);}); var allow=pane==='storage'&&section==='all'; document.getElementById('upload-btn').disabled=!allow; document.getElementById('folder-btn').disabled=!allow; }
    function updateProps(){ var box=document.getElementById('props'); var f=findItem(selectedId); if(!f){box.innerHTML='选中文件 / 文件夹，查看名称、大小、类型和修改时间。';return;} box.innerHTML='<div><b>名称：</b>'+escapeHTML(f.name)+'</div><div><b>类型：</b>'+typeLabel(f)+'</div><div><b>大小：</b>'+(f.folder?'-':fmtBytes(f.size))+'</div><div><b>修改时间：</b>'+dateOnly(f.updated_at||f.created_at)+'</div>'+(f.starred?'<div><b>状态：</b>已星标</div>':''); }
    function bindContentEvents(){ var content=document.getElementById('content'); content.querySelectorAll('tr[data-id],.card[data-id]').forEach(function(r){ r.onclick=function(e){ if(e.target.tagName==='BUTTON')return; selectedId=r.getAttribute('data-id'); updateProps(); content.querySelectorAll('tr.selected').forEach(function(s){s.classList.remove('selected');}); r.classList.add('selected'); }; r.ondblclick=function(){ var f=findItem(r.getAttribute('data-id')); if(!f||f.trashed)return; if(f.folder){cwd.push({id:f.id,name:f.name});renderAll();} else openPreview(f.id); }; }); content.querySelectorAll('button[data-menu]').forEach(function(b){ b.onclick=function(e){e.stopPropagation();showRowMenu(b);}; }); }
    function uploadActiveTasks(){ return uploadTasks.filter(function(t){return t.status==='waiting'||t.status==='uploading';}); }
    function uploadStatusLabel(t){ if(t.status==='done')return'已完成'; if(t.status==='failed')return'失败'; if(t.status==='uploading')return'上传中'; return'等待中'; }
    function renderQueue(){ var active=uploadActiveTasks().length; var failed=uploadTasks.filter(function(t){return t.status==='failed';}).length; var btn=document.getElementById('queue-btn'); var count=document.getElementById('queue-count'); var summary=document.getElementById('queue-summary'); var rows=document.getElementById('queue-rows'); var empty=document.getElementById('queue-empty'); if(!btn||!rows)return; btn.classList.toggle('busy',active>0); count.textContent=active>0?active:uploadTasks.length; summary.innerHTML=uploadTasks.length?'<span class="queue-summary-strong">'+active+'</span> 个进行中，'+failed+' 个失败，共 '+uploadTasks.length+' 个任务':'暂无上传任务'; empty.classList.toggle('hidden',uploadTasks.length>0); rows.innerHTML=uploadTasks.map(function(t){ var pct=Math.max(0,Math.min(100,t.progress||0)); var activeClass=t.status==='uploading'?' active':''; return '<tr><td><span class="queue-name" title="'+escapeHTML(t.name)+'">'+escapeHTML(t.name)+'</span></td><td>'+fmtBytes(t.size)+'</td><td><span class="status-pill '+t.status+'">'+uploadStatusLabel(t)+'</span>'+(t.error?'<div class="queue-muted">'+escapeHTML(t.error)+'</div>':'')+'</td><td><div class="queue-progress'+activeClass+'"><span style="width:'+pct.toFixed(1)+'%"></span><b>'+Math.round(pct)+'%</b></div></td><td>'+fmtBytes(t.loaded)+'</td><td>'+(t.status==='uploading'?fmtBytes(t.speed)+'/s':'--')+'</td></tr>'; }).join(''); }
    function openQueueModal(){ renderQueue(); document.getElementById('queue-modal').classList.add('open'); }
    function closeQueueModal(){ document.getElementById('queue-modal').classList.remove('open'); }
    function uploadSingleFile(file,parent){ return new Promise(function(resolve,reject){ var task={id:'up_'+(++uploadTaskSeq),name:file.name,size:file.size,loaded:0,progress:0,speed:0,status:'waiting',error:''}; uploadTasks.unshift(task); renderQueue(); var xhr=new XMLHttpRequest(); var lastLoaded=0; var lastAt=Date.now(); xhr.open('POST',apiBase+'/drive/upload',true); xhr.setRequestHeader('Authorization','Bearer '+token); xhr.upload.onloadstart=function(){task.status='uploading';lastAt=Date.now();renderQueue();}; xhr.upload.onprogress=function(e){ if(e.lengthComputable){ var now=Date.now(); var dt=Math.max((now-lastAt)/1000,.001); task.loaded=e.loaded; task.progress=e.total>0?e.loaded/e.total*100:0; task.speed=Math.max(0,(e.loaded-lastLoaded)/dt); lastLoaded=e.loaded; lastAt=now; renderQueue(); } }; xhr.onload=function(){ var data=null; try{data=xhr.responseText?JSON.parse(xhr.responseText):null;}catch(e){} if(xhr.status===401){ localStorage.removeItem('cloud_token'); location.href='/login.html'; reject(new Error('登录已过期')); return; } if(xhr.status>=200&&xhr.status<300){ task.loaded=file.size; task.progress=100; task.speed=0; task.status='done'; renderQueue(); resolve(data||{}); } else { task.status='failed'; task.error=(data&&data.error)||'上传失败'; task.speed=0; renderQueue(); reject(new Error(task.error)); } }; xhr.onerror=function(){ task.status='failed'; task.error='网络异常'; task.speed=0; renderQueue(); reject(new Error(task.error)); }; var fd=new FormData(); fd.append('parent',parent); fd.append('files',file); xhr.send(fd); }); }
    async function newFolder(){ var name=prompt('请输入文件夹名称'); if(!name)return; await api('/drive/folders',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name:name,parent:currentParent()})}); await loadState(); toast('文件夹已创建'); }
    async function handleUpload(list){ var arr=Array.prototype.slice.call(list); if(!arr.length)return; var parent=currentParent(); openQueueModal(); toast('已加入上传队列'); var settled=await Promise.allSettled(arr.map(function(f){return uploadSingleFile(f,parent);})); await loadState(); var failed=settled.filter(function(r){return r.status==='rejected';}).length; toast(failed?'部分上传失败：'+failed+' 个':'已上传 '+arr.length+' 个文件'); }
    async function saveQuota(){ var v=parseInt(document.getElementById('quota-input').value,10); if(!v||v<=0){toast('请输入有效容量');return;} await api('/drive/quota',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({quota_gb:v})}); await loadState(); toast('已更新总容量'); }
    async function updateItem(id,payload,msg){ await api('/drive/items/'+encodeURIComponent(id),{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(payload)}); await loadState(); if(msg)toast(msg); }
    function toggleStar(id){ var f=findItem(id); if(f) updateItem(id,{starred:!f.starred},f.starred?'已取消星标':'已添加星标'); }
    function renameItem(id){ var f=findItem(id); if(!f)return; var name=prompt('请输入新名称',f.name); if(name&&name!==f.name) updateItem(id,{name:name},'已重命名'); }
    async function trashItem(id){ var f=findItem(id); if(!f||!confirm('确定将「'+f.name+'」移入回收站？文件夹将连同其中内容一起移入。'))return; await api('/drive/items/'+encodeURIComponent(id)+'/trash',{method:'POST'}); selectedId=null; await loadState(); toast('已移入回收站'); }
    async function restoreItem(id){ await api('/drive/items/'+encodeURIComponent(id)+'/restore',{method:'POST'}); await loadState(); toast('已还原'); }
    async function purgeItem(id){ var f=findItem(id); if(!f||!confirm('彻底删除后无法恢复，确认继续？'))return; await api('/drive/items/'+encodeURIComponent(id),{method:'DELETE'}); selectedId=null; await loadState(); toast('已彻底删除'); }
    function folderPath(folder){ var parts=[folder.name]; var p=folder.parent; var guard=0; while(p&&guard++<64){ var pf=findItem(p); if(!pf)break; parts.unshift(pf.name); p=pf.parent; } return '/ '+parts.join(' / '); }
    function collectDescendantsLocal(id){ var ids=[id], q=[id]; while(q.length){ var p=q.shift(); files.forEach(function(f){ if(f.parent===p){ids.push(f.id);q.push(f.id);} }); } return ids; }
    function moveItem(id){ var f=findItem(id); if(!f)return; var blocked=collectDescendantsLocal(id); var folders=files.filter(function(x){return x.folder&&!x.trashed&&blocked.indexOf(x.id)===-1;}); var options=[{id:'',name:'/ 根目录'}].concat(folders.map(function(x){return{id:x.id,name:folderPath(x)};})).filter(function(o){return o.id!==(f.parent||'');}); var body=document.getElementById('move-body'); body.innerHTML='<div style="color:#6b7280;font-size:13px;margin-bottom:10px">移动「'+escapeHTML(f.name)+'」到：</div>'; if(!options.length) body.innerHTML+='<div style="color:#9aa3af;padding:20px 0">没有可选的目标文件夹</div>'; options.forEach(function(opt){ var b=document.createElement('button'); b.className='btn'; b.style.cssText='display:flex;width:100%;justify-content:flex-start;text-align:left;margin-bottom:8px;padding:10px 14px;font-size:13px;color:#1f2937'; b.textContent=opt.name; b.onclick=function(){ closeMoveModal(); updateItem(id,{parent:opt.id},'已移动'); }; body.appendChild(b); }); document.getElementById('move-modal').classList.add('open'); }
    function closeMoveModal(){ document.getElementById('move-modal').classList.remove('open'); document.getElementById('move-body').innerHTML=''; }
    function showRowMenu(btn){ closeRowMenu(); var id=btn.getAttribute('data-id'); var f=findItem(id); if(!f)return; var items=[]; if(section==='trash'||f.trashed){items.push({label:'还原',act:'restore'});items.push({label:'彻底删除',act:'purge',danger:true});} else { if(f.folder)items.push({label:'进入文件夹',act:'enter'}); else {items.push({label:'查看',act:'open'});items.push({label:'下载',act:'download'});} items.push({label:f.starred?'取消星标':'添加星标',act:'star'});items.push({label:'移动到...',act:'move'});items.push({label:'重命名',act:'rename'});items.push({sep:true});items.push({label:'移入回收站',act:'trash',danger:true}); } var menu=document.createElement('div'); menu.className='menu'; items.forEach(function(it){ if(it.sep){var s=document.createElement('div');s.className='sep';menu.appendChild(s);return;} var mb=document.createElement('button'); mb.textContent=it.label; if(it.danger)mb.className='danger'; mb.onclick=function(e){e.stopPropagation();closeRowMenu();runRowAction(id,it.act);}; menu.appendChild(mb); }); document.body.appendChild(menu); btn.classList.add('open'); menu._anchor=btn; var r=btn.getBoundingClientRect(); var x=Math.max(8,Math.min(window.innerWidth-menu.offsetWidth-8,r.right-menu.offsetWidth)); var y=r.bottom+4; if(y+menu.offsetHeight>window.innerHeight-8)y=Math.max(8,r.top-menu.offsetHeight-4); menu.style.left=x+'px'; menu.style.top=y+'px'; setTimeout(function(){document.addEventListener('click',onDocClickCloseMenu,true);document.addEventListener('keydown',onEscCloseMenu);window.addEventListener('resize',closeRowMenu);},0); }
    function runRowAction(id,act){ var f=findItem(id); if(act==='open')openPreview(id); else if(act==='download')downloadFile(id); else if(act==='star')toggleStar(id); else if(act==='rename')renameItem(id); else if(act==='move')moveItem(id); else if(act==='trash')trashItem(id); else if(act==='restore')restoreItem(id); else if(act==='purge')purgeItem(id); else if(act==='enter'&&f&&f.folder){cwd.push({id:f.id,name:f.name});renderAll();} }
    function onDocClickCloseMenu(e){var m=document.querySelector('.menu');if(m&&!m.contains(e.target))closeRowMenu();} function onEscCloseMenu(e){if(e.key==='Escape')closeRowMenu();} function closeRowMenu(){document.querySelectorAll('.menu').forEach(function(m){if(m._anchor)m._anchor.classList.remove('open');m.remove();});document.removeEventListener('click',onDocClickCloseMenu,true);document.removeEventListener('keydown',onEscCloseMenu);window.removeEventListener('resize',closeRowMenu);}
    function trackObjectURL(url){activeObjectURLs.push(url);return url;} function revokeActiveObjectURLs(){activeObjectURLs.forEach(function(u){try{URL.revokeObjectURL(u);}catch(e){}});activeObjectURLs=[];}
    async function fetchBlob(id,download){ var res=await fetch(apiBase+'/drive/files/'+encodeURIComponent(id)+(download?'/download':''),{headers:authHeaders({})}); if(res.status===401){localStorage.removeItem('cloud_token');location.href='/login.html';throw new Error('登录已过期');} if(!res.ok)throw new Error('文件数据不可用'); return await res.blob(); }
    async function downloadFile(id){ var f=findItem(id); if(!f||f.folder)return; try{ var blob=await fetchBlob(id,true); var url=URL.createObjectURL(blob); var a=document.createElement('a'); a.href=url; a.download=f.name; document.body.appendChild(a); a.click(); a.remove(); setTimeout(function(){URL.revokeObjectURL(url);},2000); }catch(e){toast(e.message||'下载失败');} }
    async function openPreview(id){ var f=findItem(id); if(!f||f.folder)return; var body=document.getElementById('modal-body'); body.innerHTML='<div class="empty">加载中...</div>'; body.className='modal-body center'; document.getElementById('modal-title').textContent=f.name; document.getElementById('modal-meta').textContent=fmtBytes(f.size)+' · '+typeLabel(f); document.getElementById('modal-download').onclick=function(){downloadFile(id);}; document.getElementById('modal').classList.add('open'); try{ var blob=await fetchBlob(id,false); var url=trackObjectURL(URL.createObjectURL(blob)); var cat=fileCategory(f.mime,f.name); body.innerHTML=''; body.className='modal-body center'; if(cat==='image'){var img=document.createElement('img');img.src=url;body.appendChild(img);} else if(cat==='video'){var v=document.createElement('video');v.src=url;v.controls=true;v.playsInline=true;body.appendChild(v);} else if(cat==='audio'){var a=document.createElement('audio');a.src=url;a.controls=true;body.appendChild(a);} else if(cat==='pdf'){body.className='modal-body';var ifr=document.createElement('iframe');ifr.src=url;body.appendChild(ifr);} else if(cat==='doc'){body.className='modal-body';var text=await blob.text();var lower=f.name.toLowerCase(); if(lower.endsWith('.csv')||lower.endsWith('.tsv')) body.innerHTML=csvToTable(text,lower.endsWith('.tsv')?'\t':','); else {var pre=document.createElement('pre');pre.textContent=text;body.appendChild(pre);}} else body.innerHTML='<div style="padding:48px;color:#9aa3af">该文件类型不支持在线查看，请下载后打开</div>'; }catch(e){ body.innerHTML='<div style="padding:48px;color:#9aa3af">'+escapeHTML(e.message||'文件数据不可用')+'</div>'; } }
    function csvToTable(text,sep){ var rows=parseCSV(text,sep); if(!rows.length)return'<pre style="padding:22px">空文件</pre>'; var html='<div style="padding:18px"><table class="csv-table"><thead><tr>'; rows[0].forEach(function(h){html+='<th>'+escapeHTML(h)+'</th>';}); html+='</tr></thead><tbody>'; rows.slice(1).forEach(function(r){html+='<tr>';r.forEach(function(c){html+='<td>'+escapeHTML(c)+'</td>';});html+='</tr>';}); return html+'</tbody></table></div>'; }
    function parseCSV(text,sep){ var rows=[],row=[],field='',q=false; for(var i=0;i<text.length;i++){var ch=text.charAt(i); if(q){if(ch==='"'&&text.charAt(i+1)==='"'){field+='"';i++;}else if(ch==='"')q=false;else field+=ch;} else {if(ch==='"')q=true;else if(ch===sep){row.push(field);field='';}else if(ch==='\n'){row.push(field);rows.push(row);row=[];field='';}else if(ch!=='\r')field+=ch;}} if(field.length||row.length){row.push(field);rows.push(row);} return rows; }
    function closeModal(){document.getElementById('modal').classList.remove('open');document.getElementById('modal-body').innerHTML='';revokeActiveObjectURLs();}
    function logout(){ if(!confirm('确定退出登录？'))return; localStorage.removeItem('cloud_token'); localStorage.removeItem('cloud_user'); location.href='/login.html'; }
    document.getElementById('upload-btn').onclick=function(){document.getElementById('file-input').click();}; document.getElementById('file-input').onchange=function(){handleUpload(this.files).catch(function(e){toast(e.message||'上传失败');});this.value='';}; document.getElementById('folder-btn').onclick=function(){newFolder().catch(function(e){toast(e.message||'创建失败');});}; document.getElementById('quota-save').onclick=function(){saveQuota().catch(function(e){toast(e.message||'保存失败');});}; document.getElementById('search').oninput=function(){query=this.value;renderAll();}; document.getElementById('list-view').onclick=function(){view='list';this.classList.add('active');document.getElementById('grid-view').classList.remove('active');renderAll();}; document.getElementById('grid-view').onclick=function(){view='grid';this.classList.add('active');document.getElementById('list-view').classList.remove('active');renderAll();}; document.getElementById('trash-clear').onclick=async function(){ if(!files.some(function(f){return f.trashed;}))return; if(!confirm('确定清空回收站？所有项目将无法恢复'))return; await api('/drive/trash',{method:'DELETE'}); await loadState(); toast('回收站已清空'); };
    document.getElementById('modal-close').onclick=closeModal; document.getElementById('modal').addEventListener('click',function(e){if(e.target===this)closeModal();}); document.getElementById('move-close').onclick=closeMoveModal; document.getElementById('move-modal').addEventListener('click',function(e){if(e.target===this)closeMoveModal();}); document.getElementById('queue-btn').onclick=openQueueModal; document.getElementById('queue-close').onclick=closeQueueModal; document.getElementById('queue-modal').addEventListener('click',function(e){if(e.target===this)closeQueueModal();}); document.addEventListener('keydown',function(e){if(e.key==='Escape'){closeModal();closeMoveModal();closeQueueModal();closeRowMenu();}}); document.querySelectorAll('.rail-btn').forEach(function(b){b.onclick=function(){pane=b.getAttribute('data-pane');section='all';query='';document.getElementById('search').value='';renderAll();};}); document.querySelectorAll('.quick-item').forEach(function(q){q.onclick=function(){section=q.getAttribute('data-section');pane='storage';renderAll();};}); document.getElementById('user-area').onclick=logout;
    setUser(); renderQueue(); renderAll(); verify().then(loadState).catch(function(e){toast(e.message||'认证失败');});
    </script>
</body>
</html>`
