package sse

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestHistoryRingAndSlowSubscriberAccounting(t *testing.T) {
	h := NewHub()
	client := &subscriber{queue: make(chan logEntry, 128)}
	h.clients[client] = true
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(h, "line-%d\n", i)
	}
	if h.count != historyCap || len(client.queue) != 128 || client.dropped.Load() != 872 {
		t.Fatal(h.count, len(client.queue), client.dropped.Load())
	}
	oldest := h.history[(h.next-h.count+historyCap)%historyCap]
	if oldest.line != "line-800" {
		t.Fatal(oldest)
	}
	h.Close()
	h.Close()
	select {
	case <-h.done:
	case <-time.After(time.Second):
		t.Fatal("hub did not close")
	}
}

func TestReconnectReplaysOnlyUnseenHistory(t *testing.T) {
	h := NewHub()
	defer h.Close()
	for i := 0; i < 3; i++ {
		fmt.Fprintf(h, "line-%d\n", i)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(h.ServeHTTP))
	defer srv.Close()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	req.Header.Set("Last-Event-ID", "2")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data:") {
			if scanner.Text() != "data: line-2" {
				t.Fatal(scanner.Text())
			}
			cancel()
			return
		}
	}
	t.Fatal("no replay")
}
