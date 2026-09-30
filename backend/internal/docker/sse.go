package docker

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"myserver/internal/httpx"
)

const sseKeepalive = 25 * time.Second

// sseWriter serialises writes to one Server-Sent Events response. It is safe
// for concurrent use, so a keep-alive ticker and a producer can share it.
type sseWriter struct {
	mu sync.Mutex
	w  http.ResponseWriter
	f  http.Flusher
}

// startSSE sends the stream headers. After it succeeds the handler must not
// return an error, because the response has already started.
func startSSE(w http.ResponseWriter) (*sseWriter, error) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, httpx.Internal(errors.New("response writer does not support streaming"))
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	s := &sseWriter{w: w, f: f}
	if err := s.comment("bağlandı"); err != nil {
		return nil, nil
	}
	return s, nil
}

func (s *sseWriter) comment(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := fmt.Fprintf(s.w, ": %s\n\n", text); err != nil {
		return err
	}
	s.f.Flush()
	return nil
}

// event writes one JSON event and flushes.
func (s *sseWriter) event(name string, v any) error {
	if err := s.write(name, v); err != nil {
		return err
	}
	s.flush()
	return nil
}

// write queues one JSON event without flushing; call flush afterwards.
func (s *sseWriter) write(name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, b)
	return err
}

func (s *sseWriter) flush() {
	s.mu.Lock()
	s.f.Flush()
	s.mu.Unlock()
}
