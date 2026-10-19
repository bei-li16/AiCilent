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
	// Only failures that another attempt (or another provider/key) can
	// plausibly fix are retryable:
	//   429 rate limit — yes, a different window/key may accept the request
	//   408/5xx       — yes, transient upstream conditions
	// Other 4xx responses do not justify retrying the same provider/key.
	// The router may still select another provider (for example after a
	// credential-specific 401/403 or a model-specific 404).
	switch {
	case e.StatusCode == 429:
		return true
	case e.StatusCode == 408:
		return true
	case e.StatusCode >= 500:
		return true
	}
	return false
}
