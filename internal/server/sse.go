package server

import (
	"encoding/json"
	"net/http"
	"sync"
)

// message is one server-sent event payload.
type message struct {
	Type    string `json:"type"`
	PassID  string `json:"passId,omitempty"`
	Label   string `json:"label,omitempty"`
	Percent int    `json:"percent,omitempty"`
	OK      bool   `json:"ok,omitempty"`
	Err     string `json:"err,omitempty"`
	Warning string `json:"warning,omitempty"`
}

// Hub fans one broadcast out to every connected browser.
type Hub struct {
	mu   sync.Mutex
	subs map[chan message]struct{}
}

// NewHub returns an empty hub.
func NewHub() *Hub {
	return &Hub{subs: map[chan message]struct{}{}}
}

func (h *Hub) subscribe() chan message {
	// Buffered generously: a slow browser must never stall a sync.
	ch := make(chan message, 256)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *Hub) unsubscribe(ch chan message) {
	h.mu.Lock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
	h.mu.Unlock()
}

// closeAll force-closes every currently connected subscriber. serveSSE's
// select sees the closed channel and returns, ending that handler, which is
// what lets http.Server.Shutdown finish promptly instead of blocking on a
// browser tab left open: an SSE connection otherwise stays open until the
// client disconnects on its own, and Shutdown does not force-close active
// handlers, only waits for them.
func (h *Hub) closeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		delete(h.subs, ch)
		close(ch)
	}
}

// broadcast delivers to every subscriber, dropping messages for any
// subscriber that has fallen behind rather than blocking the sync.
func (h *Hub) broadcast(m message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- m:
		default:
		}
	}
}

// serveSSE streams messages to one browser until it disconnects.
func (h *Hub) serveSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch := h.subscribe()
	defer h.unsubscribe(ch)

	for {
		select {
		case <-r.Context().Done():
			return
		case m, ok := <-ch:
			if !ok {
				return
			}
			b, err := json.Marshal(m)
			if err != nil {
				continue
			}
			if _, err := w.Write([]byte("data: " + string(b) + "\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
