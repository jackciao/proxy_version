package services

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	packetStreamDefaultHost = "proxy.packetstream.io"
	packetStreamDefaultPort = 31112
)

// PacketStreamService 管理 PacketStream 住宅代理的配置、出站生成与连通性测试。
type PacketStreamService struct {
	db *sql.DB
}

func NewPacketStreamService(db *sql.DB) *PacketStreamService {
	return &PacketStreamService{db: db}
}

// PacketStreamConfig 表示保存在数据库中的 PacketStream 凭据。
// AuthKey 即官网 Network Access 页的 Proxy Password，原样保存——其中已由官网
// 编码了国家与会话（例如 IW3K1xcH8csPllvO_country-Turkey）。模块不再二次拼接。
type PacketStreamConfig struct {
	ID       int64  `json:"id"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	AuthKey  string `json:"auth_key"`
}

// PacketStreamStatus 返回给前端的状态（不泄露完整密钥明文）。
type PacketStreamStatus struct {
	Configured  bool   `json:"configured"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	AuthKeyMask string `json:"auth_key_mask"`
	HasAuthKey  bool   `json:"has_auth_key"`
}

// PacketStreamTestResult 连通性测试结果。
type PacketStreamTestResult struct {
	Success bool   `json:"success"`
	IP      string `json:"ip"`
	Country string `json:"country"`
	ISP     string `json:"isp"`
	Message string `json:"message"`
}

// GetConfig 读取当前配置，不存在时返回 (nil, nil)。
func (s *PacketStreamService) GetConfig() (*PacketStreamConfig, error) {
	row := s.db.QueryRow(`SELECT id, host, port, username, auth_key
		FROM packetstream_config ORDER BY id DESC LIMIT 1`)
	var cfg PacketStreamConfig
	var host, username, authKey sql.NullString
	var port sql.NullInt64
	if err := row.Scan(&cfg.ID, &host, &port, &username, &authKey); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	cfg.Host = host.String
	cfg.Port = int(port.Int64)
	cfg.Username = username.String
	cfg.AuthKey = authKey.String
	if cfg.Host == "" {
		cfg.Host = packetStreamDefaultHost
	}
	if cfg.Port == 0 {
		cfg.Port = packetStreamDefaultPort
	}
	return &cfg, nil
}

// GetStatus 返回脱敏后的状态。
func (s *PacketStreamService) GetStatus() PacketStreamStatus {
	status := PacketStreamStatus{
		Host: packetStreamDefaultHost,
		Port: packetStreamDefaultPort,
	}
	cfg, err := s.GetConfig()
	if err != nil || cfg == nil {
		return status
	}
	status.Host = cfg.Host
	status.Port = cfg.Port
	status.Username = cfg.Username
	status.HasAuthKey = cfg.AuthKey != ""
	status.AuthKeyMask = maskSecret(cfg.AuthKey)
	status.Configured = cfg.Username != "" && cfg.AuthKey != ""
	return status
}

// SaveConfig 持久化配置（单行，覆盖式）。Proxy Password 原样保存。
func (s *PacketStreamService) SaveConfig(cfg *PacketStreamConfig) error {
	if cfg == nil {
		return fmt.Errorf("配置为空")
	}
	cfg.Host = strings.TrimSpace(cfg.Host)
	cfg.Username = strings.TrimSpace(cfg.Username)
	cfg.AuthKey = strings.TrimSpace(cfg.AuthKey)
	if cfg.Host == "" {
		cfg.Host = packetStreamDefaultHost
	}
	if cfg.Port == 0 {
		cfg.Port = packetStreamDefaultPort
	}
	if cfg.Username == "" || cfg.AuthKey == "" {
		return fmt.Errorf("请填写 PacketStream 用户名与认证密钥（官网 Proxy Password）")
	}

	if _, err := s.db.Exec("DELETE FROM packetstream_config"); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO packetstream_config
		(host, port, username, auth_key, created_at, updated_at)
		VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		cfg.Host, cfg.Port, cfg.Username, cfg.AuthKey,
	)
	return err
}

