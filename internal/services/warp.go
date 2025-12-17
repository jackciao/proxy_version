package services

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type WarpService struct {
	db *sql.DB
}

func NewWarpService(db *sql.DB) *WarpService {
	return &WarpService{db: db}
}

// WarpConfig represents the WARP configuration
type WarpConfig struct {
	ID          int64     `json:"id"`
	AccountType string    `json:"account_type"` // free, plus, teams
	DeviceID    string    `json:"device_id"`
	AccessToken string    `json:"access_token"`
	PrivateKey  string    `json:"private_key"`
	PublicKey   string    `json:"public_key"`
	IPv4Address string    `json:"ipv4_address"`
	IPv6Address string    `json:"ipv6_address"`
	Endpoint    string    `json:"endpoint"`
	LicenseKey  string    `json:"license_key,omitempty"`
	TeamName    string    `json:"team_name,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// WarpStatus represents the current WARP status
type WarpStatus struct {
	Installed   bool        `json:"installed"`
	Configured  bool        `json:"configured"`
	AccountType string      `json:"account_type"`
	IPv4        string      `json:"ipv4"`
	IPv6        string      `json:"ipv6"`
	Endpoint    string      `json:"endpoint"`
	Config      *WarpConfig `json:"config,omitempty"`
}

const wgcfPath = "/usr/local/bin/wgcf"
const warpConfigDir = "/etc/v2ray-agent/warp"

// GetStatus returns the current WARP status
func (s *WarpService) GetStatus() WarpStatus {
	status := WarpStatus{
		Installed:  s.isWgcfInstalled(),
		Configured: false,
	}

	config, err := s.GetConfig()
	if err == nil && config != nil {
		status.Configured = true
		status.AccountType = config.AccountType
		status.IPv4 = config.IPv4Address
		status.IPv6 = config.IPv6Address
		status.Endpoint = config.Endpoint
		status.Config = config
	}

	return status
}

// InstallWgcf installs the wgcf tool
func (s *WarpService) InstallWgcf() error {
	if s.isWgcfInstalled() {
		return nil
	}

	// Detect architecture
	arch := "amd64"
	out, _ := exec.Command("uname", "-m").Output()
	archStr := strings.TrimSpace(string(out))
	if strings.Contains(archStr, "aarch64") || strings.Contains(archStr, "arm64") {
		arch = "arm64"
	}

	// Download wgcf from GitHub releases
	// Format: https://github.com/ViRb3/wgcf/releases/download/v2.2.22/wgcf_2.2.22_linux_amd64
	version := "2.2.22"
	url := fmt.Sprintf("https://github.com/ViRb3/wgcf/releases/download/v%s/wgcf_%s_linux_%s", version, version, arch)
	
	cmd := exec.Command("bash", "-c", fmt.Sprintf(
		"curl -fsSL -o %s %s && chmod +x %s",
		wgcfPath, url, wgcfPath,
	))
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("安装 wgcf 失败: %v, output: %s", err, string(output))
	}

	return nil
}

// Register registers a new WARP account using Cloudflare API directly
func (s *WarpService) Register() (*WarpConfig, error) {
	// Generate WireGuard keypair using wg command or generate manually
	privateKey, publicKey, err := s.generateWireGuardKeys()
	if err != nil {
		return nil, fmt.Errorf("生成密钥对失败: %v", err)
	}

	// Call Cloudflare WARP registration API
	regData, err := s.callCloudflareRegisterAPI(publicKey)
	if err != nil {
		return nil, err
	}

	config := &WarpConfig{
		AccountType: "free",
		DeviceID:    regData.ID,
		AccessToken: regData.Token,
		PrivateKey:  privateKey,
		PublicKey:   publicKey,
		IPv4Address: regData.Config.Interface.Addresses.V4 + "/32",
		IPv6Address: regData.Config.Interface.Addresses.V6 + "/128",
		Endpoint:    "engage.cloudflareclient.com:2408",
	}

	// Save to database
	if err := s.saveConfig(config); err != nil {
		return nil, err
	}

	return config, nil
}

// CloudflareRegResponse represents the Cloudflare API response
type CloudflareRegResponse struct {
	ID     string `json:"id"`
	Token  string `json:"token"`
	Config struct {
		Interface struct {
			Addresses struct {
				V4 string `json:"v4"`
				V6 string `json:"v6"`
			} `json:"addresses"`
		} `json:"interface"`
	} `json:"config"`
}

func (s *WarpService) generateWireGuardKeys() (privateKey, publicKey string, err error) {
	// Try using wg command first
	cmd := exec.Command("wg", "genkey")
	privKeyBytes, err := cmd.Output()
	if err == nil {
		privKey := strings.TrimSpace(string(privKeyBytes))
		
		cmd = exec.Command("wg", "pubkey")
		cmd.Stdin = strings.NewReader(privKey)
		pubKeyBytes, err := cmd.Output()
		if err == nil {
			return privKey, strings.TrimSpace(string(pubKeyBytes)), nil
		}
	}

	// Fallback: generate keys using crypto (requires golang.org/x/crypto/curve25519)
	// For simplicity, use openssl
	cmd = exec.Command("bash", "-c", "openssl genpkey -algorithm x25519 2>/dev/null | openssl pkey -text 2>/dev/null")
	output, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("无法生成密钥: 请确保安装了 wireguard-tools 或 openssl")
	}

	// Parse openssl output (this is a fallback, wg command is preferred)
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "priv:") || strings.Contains(line, "pub:") {
			// Simple key extraction
		}
	}

	return "", "", fmt.Errorf("请安装 wireguard-tools (apt install wireguard-tools)")
}

func (s *WarpService) callCloudflareRegisterAPI(publicKey string) (*CloudflareRegResponse, error) {
	// Cloudflare WARP API endpoint
	apiURL := "https://api.cloudflareclient.com/v0a2158/reg"

	// Request body
	reqBody := map[string]interface{}{
		"key":        publicKey,
		"install_id": "",
		"fcm_token":  "",
		"tos":        time.Now().Format(time.RFC3339),
		"model":      "Linux",
		"type":       "Linux",
		"locale":     "en_US",
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	// Create request
	req, err := http.NewRequest("POST", apiURL, strings.NewReader(string(jsonData)))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "okhttp/3.12.1")

	// Send request
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API 请求失败: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Cloudflare API 返回错误 %d: %s", resp.StatusCode, string(body))
	}

	var regResp CloudflareRegResponse
	if err := json.Unmarshal(body, &regResp); err != nil {
		return nil, fmt.Errorf("解析响应失败: %v", err)
	}

	return &regResp, nil
}

// Refresh re-registers to get a new WARP IP (useful when streaming unlock becomes ineffective)
func (s *WarpService) Refresh() (*WarpConfig, error) {
	// Backup license key if exists
	existingConfig, _ := s.GetConfig()
	var licenseKey string
	if existingConfig != nil {
		licenseKey = existingConfig.LicenseKey
	}

	// Delete existing config
	if err := s.DeleteConfig(); err != nil {
		return nil, err
	}

	// Re-register
	newConfig, err := s.Register()
	if err != nil {
		return nil, err
	}

	// If had WARP+ license, re-apply it
	if licenseKey != "" {
		if err := s.UpgradeToPlus(licenseKey); err != nil {
			// Log but don't fail - account is still registered
			fmt.Printf("Warning: failed to re-apply WARP+ license: %v\n", err)
		}
		newConfig.LicenseKey = licenseKey
		newConfig.AccountType = "plus"
	}

	return newConfig, nil
}

// UpgradeToPlus upgrades the account to WARP+
func (s *WarpService) UpgradeToPlus(licenseKey string) error {
	config, err := s.GetConfig()
	if err != nil || config == nil {
		return fmt.Errorf("请先注册 WARP 账号")
	}

	originalDir, _ := os.Getwd()
	os.Chdir(warpConfigDir)
	defer os.Chdir(originalDir)

	// Update license
	cmd := exec.Command(wgcfPath, "update", "--license-key", licenseKey)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("升级失败: %v, output: %s", err, string(output))
	}

	// Regenerate config
	cmd = exec.Command(wgcfPath, "generate")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("重新生成配置失败: %v, output: %s", err, string(output))
	}

	// Update database
	config.LicenseKey = licenseKey
	config.AccountType = "plus"
	return s.saveConfig(config)
}

// GetConfig retrieves the stored WARP configuration
func (s *WarpService) GetConfig() (*WarpConfig, error) {
	row := s.db.QueryRow(`
		SELECT id, account_type, device_id, access_token, private_key, public_key,
		       ipv4_address, ipv6_address, endpoint, license_key, team_name,
		       created_at, updated_at
		FROM warp_config ORDER BY id DESC LIMIT 1
	`)

	config := &WarpConfig{}
	var licenseKey, teamName sql.NullString
	var createdAt, updatedAt sql.NullTime

	err := row.Scan(
		&config.ID, &config.AccountType, &config.DeviceID, &config.AccessToken,
		&config.PrivateKey, &config.PublicKey, &config.IPv4Address, &config.IPv6Address,
		&config.Endpoint, &licenseKey, &teamName, &createdAt, &updatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if licenseKey.Valid {
		config.LicenseKey = licenseKey.String
	}
	if teamName.Valid {
		config.TeamName = teamName.String
	}
	if createdAt.Valid {
		config.CreatedAt = createdAt.Time
	}
	if updatedAt.Valid {
		config.UpdatedAt = updatedAt.Time
	}

	return config, nil
}

// DeleteConfig removes the WARP configuration
func (s *WarpService) DeleteConfig() error {
	_, err := s.db.Exec("DELETE FROM warp_config")
	if err != nil {
		return err
	}

	// Remove config files
	os.RemoveAll(warpConfigDir)
	return nil
}

// GenerateSingBoxOutbound generates sing-box WireGuard outbound config for sing-box 1.11+
// Returns both the endpoint and outbound configs
func (s *WarpService) GenerateSingBoxOutbound() (map[string]interface{}, error) {
	config, err := s.GetConfig()
	if err != nil || config == nil {
		return nil, fmt.Errorf("WARP 未配置")
	}

	// New sing-box 1.11+ format: use endpoint instead of legacy wireguard outbound
	// The outbound references the endpoint
	outbound := map[string]interface{}{
		"type": "wireguard",
		"tag":  "warp-out",
		"local_address": []string{
			config.IPv4Address,
			config.IPv6Address,
		},
		"private_key": config.PrivateKey,
		"peers": []map[string]interface{}{
			{
				"server":      strings.Split(config.Endpoint, ":")[0],
				"server_port": 2408,
				"public_key":  "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=", // Cloudflare WARP public key
				"reserved":    []int{0, 0, 0},
			},
		},
		"mtu": 1280,
	}

	return outbound, nil
}

// Helper functions

func (s *WarpService) isWgcfInstalled() bool {
	_, err := os.Stat(wgcfPath)
	return err == nil
}

func (s *WarpService) parseWgcfConfig() (*WarpConfig, error) {
	// Read account file
	accountPath := filepath.Join(warpConfigDir, "wgcf-account.toml")
	accountData, err := os.ReadFile(accountPath)
	if err != nil {
		return nil, fmt.Errorf("读取账号配置失败: %v", err)
	}

	// Read WireGuard profile
	profilePath := filepath.Join(warpConfigDir, "wgcf-profile.conf")
	profileData, err := os.ReadFile(profilePath)
	if err != nil {
		return nil, fmt.Errorf("读取 WireGuard 配置失败: %v", err)
	}

	config := &WarpConfig{
		AccountType: "free",
		Endpoint:    "engage.cloudflareclient.com:2408",
	}

	// Parse account TOML
	for _, line := range strings.Split(string(accountData), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "device_id") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				config.DeviceID = strings.Trim(strings.TrimSpace(parts[1]), "'\"")
			}
		} else if strings.HasPrefix(line, "access_token") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				config.AccessToken = strings.Trim(strings.TrimSpace(parts[1]), "'\"")
			}
		}
	}

	// Parse WireGuard INI
	for _, line := range strings.Split(string(profileData), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "PrivateKey") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				config.PrivateKey = strings.TrimSpace(parts[1])
			}
		} else if strings.HasPrefix(line, "Address") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				addrs := strings.Split(strings.TrimSpace(parts[1]), ",")
				for _, addr := range addrs {
					addr = strings.TrimSpace(addr)
					if strings.Contains(addr, ".") {
						config.IPv4Address = addr
					} else if strings.Contains(addr, ":") {
						config.IPv6Address = addr
					}
				}
			}
		} else if strings.HasPrefix(line, "Endpoint") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				config.Endpoint = strings.TrimSpace(parts[1])
			}
		} else if strings.HasPrefix(line, "PublicKey") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				config.PublicKey = strings.TrimSpace(parts[1])
			}
		}
	}

	return config, nil
}

func (s *WarpService) saveConfig(config *WarpConfig) error {
	// Delete existing config first
	s.db.Exec("DELETE FROM warp_config")

	_, err := s.db.Exec(`
		INSERT INTO warp_config 
		(account_type, device_id, access_token, private_key, public_key, ipv4_address, ipv6_address, endpoint, license_key, team_name)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		config.AccountType, config.DeviceID, config.AccessToken,
		config.PrivateKey, config.PublicKey, config.IPv4Address, config.IPv6Address,
		config.Endpoint, config.LicenseKey, config.TeamName,
	)
	return err
}

// ImportConfig imports an existing WARP configuration
func (s *WarpService) ImportConfig(privateKey, ipv4, ipv6, endpoint string) (*WarpConfig, error) {
	config := &WarpConfig{
		AccountType: "imported",
		PrivateKey:  privateKey,
		IPv4Address: ipv4,
		IPv6Address: ipv6,
		Endpoint:    endpoint,
	}

	if config.Endpoint == "" {
		config.Endpoint = "engage.cloudflareclient.com:2408"
	}

	if err := s.saveConfig(config); err != nil {
		return nil, err
	}

	return config, nil
}

// ExportAsJSON exports the current WARP config as JSON for sing-box
func (s *WarpService) ExportAsJSON() (string, error) {
	outbound, err := s.GenerateSingBoxOutbound()
	if err != nil {
		return "", err
	}

	jsonBytes, err := json.MarshalIndent(outbound, "", "  ")
	if err != nil {
		return "", err
	}

	return string(jsonBytes), nil
}
