package main

import (
	"log"

	"proxy_version/internal/config"
	"proxy_version/internal/database"
	"proxy_version/internal/handlers"
	"proxy_version/internal/middleware"

	"github.com/gin-gonic/gin"
)

func main() {
	// Load configuration
	cfg := config.Load()

	// Initialize database
	db, err := database.Initialize(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()

	// Set Gin mode
	if cfg.Environment == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	// Create router
	r := gin.Default()

	// Apply middleware
	r.Use(middleware.CORS())
	
	// Server disguise middleware - make responses look like nginx
	r.Use(func(c *gin.Context) {
		c.Header("Server", "nginx/1.24.0")
		c.Header("X-Powered-By", "")
		c.Writer.Header().Del("X-Powered-By")
		c.Next()
	})

	// Static files
	r.Static("/css", "./web/css")
	r.Static("/js", "./web/js")
	r.StaticFile("/", "./web/index.html")
	r.StaticFile("/index.html", "./web/index.html")

	// API routes
	api := r.Group("/api")
	{
		// Auth routes (public - login only)
		auth := api.Group("/auth")
		{
			auth.POST("/login", handlers.Login(db, cfg.JWTSecret))
			auth.GET("/me", middleware.Auth(cfg.JWTSecret), handlers.GetCurrentUser(db))
		}

		// Protected routes
		protected := api.Group("")
		protected.Use(middleware.Auth(cfg.JWTSecret))
		{
			// User management (registration requires auth)
			protected.POST("/auth/register", handlers.Register(db))
			
			// Nodes
			nodes := protected.Group("/nodes")
			{
				nodes.GET("", handlers.ListNodes(db))
				nodes.POST("", handlers.CreateNode(db))
				nodes.GET("/:id", handlers.GetNode(db))
				nodes.PUT("/:id", handlers.UpdateNode(db))
				nodes.DELETE("/:id", handlers.DeleteNode(db))
				nodes.POST("/:id/start", handlers.StartNode(db))
				nodes.POST("/:id/stop", handlers.StopNode(db))
				nodes.GET("/:id/share", handlers.GetNodeShare(db))
			}

			// System
			system := protected.Group("/system")
			{
				system.GET("/status", handlers.GetSystemStatus())
				system.GET("/detect", handlers.DetectReverseProxy())
				system.GET("/protocols", handlers.GetProtocols())
				system.GET("/cores", handlers.GetCoreStatus())
				system.GET("/random-port", handlers.GetRandomPort())
				system.GET("/ips", handlers.GetServerIPs())
				system.POST("/check-port", handlers.CheckPort())
				system.GET("/suggest-sni", handlers.GetSuggestedSNI())
				system.POST("/cores/install", handlers.InstallCore())
				system.POST("/cores/uninstall", handlers.UninstallCore())
			}

			// Certificates
			certs := protected.Group("/certificates")
			{
				certs.GET("", handlers.ListCertificates(db))
				certs.POST("/apply", handlers.ApplyCertificate(db))
				certs.GET("/progress/:domain", handlers.GetCertProgress())
				certs.DELETE("/:domain", handlers.DeleteCertificate(db))
			}

			// Camouflage
			protected.GET("/camouflage/status/:domain", handlers.GetCamouflageStatus())

			// WARP
			warp := protected.Group("/warp")
			{
				warp.GET("/status", handlers.GetWarpStatus(db))
				warp.POST("/register", handlers.RegisterWarp(db))
				warp.POST("/refresh", handlers.RefreshWarp(db))
				warp.POST("/upgrade", handlers.UpgradeWarp(db))
				warp.POST("/import", handlers.ImportWarpConfig(db))
				warp.DELETE("", handlers.DeleteWarpConfig(db))
				warp.GET("/export", handlers.ExportWarpConfig(db))
				warp.GET("/streaming-check", handlers.CheckStreamingUnlock(db))
			}

			// Node WARP toggle
			protected.POST("/nodes/:id/warp", handlers.ToggleNodeWarp(db))
		}
	}

	// Start server
	log.Printf("Server starting on port %s", cfg.Port)
	if err := r.Run(":" + cfg.Port); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
