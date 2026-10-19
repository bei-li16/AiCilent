package stats

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

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
	c.RecordClientRequest()

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
