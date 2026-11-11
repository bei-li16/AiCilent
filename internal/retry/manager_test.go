package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWaitUsesConfiguredConstantInterval(t *testing.T) {
	m := NewManager(2, 1, 1)
	for attempt := 1; attempt <= 3; attempt++ {
		if !m.ShouldRetry() {
			t.Fatalf("attempt %d was not allowed", attempt)
		}
		if got := m.waitDuration(); got != time.Second {
			t.Fatalf("attempt %d wait = %v, want 1s", attempt, got)
		}
	}
}

func TestWaitUsesConfiguredExponentialBackoff(t *testing.T) {
	m := NewManager(2, 1, 2)
	wants := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	for i, want := range wants {
		if !m.ShouldRetry() {
			t.Fatalf("attempt %d was not allowed", i+1)
		}
		if got := m.waitDuration(); got != want {
			t.Fatalf("attempt %d wait = %v, want %v", i+1, got, want)
		}
	}
}

func TestShouldRetryUsesConfiguredRetryCount(t *testing.T) {
	m := NewManager(4, 1, 1)
	attempts := 0
	for m.ShouldRetry() {
		attempts++
	}
	if attempts != 5 {
		t.Fatalf("attempts = %d, want max_retries + 1 = 5", attempts)
	}
}

func TestWaitHonorsContextCancellation(t *testing.T) {
	m := NewManager(1, 10, 1)
	if !m.ShouldRetry() {
		t.Fatal("first attempt was not allowed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait error = %v, want context canceled", err)
	}
}

func TestWaitPacedSubtractsElapsedTime(t *testing.T) {
	m := NewManager(2, 1, 1)
	if !m.ShouldRetry() {
		t.Fatal("first attempt was not allowed")
	}
	start := time.Now()
	if err := m.WaitPaced(context.Background(), 600*time.Millisecond); err != nil {
		t.Fatalf("WaitPaced error = %v", err)
	}
	if got := time.Since(start); got < 300*time.Millisecond || got > 950*time.Millisecond {
		t.Fatalf("paced wait = %v, want ~400ms (1s interval minus 600ms elapsed)", got)
	}
}

func TestWaitPacedReturnsImmediatelyWhenElapsedCoversInterval(t *testing.T) {
	m := NewManager(2, 1, 1)
	if !m.ShouldRetry() {
		t.Fatal("first attempt was not allowed")
	}
	start := time.Now()
	if err := m.WaitPaced(context.Background(), 2*time.Second); err != nil {
		t.Fatalf("WaitPaced error = %v", err)
	}
	if d := time.Since(start); d > 250*time.Millisecond {
		t.Fatalf("WaitPaced with elapsed >= interval took %v, want immediate", d)
	}
}

func TestWaitPacedAppliesBackoffBeforeSubtracting(t *testing.T) {
	m := NewManager(2, 1, 2)
	for attempt := 1; attempt <= 2; attempt++ {
		if !m.ShouldRetry() {
			t.Fatalf("attempt %d was not allowed", attempt)
		}
	}
	// After attempt 2 the scheduled wait is interval * factor^1 = 2s;
	// 600ms of elapsed time leaves ~1.4s.
	start := time.Now()
	if err := m.WaitPaced(context.Background(), 600*time.Millisecond); err != nil {
		t.Fatalf("WaitPaced error = %v", err)
	}
	if got := time.Since(start); got < 1200*time.Millisecond || got > 1750*time.Millisecond {
		t.Fatalf("paced backoff wait = %v, want ~1.4s", got)
	}
}

func TestWaitPacedHonorsContextCancellation(t *testing.T) {
	m := NewManager(1, 10, 1)
	if !m.ShouldRetry() {
		t.Fatal("first attempt was not allowed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.WaitPaced(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitPaced error = %v, want context canceled", err)
	}
}
