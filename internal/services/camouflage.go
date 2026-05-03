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

// deploySiteHTML writes the StreamVault HTML files to the site directory
func (s *CamouflageService) deploySiteHTML(domain string) error {
	siteRoot := s.siteRootHostPath(domain)

	indexHTML := streamVaultIndexHTML
	if items, err := fetchPublicMediaItems(); err == nil && len(items) > 0 {
		indexHTML = injectMediaItems(indexHTML, items)
	}

	// Write index.html
	if err := os.WriteFile(filepath.Join(siteRoot, "index.html"), []byte(indexHTML), 0644); err != nil {
		return err
	}

	// Write login.html
	if err := os.WriteFile(filepath.Join(siteRoot, "login.html"), []byte(streamVaultLoginHTML), 0644); err != nil {
		return err
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

	config := fmt.Sprintf(`# StreamVault Camouflage Site - Auto-generated by proxy_version
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

    # Security headers
    add_header X-Content-Type-Options "nosniff" always;
    add_header X-Frame-Options "SAMEORIGIN" always;
    add_header X-XSS-Protection "1; mode=block" always;
    add_header Referrer-Policy "strict-origin-when-cross-origin" always;

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
`, domain, domain, siteRootContainerPath, domain, certContainerPath, keyContainerPath, siteRootContainerPath, logContainerDir, domain, logContainerDir, domain)

	confPath := filepath.Join(confDir, fmt.Sprintf("streamvault_%s.conf", strings.ReplaceAll(domain, ".", "_")))
	return os.WriteFile(confPath, []byte(config), 0644)
}

// removeNginxConfig removes the site configuration (for rollback)
func (s *CamouflageService) removeNginxConfig(domain string) {
	confPath := filepath.Join(s.getConfDir(), fmt.Sprintf("streamvault_%s.conf", strings.ReplaceAll(domain, ".", "_")))
	os.Remove(confPath)
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

func (s *CamouflageService) sslHostDir(domain string) string {
	return filepath.Join(s.hostSSLDir, domain)
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
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>StreamVault — Your Premium Private Theater</title>
    <meta name="description" content="StreamVault is your personal media streaming platform. Organize, stream, and enjoy your private collection anywhere.">
    <style>
        *{margin:0;padding:0;box-sizing:border-box}
        :root{--bg:#0a0a0f;--surface:#12121a;--card:#1a1a2e;--accent:#6c5ce7;--accent2:#a855f7;--text:#e4e4e7;--muted:#71717a;--border:rgba(255,255,255,0.06)}
        body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;background:var(--bg);color:var(--text);min-height:100vh;overflow-x:hidden}
        a{color:var(--accent);text-decoration:none;transition:color .2s}
        a:hover{color:var(--accent2)}

        /* Navigation */
        .nav{position:fixed;top:0;width:100%;padding:1rem 2rem;display:flex;align-items:center;justify-content:space-between;z-index:100;background:rgba(10,10,15,0.85);backdrop-filter:blur(20px);border-bottom:1px solid var(--border)}
        .nav-logo{display:flex;align-items:center;gap:.75rem;font-size:1.25rem;font-weight:700;letter-spacing:-.02em}
        .nav-logo svg{width:32px;height:32px;fill:var(--accent)}
        .nav-links{display:flex;gap:2rem;align-items:center}
        .nav-links a{color:var(--muted);font-size:.9rem;font-weight:500;transition:color .2s}
        .nav-links a:hover{color:var(--text)}
        .nav-signin{padding:.5rem 1.25rem;background:var(--accent);color:#fff;border-radius:8px;font-size:.85rem;font-weight:600;transition:all .2s}
        .nav-signin:hover{background:var(--accent2);color:#fff;transform:translateY(-1px)}

        /* Hero */
        .hero{padding:10rem 2rem 4rem;text-align:center;position:relative}
        .hero::before{content:'';position:absolute;top:0;left:50%;transform:translateX(-50%);width:600px;height:600px;background:radial-gradient(circle,rgba(108,92,231,0.15),transparent 70%);pointer-events:none}
        .hero h1{font-size:3.5rem;font-weight:800;letter-spacing:-.03em;line-height:1.1;margin-bottom:1rem;background:linear-gradient(135deg,var(--text),var(--accent));-webkit-background-clip:text;-webkit-text-fill-color:transparent;background-clip:text}
        .hero p{font-size:1.15rem;color:var(--muted);max-width:560px;margin:0 auto 2.5rem;line-height:1.6}
        .hero-actions{display:flex;gap:1rem;justify-content:center;flex-wrap:wrap}
        .btn-primary{padding:.75rem 2rem;background:linear-gradient(135deg,var(--accent),var(--accent2));color:#fff;border:none;border-radius:10px;font-size:.95rem;font-weight:600;cursor:pointer;transition:all .3s}
        .btn-primary:hover{transform:translateY(-2px);box-shadow:0 8px 30px rgba(108,92,231,0.3)}
        .btn-outline{padding:.75rem 2rem;background:transparent;color:var(--text);border:1px solid var(--border);border-radius:10px;font-size:.95rem;font-weight:500;cursor:pointer;transition:all .3s}
        .btn-outline:hover{border-color:var(--accent);background:rgba(108,92,231,0.05)}

        /* Features */
        .features{padding:4rem 2rem;max-width:1100px;margin:0 auto}
        .features h2{font-size:2rem;font-weight:700;text-align:center;margin-bottom:3rem;letter-spacing:-.02em}
        .features-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(300px,1fr));gap:1.5rem}
        .feature-card{background:var(--card);border:1px solid var(--border);border-radius:16px;padding:2rem;transition:all .3s}
        .feature-card:hover{border-color:rgba(108,92,231,0.3);transform:translateY(-4px);box-shadow:0 12px 40px rgba(0,0,0,0.3)}
        .feature-icon{width:48px;height:48px;border-radius:12px;display:flex;align-items:center;justify-content:center;margin-bottom:1rem;font-size:1.5rem}
        .feature-card h3{font-size:1.1rem;font-weight:600;margin-bottom:.5rem}
        .feature-card p{color:var(--muted);font-size:.9rem;line-height:1.6}

        /* Content Grid */
        .content-section{padding:4rem 2rem;max-width:1200px;margin:0 auto}
        .content-section h2{font-size:2rem;font-weight:700;margin-bottom:.5rem;letter-spacing:-.02em}
        .content-section .subtitle{color:var(--muted);margin-bottom:2rem;font-size:1rem}
        .content-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(200px,1fr));gap:1.25rem}
        .content-card{background:var(--card);border-radius:12px;overflow:hidden;transition:all .3s;cursor:pointer;border:1px solid var(--border)}
        .content-card:hover{transform:scale(1.03);box-shadow:0 8px 30px rgba(0,0,0,0.4)}
        .content-thumb{width:100%;aspect-ratio:2/3;position:relative}
        .content-thumb .gradient{width:100%;height:100%;border-radius:0}
        .content-card .info{padding:.75rem}
        .content-card .title{font-size:.85rem;font-weight:600;margin-bottom:.25rem;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
        .content-card .meta{font-size:.75rem;color:var(--muted)}
        .rating{color:#f59e0b;font-size:.75rem}

        /* Footer */
        .footer{padding:3rem 2rem;text-align:center;color:var(--muted);font-size:.8rem;border-top:1px solid var(--border);margin-top:4rem}
        .footer a{color:var(--muted)}

        @media(max-width:768px){
            .hero h1{font-size:2rem}
            .hero p{font-size:1rem}
            .nav-links{display:none}
            .content-grid{grid-template-columns:repeat(auto-fill,minmax(140px,1fr))}
        }
    </style>
</head>
<body>
    <nav class="nav">
        <div class="nav-logo">
            <svg viewBox="0 0 24 24"><path d="M4 8h4V4H4v4zm6 12h4v-4h-4v4zm-6 0h4v-4H4v4zm0-6h4v-4H4v4zm6 0h4v-4h-4v4zm6-10v4h4V4h-4zm-6 4h4V4h-4v4zm6 6h4v-4h-4v4zm0 6h4v-4h-4v4z"/></svg>
            StreamVault
        </div>
        <div class="nav-links">
            <a href="#">Browse</a>
            <a href="#">My Library</a>
            <a href="#">Collections</a>
            <a href="/login.html" class="nav-signin">Sign In</a>
        </div>
    </nav>

    <section class="hero">
        <h1>Your Premium<br>Private Theater</h1>
        <p>Organize, stream, and enjoy your personal media collection from anywhere. Beautiful interface, powerful organization, complete privacy.</p>
        <div class="hero-actions">
            <a href="/login.html" class="btn-primary">Get Started</a>
            <button class="btn-outline" onclick="document.querySelector('.features').scrollIntoView({behavior:'smooth'})">Learn More</button>
        </div>
    </section>

    <section class="features">
        <h2>Built for Media Enthusiasts</h2>
        <div class="features-grid">
            <div class="feature-card">
                <div class="feature-icon" style="background:rgba(108,92,231,0.15)">🎬</div>
                <h3>Smart Library</h3>
                <p>Automatically organize your media with rich metadata, cover art, and intelligent categorization.</p>
            </div>
            <div class="feature-card">
                <div class="feature-icon" style="background:rgba(168,85,247,0.15)">🌐</div>
                <h3>Stream Anywhere</h3>
                <p>Access your collection from any device. Adaptive streaming ensures smooth playback on any connection.</p>
            </div>
            <div class="feature-card">
                <div class="feature-icon" style="background:rgba(59,130,246,0.15)">🔒</div>
                <h3>Complete Privacy</h3>
                <p>Your media stays on your server. End-to-end encryption and zero data collection guarantee your privacy.</p>
            </div>
        </div>
    </section>

    <section class="content-section">
        <h2>Trending Now</h2>
        <p class="subtitle">Popular titles in your library</p>
        <div class="content-grid" id="content-grid"></div>
    </section>

    <footer class="footer">
        <p>&copy; 2024 StreamVault. Self-hosted media streaming platform. All rights reserved.</p>
        <p style="margin-top:.5rem"><a href="#">Privacy Policy</a> &middot; <a href="#">Terms of Service</a> &middot; <a href="#">Support</a></p>
    </footer>

    <script>
    // Generate mock content cards
    const titles = [
        {t:"Midnight Protocol",y:"2024",r:"8.7"},{t:"Neon Horizons",y:"2023",r:"9.1"},
        {t:"The Last Signal",y:"2024",r:"7.9"},{t:"Quantum Dreams",y:"2023",r:"8.4"},
        {t:"Cipher",y:"2024",r:"8.2"},{t:"Dark Matter",y:"2023",r:"7.6"},
        {t:"Echoes of Tomorrow",y:"2024",r:"8.8"},{t:"Zero Day",y:"2023",r:"9.0"},
        {t:"Silent Frequency",y:"2024",r:"7.5"},{t:"The Architect",y:"2023",r:"8.1"}
    ];
    const colors = [
        ["#1a1a2e","#6c5ce7"],["#0f0f23","#e17055"],["#1a0a2e","#a855f7"],
        ["#0a1a2e","#3b82f6"],["#1a2e0a","#22c55e"],["#2e1a0a","#f59e0b"],
        ["#2e0a1a","#ef4444"],["#0a2e2e","#14b8a6"],["#1a1a1a","#8b5cf6"],
        ["#0f1a2e","#06b6d4"]
    ];
    const grid = document.getElementById("content-grid");
    titles.forEach((item,i) => {
        const [c1,c2] = colors[i % colors.length];
        grid.innerHTML += '<div class="content-card" onclick="window.location.href=\'/login.html\'">' +
            '<div class="content-thumb"><div class="gradient" style="background:linear-gradient(135deg,'+c1+','+c2+')"></div></div>' +
            '<div class="info"><div class="title">'+item.t+'</div>' +
            '<div class="meta"><span class="rating">★ '+item.r+'</span> &middot; '+item.y+'</div></div></div>';
    });
    </script>
</body>
</html>`

const streamVaultLoginHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Sign In — StreamVault</title>
    <meta name="description" content="Sign in to your StreamVault account to access your private media library.">
    <style>
        *{margin:0;padding:0;box-sizing:border-box}
        :root{--bg:#0a0a0f;--surface:#12121a;--card:#1a1a2e;--accent:#6c5ce7;--accent2:#a855f7;--text:#e4e4e7;--muted:#71717a;--border:rgba(255,255,255,0.08);--error:#ef4444}
        body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;background:var(--bg);color:var(--text);min-height:100vh;display:flex;align-items:center;justify-content:center;position:relative;overflow:hidden}
        body::before{content:'';position:absolute;top:-50%;left:-50%;width:200%;height:200%;background:radial-gradient(circle at 30% 50%,rgba(108,92,231,0.08),transparent 50%),radial-gradient(circle at 70% 50%,rgba(168,85,247,0.06),transparent 50%);animation:bg-shift 20s ease infinite alternate;pointer-events:none}
        @keyframes bg-shift{from{transform:rotate(0deg)}to{transform:rotate(3deg)}}

        .login-container{width:100%;max-width:420px;padding:2rem;position:relative;z-index:1}
        .login-header{text-align:center;margin-bottom:2.5rem}
        .login-logo{display:flex;align-items:center;justify-content:center;gap:.75rem;font-size:1.5rem;font-weight:700;margin-bottom:.75rem;letter-spacing:-.02em}
        .login-logo svg{width:36px;height:36px;fill:var(--accent)}
        .login-subtitle{color:var(--muted);font-size:.95rem}

        .login-card{background:var(--card);border:1px solid var(--border);border-radius:20px;padding:2.5rem;backdrop-filter:blur(20px)}
        .form-group{margin-bottom:1.5rem}
        .form-group label{display:block;font-size:.85rem;font-weight:500;margin-bottom:.5rem;color:var(--text)}
        .form-input{width:100%;padding:.85rem 1rem;background:var(--surface);border:1px solid var(--border);border-radius:10px;font-size:.95rem;color:var(--text);outline:none;transition:all .2s}
        .form-input:focus{border-color:var(--accent);box-shadow:0 0 0 3px rgba(108,92,231,0.15)}
        .form-input::placeholder{color:var(--muted)}

        .remember-row{display:flex;align-items:center;justify-content:space-between;margin-bottom:1.5rem;font-size:.85rem}
        .remember-row label{display:flex;align-items:center;gap:.5rem;color:var(--muted);cursor:pointer}
        .remember-row input[type=checkbox]{accent-color:var(--accent);width:16px;height:16px}
        .forgot-link{color:var(--accent);font-size:.85rem}
        .forgot-link:hover{color:var(--accent2)}

        .btn-submit{width:100%;padding:.85rem;background:linear-gradient(135deg,var(--accent),var(--accent2));color:#fff;border:none;border-radius:10px;font-size:1rem;font-weight:600;cursor:pointer;transition:all .3s;position:relative;overflow:hidden}
        .btn-submit:hover{transform:translateY(-1px);box-shadow:0 8px 25px rgba(108,92,231,0.35)}
        .btn-submit:active{transform:translateY(0)}
        .btn-submit:disabled{opacity:.7;cursor:not-allowed;transform:none}
        .btn-submit .spinner{display:none;width:20px;height:20px;border:2px solid rgba(255,255,255,0.3);border-top-color:#fff;border-radius:50%;animation:spin .6s linear infinite;margin:0 auto}
        @keyframes spin{to{transform:rotate(360deg)}}

        .error-msg{display:none;margin-top:1rem;padding:.85rem 1rem;background:rgba(239,68,68,0.1);border:1px solid rgba(239,68,68,0.2);border-radius:10px;color:var(--error);font-size:.85rem;text-align:center;animation:shake .5s ease}
        @keyframes shake{0%,100%{transform:translateX(0)}25%{transform:translateX(-5px)}75%{transform:translateX(5px)}}

        .login-footer{text-align:center;margin-top:2rem;font-size:.85rem;color:var(--muted)}
        .login-footer a{color:var(--accent)}

        .back-link{position:absolute;top:2rem;left:2rem;color:var(--muted);font-size:.9rem;display:flex;align-items:center;gap:.5rem;z-index:10}
        .back-link:hover{color:var(--text)}
        .back-link svg{width:18px;height:18px;fill:currentColor}

        @media(max-width:480px){
            .login-container{padding:1rem}
            .login-card{padding:1.5rem}
        }
    </style>
</head>
<body>
    <a href="/" class="back-link">
        <svg viewBox="0 0 24 24"><path d="M20 11H7.83l5.59-5.59L12 4l-8 8 8 8 1.41-1.41L7.83 13H20v-2z"/></svg>
        Back
    </a>

    <div class="login-container">
        <div class="login-header">
            <div class="login-logo">
                <svg viewBox="0 0 24 24"><path d="M4 8h4V4H4v4zm6 12h4v-4h-4v4zm-6 0h4v-4H4v4zm0-6h4v-4H4v4zm6 0h4v-4h-4v4zm6-10v4h4V4h-4zm-6 4h4V4h-4v4zm6 6h4v-4h-4v4zm0 6h4v-4h-4v4z"/></svg>
                StreamVault
            </div>
            <p class="login-subtitle">Sign in to access your library</p>
        </div>

        <div class="login-card">
            <form id="login-form" onsubmit="return handleLogin(event)">
                <div class="form-group">
                    <label for="email">Email Address</label>
                    <input type="email" id="email" class="form-input" placeholder="you@example.com" required autocomplete="email">
                </div>
                <div class="form-group">
                    <label for="password">Password</label>
                    <input type="password" id="password" class="form-input" placeholder="Enter your password" required autocomplete="current-password">
                </div>
                <div class="remember-row">
                    <label><input type="checkbox" checked> Remember me</label>
                    <a href="#" class="forgot-link" onclick="showForgot(event)">Forgot password?</a>
                </div>
                <button type="submit" class="btn-submit" id="submit-btn">
                    <span class="btn-text">Sign In</span>
                    <div class="spinner"></div>
                </button>
                <div class="error-msg" id="error-msg"></div>
            </form>
        </div>

        <div class="login-footer">
            <p>This is a private server. <a href="#">Contact administrator</a> for access.</p>
        </div>
    </div>

    <script>
    let attempts = 0;
    const errors = [
        "Invalid email or password. Please try again.",
        "Authentication failed. Please check your credentials.",
        "Login failed. Please verify your account details.",
    ];
    const lockMsg = "Too many failed attempts. Account temporarily locked for security. Please try again in 30 minutes.";

    function handleLogin(e) {
        e.preventDefault();
        const btn = document.getElementById("submit-btn");
        const btnText = btn.querySelector(".btn-text");
        const spinner = btn.querySelector(".spinner");
        const errEl = document.getElementById("error-msg");

        btn.disabled = true;
        btnText.style.display = "none";
        spinner.style.display = "block";
        errEl.style.display = "none";

        setTimeout(function() {
            spinner.style.display = "none";
            btnText.style.display = "block";
            attempts++;
            if (attempts >= 3) {
                errEl.textContent = lockMsg;
                btn.disabled = true;
                setTimeout(function() { btn.disabled = false; attempts = 0; }, 30000);
            } else {
                errEl.textContent = errors[attempts - 1] || errors[0];
                btn.disabled = false;
            }
            errEl.style.display = "block";
        }, 1500 + Math.random() * 1000);

        return false;
    }

    function showForgot(e) {
        e.preventDefault();
        var errEl = document.getElementById("error-msg");
        errEl.textContent = "Password reset is not available. Please contact the server administrator.";
        errEl.style.display = "block";
    }
    </script>
</body>
</html>`
