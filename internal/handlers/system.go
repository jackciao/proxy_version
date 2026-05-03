package handlers

import (
	"database/sql"
	"net/http"

	"proxy_version/internal/services"

	"github.com/gin-gonic/gin"
)

func GetSystemStatus() gin.HandlerFunc {
	return func(c *gin.Context) {
		detector := services.NewDetectorService()
		status := detector.GetSystemStatus()
		c.JSON(http.StatusOK, status)
	}
}

func DetectReverseProxy() gin.HandlerFunc {
	return func(c *gin.Context) {
		detector := services.NewDetectorService()
		result := detector.DetectReverseProxy()
		c.JSON(http.StatusOK, result)
	}
}

func GetProtocols() gin.HandlerFunc {
	return func(c *gin.Context) {
		proxyService := services.NewProxyService()
		protocols := proxyService.GetSupportedProtocols()
		c.JSON(http.StatusOK, gin.H{"protocols": protocols})
	}
}

func GetCoreStatus() gin.HandlerFunc {
	return func(c *gin.Context) {
		proxyService := services.NewProxyService()
		status := proxyService.GetCoreStatus()
		c.JSON(http.StatusOK, status)
	}
}

func GetRandomPort() gin.HandlerFunc {
	return func(c *gin.Context) {
		proxyService := services.NewProxyService()
		port := proxyService.GetRandomAvailablePort()
		c.JSON(http.StatusOK, gin.H{"port": port})
	}
}

func InstallCore() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Core string `json:"core" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		proxyService := services.NewProxyService()
		var err error

		switch req.Core {
		case "singbox":
			err = proxyService.InstallSingBox()
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "不支持的核心: " + req.Core})
			return
		}

		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": req.Core + " 安装成功"})
	}
}

func UninstallCore() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Core string `json:"core" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		proxyService := services.NewProxyService()
		var err error

		switch req.Core {
		case "singbox":
			err = proxyService.UninstallSingBox()
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "不支持的核心: " + req.Core})
			return
		}

		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": req.Core + " 卸载成功"})
	}
}

// GetServerIPs returns all public IPs of the server
func GetServerIPs() gin.HandlerFunc {
	return func(c *gin.Context) {
		detector := services.NewDetectorService()
		ips := detector.GetAllServerIPs()
		c.JSON(http.StatusOK, ips)
	}
}

// CheckPort checks if a port is available on specific IP
func CheckPort() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Port int    `json:"port" binding:"required"`
			IP   string `json:"ip"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		detector := services.NewDetectorService()
		result := detector.CheckPortAvailability(req.Port, req.IP)
		c.JSON(http.StatusOK, result)
	}
}

// GetSuggestedSNI returns recommended Reality SNI sites based on server location
func GetSuggestedSNI() gin.HandlerFunc {
	return func(c *gin.Context) {
		result := services.GetSuggestedRealitySNI()
		c.JSON(http.StatusOK, result)
	}
}

func ListCertificates(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		rows, err := db.Query("SELECT id, domain, cert_path, key_path, provider, expires_at, created_at FROM certificates ORDER BY created_at DESC")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
			return
		}
		defer rows.Close()

		certService := services.NewCertificateService()
		var certs []map[string]interface{}
		for rows.Next() {
			var id int64
			var domain, certPath, keyPath, provider string
			var expiresAt, createdAt sql.NullTime

			if err := rows.Scan(&id, &domain, &certPath, &keyPath, &provider, &expiresAt, &createdAt); err != nil {
				continue
			}

			cert := map[string]interface{}{
				"id":        id,
				"domain":    domain,
				"cert_path": certPath,
				"key_path":  keyPath,
				"provider":  provider,
			}

			// Get detailed info from certificate service
			if info, err := certService.GetCertificateInfo(domain); err == nil {
				if !info.ExpiresAt.IsZero() {
					cert["expires_at"] = info.ExpiresAt
				}
				if !info.NextRenewAt.IsZero() {
					cert["next_renew_at"] = info.NextRenewAt
				}
				cert["acme_path"] = info.AcmePath
			} else {
				// Fallback to database value
				if expiresAt.Valid {
					cert["expires_at"] = expiresAt.Time
				}
			}

			if createdAt.Valid {
				cert["created_at"] = createdAt.Time
			}
			certs = append(certs, cert)
		}

		c.JSON(http.StatusOK, gin.H{"certificates": certs})
	}
}

func DeleteCertificate(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		domain := c.Param("domain")
		if domain == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "域名不能为空"})
			return
		}

		// Check if certificate exists
		var id int64
		err := db.QueryRow("SELECT id FROM certificates WHERE domain = ?", domain).Scan(&id)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "证书不存在"})
			return
		}

		// Delete from system
		certService := services.NewCertificateService()
		if err := certService.DeleteCertificate(domain); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		// Delete from database
		_, err = db.Exec("DELETE FROM certificates WHERE domain = ?", domain)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "删除数据库记录失败"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "证书已删除"})
	}
}

type ApplyCertificateRequest struct {
	Domain      string `json:"domain" binding:"required"`
	Email       string `json:"email"`
	Provider    string `json:"provider"`
	Method      string `json:"method"`
	DNSProvider string `json:"dns_provider"`
	APIToken    string `json:"api_token"`
	CFEmail     string `json:"cf_email"` // Cloudflare account email for Global API Key
}

func ApplyCertificate(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req ApplyCertificateRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		certService := services.NewCertificateService()
		certPath, keyPath, err := certService.ApplyCertificate(
			req.Domain,
			req.Email,
			req.Provider,
			req.Method,
			req.DNSProvider,
			req.APIToken,
			req.CFEmail,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		// Save to database
		_, err = db.Exec(
			"INSERT OR REPLACE INTO certificates (domain, cert_path, key_path, provider) VALUES (?, ?, ?, ?)",
			req.Domain, certPath, keyPath, req.Provider,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save certificate"})
			return
		}

		// Auto-deploy camouflage site (already done in certificate service)
		// Check camouflage status
		camoService := services.NewCamouflageService()
		camoStatus := camoService.GetStatus(req.Domain)

		c.JSON(http.StatusOK, gin.H{
			"message":             "Certificate applied successfully",
			"cert_path":           certPath,
			"key_path":            keyPath,
			"camouflage_deployed": camoStatus.Deployed,
			"camouflage_url":      camoStatus.URL,
		})
	}
}

// GetCertProgress returns the current certificate application progress
func GetCertProgress() gin.HandlerFunc {
	return func(c *gin.Context) {
		domain := c.Param("domain")
		if domain == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "域名不能为空"})
			return
		}

		progress := services.GetCertProgress(domain)
		if progress == nil {
			c.JSON(http.StatusOK, gin.H{
				"domain":     domain,
				"status":     "pending",
				"step":       0,
				"total_step": 6,
				"step_name":  "等待开始...",
			})
			return
		}

		c.JSON(http.StatusOK, progress)
	}
}

// GetCamouflageStatus returns the camouflage site deployment status
func GetCamouflageStatus() gin.HandlerFunc {
	return func(c *gin.Context) {
		domain := c.Param("domain")
		if domain == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "域名不能为空"})
			return
		}

		camoService := services.NewCamouflageService()
		status := camoService.GetStatus(domain)
		c.JSON(http.StatusOK, status)
	}
}
