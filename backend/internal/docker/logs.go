package docker

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"

	"myserver/internal/auth"
	"myserver/internal/httpx"
)

const (
	logTailDefault = 200
	logTailMax     = 2000
	logLineMax     = 16 << 10
)

// LogLine is the payload of the "log" SSE event.
type LogLine struct {
	Stream string `json:"stream"` // stdout or stderr
	Line   string `json:"line"`
	// Time is set when timestamps were requested (Unix seconds).
	Time *int64 `json:"time"`
}

// lineWriter splits a byte stream into lines and sends each as an event.
type lineWriter struct {
	sse        *sseWriter
	stream     string
	timestamps bool
	buf        []byte
}

func (l *lineWriter) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		if err := l.emit(l.buf[:i]); err != nil {
			return 0, err
		}
		l.buf = l.buf[i+1:]
	}
	if len(l.buf) > logLineMax {
		if err := l.emit(l.buf); err != nil {
			return 0, err
		}
		l.buf = l.buf[:0]
	}
	// Compact so the backing array does not grow without bound.
	l.buf = append([]byte(nil), l.buf...)
	l.sse.flush()
	return len(p), nil
}

// finish sends a trailing line that had no newline.
func (l *lineWriter) finish() {
	if len(l.buf) > 0 {
		_ = l.emit(l.buf)
		l.buf = nil
		l.sse.flush()
	}
}

func (l *lineWriter) emit(raw []byte) error {
	text := strings.ToValidUTF8(string(bytes.TrimRight(raw, "\r")), "�")
	line := LogLine{Stream: l.stream, Line: text}
	if l.timestamps {
		if stamp, rest, ok := strings.Cut(text, " "); ok {
			if t, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
				u := t.Unix()
				line.Time = &u
				line.Line = rest
			}
		}
	}
	return l.sse.write("log", line)
}

// handleLogStream streams a container's log in real time:
//
//	event "start" → {"tty": bool, "tail": n}, first event of every connection
//	event "log" → LogLine
//	event "end" → {"reason": "..."} when the stream is over; the client must
//	              not reconnect automatically after it.
func (m *Module) handleLogStream(w http.ResponseWriter, r *http.Request) error {
	id, err := pathContainerID(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	tail := logTailDefault
	if v := q.Get("tail"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return httpx.BadRequest("'tail' değeri geçersiz.")
		}
		tail = min(n, logTailMax)
	}
	timestamps := false
	if v := q.Get("timestamps"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return httpx.BadRequest("'timestamps' değeri geçersiz.")
		}
		timestamps = b
	}

	cli, err := m.client()
	if err != nil {
		return err
	}
	ictx, icancel := context.WithTimeout(r.Context(), listTimeout)
	insp, err := cli.ContainerInspect(ictx, id)
	icancel()
	if err != nil {
		return apiError(err, errText{notFound: containerNotFound, fallback: "Konteyner bilgileri alınamadı."})
	}
	tty := insp.Config != nil && insp.Config.Tty

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(m.ctx, cancel)
	defer stop()

	reader, err := cli.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Timestamps: timestamps,
		Tail:       strconv.Itoa(tail),
	})
	if err != nil {
		return apiError(err, errText{notFound: containerNotFound, fallback: "Konteyner logları okunamadı."})
	}
	defer reader.Close()

	// Reading logs can expose secrets, so it is recorded.
	name := id
	if insp.ContainerJSONBase != nil {
		name = strings.TrimPrefix(insp.Name, "/")
	}
	m.deps.Audit.Log(r.Context(), auth.ActorFrom(r), "docker.container_logs", name, "", true)

	sse, err := startSSE(w)
	if err != nil || sse == nil {
		return err
	}
	// Each connection replays the tail; "start" tells the client to begin
	// with an empty view.
	if sse.event("start", map[string]any{"tty": tty, "tail": tail}) != nil {
		return nil
	}

	done := make(chan struct{})
	go func() {
		t := time.NewTicker(sseKeepalive)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if sse.comment("ping") != nil {
					cancel()
					return
				}
			}
		}
	}()

	out := &lineWriter{sse: sse, stream: "stdout", timestamps: timestamps}
	errOut := &lineWriter{sse: sse, stream: "stderr", timestamps: timestamps}
	var copyErr error
	if tty {
		// A TTY container has a single raw stream.
		_, copyErr = io.Copy(out, reader)
	} else {
		// Otherwise stdout and stderr are multiplexed with 8-byte headers.
		_, copyErr = stdcopy.StdCopy(out, errOut, reader)
	}
	close(done)
	if ctx.Err() != nil {
		return nil // the client left or the panel is shutting down
	}
	out.finish()
	errOut.finish()
	reason := "stopped"
	if copyErr != nil {
		reason = "error"
	}
	_ = sse.event("end", map[string]string{"reason": reason})
	return nil
}
