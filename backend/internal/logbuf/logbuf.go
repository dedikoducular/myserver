// Package logbuf provides the structured logger: JSON to stdout plus a
// bounded in-memory ring that backs the panel's "Son Loglar" view. Attribute
// values whose key looks secret are redacted before they reach either sink.
package logbuf

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Entry is one log line as exposed to the panel.
type Entry struct {
	Time    time.Time         `json:"time"`
	Level   string            `json:"level"`
	Message string            `json:"message"`
	Attrs   map[string]string `json:"attrs,omitempty"`
}

// Buffer is a fixed-size ring of recent entries.
type Buffer struct {
	mu    sync.Mutex
	items []Entry
	next  int
	full  bool
}

func NewBuffer(size int) *Buffer { return &Buffer{items: make([]Entry, size)} }

func (b *Buffer) add(e Entry) {
	b.mu.Lock()
	b.items[b.next] = e
	b.next = (b.next + 1) % len(b.items)
	if b.next == 0 {
		b.full = true
	}
	b.mu.Unlock()
}

// Recent returns up to limit entries, newest first.
func (b *Buffer) Recent(limit int) []Entry {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := b.next
	if b.full {
		n = len(b.items)
	}
	if limit <= 0 || limit > n {
		limit = n
	}
	out := make([]Entry, 0, limit)
	for i := 1; i <= limit; i++ {
		idx := (b.next - i + len(b.items)) % len(b.items)
		out = append(out, b.items[idx])
	}
	return out
}

var secretHints = []string{"password", "passwd", "secret", "token", "cookie", "authorization", "csrf", "apikey", "api_key", "sifre", "parola"}

func isSecretKey(key string) bool {
	k := strings.ToLower(key)
	for _, h := range secretHints {
		if strings.Contains(k, h) {
			return true
		}
	}
	return false
}

const redacted = "[gizlendi]"

// redact is the JSON handler's ReplaceAttr. It is called for every leaf
// attribute with the names of the groups that contain it; a secret-looking
// name anywhere on that path hides the value.
func redact(groups []string, a slog.Attr) slog.Attr {
	if isSecretKey(a.Key) {
		return slog.String(a.Key, redacted)
	}
	for _, g := range groups {
		if isSecretKey(g) {
			return slog.String(a.Key, redacted)
		}
	}
	return a
}

// boundAttr is an attribute added with With, together with the group it was
// added under.
type boundAttr struct {
	prefix string
	secret bool
	attr   slog.Attr
}

type handler struct {
	inner slog.Handler
	buf   *Buffer
	attrs []boundAttr
	// prefix and secret describe the groups opened with WithGroup.
	prefix string
	secret bool
}

func (h *handler) Enabled(ctx context.Context, l slog.Level) bool { return h.inner.Enabled(ctx, l) }

// flatten stores a into dst under prefix. Groups are expanded to dotted keys
// so that every leaf is checked on its own; formatting a group as one string
// would carry the secrets inside it past the redaction.
func flatten(dst map[string]string, prefix string, secret bool, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Value.Kind() == slog.KindGroup {
		members := a.Value.Group()
		if len(members) == 0 {
			return
		}
		if a.Key != "" {
			secret = secret || isSecretKey(a.Key)
			prefix += a.Key + "."
		}
		for _, m := range members {
			flatten(dst, prefix, secret, m)
		}
		return
	}
	if a.Key == "" {
		return
	}
	if prefix == "" && a.Key == "stack" {
		return
	}
	if secret || isSecretKey(a.Key) {
		dst[prefix+a.Key] = redacted
		return
	}
	dst[prefix+a.Key] = a.Value.String()
}

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	e := Entry{Time: r.Time, Level: levelName(r.Level), Message: r.Message}
	attrs := map[string]string{}
	for _, b := range h.attrs {
		flatten(attrs, b.prefix, b.secret, b.attr)
	}
	r.Attrs(func(a slog.Attr) bool {
		flatten(attrs, h.prefix, h.secret, a)
		return true
	})
	if len(attrs) > 0 {
		e.Attrs = attrs
	}
	h.buf.add(e)
	return h.inner.Handle(ctx, r)
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make([]boundAttr, 0, len(h.attrs)+len(attrs))
	merged = append(merged, h.attrs...)
	for _, a := range attrs {
		merged = append(merged, boundAttr{prefix: h.prefix, secret: h.secret, attr: a})
	}
	return &handler{inner: h.inner.WithAttrs(attrs), buf: h.buf, attrs: merged, prefix: h.prefix, secret: h.secret}
}

func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &handler{
		inner: h.inner.WithGroup(name), buf: h.buf, attrs: h.attrs,
		prefix: h.prefix + name + ".", secret: h.secret || isSecretKey(name),
	}
}

func levelName(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "ERROR"
	case l >= slog.LevelWarn:
		return "WARN"
	default:
		return "INFO"
	}
}

// New builds the process logger writing JSON to w.
func New(w io.Writer, level string, buf *Buffer) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	inner := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lv, ReplaceAttr: redact})
	return slog.New(&handler{inner: inner, buf: buf})
}
