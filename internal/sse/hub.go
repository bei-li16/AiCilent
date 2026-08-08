package sse

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
)

const historyCap = 200

type Hub struct {
	mu      sync.RWMutex
	clients map[chan string]bool
	history []string
}

func NewHub() *Hub {
	return &Hub{
		clients: make(map[chan string]bool),
	}
}

func (h *Hub) Write(p []byte) (int, error) {
	lines := strings.Split(strings.TrimSuffix(string(p), "\n"), "\n")
	h.mu.Lock()
	for _, line := range lines {
		if line == "" {
			continue
		}
		h.history = append(h.history, line)
		if len(h.history) > historyCap {
			copy(h.history, h.history[len(h.history)-historyCap:])
			h.history = h.history[:historyCap]
		}
		for ch := range h.clients {
			select {
			case ch <- line:
			default:
			}
		}
	}
	h.mu.Unlock()
	return len(p), nil
}

func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan string, 128)

	h.mu.Lock()
	h.clients[ch] = true
	history := append([]string(nil), h.history...)
	h.mu.Unlock()
	for _, line := range history {
		fmt.Fprintf(w, "data: %s\n\n", line)
	}
	flusher.Flush()

	defer func() {
		h.mu.Lock()
		delete(h.clients, ch)
		h.mu.Unlock()
	}()

	ctx := r.Context()
	for {
		select {
		case msg := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		case <-ctx.Done():
			return
		}
	}
}
