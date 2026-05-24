package services

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CertProgress 证书申请进度
type CertProgress struct {
	Domain    string `json:"domain"`
	Status    string `json:"status"`     // pending, running, success, failed
	Step      int    `json:"step"`       // 当前步骤 1-6
	TotalStep int    `json:"total_step"` // 总步骤数 6
	StepName  string `json:"step_name"`  // 当前步骤描述
	Error     string `json:"error,omitempty"`
	UpdatedAt int64  `json:"updated_at"` // Unix timestamp
}

// 全局进度存储
var (
	certProgressMap   = make(map[string]*CertProgress)
	certProgressMutex sync.RWMutex
)

// GetCertProgress 获取证书申请进度
func GetCertProgress(domain string) *CertProgress {
	certProgressMutex.RLock()
	defer certProgressMutex.RUnlock()
	if p, ok := certProgressMap[domain]; ok {
		return p
	}
	return nil
}

// updateProgress 更新证书申请进度
func updateProgress(domain string, step int, stepName, status, errMsg string) {
	certProgressMutex.Lock()
	defer certProgressMutex.Unlock()
	certProgressMap[domain] = &CertProgress{
		Domain:    domain,
		Status:    status,
		Step:      step,
		TotalStep: 6,
		StepName:  stepName,
		Error:     errMsg,
		UpdatedAt: time.Now().Unix(),
	}
}

