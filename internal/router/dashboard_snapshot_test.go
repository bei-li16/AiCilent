package router

import (
	"io"
	"reflect"
	"testing"
	"time"

	"ai-proxy/internal/config"
)

func TestDashboardCircuitGroupsFollowCurrentConfig(t *testing.T) {
	cfg := &config.Config{Providers: []config.Provider{{Priority: 3}, {Priority: 1}, {Priority: 2}, {Priority: 1}}}
	e := NewEngine(cfg, "", io.Discard, io.Discard, nil)
	e.failureCount[1] = 2
	e.failureCount[5] = 10
	now := time.Now()
	e.circuitOpenSince[4] = &now
	snap := e.CBSnapshot()
	var got []int
	for _, group := range snap {
		got = append(got, group.Priority)
	}
	if !reflect.DeepEqual(got, []int{1, 2, 3}) || snap[0].Failures != 2 || snap[1].Open {
		t.Fatalf("configured groups missing or stale groups leaked: %+v", snap)
	}
	// A config reload must immediately affect the dashboard, including a new
	// priority without any requests. No hardcoded P1–P3 range is involved.
	e.reloadMu.Lock()
	e.cfg = &config.Config{Providers: []config.Provider{{Priority: 7}}}
	e.reloadMu.Unlock()
	if next := e.CBSnapshot(); len(next) != 1 || next[0].Priority != 7 {
		t.Fatalf("dashboard retained old config: %+v", next)
	}
}
