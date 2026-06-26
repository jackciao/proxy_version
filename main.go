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
		// Auth routes (public)
		auth := api.Group("/auth")
		{
			auth.POST("/login", handlers.Login(db, cfg.JWTSecret))
			auth.POST("/setup", handlers.Setup(db))
			auth.GET("/me", middleware.Auth(cfg.JWTSecret, db), handlers.GetCurrentUser(db))
		}

		// Protected routes
		protected := api.Group("")
		protected.Use(middleware.Auth(cfg.JWTSecret, db))
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

			// Gotee drive camouflage storage
			driveHandler := handlers.NewDriveHandler(db, cfg.DataDir)
			drive := protected.Group("/drive")
			{
				drive.GET("/state", driveHandler.State())
				drive.GET("/api-token", driveHandler.APIToken())
				drive.GET("/remotes", driveHandler.ListRemotes())
				drive.POST("/remotes", driveHandler.CreateRemote())
				drive.DELETE("/remotes/:id", driveHandler.DeleteRemote())
				drive.POST("/folders", driveHandler.CreateFolder())
				drive.POST("/upload", driveHandler.UploadFiles())
				drive.PUT("/items/:id", driveHandler.UpdateItem())
				drive.POST("/items/:id/trash", driveHandler.TrashItem())
				drive.POST("/items/:id/restore", driveHandler.RestoreItem())
				drive.DELETE("/items/:id", driveHandler.PurgeItem())
				drive.DELETE("/trash", driveHandler.ClearTrash())
				drive.PUT("/quota", driveHandler.UpdateQuota())
				drive.GET("/files/:id", driveHandler.PreviewFile())
				drive.GET("/files/:id/download", driveHandler.DownloadFile())
			}

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

			// Aimili VPN
			aimili := protected.Group("/aimili")
			{
				aimili.GET("/status", handlers.GetAimiliStatus())
				aimili.POST("/install", handlers.InstallAimili())
				aimili.POST("/refresh", handlers.RefreshAimiliCountries())
				aimili.POST("/configure", handlers.ConfigureAimili())
			}

			// PacketStream 全球住宅代理
			packetstream := protected.Group("/packetstream")
			{
				packetstream.GET("/status", handlers.GetPacketStreamStatus(db))
				packetstream.POST("/config", handlers.SavePacketStreamConfig(db))
				packetstream.DELETE("/config", handlers.DeletePacketStreamConfig(db))
				packetstream.POST("/test", handlers.TestPacketStream(db))
			}

			// Mutually exclusive node outbound toggles
			protected.POST("/nodes/:id/warp", handlers.ToggleNodeWarp(db))
			protected.POST("/nodes/:id/aimili", handlers.ToggleNodeAimili(db))
			protected.POST("/nodes/:id/packetstream", handlers.TogglePacketStream(db))
		}
	}

	// Deploy the image-bundled Aimili version in the background when needed.
	handlers.BootstrapAimili()

	// Start server
	log.Printf("Server starting on port %s", cfg.Port)
	if err := r.Run(":" + cfg.Port); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
