package sse

import (
	"fmt"
	"net/http"
	"sync"
)

type Hub struct {
	mu      sync.RWMutex
	clients map[chan string]bool
}

func NewHub() *Hub {
	return &Hub{
		clients: make(map[chan string]bool),
	}
}

func (h *Hub) Write(p []byte) (int, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.clients {
		select {
		case ch <- string(p):
		default:
		}
	}
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
	h.mu.Unlock()

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