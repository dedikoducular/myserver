package docker

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/docker/docker/pkg/stdcopy"
)

// frame builds one frame of Docker's multiplexed stream: an 8 byte header
// (stream type, three zero bytes, big endian payload length) and the payload.
func frame(stream byte, payload string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(payload)))
	return append(h, payload...)
}

const (
	stdoutStream = 1
	stderrStream = 2
)

// chunkReader returns at most n bytes per Read, so frames and headers are
// split across reads.
type chunkReader struct {
	data []byte
	n    int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if len(c.data) == 0 {
		return 0, io.EOF
	}
	n := min(c.n, len(p), len(c.data))
	copy(p, c.data[:n])
	c.data = c.data[n:]
	return n, nil
}

type logOut struct {
	Stream string
	Line   string
	Time   *int64
}

func logEvents(t *testing.T, body string) []logOut {
	t.Helper()
	var out []logOut
	for _, ev := range parseSSE(body) {
		if ev.Name != "log" {
			continue
		}
		var l LogLine
		if err := json.Unmarshal([]byte(ev.Data), &l); err != nil {
			t.Fatalf("bad log event %q: %v", ev.Data, err)
		}
		out = append(out, logOut{l.Stream, l.Line, l.Time})
	}
	return out
}

// demux runs the same copy the handler runs for a non-TTY container.
func demux(t *testing.T, src io.Reader, timestamps bool) []logOut {
	t.Helper()
	rec := httptest.NewRecorder()
	sse, err := startSSE(rec)
	if err != nil || sse == nil {
		t.Fatalf("startSSE: %v", err)
	}
	out := &lineWriter{sse: sse, stream: "stdout", timestamps: timestamps}
	errOut := &lineWriter{sse: sse, stream: "stderr", timestamps: timestamps}
	if _, err := stdcopy.StdCopy(out, errOut, src); err != nil {
		t.Fatalf("StdCopy: %v", err)
	}
	out.finish()
	errOut.finish()
	return logEvents(t, rec.Body.String())
}

func TestDemultiplexWithFramesSplitAcrossReads(t *testing.T) {
	var stream []byte
	stream = append(stream, frame(stdoutStream, "hello\nwor")...)
	stream = append(stream, frame(stderrStream, "error line\n")...)
	stream = append(stream, frame(stdoutStream, "ld\n")...)
	stream = append(stream, frame(stdoutStream, "windows\r\n\n")...)
	stream = append(stream, frame(stderrStream, "çöktü: ğüşiöç\n")...)
	stream = append(stream, frame(stdoutStream, "")...)
	stream = append(stream, frame(stdoutStream, "no newline at the end")...)

	want := []logOut{
		{"stdout", "hello", nil},
		{"stderr", "error line", nil},
		{"stdout", "world", nil},
		{"stdout", "windows", nil},
		{"stdout", "", nil},
		{"stderr", "çöktü: ğüşiöç", nil},
		{"stdout", "no newline at the end", nil},
	}
	for _, size := range []int{1, 2, 3, 5, 7, 8, 9, 13, 64, 4096} {
		t.Run("chunk"+strconv.Itoa(size), func(t *testing.T) {
			got := demux(t, &chunkReader{data: bytes.Clone(stream), n: size}, false)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("chunk size %d:\n got %+v\nwant %+v", size, got, want)
			}
		})
	}
}

func TestDemultiplexTimestamps(t *testing.T) {
	var stream []byte
	stream = append(stream, frame(stdoutStream, "2024-05-01T10:00:00.123456789Z started server\n")...)
	stream = append(stream, frame(stderrStream, "2024-05-01T10:00:01.000000000Z \n")...)
	stream = append(stream, frame(stdoutStream, "not-a-timestamp message\n")...)
	got := demux(t, &chunkReader{data: stream, n: 5}, true)
	if len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Time == nil || *got[0].Time != 1714557600 || got[0].Line != "started server" {
		t.Errorf("first line = %+v", got[0])
	}
	if got[1].Time == nil || *got[1].Time != 1714557601 || got[1].Line != "" || got[1].Stream != "stderr" {
		t.Errorf("empty line with timestamp = %+v", got[1])
	}
	if got[2].Time != nil || got[2].Line != "not-a-timestamp message" {
		t.Errorf("line without timestamp = %+v", got[2])
	}
}

