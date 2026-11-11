package retry

import (
	"context"
	"math"
	"time"
)

type Manager struct {
	maxRetries    int
	retryInterval int
	backoffFactor float64
	attempt       int
}

func NewManager(maxRetries, retryInterval int, backoffFactor float64) *Manager {
	return &Manager{
		maxRetries:    maxRetries,
		retryInterval: retryInterval,
		backoffFactor: backoffFactor,
		attempt:       0,
	}
}

func (m *Manager) ShouldRetry() bool {
	m.attempt++
	return m.attempt == 1 || m.attempt <= m.maxRetries+1
}

func (m *Manager) Attempt() int {
	return m.attempt
}

func (m *Manager) waitDuration() time.Duration {
	return time.Duration(float64(m.retryInterval)*math.Pow(m.backoffFactor, float64(m.attempt-1))) * time.Second
}

func (m *Manager) Wait(ctx context.Context) error {
	wait := m.waitDuration()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WaitPaced sleeps until the retry interval measured from the failed
// attempt's start has elapsed. elapsed is how long that attempt took: a round
// trip that already consumed the interval fires the next attempt immediately,
// while one that returned early waits only the remaining fraction of it.
// Attempt starts therefore stay at least interval apart without stacking wait
// time on top of slow responses. The exponential backoff schedule of
// waitDuration still applies before the elapsed time is subtracted.
func (m *Manager) WaitPaced(ctx context.Context, elapsed time.Duration) error {
	wait := m.waitDuration() - elapsed
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
