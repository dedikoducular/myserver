package logbuf

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

const secretValue = "hunter2-COK-GIZLI"

func newLogger(level string, size int) (*slog.Logger, *bytes.Buffer, *Buffer) {
	var out bytes.Buffer
	buf := NewBuffer(size)
	return New(&out, level, buf), &out, buf
}

// assertNoSecret checks both sinks.
func assertNoSecret(t *testing.T, what string, out *bytes.Buffer, buf *Buffer) {
	t.Helper()
	if strings.Contains(out.String(), secretValue) {
		t.Errorf("%s: secret in the JSON output: %s", what, out.String())
	}
	b, err := json.Marshal(buf.Recent(0))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), secretValue) {
		t.Errorf("%s: secret in the memory buffer: %s", what, b)
	}
}

func TestSecretKeysAreRedacted(t *testing.T) {
	keys := []string{
		"password", "Password", "PASSWORD", "new_password", "current_password", "passwd", "db_passwd",
		"secret", "client_secret", "SecretKey", "token", "csrf_token", "access_token", "sessionToken", "X-CSRF-Token", "csrf",
		"cookie", "Cookie", "set-cookie", "authorization", "Authorization", "apikey", "api_key", "API_KEY",
		"sifre", "parola", "yeni_parola",
	}
	for _, k := range keys {
		log, out, buf := newLogger("info", 8)
		log.Info("istek", k, secretValue, "user", "ali")
		assertNoSecret(t, "key "+k, out, buf)
		if !strings.Contains(out.String(), "[gizlendi]") {
			t.Errorf("key %s: no redaction marker in the output: %s", k, out.String())
		}
		e := buf.Recent(1)
		if len(e) != 1 || e[0].Attrs[k] != "[gizlendi]" {
			t.Errorf("key %s: buffer entry %+v", k, e)
		}
		if e[0].Attrs["user"] != "ali" || !strings.Contains(out.String(), `"user":"ali"`) {
			t.Errorf("key %s: ordinary attribute lost: %+v / %s", k, e, out.String())
		}
	}
}

func TestSecretsAreRedactedForEveryValueKind(t *testing.T) {
	values := map[string]any{
		"string":   secretValue,
		"bytes":    []byte(secretValue),
		"error":    errors.New(secretValue),
		"stringer": stringer{},
		"struct":   struct{ P string }{secretValue},
		"slice":    []string{secretValue},
		"map":      map[string]string{"v": secretValue},
		"valuer":   valuer{},
	}
	for name, v := range values {
		log, out, buf := newLogger("info", 8)
		log.Info("istek", "password", v)
		log.Warn("istek", slog.Any("token", v))
		assertNoSecret(t, name, out, buf)
	}
}

type stringer struct{}

func (stringer) String() string { return secretValue }

type valuer struct{}

func (valuer) LogValue() slog.Value { return slog.StringValue(secretValue) }

// groupValuer resolves to a group that contains a secret attribute.
type groupValuer struct{}

func (groupValuer) LogValue() slog.Value {
	return slog.GroupValue(slog.String("username", "ali"), slog.String("password", secretValue))
}

func TestSecretsInsideGroupsAreRedacted(t *testing.T) {
	t.Run("group attribute", func(t *testing.T) {
		log, out, buf := newLogger("info", 8)
		log.Info("giriş", slog.Group("request", slog.String("username", "ali"), slog.String("password", secretValue)))
		assertNoSecret(t, "group", out, buf)
		if !strings.Contains(out.String(), "ali") {
			t.Errorf("ordinary group member lost: %s", out.String())
		}
	})
	t.Run("nested group", func(t *testing.T) {
		log, out, buf := newLogger("info", 8)
		log.Info("giriş", slog.Group("a", slog.Group("b", slog.String("api_key", secretValue))))
		assertNoSecret(t, "nested group", out, buf)
	})
	t.Run("LogValuer returning a group", func(t *testing.T) {
		log, out, buf := newLogger("info", 8)
		log.Info("giriş", "request", groupValuer{})
		assertNoSecret(t, "group valuer", out, buf)
	})
	t.Run("WithGroup", func(t *testing.T) {
		log, out, buf := newLogger("info", 8)
		log.WithGroup("auth").Info("giriş", "password", secretValue, "user", "ali")
		assertNoSecret(t, "WithGroup", out, buf)
	})
	t.Run("With", func(t *testing.T) {
		log, out, buf := newLogger("info", 8)
		log.With("token", secretValue, "module", "docker").Info("istek", "cookie", secretValue)
		assertNoSecret(t, "With", out, buf)
		e := buf.Recent(1)
		if len(e) != 1 || e[0].Attrs["module"] != "docker" || e[0].Attrs["token"] != "[gizlendi]" || e[0].Attrs["cookie"] != "[gizlendi]" {
			t.Errorf("buffer entry %+v", e)
		}
	})
	t.Run("With group attribute", func(t *testing.T) {
		log, out, buf := newLogger("info", 8)
		log.With(slog.Group("conn", slog.String("secret", secretValue))).Info("istek")
		assertNoSecret(t, "With group", out, buf)
	})
	t.Run("secret group key", func(t *testing.T) {
		log, out, buf := newLogger("info", 8)
		log.Info("istek", slog.Group("credentials_token", slog.String("value", secretValue)))
		assertNoSecret(t, "secret group key", out, buf)
	})
}