func TestDemultiplexKeepsContentOfVeryLongLines(t *testing.T) {
	long := strings.Repeat("x", 3*logLineMax+17)
	stream := append(frame(stdoutStream, long[:20000]), frame(stdoutStream, long[20000:]+"\nnext\n")...)
	got := demux(t, &chunkReader{data: stream, n: 4096}, false)
	if len(got) < 2 {
		t.Fatalf("got %d events", len(got))
	}
	var joined strings.Builder
	for _, l := range got[:len(got)-1] {
		if len(l.Line) > 2*logLineMax+4096 {
			t.Errorf("a single event carries %d bytes", len(l.Line))
		}
		joined.WriteString(l.Line)
	}
	if joined.String() != long {
		t.Errorf("long line content changed: %d bytes in, %d bytes out", len(long), joined.Len())
	}
	if got[len(got)-1].Line != "next" {
		t.Errorf("line after the long line = %q", got[len(got)-1].Line)
	}
}

func TestInvalidUTF8IsReplaced(t *testing.T) {
	got := demux(t, bytes.NewReader(frame(stdoutStream, "bad \xff\xfe bytes\n")), false)
	if len(got) != 1 || !strings.HasPrefix(got[0].Line, "bad ") || !strings.HasSuffix(got[0].Line, " bytes") {
		t.Fatalf("got %+v", got)
	}
	if strings.Contains(got[0].Line, "\xff") {
		t.Error("invalid bytes were passed through")
	}
}

/* ---------- through the HTTP handler ---------- */

func inspectJSON(id, name string, tty bool) string {
	return `{"Id":"` + id + `","Name":"/` + name + `","Created":"2024-05-01T09:00:00Z",
		"State":{"Status":"running","Running":true,"StartedAt":"2024-05-01T10:00:00.5Z","FinishedAt":"0001-01-01T00:00:00Z"},
		"Config":{"Tty":` + strconv.FormatBool(tty) + `,"Image":"nginx:latest"},
		"HostConfig":{"RestartPolicy":{"Name":"unless-stopped"}}}`
}

// chunkedBody writes the parts one by one, flushing between them.
func chunkedBody(contentType string, parts ...[]byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(200)
		for _, p := range parts {
			w.Write(p)
			w.(http.Flusher).Flush()
		}
	}
}

func TestLogStreamOfPlainContainer(t *testing.T) {
	fake := newFakeDocker(t)
	fake.handleJSON("GET /containers/web/json", 200, inspectJSON(idWeb, "web", false))
	f1 := frame(stdoutStream, "first\nsec")
	f2 := frame(stderrStream, "problem\n")
	f3 := frame(stdoutStream, "ond\nlast without newline")
	// Headers and payloads are cut in the middle on purpose.
	fake.handle("GET /containers/web/logs", chunkedBody("application/vnd.docker.multiplexed-stream",
		f1[:3], f1[3:10], append(bytes.Clone(f1[10:]), f2[:5]...), f2[5:], f3[:8], f3[8:]))
	e := newTestEnv(t, fake.host())

	w := e.do("GET", "/api/v1/docker/containers/web/logs/stream", e.admin, "")
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content type %q", ct)
	}
	events := parseSSE(w.Body.String())
	if len(events) == 0 || events[0].Name != "start" || events[0].Data != `{"tail":200,"tty":false}` {
		t.Fatalf("first event = %+v", events)
	}
	last := events[len(events)-1]
	if last.Name != "end" || last.Data != `{"reason":"stopped"}` {
		t.Errorf("last event = %+v", last)
	}
	want := []logOut{
		{"stdout", "first", nil},
		{"stderr", "problem", nil},
		{"stdout", "second", nil},
		{"stdout", "last without newline", nil},
	}
	if got := logEvents(t, w.Body.String()); !reflect.DeepEqual(got, want) {
		t.Errorf("log events:\n got %+v\nwant %+v", got, want)
	}

	line, ok := fake.seenPath("GET", "/containers/web/logs")
	if !ok {
		t.Fatalf("logs endpoint not called: %v", fake.seen())
	}
	for _, q := range []string{"follow=1", "stdout=1", "stderr=1", "tail=200"} {
		if !strings.Contains(line, q) {
			t.Errorf("log request %q lacks %q", line, q)
		}
	}
	if strings.Contains(line, "timestamps=1") {
		t.Errorf("timestamps were not requested: %q", line)
	}

	// Reading logs is recorded.
	var found bool
	for _, a := range e.auditRows() {
		if a.Action == "docker.container_logs" && a.Target == "web" && a.Username == "yonetici" && a.Success {
			found = true
		}
	}
	if !found {
		t.Errorf("no audit record for reading logs: %+v", e.auditRows())
	}
}