// clearProgress 清除进度记录
func clearProgress(domain string) {
	certProgressMutex.Lock()
	defer certProgressMutex.Unlock()
	delete(certProgressMap, domain)
}

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
	// 初始化进度
	updateProgress(domain, 1, "正在检查环境...", "running", "")

	// 步骤1: 确保 acme.sh 已安装
	updateProgress(domain, 1, "检查/安装 acme.sh", "running", "")
	if _, err := os.Stat(s.acmePath); os.IsNotExist(err) {
		updateProgress(domain, 1, "正在安装 acme.sh...", "running", "")
		if err := s.installAcme(email); err != nil {
			updateProgress(domain, 1, "安装 acme.sh 失败", "failed", err.Error())
			return "", "", fmt.Errorf("安装 acme.sh 失败: %v", err)
		}
	}

	// 验证邮箱
	if email == "" {
		updateProgress(domain, 1, "邮箱验证失败", "failed", "请提供有效的邮箱地址")
		return "", "", fmt.Errorf("请提供有效的邮箱地址用于证书申请")
	}
	if !strings.Contains(email, "@") || strings.Contains(email, "example.com") {
		updateProgress(domain, 1, "邮箱验证失败", "failed", "请提供有效的邮箱地址")
		return "", "", fmt.Errorf("请提供有效的邮箱地址，不能使用示例邮箱")
	}

	// 步骤2: 注册账号
	updateProgress(domain, 2, "正在注册 ACME 账号...", "running", "")
	s.cleanOldAccount()
	registerOutput, _ := s.runAcme("--register-account", "-m", email, "--force")
	if strings.Contains(registerOutput, "error") && !strings.Contains(registerOutput, "already") {
		s.runAcme("--update-account", "--email", email)
	}

	// 设置 CA
	switch provider {
	case "letsencrypt", "":
		s.runAcme("--set-default-ca", "--server", "letsencrypt")
	case "zerossl":
		s.runAcme("--set-default-ca", "--server", "zerossl")
	case "buypass":
		s.runAcme("--set-default-ca", "--server", "buypass")
	}

	// 确保证书目录存在
	if err := os.MkdirAll(s.certDir, 0755); err != nil {
		updateProgress(domain, 2, "创建目录失败", "failed", err.Error())
		return "", "", fmt.Errorf("创建证书目录失败: %v", err)
	}

	certPath := filepath.Join(s.certDir, domain+".crt")
	keyPath := filepath.Join(s.certDir, domain+".key")

	// 步骤3: 验证域名并签发证书
	var output string
	var err error
	skipIssue := false
	skipInstall := false

	// Reuse logic: if a still-valid certificate already exists locally we skip
	// the ACME call entirely so repeated camouflage redeploys do not trip
	// Let's Encrypt's "duplicate certificate" rate limit (5 per domain / week).
	// We require that the cached private key actually matches the certificate
	// - acme.sh may have rotated the key on a failed previous renewal which
	// leaves a stale fullchain.cer that nginx will refuse to load.
	acmeFullchain := fmt.Sprintf("/root/.acme.sh/%s_ecc/fullchain.cer", domain)
	acmeKey := fmt.Sprintf("/root/.acme.sh/%s_ecc/%s.key", domain, domain)
	if expiry, ok := usableCachedCert(certPath, keyPath); ok {
		skipIssue = true
		skipInstall = true
		updateProgress(domain, 3, fmt.Sprintf("已部署证书仍有效（%s 到期），直接复用并重新部署伪装站", expiry.Format("2006-01-02")), "running", "")
	} else if expiry, ok := usableCachedCert(acmeFullchain, acmeKey); ok {
		skipIssue = true
		updateProgress(domain, 3, fmt.Sprintf("检测到 acme.sh 已有有效证书（%s 到期），跳过签发直接复用", expiry.Format("2006-01-02")), "running", "")
	}

	if !skipIssue {
		if method == "dns" {
			updateProgress(domain, 3, "正在进行 DNS 验证...", "running", "")
			output, err = s.issueViaDNS(domain, dnsProvider, apiToken, cfEmail)
		} else {
			updateProgress(domain, 3, "正在通过 OpenResty 进行 HTTP 验证...", "running", "")
			output, err = s.issueViaOpenRestyWebroot(domain)
			if err != nil {
				// Fallback: try legacy webroot paths
				updateProgress(domain, 3, "尝试其他 Webroot 路径...", "running", "")
				output, err = s.issueViaWebroot(domain)
			}
			if err != nil {
				// Fallback: try standalone via host network namespace
				updateProgress(domain, 3, "尝试 Standalone 模式...", "running", "")
				output, err = s.issueViaStandaloneHost(domain)
			}
		}

		if err != nil {
			if strings.Contains(output, "Cert success") || strings.Contains(output, "Cert already exists") {
				// Certificate was actually issued successfully
			} else {
				var errMsg string
				switch {
				case strings.Contains(output, "rateLimited") || strings.Contains(output, "too many certificates") || strings.Contains(output, "exceeded"):
					errMsg = "Let's Encrypt 已限速：同一域名 7 天内最多 5 张证书。请等待几天后再试，或在“证书机构”里换成 ZeroSSL / Buypass。本系统已自动跳过有效证书的重复申请，但当前域名近期申请过多。"
				case strings.Contains(output, "port 80 is already used"):
					errMsg = "80端口被占用，请使用 DNS 验证方式"
				case strings.Contains(output, "invalid domain") || strings.Contains(output, "Error add TXT"):
					errMsg = fmt.Sprintf("DNS 验证失败: %s", s.extractError(output))
				default:
					errMsg = fmt.Sprintf("证书申请失败: %s", s.extractError(output))
				}
				updateProgress(domain, 3, "验证失败", "failed", errMsg)
				return "", "", fmt.Errorf(errMsg)
			}
		}
	}

	// 步骤4: 安装证书
	if !skipInstall {
		updateProgress(domain, 4, "正在安装证书...", "running", "")
		installArgs := []string{
			"--install-cert", "-d", domain,
			"--ecc",
			"--fullchain-file", certPath,
			"--key-file", keyPath,
		}

		installOutput, ierr := s.runAcme(installArgs...)
		installOK := ierr == nil || strings.Contains(installOutput, "Installing") || strings.Contains(installOutput, "installed")

		// Fallback: acme.sh may have removed the domain's .conf file in a previous
		// "delete" round but the .cer / .key files are still on disk. In that
		// case --install-cert reports "Domain not found" yet we can simply copy
		// the cached files into the target paths so the camouflage redeploy can
		// continue without a fresh ACME order.
		if !installOK {
			cachedCert := fmt.Sprintf("/root/.acme.sh/%s_ecc/fullchain.cer", domain)
			cachedKey := fmt.Sprintf("/root/.acme.sh/%s_ecc/%s.key", domain, domain)
			if data, cerr := os.ReadFile(cachedCert); cerr == nil {
				if key, kerr := os.ReadFile(cachedKey); kerr == nil {
					if werr := os.WriteFile(certPath, data, 0644); werr == nil {
						if werr := os.WriteFile(keyPath, key, 0600); werr == nil {
							installOK = true
							updateProgress(domain, 4, "通过 acme.sh 缓存复用证书完成", "running", "")
						}
					}
				}
			}
		}

		if !installOK {
			updateProgress(domain, 4, "安装证书失败", "failed", installOutput)
			return "", "", fmt.Errorf("安装证书失败: %s", installOutput)
		}
	} else {
		updateProgress(domain, 4, "证书已存在，跳过安装", "running", "")
	}

	// 步骤5: 配置自动续签
	updateProgress(domain, 5, "配置自动续签...", "running", "")
	go func() {
		time.Sleep(time.Second * 2)
		s.runAcme("--cron", "--home", "/root/.acme.sh")
	}()

	// 步骤6: 部署伪装站
	updateProgress(domain, 6, "正在部署伪装站...", "running", "")
	camoService := NewCamouflageService()
	if camoService.IsAvailable() {
		if camoErr := camoService.DeployCamouflage(domain, certPath, keyPath); camoErr != nil {
			// Camouflage deployment failure is non-fatal, but keep a server-side trace for diagnosis.
			fmt.Printf("[certificate] camouflage deploy failed for %s: %v\n", domain, camoErr)
			updateProgress(domain, 6, "证书申请成功（伪装站部署失败: "+camoErr.Error()+"）", "success", "")
		} else {
			updateProgress(domain, 6, "证书申请成功，伪装站已部署！", "success", "")
		}
	} else {
		updateProgress(domain, 6, "证书申请成功！（未检测到 OpenResty，跳过伪装站）", "success", "")
	}

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
			var newEnv []string
			for _, e := range env {
				if !strings.HasPrefix(e, "CF_Key=") && !strings.HasPrefix(e, "CF_Email=") && !strings.HasPrefix(e, "CF_Token=") {
					newEnv = append(newEnv, e)
				}
			}
			env = append(newEnv, "CF_Token="+apiToken)
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

