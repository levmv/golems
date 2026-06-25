package murm

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// SSEWriter writes murm stream events as Server-Sent Events: a stable
// incrementing id, an event: type line, and a data: JSON payload, flushed after
// each event. Not safe for concurrent writers.
type SSEWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	n       int
}

// NewSSEWriter sets the SSE response headers and returns a writer. It errors if
// the ResponseWriter cannot flush (required for streaming).
func NewSSEWriter(w http.ResponseWriter) (*SSEWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("murm: response writer does not support flushing")
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // disable proxy buffering (nginx)
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	return &SSEWriter{w: w, flusher: flusher}, nil
}

// Send writes one event and flushes.
func (s *SSEWriter) Send(ev StreamEvent) error {
	data, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("murm: marshal event: %w", err)
	}
	s.n++
	if _, err := fmt.Fprintf(s.w, "id: evt_%d\nevent: %s\ndata: %s\n\n", s.n, ev.Type, data); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// Keepalive writes an SSE comment to keep the connection alive through idle
// proxies.
func (s *SSEWriter) Keepalive() error {
	if _, err := fmt.Fprint(s.w, ": keepalive\n\n"); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}
