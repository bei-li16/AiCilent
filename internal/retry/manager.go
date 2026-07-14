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

func (m *Manager) Wait(ctx context.Context) error {
	wait := time.Duration(float64(m.retryInterval)*math.Pow(m.backoffFactor, float64(m.attempt-1))) * time.Second
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}