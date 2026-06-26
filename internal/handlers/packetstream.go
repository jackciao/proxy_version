package handlers

import (
	"database/sql"
	"net/http"
	"strconv"

	"proxy_version/internal/models"
	"proxy_version/internal/services"

	"github.com/gin-gonic/gin"
)

// packetStreamConfigRequest 同时支持两种录入模式：
//   - mode=credentials：用户名 + 认证密钥（官网 Proxy Password，已含国家/会话）
//   - mode=proxy_string：直接粘贴官网完整代理串，由后端解析
type packetStreamConfigRequest struct {
	Mode        string `json:"mode"`
	ProxyString string `json:"proxy_string"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	AuthKey     string `json:"auth_key"`
}

func (r packetStreamConfigRequest) toConfig() (*services.PacketStreamConfig, error) {
	if r.Mode == "proxy_string" || (r.ProxyString != "" && r.Username == "") {
		return services.ParsePacketStreamProxyString(r.ProxyString)
	}
	return &services.PacketStreamConfig{
		Host:     r.Host,
		Port:     r.Port,
		Username: r.Username,
		AuthKey:  r.AuthKey,
	}, nil
}

// GetPacketStreamStatus 返回当前 PacketStream 配置状态（脱敏）。
func GetPacketStreamStatus(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		service := services.NewPacketStreamService(db)
		c.JSON(http.StatusOK, gin.H{
			"status": service.GetStatus(),
		})
	}
}

// SavePacketStreamConfig 保存配置（两种模式共用）。
func SavePacketStreamConfig(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req packetStreamConfigRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		cfg, err := req.toConfig()
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		service := services.NewPacketStreamService(db)
		if err := service.SaveConfig(cfg); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"message": "PacketStream 配置已保存",
			"status":  service.GetStatus(),
		})
	}
}

// DeletePacketStreamConfig 删除配置。
func DeletePacketStreamConfig(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		service := services.NewPacketStreamService(db)
		if err := service.DeleteConfig(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "PacketStream 配置已删除"})
	}
}

// TestPacketStream 通过代理实测出口 IP。请求体可携带临时配置（保存前测试），
// 留空则使用已保存配置。
func TestPacketStream(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req packetStreamConfigRequest
		_ = c.ShouldBindJSON(&req)

		service := services.NewPacketStreamService(db)
		var override *services.PacketStreamConfig
		if req.ProxyString != "" || req.Username != "" || req.AuthKey != "" {
			cfg, err := req.toConfig()
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			override = cfg
		}
		result := service.TestConnection(override)
		if !result.Success {
			c.JSON(http.StatusOK, result)
			return
		}
		c.JSON(http.StatusOK, result)
	}
}

// TogglePacketStream 为指定节点开关 PacketStream 出口（与 WARP/Aimili 互斥）。
func TogglePacketStream(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		nodeID := c.Param("id")
		userID := c.GetInt64("user_id")

		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		service := services.NewPacketStreamService(db)
		if req.Enabled {
			if err := service.ValidateReady(); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
		}

		var protocol, status, config string
		var warpEnabled, aimiliEnabled int
		if err := db.QueryRow(
			"SELECT protocol, status, config, COALESCE(warp_enabled, 0), COALESCE(aimili_enabled, 0) FROM nodes WHERE id = ? AND user_id = ?",
			nodeID, userID,
		).Scan(&protocol, &status, &config, &warpEnabled, &aimiliEnabled); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "节点不存在"})
			return
		}

		packetstreamEnabled := 0
		if req.Enabled {
			packetstreamEnabled = 1
			warpEnabled = 0
			aimiliEnabled = 0
		}
		result, err := db.Exec(
			"UPDATE nodes SET packetstream_enabled = ?, warp_enabled = ?, aimili_enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND user_id = ?",
			packetstreamEnabled, warpEnabled, aimiliEnabled, nodeID, userID,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
			return
		}
		if rows, _ := result.RowsAffected(); rows == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "节点不存在"})
			return
		}

		restarted := false
		if status == models.NodeStatusRunning {
			id, err := strconv.ParseInt(nodeID, 10, 64)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "节点 ID 无效"})
				return
			}
			proxyService := services.NewProxyService()
			proxyService.StopNode(id)
			if err := proxyService.StartNode(id, protocol, config, warpEnabled == 1, aimiliEnabled == 1, req.Enabled, db); err != nil {
				db.Exec("UPDATE nodes SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND user_id = ?", models.NodeStatusError, nodeID, userID)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "PacketStream 设置已更新，但节点重启失败: " + err.Error()})
				return
			}
			db.Exec("UPDATE nodes SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND user_id = ?", models.NodeStatusRunning, nodeID, userID)
			restarted = true
		}

		c.JSON(http.StatusOK, gin.H{
			"message":              "PacketStream 设置已更新",
			"packetstream_enabled": req.Enabled,
			"warp_enabled":         warpEnabled == 1,
			"aimili_enabled":       aimiliEnabled == 1,
			"restarted":            restarted,
		})
	}
}
