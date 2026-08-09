package adapter

import (
	"fmt"
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
	// 4xx client errors are retryable (same as 429)
	if e.StatusCode >= 400 {
		return true
	}
	return false
}
