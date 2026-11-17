package stats

import (
	"strings"
	"time"
)

// TokenUsage contains only upstream-reported counts and measured stream timings.
type TokenUsage struct {
	Input, Output, CachedInput, CacheEligible int64
	FirstToken, Decode                        time.Duration
	HasFirstToken                             bool
}

type UsageTotals struct {
	Requests      int64   `json:"requests"`
	Input         int64   `json:"input_tokens"`
	Output        int64   `json:"output_tokens"`
	CachedInput   int64   `json:"cached_input_tokens"`
	CacheEligible int64   `json:"cache_eligible_tokens"`
	FirstCount    int64   `json:"first_count,omitempty"`
	FirstMs       float64 `json:"first_ms,omitempty"`
	DecodeTokens  int64   `json:"decode_tokens,omitempty"`
	DecodeMs      float64 `json:"decode_ms,omitempty"`
}

func (t *UsageTotals) add(sample TokenUsage) {
	t.Requests++
	t.Input += sample.Input
	t.Output += sample.Output
	t.CachedInput += sample.CachedInput
	t.CacheEligible += sample.CacheEligible
	if sample.HasFirstToken {
		t.FirstCount++
		t.FirstMs += float64(sample.FirstToken) / float64(time.Millisecond)
	}
	if sample.Decode > 0 && sample.Output > 0 {
		t.DecodeTokens += sample.Output
		t.DecodeMs += float64(sample.Decode) / float64(time.Millisecond)
	}
}

type usageBuckets map[string]map[string]UsageTotals

func (b usageBuckets) add(bucket, model string, sample TokenUsage) {
	models := b[bucket]
	if models == nil {
		models = make(map[string]UsageTotals)
		b[bucket] = models
	}
	totals := models[model]
	totals.add(sample)
	models[model] = totals
}

type usageHistory struct {
	StartedAt time.Time    `json:"started_at"`
	Total     UsageTotals  `json:"total"`
	Days      usageBuckets `json:"days"`
	Hours     usageBuckets `json:"hours"`
}

const usageDateLayout = "2006-01-02"
const usageHourLayout = "2006-01-02T15"

func (c *Collector) RecordTokenUsage(epoch RequestEpoch, model string, at time.Time, sample TokenUsage) {
	if sample.Input+sample.Output <= 0 {
		return
	}
	at = at.In(time.Local)
	c.mu.Lock()
	defer c.mu.Unlock()
	if epoch.usage != c.usageEpoch {
		return
	}
	h := &c.usage
	if h.StartedAt.IsZero() {
		h.StartedAt = at
	}
	if h.Days == nil {
		h.Days = make(usageBuckets)
	}
	if h.Hours == nil {
		h.Hours = make(usageBuckets)
	}
	if model == "" {
		model = "未知模型"
	}
	h.Total.add(sample)
	h.Days.add(at.Format(usageDateLayout), model, sample)
	h.Hours.add(at.Format(usageHourLayout), model, sample)
	c.revision.Add(1)
}

// ResetTokenUsage starts a new accounting period and persists it immediately.
func (c *Collector) ResetTokenUsage() error {
	c.mu.Lock()
	c.usageEpoch++
	c.usage = usageHistory{}
	c.revision.Add(1)
	c.mu.Unlock()
	return c.save(time.Now())
}

type UsageSnapshot struct {
	Today     string       `json:"today"`
	Hour      int          `json:"hour"`
	Zone      string       `json:"timezone"`
	StartedAt *time.Time   `json:"started_at"`
	Total     UsageTotals  `json:"total"`
	Days      usageBuckets `json:"days"`
	Hours     usageBuckets `json:"hours"`
}

func (c *Collector) TokenUsageSnapshot(now time.Time) UsageSnapshot {
	now = now.In(time.Local)
	c.mu.Lock()
	defer c.mu.Unlock()
	h := &c.usage
	today := now.Format(usageDateLayout)
	s := UsageSnapshot{Today: today, Hour: now.Hour(), Zone: now.Format("UTC-07:00"),
		Total: h.Total, Days: make(usageBuckets), Hours: make(usageBuckets)}
	if !h.StartedAt.IsZero() {
		start := h.StartedAt
		s.StartedAt = &start
	}
	oldestDay := now.AddDate(0, 0, -364).Format(usageDateLayout)
	for day, models := range h.Days {
		if day >= oldestDay && day <= today {
			s.Days[day] = cloneUsageModels(models)
		}
	}
	hourPrefix := today + "T"
	for hour, models := range h.Hours {
		if strings.HasPrefix(hour, hourPrefix) {
			s.Hours[hour] = cloneUsageModels(models)
		}
	}
	return s
}

func cloneUsageModels(models map[string]UsageTotals) map[string]UsageTotals {
	copy := make(map[string]UsageTotals, len(models))
	for model, value := range models {
		copy[model] = value
	}
	return copy
}

func (h *usageHistory) snapshotForSave() *usageHistory {
	if h.StartedAt.IsZero() {
		return nil
	}
	snapshot := *h
	snapshot.Days = make(usageBuckets, len(h.Days))
	snapshot.Hours = make(usageBuckets, len(h.Hours))
	for day, models := range h.Days {
		snapshot.Days[day] = cloneUsageModels(models)
	}
	for hour, models := range h.Hours {
		snapshot.Hours[hour] = cloneUsageModels(models)
	}
	return &snapshot
}

func (c *Collector) pruneUsageHoursLocked(today string) {
	hours := c.usage.Hours
	before := len(hours)
	firstHour := today + "T00"
	for hour := range hours {
		if hour < firstHour {
			delete(hours, hour)
		}
	}
	if len(hours) != before {
		c.revision.Add(1)
	}
}