func TestLogStreamOfTTYContainerIsNotDemultiplexed(t *testing.T) {
	fake := newFakeDocker(t)
	fake.handleJSON("GET /containers/web/json", 200, inspectJSON(idWeb, "web", true))
	// A raw TTY stream. The second line starts with bytes that look like a
	// frame header; in TTY mode they are content.
	raw := []byte("prompt$ ls\r\n\x01\x00\x00\x00\x00\x00\x00\x02hi\r\nbye")
	fake.handle("GET /containers/web/logs", chunkedBody("application/vnd.docker.raw-stream", raw[:5], raw[5:14], raw[14:]))
	e := newTestEnv(t, fake.host())

	w := e.do("GET", "/api/v1/docker/containers/web/logs/stream?tail=50&timestamps=false", e.admin, "")
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	events := parseSSE(w.Body.String())
	if len(events) == 0 || events[0].Data != `{"tail":50,"tty":true}` {
		t.Fatalf("first event = %+v", events)
	}
	want := []logOut{
		{"stdout", "prompt$ ls", nil},
		{"stdout", "\x01\x00\x00\x00\x00\x00\x00\x02hi", nil},
		{"stdout", "bye", nil},
	}
	if got := logEvents(t, w.Body.String()); !reflect.DeepEqual(got, want) {
		t.Errorf("log events:\n got %+v\nwant %+v", got, want)
	}
	if events[len(events)-1].Name != "end" {
		t.Errorf("last event = %+v", events[len(events)-1])
	}
}

func TestLogStreamParameters(t *testing.T) {
	fake := newFakeDocker(t)
	fake.handleJSON("GET /containers/web/json", 200, inspectJSON(idWeb, "web", false))
	fake.handle("GET /containers/web/logs", chunkedBody("application/vnd.docker.multiplexed-stream",
		frame(stdoutStream, "2024-05-01T10:00:00.000000001Z with time\n")))
	e := newTestEnv(t, fake.host())

	for _, q := range []string{"tail=-1", "tail=abc", "tail=1.5", "timestamps=maybe"} {
		w := e.do("GET", "/api/v1/docker/containers/web/logs/stream?"+q, e.admin, "")
		if w.Code != 400 || errorCode(parseEnvelope(t, w.Body.Bytes())) != "bad_request" {
			t.Errorf("%s: status %d body %s", q, w.Code, w.Body)
		}
	}
	if got := fake.seen(); len(got) != 0 {
		t.Errorf("invalid parameters reached Docker: %v", got)
	}

	w := e.do("GET", "/api/v1/docker/containers/web/logs/stream?tail=999999&timestamps=true", e.admin, "")
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	line, _ := fake.seenPath("GET", "/containers/web/logs")
	if !strings.Contains(line, "tail=2000") || !strings.Contains(line, "timestamps=1") {
		t.Errorf("request = %q, want tail capped to 2000 and timestamps", line)
	}
	got := logEvents(t, w.Body.String())
	if len(got) != 1 || got[0].Time == nil || *got[0].Time != 1714557600 || got[0].Line != "with time" {
		t.Errorf("log events = %+v", got)
	}
}

func TestLogStreamOfMissingContainer(t *testing.T) {
	fake := newFakeDocker(t)
	e := newTestEnv(t, fake.host())
	w := e.do("GET", "/api/v1/docker/containers/ghost/logs/stream", e.admin, "")
	env := parseEnvelope(t, w.Body.Bytes())
	if w.Code != 404 || errorCode(env) != "not_found" || env.Error.Message != "Konteyner bulunamadı." {
		t.Errorf("status %d body %s", w.Code, w.Body)
	}
}
