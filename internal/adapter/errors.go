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
	// All upstream 4xx and 5xx responses are retryable. The retry count and
	// backoff schedule are controlled exclusively by the provider YAML.
	if e.StatusCode >= 400 {
		return true
	}
	return false
}
