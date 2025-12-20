package services

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type CertificateService struct {
	acmePath string
	certDir  string
}

func NewCertificateService() *CertificateService {
	return &CertificateService{
		acmePath: "/root/.acme.sh/acme.sh",
		certDir:  "/etc/v2ray-agent/tls",
	}
}

func (s *CertificateService) ApplyCertificate(domain, email, provider, method, dnsProvider, apiToken, cfEmail string) (string, string, error) {
	// Ensure acme.sh is installed
	if _, err := os.Stat(s.acmePath); os.IsNotExist(err) {
		if err := s.installAcme(email); err != nil {
			return "", "", fmt.Errorf("安装 acme.sh 失败: %v", err)
		}
	}

	// Set default email if not provided - MUST be a valid email, not example.com
	if email == "" {
		return "", "", fmt.Errorf("请提供有效的邮箱地址用于证书申请")
	}

	// Validate email format (basic check)
	if !strings.Contains(email, "@") || strings.Contains(email, "example.com") {
		return "", "", fmt.Errorf("请提供有效的邮箱地址，不能使用示例邮箱")
	}

	// Update acme.sh account email to avoid invalidContact error
	s.runAcme("--update-account", "--email", email)

	// Set CA based on provider
	switch provider {
	case "letsencrypt", "":
		s.runAcme("--set-default-ca", "--server", "letsencrypt")
	case "zerossl":
		s.runAcme("--set-default-ca", "--server", "zerossl")
	case "buypass":
		s.runAcme("--set-default-ca", "--server", "buypass")
	}

	// Ensure cert directory exists
	if err := os.MkdirAll(s.certDir, 0755); err != nil {
		return "", "", fmt.Errorf("创建证书目录失败: %v", err)
	}

	certPath := filepath.Join(s.certDir, domain+".crt")
	keyPath := filepath.Join(s.certDir, domain+".key")

	var output string
	var err error

	if method == "dns" {
		// DNS validation
		output, err = s.issueViaDNS(domain, dnsProvider, apiToken, cfEmail)
	} else {
		// Try webroot first (works with existing reverse proxy)
		output, err = s.issueViaWebroot(domain)
		if err != nil {
			// If webroot fails, try standalone
			output, err = s.issueViaStandalone(domain)
		}
	}

	if err != nil {
		if strings.Contains(output, "Cert success") || strings.Contains(output, "Cert already exists") {
			// Certificate was actually issued successfully
		} else {
			// Check specific errors
			if strings.Contains(output, "port 80 is already used") {
				return "", "", fmt.Errorf("80端口被占用，请使用 DNS 验证方式")
			}
			if strings.Contains(output, "invalid domain") || strings.Contains(output, "Error add TXT") {
				return "", "", fmt.Errorf("DNS 验证失败: %s", s.extractError(output))
			}
			return "", "", fmt.Errorf("证书申请失败: %s", s.extractError(output))
		}
	}

	// Install certificate
	installArgs := []string{
		"--install-cert", "-d", domain,
		"--ecc",
		"--fullchain-file", certPath,
		"--key-file", keyPath,
	}

	installOutput, err := s.runAcme(installArgs...)
	if err != nil && !strings.Contains(installOutput, "Installing") && !strings.Contains(installOutput, "installed") {
		return "", "", fmt.Errorf("安装证书失败: %s", installOutput)
	}

	// Setup auto-renewal cron
	go func() {
		time.Sleep(time.Second * 5)
		s.runAcme("--cron", "--home", "/root/.acme.sh")
	}()

	return certPath, keyPath, nil
}

// extractError gets the relevant error message from acme.sh output
func (s *CertificateService) extractError(output string) string {
	lines := strings.Split(output, "\n")
	var relevantLines []string
	for _, line := range lines {
		if strings.Contains(line, "Error") || strings.Contains(line, "error") || 
		   strings.Contains(line, "invalid") || strings.Contains(line, "response=") {
			relevantLines = append(relevantLines, strings.TrimSpace(line))
		}
	}
	if len(relevantLines) > 0 {
		// Return max 2 lines for readability
		if len(relevantLines) > 2 {
			relevantLines = relevantLines[:2]
		}
		return strings.Join(relevantLines, "; ")
	}
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	return strings.Join(lines, " ")
}