// cleanOldAccount removes old ACME account with invalid email to allow fresh registration
func (s *CertificateService) cleanOldAccount() {
	// Remove ca directory which contains account info with invalid email
	caDir := "/root/.acme.sh/ca"
	os.RemoveAll(caDir)

	// Also clean the account email from account.conf
	confPath := "/root/.acme.sh/account.conf"
	data, err := os.ReadFile(confPath)
	if err != nil {
		return
	}

	lines := strings.Split(string(data), "\n")
	var newLines []string
	for _, line := range lines {
		// Skip account email/key entries that may have invalid email
		if strings.HasPrefix(line, "ACCOUNT_EMAIL") ||
			strings.HasPrefix(line, "ACCOUNT_THUMBPRINT") ||
			strings.Contains(line, "example.com") {
			continue
		}
		newLines = append(newLines, line)
	}
	os.WriteFile(confPath, []byte(strings.Join(newLines, "\n")), 0644)
}

// issueViaOpenRestyWebroot uses 1Panel OpenResty's webroot for HTTP-01 validation
func (s *CertificateService) issueViaOpenRestyWebroot(domain string) (string, error) {
	camoService := NewCamouflageService()
	if !camoService.IsAvailable() {
		return "", fmt.Errorf("1Panel OpenResty 未检测到")
	}

	// Create webroot directory in OpenResty's site path
	webrootDir, err := camoService.CreateWebrootDir(domain)
	if err != nil {
		return "", fmt.Errorf("创建 webroot 目录失败: %v", err)
	}

	// Ensure OpenResty has a server block to handle this domain's ACME challenge
	if err := camoService.EnsureHTTPServerBlock(domain); err != nil {
		return "", fmt.Errorf("配置 OpenResty 临时站点失败: %v", err)
	}

	// Use webroot mode with the OpenResty site directory
	args := []string{
		"--issue", "-d", domain,
		"--webroot", webrootDir,
		"--keylength", "ec-256",
		"--force",
	}
	output, err := s.runAcme(args...)

	// Clean up temporary config (will be replaced by full camouflage config later)
	camoService.RemoveTempHTTPConfig(domain)

	if err == nil || strings.Contains(output, "Cert success") {
		return output, nil
	}
	return output, err
}

