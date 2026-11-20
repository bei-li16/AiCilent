package adapter

import (
	"fmt"
	"net/http"
	"strings"
)

// entitlementExhaustedMarker identifies an account whose token plan quota is
// used up. The vendor reports it as a 429 with the same generic code ("8") it
// uses for ordinary rps/rpm/tpm throttling, so the message text is the only
// signal that separates the two. Unlike throttling it does not recover when a
// rate window slides: the plan refills on the vendor's own billing cycle.
const entitlementExhaustedMarker = "entitlement exhausted"

type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("API error (status %d): %s", e.StatusCode, e.Message)
}

// EntitlementExhausted reports that the account's token plan quota is gone,
// as opposed to a rate limit that a later window would accept.
func (e *APIError) EntitlementExhausted() bool {
	if e.StatusCode != http.StatusTooManyRequests {
		return false
	}
	return strings.Contains(strings.ToLower(e.Message), entitlementExhaustedMarker)
}

func (e *APIError) Retryable() bool {
	// Only failures that another attempt (or another provider/key) can
	// plausibly fix are retryable:
	//   429 rate limit — yes, a different window/key may accept the request
	//   408/5xx       — yes, transient upstream conditions
	// Other 4xx responses do not justify retrying the same provider/key.
	// The router may still select another provider (for example after a
	// credential-specific 401/403 or a model-specific 404).
	if e.EntitlementExhausted() {
		// An exhausted plan quota is not a window that slides, so retrying the
		// same key only burns latency for a result that cannot change.
		return false
	}
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
