package server

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"ai-proxy/internal/router"
	"ai-proxy/internal/stats"
	"github.com/gin-gonic/gin"
)

// Only serialized immutable bytes are shared. Mutations invalidate immediately;
// the second key keeps uptime and CB countdown current.
func serveStats(collector *stats.Collector, engine *router.Engine) gin.HandlerFunc {
	var mu sync.Mutex
	var payload []byte
	var lastRevision uint64
	var lastSecond int64
	var lastDimensions string
	return func(c *gin.Context) {
		configured, cb := engine.ConfiguredProviders(), engine.CBSnapshot()
		dimensions, _ := json.Marshal([]interface{}{configured, cb})
		revision, second := collector.Revision(), time.Now().Unix()
		mu.Lock()
		if payload == nil || revision != lastRevision || second != lastSecond || string(dimensions) != lastDimensions {
			snap := collector.Snapshot()
			snap.UseConfiguredProviders(configured)
			snap.CB = cb
			snap.ConfiguredPriorities = make([]int, 0, len(cb))
			for _, group := range cb {
				snap.ConfiguredPriorities = append(snap.ConfiguredPriorities, group.Priority)
			}
			payload, _ = json.Marshal(snap)
			lastRevision, lastSecond, lastDimensions = revision, second, string(dimensions)
		}
		data := payload
		mu.Unlock()
		c.Header("Cache-Control", "no-store")
		c.Data(http.StatusOK, "application/json; charset=utf-8", data)
	}
}
