package sse

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
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
		if scanner.Text() == "event: log-generation" {
			scanner.Scan()
			continue
		}
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
	req.Header.Set("Last-Event-ID", strconv.FormatUint(h.generation, 10)+":2")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if scanner.Text() == "event: log-generation" {
			scanner.Scan()
			continue
		}
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

func TestClearDisconnectsAndInvalidatesReplayCursor(t *testing.T) {
	h := NewHub()
	defer h.Close()
	_, _ = h.Write([]byte("old P6\n"))
	server := httptest.NewServer(http.HandlerFunc(h.ServeHTTP))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	connect := func(lastID string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Last-Event-ID", lastID)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	previous := strconv.FormatUint(h.generation, 10)
	first := bufio.NewScanner(connect("").Body)
	for first.Scan() && first.Text() != "data: old P6" {
	}
	if first.Text() != "data: old P6" {
		t.Fatal("initial log was not delivered")
	}
	h.Clear()
	if !first.Scan() || first.Text() != "" || first.Scan() || first.Err() != nil {
		t.Fatalf("old stream did not close cleanly: %v", first.Err())
	}
	_, _ = h.Write([]byte("new P1\n"))
	next := bufio.NewScanner(connect(previous + ":1").Body)
	wants := []string{"event: log-generation", "data: " + strconv.FormatUint(h.generation, 10), "", "id: " + strconv.FormatUint(h.generation, 10) + ":1", "data: new P1"}
	for _, want := range wants {
		if !next.Scan() || next.Text() != want {
			t.Fatalf("new stream line = %q, want %q (err=%v)", next.Text(), want, next.Err())
		}
	}
}
