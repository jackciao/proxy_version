package services

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
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

// PacketStreamConfig 表示保存在数据库中的 PacketStream 凭据与节点选择。
// AuthKey 仅保存“基础认证密钥”（不含 _country/_session 后缀），
// 国家与会话在生成出站时动态拼接，便于在模块内随时切换地区。
type PacketStreamConfig struct {
	ID          int64  `json:"id"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	AuthKey     string `json:"auth_key"`
	Country     string `json:"country"`      // PacketStream 国家英文名（无空格），空表示全球随机
	SessionMode string `json:"session_mode"` // rotating | sticky
	SessionID   string `json:"session_id"`   // sticky 模式下的会话 ID
}

// PacketStreamStatus 返回给前端的状态（不泄露完整密钥明文）。
type PacketStreamStatus struct {
	Configured  bool   `json:"configured"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	Country     string `json:"country"`
	SessionMode string `json:"session_mode"`
	SessionID   string `json:"session_id"`
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
	row := s.db.QueryRow(`SELECT id, host, port, username, auth_key, country, session_mode, session_id
		FROM packetstream_config ORDER BY id DESC LIMIT 1`)
	var cfg PacketStreamConfig
	var host, username, authKey, country, sessionMode, sessionID sql.NullString
	var port sql.NullInt64
	if err := row.Scan(&cfg.ID, &host, &port, &username, &authKey, &country, &sessionMode, &sessionID); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	cfg.Host = host.String
	cfg.Port = int(port.Int64)
	cfg.Username = username.String
	cfg.AuthKey = authKey.String
	cfg.Country = country.String
	cfg.SessionMode = sessionMode.String
	cfg.SessionID = sessionID.String
	if cfg.Host == "" {
		cfg.Host = packetStreamDefaultHost
	}
	if cfg.Port == 0 {
		cfg.Port = packetStreamDefaultPort
	}
	if cfg.SessionMode == "" {
		cfg.SessionMode = "rotating"
	}
	return &cfg, nil
}

// GetStatus 返回脱敏后的状态。
func (s *PacketStreamService) GetStatus() PacketStreamStatus {
	status := PacketStreamStatus{
		Host:        packetStreamDefaultHost,
		Port:        packetStreamDefaultPort,
		SessionMode: "rotating",
	}
	cfg, err := s.GetConfig()
	if err != nil || cfg == nil {
		return status
	}
	status.Host = cfg.Host
	status.Port = cfg.Port
	status.Username = cfg.Username
	status.Country = cfg.Country
	status.SessionMode = cfg.SessionMode
	status.SessionID = cfg.SessionID
	status.HasAuthKey = cfg.AuthKey != ""
	status.AuthKeyMask = maskSecret(cfg.AuthKey)
	status.Configured = cfg.Username != "" && cfg.AuthKey != ""
	return status
}

