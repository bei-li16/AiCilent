package server

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ai-proxy/internal/config"
	"ai-proxy/internal/middleware"
	"ai-proxy/internal/rotator"
	"ai-proxy/internal/router"
	"ai-proxy/internal/sse"
	"ai-proxy/internal/stats"
	"ai-proxy/internal/version"

	"github.com/gin-gonic/gin"
)

//go:embed web/*
var webFS embed.FS

var dashboardAssetHash = func() string {
	hash := sha256.New()
	for _, name := range []string{"index.html", "app.js", "dashboard-core.js", "style.css"} {
		data, err := webFS.ReadFile("web/" + name)
		if err != nil {
			panic(err)
		}
		_, _ = hash.Write(data)
	}
	return hex.EncodeToString(hash.Sum(nil))[:16]
}()

func dashboardBuildID() string {
	return url.QueryEscape(version.Version + "." + version.Commit + "." + version.BuildDate + "." + dashboardAssetHash)
}

type Instance struct {
	*gin.Engine
	Rot             *rotator.Rotator
	statsCollector  *stats.Collector
	cancelCtx       context.CancelFunc
	statsStop       chan struct{}
	statsDone       <-chan struct{}
	watcherDone     <-chan struct{}
	stopOnce        sync.Once
	hub             *sse.Hub
	requests        sync.WaitGroup
	lifecycleMu     sync.Mutex
	stopping        bool
	closeTransports func()
}

