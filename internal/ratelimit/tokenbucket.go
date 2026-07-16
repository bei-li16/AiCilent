package ratelimit

import (
	"sync"
	"time"
)

// TokenBucket implements a per-key token bucket rate limiter.
// Each key gets its own bucket that refills at a given rate.
type TokenBucket struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	rate     float64 // tokens per second
	burst    int     // max burst
}

type bucket struct {
	tokens    float64
	lastCheck time.Time
}

// New creates a token bucket limiter with the given rate (tokens/sec) and burst.
func New(rate float64, burst int) *TokenBucket {
	if rate <= 0 {
		rate = 100 // sensible default
	}
	if burst <= 0 {
		burst = int(rate) // default burst = 1 second worth of tokens
	}
	return &TokenBucket{
		buckets: make(map[string]*bucket),
		rate:    rate,
		burst:   burst,
	}
}

// Allow checks if a request for the given key is allowed.
// Returns true if allowed, false if rate limited.
func (tb *TokenBucket) Allow(key string) bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	b, ok := tb.buckets[key]
	if !ok {
		b = &bucket{
			tokens:    float64(tb.burst),
			lastCheck: time.Now(),
		}
		tb.buckets[key] = b
	}

	now := time.Now()
	elapsed := now.Sub(b.lastCheck).Seconds()
	b.lastCheck = now

	// Refill tokens based on elapsed time
	b.tokens += elapsed * tb.rate
	if b.tokens > float64(tb.burst) {
		b.tokens = float64(tb.burst)
	}

	if b.tokens < 1 {
		return false
	}

	b.tokens--
	return true
}

// SetRate updates the rate dynamically.
func (tb *TokenBucket) SetRate(rate float64, burst int) {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.rate = rate
	if burst > 0 {
		tb.burst = burst
	}
	if tb.burst <= 0 {
		tb.burst = int(tb.rate)
	}
}