// issueViaWebroot uses existing web server (legacy paths fallback)
func (s *CertificateService) issueViaWebroot(domain string) (string, error) {
	webrootPaths := []string{
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

// issueViaStandaloneHost uses standalone mode via host network namespace
func (s *CertificateService) issueViaStandaloneHost(domain string) (string, error) {
	// First try standalone in container (works if port 80 is free)
	args := []string{
		"--issue", "-d", domain,
		"--standalone",
		"--httpport", "80",
		"--keylength", "ec-256",
		"--force",
	}
	output, err := s.runAcme(args...)
	if err == nil || strings.Contains(output, "Cert success") {
		return output, nil
	}

	// Fallback: use socat to forward port 80 from host to a temp port in container
	// This is needed when 80 is occupied on host but we still need standalone mode
	return output, fmt.Errorf("Standalone 模式失败 (80端口可能被占用): %s", s.extractError(output))
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

// parseCertExpiry returns the not-after time of a PEM certificate at the
// given path. Returns an error if the file does not exist or cannot be parsed.
func parseCertExpiry(certPath string) (time.Time, error) {
	if _, err := os.Stat(certPath); err != nil {
		return time.Time{}, err
	}
	output, err := exec.Command("openssl", "x509", "-enddate", "-noout", "-in", certPath).Output()
	if err != nil {
		return time.Time{}, err
	}
	dateStr := strings.TrimPrefix(strings.TrimSpace(string(output)), "notAfter=")
	return time.Parse("Jan 2 15:04:05 2006 MST", dateStr)
}

// certKeyMatch returns true when the certificate and private key files share
// the same public key. acme.sh occasionally rotates the private key on a
// failed renewal (e.g. when Let's Encrypt rate-limits the new order) which
// leaves a stale fullchain.cer on disk that cannot be served because its
// public key no longer matches the local key. We must catch that mismatch
// before declaring the cached certificate "valid for reuse", otherwise
// nginx fails to load with key values mismatch.
func certKeyMatch(certPath, keyPath string) bool {
	certPub, err := exec.Command("sh", "-c",
		fmt.Sprintf("openssl x509 -in %q -pubkey -noout | openssl pkey -pubin -outform DER", certPath)).Output()
	if err != nil || len(certPub) == 0 {
		return false
	}
	keyPub, err := exec.Command("sh", "-c",
		fmt.Sprintf("openssl pkey -in %q -pubout -outform DER", keyPath)).Output()
	if err != nil || len(keyPub) == 0 {
		return false
	}
	return bytes.Equal(certPub, keyPub)
}

// usableCachedCert checks whether the given (cert, key) pair is still valid
// for reuse: file exists, expiry > 30 days, public key matches private key.
// On success returns the parsed expiry time.
func usableCachedCert(certPath, keyPath string) (time.Time, bool) {
	expiry, err := parseCertExpiry(certPath)
	if err != nil || time.Until(expiry) <= 30*24*time.Hour {
		return time.Time{}, false
	}
	if _, err := os.Stat(keyPath); err != nil {
		return time.Time{}, false
	}
	if !certKeyMatch(certPath, keyPath) {
		return time.Time{}, false
	}
	return expiry, true
}

// GetCertificateExpiry returns the expiry date of a certificate
func (s *CertificateService) GetCertificateExpiry(domain string) (time.Time, error) {
	return parseCertExpiry(filepath.Join(s.certDir, domain+".crt"))
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
	Domain      string    `json:"domain"`
	ExpiresAt   time.Time `json:"expires_at"`
	NextRenewAt time.Time `json:"next_renew_at"`
	CertPath    string    `json:"cert_path"`
	KeyPath     string    `json:"key_path"`
	AcmePath    string    `json:"acme_path"`
	Provider    string    `json:"provider"`
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
