package stats

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func cloneSeries[K comparable](source map[K]trendSeries) map[K]trendSeries {
	copy := make(map[K]trendSeries, len(source))
	for k, series := range source {
		copy[k] = append(trendSeries(nil), series...)
	}
	return copy
}

func (c *Collector) Revision() uint64 { return c.revision.Load() }

type archivedProvider struct {
	Stats        ProviderStats
	LatencySumMs float64
	Epoch        string
}

func (c *Collector) loadArchived(id ProviderIdentity) *ProviderStats {
	if c.statsPath == "" {
		return nil
	}
	data, err := os.ReadFile(c.archivePath(id))
	if err != nil {
		return nil
	}
	var p archivedProvider
	if json.Unmarshal(data, &p) != nil || p.Stats.identity() != id || p.Epoch != c.archiveEpoch {
		return nil
	}
	p.Stats.LatencySumMs = p.LatencySumMs
	return &p.Stats
}

func (c *Collector) archivePath(id ProviderIdentity) string {
	key := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", id.Name, id.ModelID, id.Priority)))
	return filepath.Join(c.statsPath+".archive", fmt.Sprintf("%x.json", key))
}

// History stays on disk, while active provider identities and chart dimensions
// stay proportional to the configuration. In-flight old providers are archived
// by the next Save; cumulative global counters are never subtracted.
func (c *Collector) ReconcileProviders(ids []ProviderIdentity) {
	c.fileMu.Lock()
	defer c.fileMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.configured = make(map[ProviderIdentity]bool, len(ids))
	models, priorities := make(map[string]bool), make(map[int]bool)
	for _, id := range ids {
		c.configured[id] = true
		models[id.ModelID], priorities[id.Priority] = true, true
		if c.providers[id] == nil && c.statsPath != "" {
			if p := c.loadArchived(id); p != nil {
				c.providers[id] = p
			}
		}
	}
	c.archiveInactiveLocked()
	for model := range c.modelLatency {
		if !models[model] {
			delete(c.modelLatency, model)
		}
	}
	for priority := range c.prioSeries {
		if !priorities[priority] {
			delete(c.prioSeries, priority)
			delete(c.prioLatency, priority)
			delete(c.prioWin, priority)
		}
	}
	c.revision.Add(1)
}

// Both fileMu and mu are held; rare config changes must not race late outcomes.
func (c *Collector) archiveInactiveLocked() {
	if c.configured != nil {
		models, priorities := make(map[string]bool), make(map[int]bool)
		for id := range c.configured {
			models[id.ModelID], priorities[id.Priority] = true, true
		}
		for model := range c.modelLatency {
			if !models[model] {
				delete(c.modelLatency, model)
			}
		}
		for p := range c.prioSeries {
			if !priorities[p] {
				delete(c.prioSeries, p)
				delete(c.prioLatency, p)
				delete(c.prioWin, p)
			}
		}
	}
	if c.statsPath == "" || c.configured == nil {
		return
	}
	for id, p := range c.providers {
		if c.configured[id] {
			continue
		}
		path := c.archivePath(id)
		data, err := json.Marshal(archivedProvider{*p, p.LatencySumMs, c.archiveEpoch})
		if err != nil || os.MkdirAll(filepath.Dir(path), 0755) != nil {
			continue
		}
		if os.WriteFile(path+".tmp", data, 0644) == nil && os.Rename(path+".tmp", path) == nil {
			delete(c.providers, id)
		}
	}
}

func (c *Collector) clearArchives() {
	if c.statsPath == "" {
		return
	}
	dir := c.statsPath + ".archive"
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if !entry.IsDir() && len(entry.Name()) == 69 && filepath.Ext(entry.Name()) == ".json" {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}
