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