// issueViaDNS uses DNS validation
func (s *CertificateService) issueViaDNS(domain, dnsProvider, apiToken, cfEmail string) (string, error) {
	// Clear any old saved credentials first
	s.cleanSavedCredentials()

	args := []string{"--issue", "-d", domain, "--keylength", "ec-256", "--force", "--debug", "2"}

	// Build environment for the command
	env := os.Environ()

	// Clean the token
	apiToken = strings.TrimSpace(apiToken)
	cfEmail = strings.TrimSpace(cfEmail)

	switch dnsProvider {
	case "cloudflare":
		if apiToken == "" {
			return "", fmt.Errorf("请输入 Cloudflare API Token")
		}

		// Determine authentication method:
		// - Global API Key: 37 chars, used with CF_Key + CF_Email
		// - API Token: 40 chars, used with CF_Token alone
		//
		// If user provides email AND the token looks like a Global API Key, use CF_Key method
		// Otherwise, always use CF_Token method (modern, recommended)
		
		isGlobalKey := cfEmail != "" && len(apiToken) >= 32 && len(apiToken) <= 40
		
		if isGlobalKey {
			// Global API Key method (legacy)
			env = append(env, "CF_Key="+apiToken)
			env = append(env, "CF_Email="+cfEmail)
			// Clear CF_Token to avoid conflicts
			for i, e := range env {
				if strings.HasPrefix(e, "CF_Token=") {
					env = append(env[:i], env[i+1:]...)
					break
				}
			}
		} else {
			// API Token method (recommended)
			env = append(env, "CF_Token="+apiToken)
			// Clear CF_Key and CF_Email to avoid conflicts
			var newEnv []string
			for _, e := range env {
				if !strings.HasPrefix(e, "CF_Key=") && !strings.HasPrefix(e, "CF_Email=") {
					newEnv = append(newEnv, e)
				}
			}
			env = newEnv
			env = append(env, "CF_Token="+apiToken)
		}
		args = append(args, "--dns", "dns_cf")

	case "aliyun":
		parts := strings.Split(apiToken, ":")
		if len(parts) != 2 {
			return "", fmt.Errorf("阿里云格式错误，请使用: AccessKey:AccessSecret")
		}
		env = append(env, "Ali_Key="+strings.TrimSpace(parts[0]))
		env = append(env, "Ali_Secret="+strings.TrimSpace(parts[1]))
		args = append(args, "--dns", "dns_ali")

	case "dnspod":
		parts := strings.Split(apiToken, ",")
		if len(parts) != 2 {
			return "", fmt.Errorf("DNSPod 格式错误，请使用: ID,Token")
		}
		env = append(env, "DP_Id="+strings.TrimSpace(parts[0]))
		env = append(env, "DP_Key="+strings.TrimSpace(parts[1]))
		args = append(args, "--dns", "dns_dp")

	default:
		return "", fmt.Errorf("不支持的 DNS 服务商: %s", dnsProvider)
	}

	return s.runAcmeWithEnv(args, env)
}

// cleanSavedCredentials removes old saved credentials from account.conf
func (s *CertificateService) cleanSavedCredentials() {
	confPath := "/root/.acme.sh/account.conf"
	data, err := os.ReadFile(confPath)
	if err != nil {
		return
	}

	lines := strings.Split(string(data), "\n")
	var newLines []string
	for _, line := range lines {
		// Skip saved CF/Ali/DP credentials
		if strings.HasPrefix(line, "SAVED_CF") || strings.HasPrefix(line, "SAVED_Ali") || strings.HasPrefix(line, "SAVED_DP") {
			continue
		}
		newLines = append(newLines, line)
	}

	os.WriteFile(confPath, []byte(strings.Join(newLines, "\n")), 0644)
}

// issueViaWebroot uses existing web server
func (s *CertificateService) issueViaWebroot(domain string) (string, error) {
	webrootPaths := []string{
		"/opt/1panel/apps/openresty/openresty/www/sites/" + domain,
		"/www/wwwroot/" + domain,
		"/var/www/" + domain,
		"/var/www/html",
	}

	for _, webroot := range webrootPaths {
		if _, err := os.Stat(webroot); err == nil {
			wellKnown := filepath.Join(webroot, ".well-known", "acme-challenge")
			os.MkdirAll(wellKnown, 0755)

			args := []string{
				"--issue", "-d", domain,
				"--webroot", webroot,
				"--keylength", "ec-256",
				"--force",
			}
			output, err := s.runAcme(args...)
			if err == nil || strings.Contains(output, "Cert success") {
				return output, nil
			}
		}
	}

	return "", fmt.Errorf("未找到可用的网站目录")
}