func TestStackTracesStayOutOfTheBuffer(t *testing.T) {
	log, _, buf := newLogger("info", 8)
	log.Error("handler panic", "path", "/x", "stack", "goroutine 1 [running]:\nmain.go:42")
	b, _ := json.Marshal(buf.Recent(0))
	if strings.Contains(string(b), "goroutine") {
		t.Fatalf("stack trace exposed through the buffer: %s", b)
	}
}

func TestLevels(t *testing.T) {
	cases := map[string][]string{
		"debug": {"INFO", "INFO", "WARN", "ERROR"}, // debug lines are shown as INFO
		"info":  {"INFO", "WARN", "ERROR"},
		"":      {"INFO", "WARN", "ERROR"},
		"bogus": {"INFO", "WARN", "ERROR"},
		"WARN":  {"WARN", "ERROR"},
		"error": {"ERROR"},
	}
	for level, want := range cases {
		log, out, buf := newLogger(level, 8)
		log.Debug("d")
		log.Info("i")
		log.Warn("w")
		log.Error("e")
		var got []string
		entries := buf.Recent(0)
		for i := len(entries) - 1; i >= 0; i-- {
			got = append(got, entries[i].Level)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("level %q: buffer has %v, want %v", level, got, want)
		}
		if n := strings.Count(out.String(), "\n"); n != len(want) {
			t.Errorf("level %q: %d lines written, want %d", level, n, len(want))
		}
	}
}

func TestOutputIsJSONLines(t *testing.T) {
	log, out, _ := newLogger("info", 8)
	log.Info("birinci \"satır\"\nikinci", "k", "v\nw")
	log.Warn("üçüncü")
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d lines for 2 records (log injection through a newline?): %q", len(lines), out.String())
	}
	for _, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("line is not JSON: %q", l)
		}
		if m["msg"] == nil || m["level"] == nil || m["time"] == nil {
			t.Fatalf("line lacks standard fields: %q", l)
		}
	}
}

func messages(entries []Entry) string {
	var m []string
	for _, e := range entries {
		m = append(m, e.Message)
	}
	return strings.Join(m, ",")
}

func TestRingBufferOrderAndWrap(t *testing.T) {
	log, _, buf := newLogger("info", 4)
	if got := buf.Recent(10); len(got) != 0 {
		t.Fatalf("empty buffer returned %d entries", len(got))
	}
	log.Info("1")
	if got := messages(buf.Recent(10)); got != "1" {
		t.Fatalf("one entry: %q", got)
	}
	log.Info("2")
	log.Info("3")
	if got := messages(buf.Recent(10)); got != "3,2,1" {
		t.Fatalf("partly filled: %q, want newest first", got)
	}
	if got := messages(buf.Recent(2)); got != "3,2" {
		t.Fatalf("limit 2: %q", got)
	}
	log.Info("4") // exactly full
	if got := messages(buf.Recent(10)); got != "4,3,2,1" {
		t.Fatalf("exactly full: %q", got)
	}
	log.Info("5") // first overwrite
	if got := messages(buf.Recent(10)); got != "5,4,3,2" {
		t.Fatalf("after wrapping once: %q", got)
	}
	for i := 6; i <= 11; i++ {
		log.Info(fmt.Sprint(i))
	}
	if got := messages(buf.Recent(0)); got != "11,10,9,8" {
		t.Fatalf("after wrapping repeatedly (limit 0 = all): %q", got)
	}
	if got := messages(buf.Recent(-3)); got != "11,10,9,8" {
		t.Fatalf("negative limit: %q", got)
	}
	if got := messages(buf.Recent(3)); got != "11,10,9" {
		t.Fatalf("limit 3 after wrap: %q", got)
	}
	if got := messages(buf.Recent(1)); got != "11" {
		t.Fatalf("limit 1: %q", got)
	}
}

func TestRecentReturnsACopy(t *testing.T) {
	log, _, buf := newLogger("info", 4)
	log.Info("asıl", "k", "v")
	got := buf.Recent(1)
	got[0].Message = "değişti"
	got[0].Attrs["k"] = "değişti"
	again := buf.Recent(1)
	if again[0].Message != "asıl" {
		t.Fatal("a caller can modify buffered entries")
	}
}

func TestConcurrentLogging(t *testing.T) {
	log, _, buf := newLogger("info", 64)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			l := log.With("worker", g)
			for i := 0; i < 50; i++ {
				l.Info("satır", "i", i, "password", secretValue)
				buf.Recent(10)
			}
		}(g)
	}
	wg.Wait()
	got := buf.Recent(0)
	if len(got) != 64 {
		t.Fatalf("%d entries in a full buffer of 64", len(got))
	}
	for _, e := range got {
		if e.Message != "satır" || e.Attrs["password"] != "[gizlendi]" {
			t.Fatalf("damaged entry %+v", e)
		}
	}
}
