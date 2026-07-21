package server

import (
	"context"
	"embed"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
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
	Rot             *rotator.Rotator
	statsCollector  *stats.Collector
	cancelCtx       context.CancelFunc
	statsStop       chan struct{}
}

// New creates and configures the Gin engine with all routes and middleware.
func New(cfg *config.Config, configPath string) *Instance {
	gin.SetMode(gin.ReleaseMode)

	sseHub := sse.NewHub()
	statsCollector := stats.New(statsFilePath(configPath))

	// Two sinks:
	//   fileW      — file only (rotator), used for full request bodies
	//   combinedW  — file + live SSE console, used for every normal log line
	rot := logWriter(cfg.Global.LogFile)
	var fileW io.Writer
	if rot != nil {
		fileW = rot
	} else {
		fileW = os.Stdout
	}
	combinedW := io.MultiWriter(fileW, sseHub)
	gin.DefaultWriter = combinedW

	r := gin.New()

	r.Use(middleware.Logger())
	r.Use(middleware.DetectFormat())

	engine := router.NewEngine(cfg, configPath, combinedW, fileW, statsCollector)

	// Start config file watcher for hot-reload
	var cancel context.CancelFunc
	statsStop := make(chan struct{})
	if configPath != "" {
		ctx, c := context.WithCancel(context.Background())
		cancel = c
		engine.StartWatcher(ctx, 30*time.Second)
	}

	// Periodically persist stats (and once more on shutdown via SaveStats).
	statsCollector.StartAutoSave(statsStop, 30*time.Second)

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
		snap := statsCollector.Snapshot()
		snap.CB = engine.CBSnapshot()
		c.JSON(200, snap)
	})

	r.GET("/api/logs", func(c *gin.Context) {
		sseHub.ServeHTTP(c.Writer, c.Request)
	})

	r.POST("/api/control", func(c *gin.Context) {
		// Kill-switch protection: unless control_allow_remote is set, only
		// loopback callers may toggle the proxy. Prevents a remote visitor from
		// disabling the service.
		if !cfg.Global.ControlAllowRemote && !isLoopback(c.Request.RemoteAddr) {
			c.JSON(http.StatusForbidden, gin.H{"error": "control access denied: loopback only"})
			return
		}
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

	return &Instance{Engine: r, Rot: rot, statsCollector: statsCollector, cancelCtx: cancel, statsStop: statsStop}
}

// isLoopback reports whether addr (host:port form) is a loopback address.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// StopWatcher cancels the config watcher goroutine for graceful shutdown.
func (i *Instance) StopWatcher() {
	if i.cancelCtx != nil {
		i.cancelCtx()
	}
}

// SaveStats flushes the latest stats to disk and stops the auto-save goroutine.
// Call once during shutdown (after StopWatcher / before rotator close).
func (i *Instance) SaveStats() {
	if i.statsStop != nil {
		// Closing signals StartAutoSave to do a final Save and exit.
		select {
		case <-i.statsStop:
		default:
			close(i.statsStop)
		}
		// Give the final save a moment to land.
		time.Sleep(50 * time.Millisecond)
	}
	if i.statsCollector != nil {
		i.statsCollector.Save()
	}
}

// serveWeb serves an embedded asset.
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

func statsFilePath(configPath string) string {
	if configPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(configPath), ".stats.json")
}
