package handlers

import (
	"database/sql"
	"net/http"
	"strconv"

	"proxy_version/internal/models"
	"proxy_version/internal/services"

	"github.com/gin-gonic/gin"
)

// GetWarpStatus returns the current WARP configuration status
func GetWarpStatus(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		warpService := services.NewWarpService(db)
		status := warpService.GetStatus()
		c.JSON(http.StatusOK, status)
	}
}

// RegisterWarp registers a new WARP account
func RegisterWarp(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		warpService := services.NewWarpService(db)
		config, err := warpService.Register()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"message": "WARP 账号注册成功",
			"config":  config,
		})
	}
}

// RefreshWarp re-registers to get a new WARP IP
func RefreshWarp(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		warpService := services.NewWarpService(db)
		config, err := warpService.Refresh()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"message": "WARP 节点已更换",
			"config":  config,
		})
	}
}

// UpgradeWarp upgrades the account to WARP+
func UpgradeWarp(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			LicenseKey string `json:"license_key" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		warpService := services.NewWarpService(db)
		if err := warpService.UpgradeToPlus(req.LicenseKey); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		config, _ := warpService.GetConfig()
		c.JSON(http.StatusOK, gin.H{
			"message": "升级到 WARP+ 成功",
			"config":  config,
		})
	}
}

// ImportWarpConfig imports an existing WARP configuration
func ImportWarpConfig(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			PrivateKey string `json:"private_key" binding:"required"`
			IPv4       string `json:"ipv4" binding:"required"`
			IPv6       string `json:"ipv6"`
			Endpoint   string `json:"endpoint"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		warpService := services.NewWarpService(db)
		config, err := warpService.ImportConfig(req.PrivateKey, req.IPv4, req.IPv6, req.Endpoint)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"message": "WARP 配置导入成功",
			"config":  config,
		})
	}
}

// DeleteWarpConfig removes the WARP configuration
func DeleteWarpConfig(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		warpService := services.NewWarpService(db)
		if err := warpService.DeleteConfig(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "WARP 配置已删除"})
	}
}

// ExportWarpConfig exports the WARP config as sing-box JSON
func ExportWarpConfig(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		warpService := services.NewWarpService(db)
		jsonStr, err := warpService.ExportAsJSON()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"config": jsonStr,
		})
	}
}

// ToggleNodeWarp toggles WARP for a specific node
func ToggleNodeWarp(db *sql.DB) gin.HandlerFunc {
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

		warpEnabled := 0
		if req.Enabled {
			warpEnabled = 1
		}

		var protocol, status, config string
		if err := db.QueryRow(
			"SELECT protocol, status, config FROM nodes WHERE id = ? AND user_id = ?",
			nodeID, userID,
		).Scan(&protocol, &status, &config); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "节点不存在"})
			return
		}

		result, err := db.Exec("UPDATE nodes SET warp_enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND user_id = ?", warpEnabled, nodeID, userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
			return
		}

		rows, _ := result.RowsAffected()
		if rows == 0 {
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
			if err := proxyService.StartNode(id, protocol, config, req.Enabled, db); err != nil {
				db.Exec("UPDATE nodes SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND user_id = ?", models.NodeStatusError, nodeID, userID)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "WARP 设置已更新，但节点重启失败: " + err.Error()})
				return
			}
			db.Exec("UPDATE nodes SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND user_id = ?", models.NodeStatusRunning, nodeID, userID)
			restarted = true
		}

		c.JSON(http.StatusOK, gin.H{
			"message":      "WARP 设置已更新",
			"warp_enabled": req.Enabled,
			"restarted":    restarted,
		})
	}
}

// CheckStreamingUnlock checks if WARP IP can unlock streaming services
func CheckStreamingUnlock(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		warpService := services.NewWarpService(db)
		result, err := warpService.CheckStreamingUnlock()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, result)
	}
}
