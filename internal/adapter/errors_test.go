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
		{name: "rate limit", status: 429, retryable: true},
		{name: "request extraction", status: 400, message: `{"error":{"message":"Request extraction failed","code":"BadRequest"}}`, retryable: true},
		{name: "ordinary bad request", status: 400, message: `{"error":{"message":"invalid model"}}`, retryable: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := (&APIError{StatusCode: tt.status, Message: tt.message}).Retryable()
			if err != tt.retryable {
				t.Fatalf("Retryable() = %v, want %v", err, tt.retryable)
			}
		})
	}
}
