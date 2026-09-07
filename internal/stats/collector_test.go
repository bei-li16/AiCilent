package stats

import (
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
