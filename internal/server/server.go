package server

import (
	"context"
	"embed"
	"io"
	"net/http"
	"time"

	"ai-proxy/internal/config"
	"ai-proxy/internal/middleware"
	"ai-proxy/internal/rotator"
	"ai-proxy/internal/router"
	"ai-proxy/internal/sse"
	"ai-proxy/internal/stats"

	"github.com/gin-gonic/gin"
)

//go:embed web/*
var webFS embed.FS

type Instance struct {
	*gin.Engine
	Rot *rotator.Rotator
}

// New creates and configures the Gin engine with all routes and middleware.
func New(cfg *config.Config, configPath string) *Instance {
	gin.SetMode(gin.ReleaseMode)

	sseHub := sse.NewHub()
	statsCollector := stats.New()

	rot := logWriter(cfg.Global.LogFile)
	gin.DefaultWriter = io.MultiWriter(rot, sseHub)

	r := gin.New()

	r.Use(middleware.Logger())
	r.Use(middleware.DetectFormat())

	engine := router.NewEngine(cfg, configPath, gin.DefaultWriter, statsCollector)

	// Start config file watcher for hot-reload
	if configPath != "" {
		ctx, cancel := context.WithCancel(context.Background())
		engine.StartWatcher(ctx, 30*time.Second)
		_ = cancel // kept for future graceful shutdown
	}

	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	r.POST("/v1/chat/completions", engine.HandleRequest)
	r.POST("/chat/completions", engine.HandleRequest)
	r.POST("/v1/messages", engine.HandleRequest)

	r.GET("/", serveWeb("web/index.html", "text/html; charset=utf-8"))
	r.GET("/style.css", serveWeb("web/style.css", "text/css; charset=utf-8"))
	r.GET("/app.js", serveWeb("web/app.js", "application/javascript; charset=utf-8"))

	r.GET("/api/stats", func(c *gin.Context) {
		c.JSON(200, statsCollector.Snapshot())
	})

	r.GET("/api/logs", func(c *gin.Context) {
		sseHub.ServeHTTP(c.Writer, c.Request)
	})

	r.POST("/api/control", func(c *gin.Context) {
		var body struct {
			Running bool `json:"running"`
		}
		if err := c.BindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		statsCollector.SetRunning(body.Running)
		c.JSON(200, gin.H{"running": body.Running})
	})

	return &Instance{Engine: r, Rot: rot}
}

func serveWeb(filePath, contentType string) gin.HandlerFunc {
	return func(c *gin.Context) {
		data, err := webFS.ReadFile(filePath)
		if err != nil {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		c.Data(http.StatusOK, contentType, data)
	}
}

func logWriter(path string) *rotator.Rotator {
	if path == "" {
		return nil
	}
	return rotator.New(path, rotator.DefaultMaxSize, rotator.DefaultMaxBackups)
}