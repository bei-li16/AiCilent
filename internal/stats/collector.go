package stats

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type ProviderStats struct {
	Name            string  `json:"name"`
	ModelID         string  `json:"model_id"`
	Priority        int     `json:"priority"`
	Total           int64   `json:"total"`
	Success         int64   `json:"success"`
	Fail            int64   `json:"fail"`
	Rate            float64 `json:"rate"`
	ConsecutiveFail int64   `json:"consecutive_fail"` // 当前连续失败数，成功后归零
	LastErrType     string  `json:"last_err_type"`   // 最近错误类型，如 tpm_limit/rpm_exhausted/not_found/upstream_error
	LastErr         string  `json:"last_err"`         // 最近错误简述（截断）
	LatencySumMs    float64 `json:"-"`                // 累计延迟(ms)，用于算平均
	LatencyCount    int64   `json:"latency_count"`
	LatencyAvgMs    float64 `json:"latency_avg_ms"` // 平均延迟(ms)
	LastLatencyMs   float64 `json:"last_latency_ms"`
}

// CBState is a read-only snapshot of one priority's circuit-breaker state.
type CBState struct {
	Priority       int   `json:"priority"`
	Open           bool  `json:"open"`
	Failures       int   `json:"failures"`
	CooldownSec    int   `json:"cooldown_sec"`     // 配置的冷却秒数
	CooldownRemSec int   `json:"cooldown_rem_sec"` // 剩余冷却秒数（未熔断或已过冷却为 0）
	SkipRemaining  int   `json:"skip_remaining"`
}

// HitRateCurve is one line of the hit-rate chart. Priority 0 = overall;
// otherwise it is the per-priority rolling success rate.
type HitRateCurve struct {
	Priority int       `json:"priority"` // 0 = 全部请求
	Points   []float64 `json:"points"`   // 每个=最近 hitWindowSize 次尝试的成功率(%)
}

const (
	hitWindowSize = 50  // 滚动窗口大小：每个点取最近 50 次尝试
	hitSeriesCap  = 240 // 曲线保留的最近点数
)

// rollingHit is a fixed-size ring of the last N attempt outcomes (success
// booleans) with an O(1) running success count.
type rollingHit struct {
	buf    []bool
	pos    int
	full   bool
	sum    int // buf 中 true 的个数
}

func (r *rollingHit) push(success bool) {
	if success {
		r.sum++
	}
	if !r.full && len(r.buf) < hitWindowSize {
		r.buf = append(r.buf, success)
		r.pos = len(r.buf) % hitWindowSize
		if len(r.buf) == hitWindowSize {
			r.full = true
			r.pos = 0
		}
		return
	}
	// Overwrite oldest.
	if r.buf[r.pos] {
		r.sum-- // remove the outgoing true
	}
	r.buf[r.pos] = success
	r.pos = (r.pos + 1) % hitWindowSize
}

func (r *rollingHit) rate() float64 {
	n := len(r.buf)
	if n == 0 {
		return 0
	}
	return float64(r.sum) / float64(n) * 100
}

// pushSeries appends v to a capped series, shifting left when full so the
// underlying array stays bounded (no unbounded slice growth).
func pushSeries(s []float64, v float64) []float64 {
	if len(s) < hitSeriesCap {
		return append(s, v)
	}
	copy(s, s[1:])
	s[len(s)-1] = v
	return s
}

type Snapshot struct {
	Providers       []ProviderStats `json:"providers"`
	TotalReq        int64           `json:"total_req"`         // 上游尝试次数（含重试/降级）
	TotalClientReq  int64           `json:"total_client_req"`  // 客户端请求数
	TotalSuccess    int64           `json:"total_success"`
	TotalFail       int64           `json:"total_fail"`
	TotalRate       float64         `json:"total_rate"`
	Running         bool            `json:"running"`
	StartTime       time.Time       `json:"start_time"`
	Uptime          string          `json:"uptime"`
	CB              []CBState       `json:"cb"`
	Curves          []HitRateCurve  `json:"curves"`
}

type Collector struct {
	mu            sync.Mutex
	providers     map[string]*ProviderStats
	totalReq      atomic.Int64 // 上游尝试次数
	totalClient   atomic.Int64 // 客户端请求数
	totalSuccess  atomic.Int64
	totalFail     atomic.Int64
	startTime     time.Time
	running       atomic.Bool
	statsPath     string
	fileMu        sync.Mutex
	// Hit-rate rolling windows (last hitWindowSize outcomes) + time series of
	// rolling success rates. priority 0 = overall.
	overallWin    rollingHit
	prioWin       map[int]*rollingHit
	overallSeries []float64
	prioSeries    map[int][]float64
}

