package server

import (
	"io"
	"os"

	"ai-proxy/internal/config"
	"ai-proxy/internal/middleware"
	"ai-proxy/internal/router"

	"github.com/gin-gonic/gin"
)

func New(cfg *config.Config) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	gin.DefaultWriter = logWriter(cfg.Global.LogFile)

	r := gin.New()

	r.Use(middleware.Logger())
	r.Use(middleware.DetectFormat())

	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	engine := router.NewEngine(cfg, gin.DefaultWriter)

	r.POST("/v1/chat/completions", engine.HandleRequest)
	r.POST("/chat/completions", engine.HandleRequest)
	r.POST("/v1/messages", engine.HandleRequest)

	return r
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