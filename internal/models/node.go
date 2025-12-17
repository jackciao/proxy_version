package models

import "time"

// Protocol types
const (
	ProtocolVLESSVision  = "vless-vision"
	ProtocolVLESSReality = "vless-reality"
	ProtocolVLESSWS      = "vless-ws"
	ProtocolVMessWS      = "vmess-ws"
	ProtocolTrojan       = "trojan"
	ProtocolHysteria2    = "hysteria2"
	ProtocolTUIC         = "tuic"
)

// Node status
const (
	NodeStatusStopped = "stopped"
	NodeStatusRunning = "running"
	NodeStatusError   = "error"
)

type Node struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"user_id"`
	Name      string    `json:"name"`
	Protocol  string    `json:"protocol"`
	Domain    string    `json:"domain,omitempty"`
	Port      int       `json:"port"`
	Status    string    `json:"status"`
	Config    string    `json:"config,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type CreateNodeRequest struct {
	Name     string     `json:"name" binding:"required"`
	Protocol string     `json:"protocol" binding:"required"`
	Domain   string     `json:"domain"`
	Port     int        `json:"port" binding:"required,min=1,max=65535"`
	Config   NodeConfig `json:"config"`
}

type UpdateNodeRequest struct {
	Name   string                 `json:"name"`
	Domain string                 `json:"domain"`
	Port   int                    `json:"port" binding:"omitempty,min=1,max=65535"`
	Config map[string]interface{} `json:"config"`
}

type NodeConfig struct {
	UUID           string `json:"uuid,omitempty"`
	Flow           string `json:"flow,omitempty"`
	PublicKey      string `json:"public_key,omitempty"`
	PrivateKey     string `json:"private_key,omitempty"`
	ShortID        string `json:"short_id,omitempty"`
	ServerName     string `json:"server_name,omitempty"`
	Path           string `json:"path,omitempty"`
	Password       string `json:"password,omitempty"`
	UpMbps         int    `json:"up_mbps,omitempty"`
	DownMbps       int    `json:"down_mbps,omitempty"`
	CertPath       string `json:"cert_path,omitempty"`
	KeyPath        string `json:"key_path,omitempty"`
	CongestionCtrl string `json:"congestion_ctrl,omitempty"`
	Listen         string `json:"listen,omitempty"` // IP to bind to (for port 443 sharing)
}
