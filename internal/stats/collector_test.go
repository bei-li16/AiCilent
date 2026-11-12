package stats

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestClientOutcomesPersistWithoutGuessingLegacyResults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.json")
	if err := os.WriteFile(path, []byte(`{"total_client_req":149,"total_success":132}`), 0600); err != nil {
		t.Fatal(err)
	}
	c := New(path)
	if got := c.Snapshot(); got.TotalClientReq != 149 || got.TotalClientCompleted != 0 || got.TotalClientSuccess != 0 {
		t.Fatalf("legacy attempts must not be treated as client outcomes: %+v", got)
	}
	c.RecordClientOutcome(c.RecordClientRequest("success"), true)
	c.RecordClientOutcome(c.RecordClientRequest("failure"), false)
	c.Save()
	c = New(path)
	if got := c.Snapshot(); got.TotalClientReq != 151 || got.TotalClientCompleted != 2 || got.TotalClientSuccess != 1 {
		t.Fatalf("request outcomes did not survive restart: %+v", got)
	}
	epoch := c.RecordClientRequest("in-flight")
	c.Reset()
	c.RecordClientOutcome(epoch, true)
	if got := c.Snapshot(); got.TotalClientReq != 0 || got.TotalClientCompleted != 0 || got.TotalClientSuccess != 0 {
		t.Fatalf("request from before reset counted in the new period: %+v", got)
	}
}

func TestSnapshotIncludesRecentAttemptsByRequestID(t *testing.T) {
	c := New("")
	c.Record("req-1", "provider", "model", 1, true, 200, "", 25*time.Millisecond)
	c.Record("req-2", "provider", "model", 1, false, 503, "upstream", 30*time.Millisecond)

	snap := c.Snapshot()
	if len(snap.RecentAttempts) != 2 {
		t.Fatalf("recent attempts = %d, want 2", len(snap.RecentAttempts))
	}
	if snap.RecentAttempts[1].RequestID != "req-2" || snap.RecentAttempts[1].StatusCode != 503 {
		t.Fatalf("unexpected attempt: %#v", snap.RecentAttempts[1])
	}
}

func TestFailureStreakSurvivesReloadAndSuccessClearsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.json")
	c := New(path)
	c.Record("r1", "p", "m", 1, false, 429, "rate limit", time.Millisecond)
	c.Record("r2", "p", "m", 1, false, 503, "unavailable", time.Millisecond)
	c.RecordCanceled("r3", "p", "m", 1, "client left", time.Millisecond)
	c.Save()
	c = New(path)
	p := c.Snapshot().Providers[0]
	if p.ConsecutiveFail != 2 || p.LastActivity.IsZero() || p.LastErrType != "client_abort" || p.LastErr != "client left" {
		t.Fatalf("reload lost recent state: %+v", p)
	}
	c.Record("r4", "p", "m", 1, true, 200, "", time.Millisecond)
	c.Save()
	c = New(path)
	p = c.Snapshot().Providers[0]
	if p.ConsecutiveFail != 0 || p.LastErrType != "" || p.LastErr != "" || p.LastActivity.IsZero() {
		t.Fatalf("success did not reset persisted streak: %+v", p)
	}
	if p.Total != 4 || p.Success != 1 || p.Fail != 2 || p.ClientAbort != 1 {
		t.Fatalf("persisting metadata changed counters: %+v", p)
	}
}

func TestCanceledPersistenceResetAndOutcome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.json")
	c := New(path)
	c.RecordCanceled("canceled", "p", "m", 1, "client disconnected", time.Millisecond)
	c.Save()
	c = New(path)
	s := c.Snapshot()
	if s.TotalReq != 1 || s.TotalCanceled != 1 || s.TotalFail != 0 || s.Providers[0].ClientAbort != 1 {
		t.Fatalf("persisted=%+v", s)
	}
	c.Reset()
	c.Record("success", "p", "m", 1, true, 200, "", time.Millisecond)
	c.RecordCanceled("canceled", "p", "m", 1, "client disconnected", time.Millisecond)
	s = c.Snapshot()
	a := s.RecentAttempts[1]
	if s.TotalReq != 2 || s.TotalCanceled != 1 || s.TotalRate != 100 || s.Providers[0].Rate != 100 || a.Outcome != "canceled" || a.StatusCode != 0 {
		t.Fatalf("reset/outcome=%+v attempt=%+v", s, a)
	}
}

