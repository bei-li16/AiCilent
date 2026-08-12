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