func New(statsPath string) *Collector {
	c := &Collector{
		providers:  make(map[string]*ProviderStats),
		startTime:  time.Now(),
		statsPath:  statsPath,
		prioWin:    make(map[int]*rollingHit),
		prioSeries: make(map[int][]float64),
	}
	c.running.Store(true)
	c.load()
	return c
}

// ClassifyError maps an upstream status code + message to a short error type.
func ClassifyError(statusCode int, msg string) string {
	switch {
	case statusCode == 429:
		switch {
		case strings.Contains(msg, "rpm exhausted"):
			return "rpm_exhausted"
		case strings.Contains(msg, "TPM limit"), strings.Contains(msg, "tpm limit"):
			return "tpm_limit"
		case strings.Contains(msg, "token plan limit"):
			return "plan_exhausted"
		}
		return "rate_limited"
	case statusCode == 404:
		return "not_found"
	case statusCode == 401 || statusCode == 403:
		return "auth_error"
	case statusCode == 408 || statusCode == 504:
		return "timeout"
	case statusCode >= 500:
		return "upstream_error"
	case statusCode == 0:
		return "network_error"
	}
	return "http_" + strconv.Itoa(statusCode)
}

// Record records one upstream attempt outcome (success or failure).
// statusCode is the upstream HTTP status (0 for network errors); errMsg is the
// raw error message (used to classify the failure type); latency is the attempt duration.
func (c *Collector) Record(name string, modelID string, priority int, success bool, statusCode int, errMsg string, latency time.Duration) {
	c.mu.Lock()
	ps, ok := c.providers[name]
	if !ok {
		ps = &ProviderStats{Name: name, ModelID: modelID, Priority: priority}
		c.providers[name] = ps
	}
	ps.Total++
	latMs := float64(latency.Microseconds()) / 1000.0
	ps.LastLatencyMs = latMs
	ps.LatencySumMs += latMs
	ps.LatencyCount++
	if ps.Total > 0 {
		ps.LatencyAvgMs = ps.LatencySumMs / float64(ps.LatencyCount)
	}
	if success {
		ps.Success++
		ps.ConsecutiveFail = 0
		ps.LastErrType = ""
		ps.LastErr = ""
	} else {
		ps.Fail++
		ps.ConsecutiveFail++
		ps.LastErrType = ClassifyError(statusCode, errMsg)
		// Truncate to keep the stored message short.
		if len(errMsg) > 120 {
			errMsg = errMsg[:120] + "..."
		}
		ps.LastErr = errMsg
	}
	if ps.Total > 0 {
		ps.Rate = float64(ps.Success) / float64(ps.Total) * 100
	}
	c.mu.Unlock()

	c.totalReq.Add(1)
	if success {
		c.totalSuccess.Add(1)
	} else {
		c.totalFail.Add(1)
	}

	// Update hit-rate rolling windows + series (re-lock for the map/slice work).
	c.mu.Lock()
	c.overallWin.push(success)
	c.overallSeries = pushSeries(c.overallSeries, c.overallWin.rate())
	pw, ok := c.prioWin[priority]
	if !ok {
		pw = &rollingHit{}
		c.prioWin[priority] = pw
	}
	pw.push(success)
	c.prioSeries[priority] = pushSeries(c.prioSeries[priority], pw.rate())
	c.mu.Unlock()
}

// RecordClientRequest increments the client-side request counter once per
// inbound client request (distinct from upstream attempts, which include
// retries and degradation).
func (c *Collector) RecordClientRequest() {
	c.totalClient.Add(1)
}

func (c *Collector) Snapshot() Snapshot {
	c.mu.Lock()
	providers := make([]ProviderStats, 0, len(c.providers))
	for _, ps := range c.providers {
		providers = append(providers, *ps)
	}
	c.mu.Unlock()

	totalReq := c.totalReq.Load()
	totalClient := c.totalClient.Load()
	totalSuccess := c.totalSuccess.Load()
	totalFail := c.totalFail.Load()
	totalRate := 0.0
	if totalReq > 0 {
		totalRate = float64(totalSuccess) / float64(totalReq) * 100
	}
	running := c.running.Load()
	uptime := time.Since(c.startTime).Round(time.Second).String()

	// Build hit-rate curves: overall first (priority 0), then per-priority sorted.
	curves := make([]HitRateCurve, 0, 1+len(c.prioSeries))
	curves = append(curves, HitRateCurve{Priority: 0, Points: copySeries(c.overallSeries)})
	prios := make([]int, 0, len(c.prioSeries))
	for p := range c.prioSeries {
		prios = append(prios, p)
	}
	for i := 1; i < len(prios); i++ {
		for j := i; j > 0 && prios[j-1] > prios[j]; j-- {
			prios[j-1], prios[j] = prios[j], prios[j-1]
		}
	}
	for _, p := range prios {
		curves = append(curves, HitRateCurve{Priority: p, Points: copySeries(c.prioSeries[p])})
	}

	return Snapshot{
		Providers:      providers,
		TotalReq:       totalReq,
		TotalClientReq: totalClient,
		TotalSuccess:   totalSuccess,
		TotalFail:      totalFail,
		TotalRate:      totalRate,
		Running:        running,
		StartTime:      c.startTime,
		Uptime:         uptime,
		Curves:         curves,
	}
}

