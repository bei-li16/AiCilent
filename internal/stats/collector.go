package stats

import (
	"sync"
	"sync/atomic"
	"time"
)

type ProviderStats struct {
	Name     string  `json:"name"`
	ModelID  string  `json:"model_id"`
	Priority int     `json:"priority"`
	Total    int64   `json:"total"`
	Success  int64   `json:"success"`
	Fail     int64   `json:"fail"`
	Rate     float64 `json:"rate"`
}

type Snapshot struct {
	Providers    []ProviderStats `json:"providers"`
	TotalReq     int64           `json:"total_req"`
	TotalSuccess int64           `json:"total_success"`
	TotalFail    int64           `json:"total_fail"`
	TotalRate    float64         `json:"total_rate"`
	Running      bool            `json:"running"`
	StartTime    time.Time       `json:"start_time"`
	Uptime       string          `json:"uptime"`
}

type Collector struct {
	mu           sync.Mutex
	providers    map[string]*ProviderStats
	totalReq     atomic.Int64
	totalSuccess atomic.Int64
	totalFail    atomic.Int64
	startTime    time.Time
	running      atomic.Bool
}

func New() *Collector {
	c := &Collector{
		providers: make(map[string]*ProviderStats),
		startTime: time.Now(),
	}
	c.running.Store(true)
	return c
}

func (c *Collector) Record(name string, modelID string, priority int, success bool) {
	c.mu.Lock()
	ps, ok := c.providers[name]
	if !ok {
		ps = &ProviderStats{Name: name, ModelID: modelID, Priority: priority}
		c.providers[name] = ps
	}
	ps.Total++
	if success {
		ps.Success++
	} else {
		ps.Fail++
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
}

func (c *Collector) Snapshot() Snapshot {
	c.mu.Lock()
	providers := make([]ProviderStats, 0, len(c.providers))
	for _, ps := range c.providers {
		providers = append(providers, *ps)
	}
	c.mu.Unlock()

	totalReq := c.totalReq.Load()
	totalSuccess := c.totalSuccess.Load()
	totalFail := c.totalFail.Load()
	totalRate := 0.0
	if totalReq > 0 {
		totalRate = float64(totalSuccess) / float64(totalReq) * 100
	}
	running := c.running.Load()
	uptime := time.Since(c.startTime).Round(time.Second).String()

	return Snapshot{
		Providers:    providers,
		TotalReq:     totalReq,
		TotalSuccess: totalSuccess,
		TotalFail:    totalFail,
		TotalRate:    totalRate,
		Running:      running,
		StartTime:    c.startTime,
		Uptime:       uptime,
	}
}

func (c *Collector) SetRunning(v bool) {
	c.running.Store(v)
}

func (c *Collector) IsRunning() bool {
	return c.running.Load()
}