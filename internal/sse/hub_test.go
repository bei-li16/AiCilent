package sse

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHubReplaysBoundedHistory(t *testing.T) {
	h := NewHub()
	_, _ = h.Write([]byte("first\n"))
	_, _ = h.Write([]byte("second\n"))

	server := httptest.NewServer(http.HandlerFunc(h.ServeHTTP))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var got []string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if line := scanner.Text(); strings.HasPrefix(line, "data: ") {
			got = append(got, strings.TrimPrefix(line, "data: "))
			if len(got) == 2 {
				break
			}
		}
	}
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("history = %#v, want [first second]", got)
	}
}
