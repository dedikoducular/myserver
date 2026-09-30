package services

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"

	"myserver/internal/httpx"
	"myserver/internal/privileged"
	"myserver/internal/services/servicescheck"
)

const (
	maxLogLines     = 500
	defaultLogLines = 200
	maxLineLength   = 4000
	maxFollowers    = 4
	followMax       = 10 * time.Minute
)

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func trimLine(s string) string {
	s = strings.TrimRight(s, "\r")
	if len(s) > maxLineLength {
		s = strings.ToValidUTF8(s[:maxLineLength], "") + "…"
	}
	return strings.ToValidUTF8(s, "?")
}

func (m *Module) handleLogs(w http.ResponseWriter, r *http.Request) error {
	unit := r.PathValue("unit")
	if !servicescheck.ValidUnit(unit) {
		return httpx.BadRequest("Servis adı geçersiz.")
	}
	n := defaultLogLines
	if q := r.URL.Query().Get("lines"); q != "" {
		v, err := strconv.Atoi(q)
		if err != nil || v < 1 {
			return httpx.BadRequest("Satır sayısı geçersiz.")
		}
		n = min(v, maxLogLines)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	out, err := m.deps.Priv.Run(ctx, "services-logs", unit, strconv.Itoa(n))
	if err != nil {
		return httpx.NewError(http.StatusBadGateway, "logs_failed",
			privileged.UserMessage(err, "Servis logları okunamadı.")).Wrap(err)
	}
	lines := []string{}
	for _, l := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(l) == "" || strings.HasPrefix(l, "-- No entries") {
			continue
		}
		lines = append(lines, trimLine(l))
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	httpx.OK(w, map[string]any{"unit": unit, "lines": lines, "requested": n})
	return nil
}

// handleLogStream follows a unit's journal over SSE for at most followMax.
// Events: "log" (data: JSON string, one line) and "end" (the cap was reached
// or the stream stopped; the client must not reconnect by itself).
func (m *Module) handleLogStream(w http.ResponseWriter, r *http.Request) error {
	unit := r.PathValue("unit")
	if !servicescheck.ValidUnit(unit) {
		return httpx.BadRequest("Servis adı geçersiz.")
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		return httpx.Internal(errors.New("response writer does not support streaming"))
	}
	select {
	case m.followers <- struct{}{}:
		defer func() { <-m.followers }()
	default:
		return httpx.TooManyRequests("Aynı anda en fazla 4 canlı log akışı açılabilir.")
	}

	ctx, cancel := context.WithTimeout(r.Context(), followMax)
	defer cancel()
	cmd, err := m.deps.Priv.Command(ctx, "services-logs-follow", unit)
	if err != nil {
		return httpx.Internal(err)
	}
	// SIGTERM is relayed by sudo, so the helper can stop journalctl.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return httpx.Internal(err)
	}
	if err := cmd.Start(); err != nil {
		return httpx.NewError(http.StatusBadGateway, "logs_failed", "Canlı log akışı başlatılamadı.").Wrap(err)
	}
	defer func() {
		cancel()
		_ = cmd.Wait()
	}()

	lines := make(chan string, 128)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-ctx.Done():
				return
			}
		}
	}()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": bağlandı\n\n")
	flusher.Flush()

	end := func(reason string) {
		fmt.Fprintf(w, "event: end\ndata: %s\n\n", mustJSON(reason))
		flusher.Flush()
	}
	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-ctx.Done():
			if r.Context().Err() == nil {
				end("timeout")
			}
			return nil
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return nil
			}
			flusher.Flush()
		case line, ok := <-lines:
			if !ok {
				if r.Context().Err() == nil {
					end("closed")
				}
				return nil
			}
			if _, err := fmt.Fprintf(w, "event: log\ndata: %s\n\n", mustJSON(trimLine(line))); err != nil {
				return nil
			}
			flusher.Flush()
		}
	}
}
