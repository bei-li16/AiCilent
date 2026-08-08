package adapter

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCallOpenAIRawContextPropagatesRequestID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Request-ID"); got != "req-test" {
			t.Errorf("X-Request-ID = %q, want req-test", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"ok"}`)
	}))
	defer server.Close()

	body, err := CallOpenAIRawContext(context.Background(), server.URL, "key", []byte(`{"model":"m"}`), 5, "req-test", http.DefaultTransport)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"id":"ok"}` {
		t.Fatalf("body = %s", body)
	}
}
