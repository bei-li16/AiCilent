package adapter

import (
	"fmt"
	"strings"
)

type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("API error (status %d): %s", e.StatusCode, e.Message)
}

func (e *APIError) Retryable() bool {
	// 5xx server errors are retryable
	if e.StatusCode >= 500 {
		return true
	}
	// 429 Too Many Requests is retryable (rate limit)
	if e.StatusCode == 429 {
		return true
	}
	// Sensenova occasionally returns this generic 400 while request extraction
	// fails on an upstream worker. The same request is accepted by another
	// worker, so retry this specific transient error without retrying ordinary
	// client-side 400 errors.
	if e.StatusCode == 400 && strings.Contains(strings.ToLower(e.Message), "request extraction failed") {
		return true
	}
	return false
}