// DeleteConfig 删除配置。
func (s *PacketStreamService) DeleteConfig() error {
	_, err := s.db.Exec("DELETE FROM packetstream_config")
	return err
}

// ValidateReady 校验是否已配置可用凭据。
func (s *PacketStreamService) ValidateReady() error {
	cfg, err := s.GetConfig()
	if err != nil {
		return fmt.Errorf("读取 PacketStream 配置失败: %v", err)
	}
	if cfg == nil || cfg.Username == "" || cfg.AuthKey == "" {
		return fmt.Errorf("PacketStream 尚未配置，请先在系统设置中填写代理凭据")
	}
	return nil
}

// GenerateSingBoxOutbound 生成 sing-box HTTP 代理出站。
func (s *PacketStreamService) GenerateSingBoxOutbound() (map[string]interface{}, error) {
	cfg, err := s.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("读取 PacketStream 配置失败: %v", err)
	}
	if cfg == nil || cfg.Username == "" || cfg.AuthKey == "" {
		return nil, fmt.Errorf("PacketStream 尚未配置")
	}
	return map[string]interface{}{
		"type":        "http",
		"tag":         "packetstream-out",
		"server":      cfg.Host,
		"server_port": cfg.Port,
		"username":    cfg.Username,
		"password":    cfg.AuthKey,
	}, nil
}

// TestConnection 通过代理实测出口 IP / 国家。传入的 override 用于“保存前测试”。
func (s *PacketStreamService) TestConnection(override *PacketStreamConfig) PacketStreamTestResult {
	cfg := override
	if cfg == nil {
		stored, err := s.GetConfig()
		if err != nil || stored == nil {
			return PacketStreamTestResult{Success: false, Message: "PacketStream 尚未配置"}
		}
		cfg = stored
	}
	cfg.Host = strings.TrimSpace(cfg.Host)
	cfg.Username = strings.TrimSpace(cfg.Username)
	cfg.AuthKey = strings.TrimSpace(cfg.AuthKey)
	if cfg.Host == "" {
		cfg.Host = packetStreamDefaultHost
	}
	if cfg.Port == 0 {
		cfg.Port = packetStreamDefaultPort
	}
	if cfg.Username == "" || cfg.AuthKey == "" {
		return PacketStreamTestResult{Success: false, Message: "请填写用户名与认证密钥（官网 Proxy Password）"}
	}

	proxyURL := &url.URL{
		Scheme: "http",
		User:   url.UserPassword(cfg.Username, cfg.AuthKey),
		Host:   fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyURL(proxyURL),
		DisableKeepAlives:     true,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
	}
	client := &http.Client{Transport: transport, Timeout: 25 * time.Second}

	// 优先 ip-api（含国家/ISP），失败再退回 ipify（仅 IP）
	if res, ok := packetStreamProbeIPAPI(client); ok {
		res.Success = true
		res.Message = "连接成功"
		return res
	}
	if ip, ok := packetStreamProbeIPify(client); ok {
		return PacketStreamTestResult{Success: true, IP: ip, Message: "连接成功（未能识别地区）"}
	}
	return PacketStreamTestResult{
		Success: false,
		Message: "无法通过 PacketStream 代理建立连接，请检查用户名、认证密钥或账户余额",
	}
}

func packetStreamProbeIPAPI(client *http.Client) (PacketStreamTestResult, bool) {
	resp, err := client.Get("http://ip-api.com/json/?fields=status,country,countryCode,query,isp")
	if err != nil {
		return PacketStreamTestResult{}, false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return PacketStreamTestResult{}, false
	}
	var data struct {
		Status  string `json:"status"`
		Country string `json:"country"`
		Query   string `json:"query"`
		ISP     string `json:"isp"`
	}
	if err := json.Unmarshal(body, &data); err != nil || data.Status != "success" || data.Query == "" {
		return PacketStreamTestResult{}, false
	}
	return PacketStreamTestResult{IP: data.Query, Country: data.Country, ISP: data.ISP}, true
}

