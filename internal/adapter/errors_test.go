package adapter

import "testing"

func TestAPIErrorRetryable(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		message   string
		retryable bool
	}{
		{name: "server error", status: 500, retryable: true},
		{name: "bad gateway", status: 502, retryable: true},
		{name: "rate limit", status: 429, retryable: true},
		{name: "request timeout", status: 408, retryable: true},
		// Deterministic client-side rejections: the same payload fails
		// identically on every provider, so retrying only burns quota and
		// delays failover.
		{name: "bad request", status: 400, message: `{"error":{"message":"invalid model"}}`, retryable: false},
		{name: "unauthorized", status: 401, retryable: false},
		{name: "forbidden", status: 403, retryable: false},
		{name: "not found", status: 404, retryable: false},
		{name: "conflict", status: 409, retryable: false},
		{name: "too early", status: 425, retryable: false},
		{name: "unprocessable", status: 422, retryable: false},
		{name: "client abort passthrough", status: 499, retryable: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := (&APIError{StatusCode: tt.status, Message: tt.message}).Retryable()
			if got != tt.retryable {
				t.Fatalf("Retryable() = %v, want %v", got, tt.retryable)
			}
		})
	}
}