func TestConcurrentSnapshotsKeepAttemptCountersConsistent(t *testing.T) {
	c := New("")
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				c.RecordCanceled("r", "p", "m", 1, "cancel", time.Millisecond)
			}
		}()
	}
	for i := 0; i < 100; i++ {
		s := c.Snapshot()
		if s.TotalReq != s.TotalSuccess+s.TotalFail+s.TotalCanceled {
			t.Fatalf("inconsistent counters: %+v", s)
		}
	}
	wg.Wait()
}

func TestUpstream499IsNotClientCancellation(t *testing.T) {
	c := New("")
	c.Record("r", "p", "m", 1, false, 499, "upstream HTTP 499", time.Millisecond)
	s := c.Snapshot()
	if s.TotalFail != 1 || s.TotalCanceled != 0 || s.RecentAttempts[0].Outcome != "failed" {
		t.Fatalf("HTTP 499 misclassified: %+v", s)
	}
}

func TestResetZeroesCountersButKeepsProviders(t *testing.T) {
	c := New("")
	c.Record("req-1", "provider", "model", 1, true, 200, "", 25*time.Millisecond)
	c.Record("req-2", "provider", "model", 1, false, 503, "upstream", 30*time.Millisecond)
	c.RecordClientRequest("req-3")

	c.Reset()

	snap := c.Snapshot()
	if snap.TotalReq != 0 || snap.TotalClientReq != 0 || snap.TotalSuccess != 0 || snap.TotalFail != 0 {
		t.Fatalf("totals not reset: %+v", snap)
	}
	if len(snap.Providers) != 1 {
		t.Fatalf("providers = %d, want 1 (entry kept)", len(snap.Providers))
	}
	p := snap.Providers[0]
	if p.Total != 0 || p.Success != 0 || p.Fail != 0 || p.ConsecutiveFail != 0 || p.LastErrType != "" || p.LatencyAvgMs != 0 {
		t.Fatalf("provider counters not reset: %+v", p)
	}
	if len(snap.RecentAttempts) != 0 {
		t.Fatalf("recent attempts = %d, want 0", len(snap.RecentAttempts))
	}
	for _, curve := range snap.Curves {
		if len(curve.Points) != 0 {
			t.Fatalf("curve %d not cleared: %d points", curve.Priority, len(curve.Points))
		}
	}
	if len(snap.LatencyCurve) != 0 {
		t.Fatalf("latency curve = %d, want 0", len(snap.LatencyCurve))
	}
	if snap.TrendRequestSeq != 0 || len(snap.ModelLatencyCurves) != 0 {
		t.Fatalf("reset kept trend request sequence or per-model data: %+v", snap)
	}
}

func TestTrendsAssociateAttemptsWithClientRequestsAndSuccessfulModels(t *testing.T) {
	c := New("")
	for i := 1; i <= 105; i++ {
		c.RecordClientRequest(fmt.Sprintf("r%d", i))
	}
	c.Record("r105", "p", "model-a", 1, false, 429, "limit", time.Second)
	c.Record("r105", "p", "model-a", 1, true, 200, "", 2*time.Second)
	c.Record("r6", "q", "model-b", 2, true, 200, "", 3*time.Second)
	c.Record("r5", "p", "model-a", 1, true, 200, "", 4*time.Second)
	c.RecordCanceled("r104", "p", "model-a", 1, "client left", time.Second)

	snapshot := c.Snapshot()
	if snapshot.TrendRequestSeq != 105 || !slices.Equal(snapshot.Curves[0].Requests, []uint64{105, 105, 6, 0}) {
		t.Fatalf("rate samples must use inbound request order for retries and late results: seq=%d, samples=%+v",
			snapshot.TrendRequestSeq, snapshot.Curves[0])
	}
	if !slices.Equal(snapshot.Curves[0].Successes, []bool{false, true, true, true}) {
		t.Fatalf("rate outcomes were not retained: %+v", snapshot.Curves[0])
	}
	if !slices.Equal(snapshot.LatencyCurves[0].Requests, []uint64{105, 6, 0}) {
		t.Fatalf("latency curve must contain successful samples only: %+v", snapshot.LatencyCurves[0])
	}
	models := snapshot.ModelLatencyCurves
	if len(models) != 2 || models[0].Model != "model-a" || !slices.Equal(models[0].Points, []float64{2, 4}) ||
		!slices.Equal(models[0].Requests, []uint64{105, 0}) || models[1].Model != "model-b" ||
		!slices.Equal(models[1].Points, []float64{3}) || !slices.Equal(models[1].Requests, []uint64{6}) {
		t.Fatalf("model samples must preserve successful durations and request order: %+v", models)
	}
}