func packetStreamProbeIPify(client *http.Client) (string, bool) {
	resp, err := client.Get("https://api.ipify.org")
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 128))
	if err != nil {
		return "", false
	}
	ip := strings.TrimSpace(string(body))
	if ip == "" {
		return "", false
	}
	return ip, true
}

// ParsePacketStreamProxyString 解析用户从官网复制的完整代理串/CURL，提取各字段。
// Proxy Password（含国家/会话）原样保留。支持：
//   - http(s)/socks5(h)://user:pass@host:port
//   - host:port:user:pass
//   - user:pass@host:port
//   - 含 -x / --proxy 的 curl 命令
func ParsePacketStreamProxyString(raw string) (*PacketStreamConfig, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return nil, fmt.Errorf("代理串为空")
	}

	// 从 curl 命令中抽取 -x/--proxy 后的值
	if strings.Contains(text, "curl") || strings.Contains(text, "-x") || strings.Contains(text, "--proxy") {
		if extracted := extractProxyFromCurl(text); extracted != "" {
			text = extracted
		}
	}
	text = strings.TrimSpace(strings.Trim(text, "'\""))

	var host, portStr, user, pass string

	if strings.Contains(text, "://") {
		u, err := url.Parse(text)
		if err != nil {
			return nil, fmt.Errorf("无法解析代理地址: %v", err)
		}
		host = u.Hostname()
		portStr = u.Port()
		if u.User != nil {
			user = u.User.Username()
			pass, _ = u.User.Password()
		}
	} else if strings.Contains(text, "@") {
		// user:pass@host:port
		atParts := strings.SplitN(text, "@", 2)
		cred := atParts[0]
		hostPart := atParts[1]
		if cp := strings.SplitN(cred, ":", 2); len(cp) == 2 {
			user, pass = cp[0], cp[1]
		}
		if hp := strings.SplitN(hostPart, ":", 2); len(hp) == 2 {
			host, portStr = hp[0], hp[1]
		} else {
			host = hostPart
		}
	} else {
		// host:port:user:pass
		parts := strings.Split(text, ":")
		if len(parts) >= 4 {
			host = parts[0]
			portStr = parts[1]
			user = parts[2]
			pass = strings.Join(parts[3:], ":")
		} else if len(parts) == 2 {
			host = parts[0]
			portStr = parts[1]
		} else {
			return nil, fmt.Errorf("无法识别的代理串格式，请粘贴形如 host:port:user:pass 或 http://user:pass@host:port")
		}
	}

	host = strings.TrimSpace(host)
	user = strings.TrimSpace(user)
	pass = strings.TrimSpace(pass)
	if host == "" {
		host = packetStreamDefaultHost
	}
	port := packetStreamDefaultPort
	if portStr != "" {
		if p, err := strconv.Atoi(strings.TrimSpace(portStr)); err == nil && p > 0 {
			port = p
		}
	}
	if pass == "" {
		return nil, fmt.Errorf("代理串中缺少认证密钥（Proxy Password）")
	}

	return &PacketStreamConfig{
		Host:     host,
		Port:     port,
		Username: user,
		AuthKey:  pass,
	}, nil
}

func extractProxyFromCurl(text string) string {
	fields := strings.Fields(text)
	for i, f := range fields {
		if (f == "-x" || f == "--proxy") && i+1 < len(fields) {
			return strings.Trim(fields[i+1], "'\"")
		}
		if strings.HasPrefix(f, "--proxy=") {
			return strings.Trim(strings.TrimPrefix(f, "--proxy="), "'\"")
		}
	}
	return ""
}

func maskSecret(secret string) string {
	if secret == "" {
		return ""
	}
	runes := []rune(secret)
	if len(runes) <= 4 {
		return strings.Repeat("*", len(runes))
	}
	return strings.Repeat("*", len(runes)-4) + string(runes[len(runes)-4:])
}