// issueViaStandalone uses standalone mode
func (s *CertificateService) issueViaStandalone(domain string) (string, error) {
	args := []string{
		"--issue", "-d", domain,
		"--standalone",
		"--httpport", "80",
		"--keylength", "ec-256",
		"--force",
	}
	return s.runAcme(args...)
}

// runAcme executes acme.sh with bash explicitly
func (s *CertificateService) runAcme(args ...string) (string, error) {
	return s.runAcmeWithEnv(args, os.Environ())
}

// runAcmeWithEnv executes acme.sh with custom environment
func (s *CertificateService) runAcmeWithEnv(args []string, env []string) (string, error) {
	fullArgs := append([]string{s.acmePath}, args...)
	cmd := exec.Command("/bin/bash", fullArgs...)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func (s *CertificateService) installAcme(email string) error {
	if email == "" {
		email = "admin@example.com"
	}

	cmd := exec.Command("/bin/bash", "-c", fmt.Sprintf("curl https://get.acme.sh | bash -s email=%s", email))
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("download failed: %s", string(output))
	}

	s.runAcme("--set-default-ca", "--server", "letsencrypt")
	return nil
}

// GetCertificateExpiry returns the expiry date of a certificate
func (s *CertificateService) GetCertificateExpiry(domain string) (time.Time, error) {
	certPath := filepath.Join(s.certDir, domain+".crt")
	output, err := exec.Command("openssl", "x509", "-enddate", "-noout", "-in", certPath).Output()
	if err != nil {
		return time.Time{}, err
	}

	dateStr := strings.TrimPrefix(strings.TrimSpace(string(output)), "notAfter=")
	expiry, err := time.Parse("Jan 2 15:04:05 2006 MST", dateStr)
	if err != nil {
		return time.Time{}, err
	}

	return expiry, nil
}

// RenewCertificate forces renewal of a certificate
func (s *CertificateService) RenewCertificate(domain string) error {
	output, err := s.runAcme("--renew", "-d", domain, "--ecc", "--force")
	if err != nil {
		return fmt.Errorf("续签失败: %s", output)
	}
	return nil
}

// CertificateInfo contains detailed information about a certificate
type CertificateInfo struct {
	Domain       string    `json:"domain"`
	ExpiresAt    time.Time `json:"expires_at"`
	NextRenewAt  time.Time `json:"next_renew_at"`
	CertPath     string    `json:"cert_path"`
	KeyPath      string    `json:"key_path"`
	AcmePath     string    `json:"acme_path"`
	Provider     string    `json:"provider"`
}

// GetCertificateInfo returns detailed information about a certificate
func (s *CertificateService) GetCertificateInfo(domain string) (*CertificateInfo, error) {
	info := &CertificateInfo{
		Domain:   domain,
		CertPath: filepath.Join(s.certDir, domain+".crt"),
		KeyPath:  filepath.Join(s.certDir, domain+".key"),
		AcmePath: fmt.Sprintf("/root/.acme.sh/%s_ecc", domain),
	}

	// Get expiry from certificate
	expiry, err := s.GetCertificateExpiry(domain)
	if err == nil {
		info.ExpiresAt = expiry
	}

	// Get next renewal time from acme.sh config
	confPath := fmt.Sprintf("/root/.acme.sh/%s_ecc/%s.conf", domain, domain)
	confData, err := os.ReadFile(confPath)
	if err == nil {
		lines := strings.Split(string(confData), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "Le_NextRenewTimeStr=") {
				timeStr := strings.Trim(strings.TrimPrefix(line, "Le_NextRenewTimeStr="), "'\"")
				if renewTime, err := time.Parse(time.RFC3339, timeStr); err == nil {
					info.NextRenewAt = renewTime
				}
			}
		}
	}

	return info, nil
}

// DeleteCertificate removes a certificate from the system
func (s *CertificateService) DeleteCertificate(domain string) error {
	// Remove from acme.sh
	s.runAcme("--remove", "-d", domain, "--ecc")

	// Delete certificate files
	certPath := filepath.Join(s.certDir, domain+".crt")
	keyPath := filepath.Join(s.certDir, domain+".key")
	os.Remove(certPath)
	os.Remove(keyPath)

	// Optionally remove acme.sh directory (keep for now to allow re-issue)
	// os.RemoveAll(fmt.Sprintf("/root/.acme.sh/%s_ecc", domain))

	return nil
}

