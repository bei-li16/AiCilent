package router

import (
	"net/http"
	"testing"
	"time"
)

func TestStreamTransportUsesConfiguredTimeout(t *testing.T) {
	e := &Engine{
		transport:        &http.Transport{},
		streamTransports: make(map[time.Duration]*http.Transport),
	}

	for _, timeout := range []int{60, 90, 120} {
		transport := e.streamTransport(timeout)
		want := time.Duration(timeout) * time.Second
		if transport.ResponseHeaderTimeout != want {
			t.Fatalf("timeout %d: ResponseHeaderTimeout = %s, want %s", timeout, transport.ResponseHeaderTimeout, want)
		}
	}
}

func TestStreamTransportZeroDisablesHeaderTimeout(t *testing.T) {
	e := &Engine{
		transport:        &http.Transport{},
		streamTransports: make(map[time.Duration]*http.Transport),
	}

	transport := e.streamTransport(0)
	if transport.ResponseHeaderTimeout != 0 {
		t.Fatalf("ResponseHeaderTimeout = %s, want 0", transport.ResponseHeaderTimeout)
	}
}
