package stats

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTokenUsageRetainsDailyTotalsAndDropsExpiredHoursOnSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".stats.json")
	c := New(path)
	today := time.Date(2026, time.November, 17, 23, 45, 0, 0, time.Local)
	tomorrow := today.AddDate(0, 0, 1)
	olderDay := today.AddDate(0, 0, -400)
	epoch := c.RecordClientRequest("demo")
	c.RecordTokenUsage(epoch, "model", olderDay, TokenUsage{Input: 3, Output: 1})
	c.RecordTokenUsage(epoch, "model", today, TokenUsage{Input: 18, Output: 2})
	if err := c.save(today); err != nil {
		t.Fatal(err)
	}
	if got := New(path).TokenUsageSnapshot(today); got.Hours[today.Format(usageHourLayout)]["model"].Input != 18 || len(got.Hours) != 1 {
		t.Fatalf("expected today's hour to survive restart: %+v", got.Hours)
	}

	// Saving after midnight with no new requests still removes expired hours.
	if err := c.save(tomorrow); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved persistedState
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Usage == nil || len(saved.Usage.Hours) != 0 || saved.Usage.Days[olderDay.Format(usageDateLayout)]["model"].Input != 3 {
		t.Fatalf("expected daily history without expired hourly buckets: %+v", saved.Usage)
	}
	got := New(path).TokenUsageSnapshot(tomorrow)
	if got.Total.Input != 21 || got.Days[today.Format(usageDateLayout)]["model"].Input != 18 || len(got.Days) != 1 {
		t.Fatalf("daily history not restored: %+v", got)
	}
}

func TestTokenUsageResetIgnoresInflightUsageButCountsRequestOutcome(t *testing.T) {
	c := New("")
	beforeReset := c.RecordClientRequest("before-reset")
	if err := c.ResetTokenUsage(); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	c.RecordClientOutcome(beforeReset, true)
	c.RecordTokenUsage(beforeReset, "model", now, TokenUsage{Input: 9, Output: 1})
	if usage, requests := c.TokenUsageSnapshot(now), c.Snapshot(); usage.Total.Requests != 0 || requests.TotalClientSuccess != 1 {
		t.Fatalf("reset mixed counting periods: usage=%+v requests=%+v", usage.Total, requests)
	}
	afterReset := c.RecordClientRequest("after-reset")
	c.RecordTokenUsage(afterReset, "model", now, TokenUsage{Input: 3, Output: 1})
	if got := c.TokenUsageSnapshot(now); got.Total.Requests != 1 || got.Total.Input != 3 {
		t.Fatalf("new token usage was not counted: %+v", got)
	}
}
