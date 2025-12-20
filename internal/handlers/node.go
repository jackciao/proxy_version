package handlers

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"proxy_version/internal/models"
	"proxy_version/internal/services"

	"github.com/gin-gonic/gin"
)

func ListNodes(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetInt64("user_id")

		rows, err := db.Query(
			"SELECT id, user_id, name, protocol, domain, port, status, config, COALESCE(warp_enabled, 0), created_at, updated_at FROM nodes WHERE user_id = ? ORDER BY created_at DESC",
			userID,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
			return
		}
		defer rows.Close()

		var nodes []gin.H
		for rows.Next() {
			var node models.Node
			var config sql.NullString
			var warpEnabled int
			err := rows.Scan(&node.ID, &node.UserID, &node.Name, &node.Protocol, &node.Domain, &node.Port, &node.Status, &config, &warpEnabled, &node.CreatedAt, &node.UpdatedAt)
			if err != nil {
				continue
			}
			
			// Parse config JSON string to object
			var configObj map[string]interface{}
			if config.Valid && config.String != "" {
				json.Unmarshal([]byte(config.String), &configObj)
			}
			
			nodes = append(nodes, gin.H{
				"id":           node.ID,
				"user_id":      node.UserID,
				"name":         node.Name,
				"protocol":     node.Protocol,
				"domain":       node.Domain,
				"port":         node.Port,
				"status":       node.Status,
				"config":       configObj,
				"warp_enabled": warpEnabled,
				"created_at":   node.CreatedAt,
				"updated_at":   node.UpdatedAt,
			})
		}

		c.JSON(http.StatusOK, gin.H{"nodes": nodes})
	}
}

func CreateNode(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetInt64("user_id")

		var req models.CreateNodeRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Generate config based on protocol
		proxyService := services.NewProxyService()
		config, err := proxyService.GenerateConfig(req.Protocol, req.Domain, req.Port, req.Config)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		configJSON, _ := json.Marshal(config)

		result, err := db.Exec(
			"INSERT INTO nodes (user_id, name, protocol, domain, port, status, config) VALUES (?, ?, ?, ?, ?, ?, ?)",
			userID, req.Name, req.Protocol, req.Domain, req.Port, models.NodeStatusStopped, string(configJSON),
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create node"})
			return
		}

		id, _ := result.LastInsertId()
		c.JSON(http.StatusCreated, gin.H{
			"message": "Node created successfully",
			"node_id": id,
			"config":  config,
		})
	}
}

func GetNode(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetInt64("user_id")
		nodeID, _ := strconv.ParseInt(c.Param("id"), 10, 64)

		var node models.Node
		var config sql.NullString
		err := db.QueryRow(
			"SELECT id, user_id, name, protocol, domain, port, status, config, created_at, updated_at FROM nodes WHERE id = ? AND user_id = ?",
			nodeID, userID,
		).Scan(&node.ID, &node.UserID, &node.Name, &node.Protocol, &node.Domain, &node.Port, &node.Status, &config, &node.CreatedAt, &node.UpdatedAt)

		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "Node not found"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
			return
		}

		if config.Valid {
			node.Config = config.String
		}
		c.JSON(http.StatusOK, node)
	}
}

func UpdateNode(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetInt64("user_id")
		nodeID, _ := strconv.ParseInt(c.Param("id"), 10, 64)

		var req models.UpdateNodeRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Get existing node to merge config
		var existingConfig sql.NullString
		var existingName, existingDomain string
		var existingPort int
		err := db.QueryRow(
			"SELECT name, domain, port, config FROM nodes WHERE id = ? AND user_id = ?",
			nodeID, userID,
		).Scan(&existingName, &existingDomain, &existingPort, &existingConfig)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Node not found"})
			return
		}

		// Use existing values if not provided
		name := req.Name
		if name == "" {
			name = existingName
		}
		domain := req.Domain
		if domain == "" {
			domain = existingDomain
		}
		port := req.Port
		if port == 0 {
			port = existingPort
		}

		// Merge config
		var configMap map[string]interface{}
		if existingConfig.Valid && existingConfig.String != "" {
			json.Unmarshal([]byte(existingConfig.String), &configMap)
		}
		if configMap == nil {
			configMap = make(map[string]interface{})
		}
		for k, v := range req.Config {
			configMap[k] = v
		}
		configJSON, _ := json.Marshal(configMap)

		result, err := db.Exec(
			"UPDATE nodes SET name = ?, domain = ?, port = ?, config = ?, updated_at = ? WHERE id = ? AND user_id = ?",
			name, domain, port, string(configJSON), time.Now(), nodeID, userID,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update node"})
			return
		}

		rows, _ := result.RowsAffected()
		if rows == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "Node not found"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Node updated successfully"})
	}
}

