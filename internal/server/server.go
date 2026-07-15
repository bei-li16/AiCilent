package server

import (
	"embed"
	"io"
	"net/http"
	"os"

	"ai-proxy/internal/config"
	"ai-proxy/internal/middleware"
	"ai-proxy/internal/router"
	"ai-proxy/internal/sse"
	"ai-proxy/internal/stats"

	"github.com/gin-gonic/gin"
)

//go:embed web/*
var webFS embed.FS

func New(cfg *config.Config) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)

	sseHub := sse.NewHub()
	statsCollector := stats.New()

	logW := logWriter(cfg.Global.LogFile)
	gin.DefaultWriter = io.MultiWriter(logW, sseHub)

	r := gin.New()

	r.Use(middleware.Logger())
	r.Use(middleware.DetectFormat())

	engine := router.NewEngine(cfg, gin.DefaultWriter, statsCollector)

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

	return r
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

func logWriter(path string) io.Writer {
	if path == "" {
		return os.Stdout
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return os.Stdout
	}
	return io.MultiWriter(os.Stdout, f)
}