// SaveConfig 持久化配置（单行，覆盖式）。
func (s *PacketStreamService) SaveConfig(cfg *PacketStreamConfig) error {
	if cfg == nil {
		return fmt.Errorf("配置为空")
	}
	cfg.Host = strings.TrimSpace(cfg.Host)
	cfg.Username = strings.TrimSpace(cfg.Username)
	cfg.AuthKey = normalizeAuthKey(cfg.AuthKey)
	cfg.Country = normalizeCountry(cfg.Country)
	if cfg.Host == "" {
		cfg.Host = packetStreamDefaultHost
	}
	if cfg.Port == 0 {
		cfg.Port = packetStreamDefaultPort
	}
	if cfg.SessionMode != "sticky" {
		cfg.SessionMode = "rotating"
	}
	if cfg.Username == "" || cfg.AuthKey == "" {
		return fmt.Errorf("请填写 PacketStream 用户名与认证密钥")
	}
	if cfg.SessionMode == "sticky" && cfg.SessionID == "" {
		cfg.SessionID = randomSessionID()
	}
	if cfg.SessionMode != "sticky" {
		cfg.SessionID = ""
	}

	if _, err := s.db.Exec("DELETE FROM packetstream_config"); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO packetstream_config
		(host, port, username, auth_key, country, session_mode, session_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		cfg.Host, cfg.Port, cfg.Username, cfg.AuthKey, cfg.Country, cfg.SessionMode, cfg.SessionID,
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

// BuildPassword 根据国家/会话动态拼接完整代理密码。
func (s *PacketStreamService) BuildPassword(cfg *PacketStreamConfig) string {
	pw := cfg.AuthKey
	if cfg.Country != "" {
		pw += "_country-" + cfg.Country
	}
	if cfg.SessionMode == "sticky" {
		sid := cfg.SessionID
		if sid == "" {
			sid = randomSessionID()
		}
		pw += "_session-" + sid
	}
	return pw
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
		"password":    s.BuildPassword(cfg),
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
	cfg.AuthKey = normalizeAuthKey(cfg.AuthKey)
	cfg.Country = normalizeCountry(cfg.Country)
	if cfg.Host == "" {
		cfg.Host = packetStreamDefaultHost
	}
	if cfg.Port == 0 {
		cfg.Port = packetStreamDefaultPort
	}
	if cfg.Username == "" || cfg.AuthKey == "" {
		return PacketStreamTestResult{Success: false, Message: "请填写用户名与认证密钥"}
	}

	password := s.BuildPassword(cfg)
	proxyURL := &url.URL{
		Scheme: "http",
		User:   url.UserPassword(cfg.Username, password),
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
// 支持：
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
	if host == "" {
		host = packetStreamDefaultHost
	}
	port := packetStreamDefaultPort
	if portStr != "" {
		if p, err := strconv.Atoi(strings.TrimSpace(portStr)); err == nil && p > 0 {
			port = p
		}
	}

	cfg := &PacketStreamConfig{
		Host:        host,
		Port:        port,
		Username:    user,
		SessionMode: "rotating",
	}

	// 从密码中拆分出国家 / 会话
	country, sessionID := extractCountrySession(pass)
	cfg.AuthKey = normalizeAuthKey(pass)
	cfg.Country = country
	if sessionID != "" {
		cfg.SessionMode = "sticky"
		cfg.SessionID = sessionID
	}

	if cfg.AuthKey == "" {
		return nil, fmt.Errorf("代理串中缺少认证密钥")
	}
	return cfg, nil
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

// extractCountrySession 从完整密码中提取 country / session，返回 (country, sessionID)。
func extractCountrySession(password string) (string, string) {
	country := ""
	session := ""
	for _, seg := range strings.Split(password, "_") {
		lower := strings.ToLower(seg)
		if strings.HasPrefix(lower, "country-") {
			country = seg[len("country-"):]
		} else if strings.HasPrefix(lower, "session-") {
			session = seg[len("session-"):]
		}
	}
	return normalizeCountry(country), strings.TrimSpace(session)
}

// normalizeAuthKey 去除密码中附带的 _country-xxx / _session-xxx，只保留基础密钥。
func normalizeAuthKey(password string) string {
	password = strings.TrimSpace(password)
	if password == "" {
		return ""
	}
	segs := strings.Split(password, "_")
	kept := make([]string, 0, len(segs))
	for _, seg := range segs {
		lower := strings.ToLower(seg)
		if strings.HasPrefix(lower, "country-") || strings.HasPrefix(lower, "session-") {
			continue
		}
		kept = append(kept, seg)
	}
	return strings.Join(kept, "_")
}

// normalizeCountry 规整国家名：去空格、首字母大写规则交给前端，这里仅去首尾空白。
func normalizeCountry(country string) string {
	c := strings.TrimSpace(country)
	if strings.EqualFold(c, "global") || strings.EqualFold(c, "random") || c == "随机" || c == "全球" {
		return ""
	}
	return c
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

// PacketStreamCountryOption 国家下拉项：Value 为 PacketStream 识别的英文名（无空格），Label 为中文显示。
type PacketStreamCountryOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// PacketStreamCountries 返回常用的国家/地区列表。Value 必须与 PacketStream 后端
// 的 country 取值一致（英文国名、无空格）。空 Value 表示全球随机出口。
func PacketStreamCountries() []PacketStreamCountryOption {
	return []PacketStreamCountryOption{
		{Value: "", Label: "全球随机"},
		{Value: "UnitedStates", Label: "美国"},
		{Value: "UnitedKingdom", Label: "英国"},
		{Value: "Canada", Label: "加拿大"},
		{Value: "Germany", Label: "德国"},
		{Value: "France", Label: "法国"},
		{Value: "Netherlands", Label: "荷兰"},
		{Value: "Singapore", Label: "新加坡"},
		{Value: "Japan", Label: "日本"},
		{Value: "HongKong", Label: "香港"},
		{Value: "Taiwan", Label: "台湾"},
		{Value: "SouthKorea", Label: "韩国"},
		{Value: "Australia", Label: "澳大利亚"},
		{Value: "Turkey", Label: "土耳其"},
		{Value: "Nigeria", Label: "尼日利亚"},
		{Value: "India", Label: "印度"},
		{Value: "Brazil", Label: "巴西"},
		{Value: "Italy", Label: "意大利"},
		{Value: "Spain", Label: "西班牙"},
		{Value: "Russia", Label: "俄罗斯"},
		{Value: "Indonesia", Label: "印度尼西亚"},
		{Value: "Vietnam", Label: "越南"},
		{Value: "Thailand", Label: "泰国"},
		{Value: "Malaysia", Label: "马来西亚"},
		{Value: "Philippines", Label: "菲律宾"},
		{Value: "UnitedArabEmirates", Label: "阿联酋"},
		{Value: "SouthAfrica", Label: "南非"},
		{Value: "Mexico", Label: "墨西哥"},
	}
}

func randomSessionID() string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 8)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			b[i] = charset[0]
			continue
		}
		b[i] = charset[n.Int64()]
	}
	return string(b)
}