func DeleteNode(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetInt64("user_id")
		nodeID, _ := strconv.ParseInt(c.Param("id"), 10, 64)

		// Stop node if running
		proxyService := services.NewProxyService()
		proxyService.StopNode(nodeID)

		result, err := db.Exec("DELETE FROM nodes WHERE id = ? AND user_id = ?", nodeID, userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete node"})
			return
		}

		rows, _ := result.RowsAffected()
		if rows == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "Node not found"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Node deleted successfully"})
	}
}

func StartNode(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetInt64("user_id")
		nodeID, _ := strconv.ParseInt(c.Param("id"), 10, 64)

		// Get node config including warp_enabled
		var node models.Node
		var config sql.NullString
		var warpEnabled int
		err := db.QueryRow(
			"SELECT id, protocol, domain, port, config, COALESCE(warp_enabled, 0) FROM nodes WHERE id = ? AND user_id = ?",
			nodeID, userID,
		).Scan(&node.ID, &node.Protocol, &node.Domain, &node.Port, &config, &warpEnabled)

		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "Node not found"})
			return
		}

		// Debug log
		log.Printf("Starting node %d with warp_enabled=%d", nodeID, warpEnabled)

		proxyService := services.NewProxyService()
		if err := proxyService.StartNode(nodeID, node.Protocol, config.String, warpEnabled == 1, db); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		db.Exec("UPDATE nodes SET status = ?, updated_at = ? WHERE id = ?", models.NodeStatusRunning, time.Now(), nodeID)

		c.JSON(http.StatusOK, gin.H{"message": "Node started successfully"})
	}
}

func StopNode(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetInt64("user_id")
		nodeID, _ := strconv.ParseInt(c.Param("id"), 10, 64)

		// Verify ownership
		var exists bool
		db.QueryRow("SELECT EXISTS(SELECT 1 FROM nodes WHERE id = ? AND user_id = ?)", nodeID, userID).Scan(&exists)
		if !exists {
			c.JSON(http.StatusNotFound, gin.H{"error": "Node not found"})
			return
		}

		proxyService := services.NewProxyService()
		if err := proxyService.StopNode(nodeID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		db.Exec("UPDATE nodes SET status = ?, updated_at = ? WHERE id = ?", models.NodeStatusStopped, time.Now(), nodeID)

		c.JSON(http.StatusOK, gin.H{"message": "Node stopped successfully"})
	}
}

func GetNodeShare(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetInt64("user_id")
		nodeID, _ := strconv.ParseInt(c.Param("id"), 10, 64)

		// Get node config
		var node models.Node
		var config sql.NullString
		var domain sql.NullString
		err := db.QueryRow(
			"SELECT id, name, protocol, domain, port, config FROM nodes WHERE id = ? AND user_id = ?",
			nodeID, userID,
		).Scan(&node.ID, &node.Name, &node.Protocol, &domain, &node.Port, &config)

		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "Node not found"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
			return
		}

		// Parse config first to check for listen IP binding
		var configMap map[string]interface{}
		if config.Valid {
			json.Unmarshal([]byte(config.String), &configMap)
		}

		// Determine server IP:
		// 1. If domain is set, use domain (for TLS protocols)
		// 2. If listen IP is bound, let GenerateShareURL handle it (pass empty)
		// 3. Otherwise, auto-detect server IP
		serverIP := ""
		if domain.Valid && domain.String != "" {
			serverIP = domain.String
		}
		// Don't call getServerIP() here - let GenerateShareURL determine from listen or auto-detect

		// Generate share URL
		proxyService := services.NewProxyService()
		shareInfo, err := proxyService.GenerateShareURL(node.Name, serverIP, configMap)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, shareInfo)
	}
}

func getServerIP() string {
	// Try to get public IP
	cmd := "curl -s --connect-timeout 3 ifconfig.me || curl -s --connect-timeout 3 ip.sb || hostname -I | awk '{print $1}'"
	if output, err := exec.Command("sh", "-c", cmd).Output(); err == nil {
		ip := strings.TrimSpace(string(output))
		if ip != "" {
			return ip
		}
	}
	return "127.0.0.1"
}