// New creates and configures the Gin engine with all routes and middleware.
func New(cfg *config.Config, configPath string) *Instance {
	gin.SetMode(gin.ReleaseMode)

	sseHub := sse.NewHub()
	statsCollector := stats.New(statsFilePath(configPath))

	// Two sinks:
	//   fileW      — file only (rotator), used for full request bodies
	//   combinedW  — file + live SSE console, used for every normal log line
	rot := logWriter(resolveLogPath(cfg.Global.LogFile, configPath))
	var fileW io.Writer
	if rot != nil {
		fileW = rot
	} else {
		fileW = os.Stdout
	}
	combinedW := io.MultiWriter(fileW, sseHub)

	r := gin.New()
	instance := &Instance{Engine: r, Rot: rot, statsCollector: statsCollector, statsStop: make(chan struct{}), hub: sseHub}
	r.Use(func(c *gin.Context) {
		instance.lifecycleMu.Lock()
		if instance.stopping {
			instance.lifecycleMu.Unlock()
			c.AbortWithStatus(http.StatusServiceUnavailable)
			return
		}
		instance.requests.Add(1)
		instance.lifecycleMu.Unlock()
		defer instance.requests.Done()
		c.Next()
	})

	r.Use(middleware.Logger(combinedW))
	r.Use(middleware.DetectFormat())

	engine := router.NewEngine(cfg, configPath, combinedW, fileW, statsCollector)
	instance.closeTransports = engine.CloseIdleConnections

	// Start config file watcher for hot-reload
	var cancel context.CancelFunc
	if configPath != "" {
		ctx, c := context.WithCancel(context.Background())
		cancel = c
		instance.watcherDone = engine.StartWatcher(ctx, 30*time.Second)
	}

	// Periodically persist stats (and once more on shutdown via SaveStats).
	instance.statsDone = statsCollector.StartAutoSave(instance.statsStop, 30*time.Second)

	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	timing := middleware.RequestTiming(combinedW, statsCollector.RecordRequestTiming)
	r.POST("/v1/chat/completions", timing, engine.HandleRequest)
	r.POST("/chat/completions", timing, engine.HandleRequest)
	r.POST("/v1/messages", timing, engine.HandleRequest)
	r.POST("/v1/responses", timing, engine.HandleRequest)
	// Some OpenAI-compatible clients (including Codex) append /responses
	// directly to the configured base URL instead of using the /v1 prefix.
	r.POST("/responses", timing, engine.HandleRequest)

	listModels := func(c *gin.Context) {
		type model struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			OwnedBy string `json:"owned_by"`
		}
		ids := engine.OpenAIModelIDs()
		models := make([]model, 0, len(ids))
		for _, id := range ids {
			models = append(models, model{ID: id, Object: "model", OwnedBy: "ai-proxy"})
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{"object": "list", "data": models})
	}
	r.GET("/v1/models", listModels)
	r.GET("/models", listModels)

	r.GET("/", serveWeb("web/index.html", "text/html; charset=utf-8"))
	r.GET("/style.css", serveWeb("web/style.css", "text/css; charset=utf-8"))
	r.GET("/app.js", serveWeb("web/app.js", "application/javascript; charset=utf-8"))
	r.GET("/dashboard-core.js", serveWeb("web/dashboard-core.js", "application/javascript; charset=utf-8"))
	r.GET("/api/version", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{
			"version":    version.Version,
			"commit":     version.Commit,
			"build_date": version.BuildDate,
		})
	})

	r.GET("/api/stats", serveStats(statsCollector, engine))

	r.GET("/api/logs", func(c *gin.Context) {
		sseHub.ServeHTTP(c.Writer, c.Request)
	})

	r.POST("/api/stats/reset", func(c *gin.Context) {
		// Same kill-switch protection as /api/control: stats reset mutates
		// shared state, so remote callers need control_allow_remote.
		if !cfg.Global.ControlAllowRemote && !isLoopback(c.Request.RemoteAddr) {
			c.JSON(http.StatusForbidden, gin.H{"error": "stats reset denied: loopback only"})
			return
		}
		statsCollector.Reset()
		c.JSON(200, gin.H{"reset": true})
	})

	r.POST("/api/logs/clear", func(c *gin.Context) {
		// Same loopback protection as /api/control: truncates the log file.
		if !cfg.Global.ControlAllowRemote && !isLoopback(c.Request.RemoteAddr) {
			c.JSON(http.StatusForbidden, gin.H{"error": "log clear denied: loopback only"})
			return
		}
		if rot == nil {
			sseHub.Clear()
			c.JSON(200, gin.H{"cleared": true, "note": "logging to stdout, no file to clear"})
			return
		}
		if err := rot.Clear(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		sseHub.Clear()
		c.JSON(200, gin.H{"cleared": true})
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

	instance.cancelCtx = cancel
	return instance
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
	if i.watcherDone != nil {
		<-i.watcherDone
	}
}

// SaveStats flushes the latest stats to disk and stops the auto-save goroutine.
// Call once during shutdown (after StopWatcher / before rotator close).
func (i *Instance) SaveStats() {
	i.stopOnce.Do(func() {
		if i.statsStop != nil {
			close(i.statsStop)
		}
	})
	if i.statsDone != nil {
		<-i.statsDone
	}
	if i.statsCollector != nil {
		i.statsCollector.Save()
	}
}

// End subscriptions before draining requests, then persist their final outcomes.
// A drain timeout forces connection closure but must never bypass persistence.
func (i *Instance) Shutdown(ctx context.Context, server *http.Server) error {
	i.lifecycleMu.Lock()
	i.stopping = true
	i.lifecycleMu.Unlock()
	i.StopWatcher()
	i.hub.Close()
	err := server.Shutdown(ctx)
	if err != nil {
		_ = server.Close()
	}
	i.requests.Wait()
	if i.closeTransports != nil {
		i.closeTransports()
	}
	i.SaveStats()
	if i.Rot != nil {
		_ = i.Rot.Close()
	}
	return err
}

// serveWeb serves an embedded asset.
func serveWeb(filePath, contentType string) gin.HandlerFunc {
	asset := dashboardBuildID()
	return func(c *gin.Context) {
		data, err := webFS.ReadFile(filePath)
		if err != nil {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		// HTML always revalidates. Asset URLs identify the embedded build, including
		// rebuilds from an uncommitted worktree with the same release version.
		c.Header("Cache-Control", "no-cache")
		if filePath == "web/index.html" {
			data = []byte(strings.NewReplacer(
				"__DASHBOARD_VERSION__", html.EscapeString(version.Version),
				"__DASHBOARD_ASSET__", asset,
			).Replace(string(data)))
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

func resolveLogPath(logPath, configPath string) string {
	if logPath == "" || filepath.IsAbs(logPath) || configPath == "" {
		return logPath
	}
	return filepath.Join(filepath.Dir(configPath), logPath)
}

func statsFilePath(configPath string) string {
	if configPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(configPath), ".stats.json")
}
