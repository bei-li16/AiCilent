package adapter

import "fmt"

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
	return false
}