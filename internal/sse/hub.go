package sse

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const historyCap = 200

type logEntry struct {
	id   uint64
	line string
}
type subscriber struct {
	queue   chan logEntry
	dropped atomic.Uint64
}
type Hub struct {
	mu          sync.Mutex
	clients     map[*subscriber]bool
	history     [historyCap]logEntry
	next, count int
	sequence    uint64
	done        chan struct{}
	closed      bool
}

func NewHub() *Hub {
	return &Hub{
		clients: make(map[*subscriber]bool), done: make(chan struct{}),
	}
}

func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.closed {
		h.closed = true
		close(h.done)
	}
}

func (h *Hub) Write(p []byte) (int, error) {
	lines := strings.Split(strings.TrimSuffix(string(p), "\n"), "\n")
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return len(p), nil
	}
	for _, line := range lines {
		if line == "" {
			continue
		}
		h.sequence++
		entry := logEntry{h.sequence, line}
		h.history[h.next] = entry
		h.next = (h.next + 1) % historyCap
		if h.count < historyCap {
			h.count++
		}
		for client := range h.clients {
			select {
			case client.queue <- entry:
			default:
				client.dropped.Add(1)
			}
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

	client := &subscriber{queue: make(chan logEntry, 128)}
	lastID, _ := strconv.ParseUint(r.Header.Get("Last-Event-ID"), 10, 64)

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.clients[client] = true
	history := make([]logEntry, 0, h.count)
	if lastID > h.sequence {
		lastID = 0
	}
	for i := 0; i < h.count; i++ {
		e := h.history[(h.next-h.count+i+historyCap)%historyCap]
		if e.id > lastID {
			history = append(history, e)
		}
	}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.clients, client)
		h.mu.Unlock()
	}()
	controller := http.NewResponseController(w)
	flush := func(entries []logEntry) error {
		_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		defer controller.SetWriteDeadline(time.Time{})
		if dropped := client.dropped.Swap(0); dropped > 0 {
			if _, err := fmt.Fprintf(w, "event: gap\ndata: %d\n\n", dropped); err != nil {
				return err
			}
		}
		for _, e := range entries {
			if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", e.id, e.line); err != nil {
				return err
			}
		}
		flusher.Flush()
		return nil
	}
	if lastID > 0 && len(history) > 0 && history[0].id > lastID+1 {
		client.dropped.Add(history[0].id - lastID - 1)
	}
	if flush(history) != nil {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	batch := make([]logEntry, 0, 32)

	ctx := r.Context()
	for {
		select {
		case entry := <-client.queue:
			batch = append(batch[:0], entry)
		drain:
			for len(batch) < 32 {
				select {
				case e := <-client.queue:
					batch = append(batch, e)
				default:
					break drain
				}
			}
			if flush(batch) != nil {
				return
			}
		case <-ticker.C:
			if client.dropped.Load() > 0 && flush(nil) != nil {
				return
			}
		case <-ctx.Done():
			return
		case <-h.done:
			return
		}
	}
}