func copySeries(s []float64) []float64 {
	if len(s) == 0 {
		return []float64{}
	}
	out := make([]float64, len(s))
	copy(out, s)
	return out
}

func (c *Collector) SetRunning(v bool) {
	c.running.Store(v)
}

func (c *Collector) IsRunning() bool {
	return c.running.Load()
}

// ─────────────────────────────────────────────────────────────
//  Persistence
// ─────────────────────────────────────────────────────────────

type persistedProvider struct {
	Name         string  `json:"name"`
	ModelID      string  `json:"model_id"`
	Priority     int     `json:"priority"`
	Total        int64   `json:"total"`
	Success      int64   `json:"success"`
	Fail         int64   `json:"fail"`
	LatencySumMs float64 `json:"latency_sum_ms"`
	LatencyCount int64   `json:"latency_count"`
}

type persistedState struct {
	Providers     []persistedProvider `json:"providers"`
	TotalReq      int64              `json:"total_req"`
	TotalClient   int64              `json:"total_client_req"`
	TotalSuccess  int64              `json:"total_success"`
	TotalFail     int64              `json:"total_fail"`
}

func (c *Collector) Save() {
	if c.statsPath == "" {
		return
	}
	c.mu.Lock()
	state := persistedState{
		TotalReq:     c.totalReq.Load(),
		TotalClient:  c.totalClient.Load(),
		TotalSuccess: c.totalSuccess.Load(),
		TotalFail:    c.totalFail.Load(),
		Providers:   make([]persistedProvider, 0, len(c.providers)),
	}
	for _, ps := range c.providers {
		state.Providers = append(state.Providers, persistedProvider{
			Name:         ps.Name,
			ModelID:      ps.ModelID,
			Priority:     ps.Priority,
			Total:        ps.Total,
			Success:      ps.Success,
			Fail:         ps.Fail,
			LatencySumMs: ps.LatencySumMs,
			LatencyCount: ps.LatencyCount,
		})
	}
	c.mu.Unlock()

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return
	}
	c.fileMu.Lock()
	defer c.fileMu.Unlock()
	tmpPath := c.statsPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return
	}
	os.Rename(tmpPath, c.statsPath)
}

func (c *Collector) load() {
	if c.statsPath == "" {
		return
	}
	data, err := os.ReadFile(c.statsPath)
	if err != nil {
		return // no state file yet
	}
	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		return
	}
	c.mu.Lock()
	for _, p := range state.Providers {
		ps := &ProviderStats{
			Name:         p.Name,
			ModelID:      p.ModelID,
			Priority:     p.Priority,
			Total:        p.Total,
			Success:      p.Success,
			Fail:         p.Fail,
			LatencySumMs: p.LatencySumMs,
			LatencyCount: p.LatencyCount,
		}
		if ps.Total > 0 {
			ps.Rate = float64(ps.Success) / float64(ps.Total) * 100
		}
		if ps.LatencyCount > 0 {
			ps.LatencyAvgMs = ps.LatencySumMs / float64(ps.LatencyCount)
		}
		c.providers[p.Name] = ps
	}
	c.mu.Unlock()
	c.totalReq.Store(state.TotalReq)
	c.totalClient.Store(state.TotalClient)
	c.totalSuccess.Store(state.TotalSuccess)
	c.totalFail.Store(state.TotalFail)
}

// StartAutoSave launches a goroutine that persists stats every interval until
// stop is closed. Call Save() once more on shutdown.
func (c *Collector) StartAutoSave(stop <-chan struct{}, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				c.Save()
			case <-stop:
				c.Save()
				return
			}
		}
	}()
}

// StatsPath returns the on-disk path (for the control API to surface if needed).
func (c *Collector) StatsPath() string { return filepath.ToSlash(c.statsPath) }
