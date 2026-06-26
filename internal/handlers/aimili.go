package handlers

import (
	"database/sql"
	"net/http"
	"strconv"
	"sync"
	"time"

	"proxy_version/internal/models"
	"proxy_version/internal/services"

	"github.com/gin-gonic/gin"
)

var aimiliInstallState = struct {
	sync.RWMutex
	installing bool
	startedAt  time.Time
	err        string
}{}

var aimiliRefreshState = struct {
	sync.RWMutex
	refreshing bool
	startedAt  time.Time
	err        string
}{}

func aimiliStatus() services.AimiliStatus {
	status := services.NewAimiliService().GetStatus()
	aimiliInstallState.RLock()
	status.Installing = aimiliInstallState.installing
	if !aimiliInstallState.startedAt.IsZero() {
		status.InstallStartedAt = aimiliInstallState.startedAt.Format(time.RFC3339)
	}
	status.InstallError = aimiliInstallState.err
	aimiliInstallState.RUnlock()
	aimiliRefreshState.RLock()
	status.Refreshing = aimiliRefreshState.refreshing
	if !aimiliRefreshState.startedAt.IsZero() {
		status.RefreshStartedAt = aimiliRefreshState.startedAt.Format(time.RFC3339)
	}
	status.RefreshError = aimiliRefreshState.err
	aimiliRefreshState.RUnlock()
	return status
}

func GetAimiliStatus() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, aimiliStatus())
	}
}

func startAimiliInstall() bool {
	aimiliInstallState.Lock()
	if aimiliInstallState.installing {
		aimiliInstallState.Unlock()
		return false
	}
	aimiliInstallState.installing = true
	aimiliInstallState.startedAt = time.Now()
	aimiliInstallState.err = ""
	aimiliInstallState.Unlock()

	go func() {
		err := services.NewAimiliService().Install()
		aimiliInstallState.Lock()
		aimiliInstallState.installing = false
		if err != nil {
			aimiliInstallState.err = err.Error()
		}
		aimiliInstallState.Unlock()
	}()
	return true
}

// BootstrapAimili deploys the image-bundled version on first startup or after an image upgrade.
func BootstrapAimili() {
	service := services.NewAimiliService()
	status := service.GetStatus()
	if !status.Installed || !service.IsBundleCurrent() {
		startAimiliInstall()
	}
}

func InstallAimili() gin.HandlerFunc {
	return func(c *gin.Context) {
		service := services.NewAimiliService()
		status := service.GetStatus()
		if status.Installed && service.IsBundleCurrent() {
			c.JSON(http.StatusOK, gin.H{"message": "Aimili VPN 内置版本已部署", "status": aimiliStatus()})
			return
		}
		if !startAimiliInstall() {
			c.JSON(http.StatusAccepted, gin.H{"message": "Aimili VPN 正在部署"})
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"message": "已开始部署内置 Aimili VPN"})
	}
}

func RefreshAimiliCountries() gin.HandlerFunc {
	return func(c *gin.Context) {
		service := services.NewAimiliService()
		status := service.GetStatus()
		if !status.Installed {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请先安装 Aimili VPN"})
			return
		}

		aimiliRefreshState.Lock()
		if aimiliRefreshState.refreshing {
			aimiliRefreshState.Unlock()
			c.JSON(http.StatusAccepted, gin.H{"message": "地区列表正在刷新"})
			return
		}
		aimiliRefreshState.refreshing = true
		aimiliRefreshState.startedAt = time.Now()
		aimiliRefreshState.err = ""
		aimiliRefreshState.Unlock()

		go func() {
			err := service.RefreshCountries()
			aimiliRefreshState.Lock()
			aimiliRefreshState.refreshing = false
			if err != nil {
				aimiliRefreshState.err = err.Error()
			}
			aimiliRefreshState.Unlock()
		}()

		c.JSON(http.StatusAccepted, gin.H{"message": "已开始刷新 Aimili 地区列表"})
	}
}

func ConfigureAimili() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Country string `json:"country"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
			return
		}
		service := services.NewAimiliService()
		if err := service.Configure(req.Country); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"message": "Aimili VPN 地区策略已保存，正在重新连接",
			"status":  service.GetStatus(),
		})
	}
}

func ToggleNodeAimili(db *sql.DB) gin.HandlerFunc {
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
		if req.Enabled {
			if err := services.NewAimiliService().ValidateReady(); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
		}

		tx, err := db.Begin()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
			return
		}
		defer tx.Rollback()

		var protocol, status, config string
		var warpEnabled, packetstreamEnabled int
		if err := tx.QueryRow(
			"SELECT protocol, status, config, COALESCE(warp_enabled, 0), COALESCE(packetstream_enabled, 0) FROM nodes WHERE id = ? AND user_id = ?",
			nodeID, userID,
		).Scan(&protocol, &status, &config, &warpEnabled, &packetstreamEnabled); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "节点不存在"})
			return
		}

		aimiliEnabled := 0
		if req.Enabled {
			aimiliEnabled = 1
			warpEnabled = 0
			packetstreamEnabled = 0
		}
		if _, err := tx.Exec(
			"UPDATE nodes SET aimili_enabled = ?, warp_enabled = ?, packetstream_enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND user_id = ?",
			aimiliEnabled, warpEnabled, packetstreamEnabled, nodeID, userID,
		); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
			return
		}
		if err := tx.Commit(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
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
			_ = proxyService.StopNode(id)
			if err := proxyService.StartNode(id, protocol, config, warpEnabled == 1, req.Enabled, packetstreamEnabled == 1, db); err != nil {
				_, _ = db.Exec("UPDATE nodes SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND user_id = ?", models.NodeStatusError, nodeID, userID)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Aimili VPN 设置已更新，但节点重启失败: " + err.Error()})
				return
			}
			_, _ = db.Exec("UPDATE nodes SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND user_id = ?", models.NodeStatusRunning, nodeID, userID)
			restarted = true
		}

		c.JSON(http.StatusOK, gin.H{
			"message":              "Aimili VPN 设置已更新",
			"aimili_enabled":       req.Enabled,
			"warp_enabled":         warpEnabled == 1,
			"packetstream_enabled": packetstreamEnabled == 1,
			"restarted":            restarted,
		})
	}
}