func TestProviderProjectionUsesCurrentModelAndPriority(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.json")
	c := New(path)
	c.Record("old", "account", "old-model", 6, false, 429, "limited", time.Second)
	c.Record("removed", "removed-account", "other-model", 5, true, 200, "", time.Second)
	c.Save()

	c = New(path)
	configured := []ProviderIdentity{{Name: "account", ModelID: "new-model", Priority: 2}}
	snapshot := func() Snapshot {
		s := c.Snapshot()
		s.UseConfiguredProviders(configured)
		return s
	}
	before := snapshot()
	if len(before.Providers) != 1 || before.Providers[0].identity() != configured[0] ||
		before.Providers[0].Total != 0 || before.TotalReq != 2 {
		t.Fatalf("configured account should start empty, preserving global totals: %+v", before)
	}
	c.Record("new", "account", "new-model", 2, true, 200, "", 2*time.Second)
	c.Record("late", "account", "old-model", 6, false, 429, "limited", time.Second)
	if row := snapshot().Providers[0]; row.Total != 1 || row.Success != 1 || row.LatencyAvgMs != 2000 {
		t.Fatalf("historical model leaked into the active account: %+v", row)
	}
	c.Reset()
	after := snapshot()
	if len(after.Providers) != 1 || after.Providers[0].identity() != configured[0] || after.Providers[0].Total != 0 {
		t.Fatalf("reset should retain only the configured account in the dashboard: %+v", after.Providers)
	}
}

// A client abort is a request attempt but not a provider failure: it must
// stay out of the fail counter, the consecutive-fail streak and the rate
// denominators, otherwise finished requests look like a degraded backend.
func TestClientAbortExcludedFromRatesAndFailures(t *testing.T) {
	c := New("")
	c.Record("req-1", "provider", "model", 1, true, 200, "", 25*time.Millisecond)
	c.RecordCanceled("req-2", "provider", "model", 1, "client disconnected", 30*time.Millisecond)

	snap := c.Snapshot()
	if snap.TotalReq != 2 || snap.TotalCanceled != 1 {
		t.Fatalf("total attempts = %d, want 2 (abort included)", snap.TotalReq)
	}
	if snap.TotalFail != 0 {
		t.Fatalf("total failures = %d, want 0", snap.TotalFail)
	}
	if snap.TotalRate != 100 {
		t.Fatalf("total rate = %v, want 100", snap.TotalRate)
	}

	p := snap.Providers[0]
	if p.ClientAbort != 1 || p.Fail != 0 || p.ConsecutiveFail != 0 {
		t.Fatalf("provider counters wrong: %+v", p)
	}
	if p.Rate != 100 {
		t.Fatalf("provider rate = %v, want 100", p.Rate)
	}
	if p.LastErrType != "client_abort" {
		t.Fatalf("last error type = %q, want client_abort", p.LastErrType)
	}
	if got := ClassifyError(StatusClientAbort, ""); got != "http_499" {
		t.Fatalf("ClassifyError(499) = %q, want http_499", got)
	}
}

func TestLatencyCurvesSeparatePrioritiesAndExcludeFailedOrCanceled(t *testing.T) {
	c := New("")
	c.Record("a", "p1", "m", 1, true, 200, "", 2*time.Second)
	c.Record("b", "p2", "m", 2, false, 429, "rate limit", time.Second)
	c.RecordCanceled("c", "p1", "m", 1, "canceled", time.Second)
	c.Record("d", "p2", "m", 2, true, 200, "", 4*time.Second)
	curves := c.Snapshot().LatencyCurves
	if len(curves) != 3 || len(curves[0].Points) != 2 || curves[1].Priority != 1 || len(curves[1].Points) != 1 || curves[1].Points[0] != 2 || curves[2].Points[0] != 4 {
		t.Fatalf("incorrect successful-latency series: %+v", curves)
	}
	curves[1].Points[0] = 999
	if c.Snapshot().LatencyCurves[1].Points[0] != 2 {
		t.Fatal("snapshot shares mutable storage")
	}
	for i := 0; i < 250; i++ {
		c.Record("bounded", "p1", "m", 1, true, 200, "", time.Second)
	}
	if len(c.Snapshot().LatencyCurves[1].Points) != 240 {
		t.Fatal("per-priority latency curve must be bounded")
	}
	c.Reset()
	if curves := c.Snapshot().LatencyCurves; len(curves) != 1 || len(curves[0].Points) != 0 {
		t.Fatalf("latency curves survived reset: %+v", curves)
	}
}
