package files

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

/* ---------- uploads ---------- */

func TestUploadNameValidation(t *testing.T) {
	e := newEnv(t)
	e.mkdir(e.in("up"))
	check := e.guard()

	// Names that must be refused outright.
	for _, name := range []string{
		"..", ".", "", " ", "a\\b.txt", "..\\evil.txt", "..\\..\\outside\\evil.txt", "C:\\evil.txt",
		" lead.txt", "trail.txt ", strings.Repeat("n", 256), strings.Repeat("ğ", 128),
		"ctl\x01.txt", "del\x7f.txt", "tab\t.txt",
	} {
		w := e.upload(e.in("up"), name, []byte("data"), true)
		if w.Code == http.StatusCreated {
			t.Errorf("upload with file name %.40q was accepted: %s", name, w.Body)
		}
		if got := names(t, e.in("up")); len(got) != 0 {
			t.Errorf("upload with file name %.40q left %v", name, got)
			os.RemoveAll(e.in("up"))
			e.mkdir(e.in("up"))
		}
		check()
	}

	// Names carrying a directory part: whatever the server decides, the
	// file may only ever appear directly inside the target directory.
	for _, name := range []string{
		"../evil.txt", "../../outside/evil.txt", "/etc/evil.txt", "sub/evil.txt", "a/../../evil.txt",
		e.outside + "/evil.txt", "../outside/secret.txt", "./evil.txt", "evil.txt/", "evil.txt/..", "../",
	} {
		w := e.upload(e.in("up"), name, []byte("data"), true)
		check()
		if exists(e.in("evil.txt")) || exists(e.in("secret.txt")) {
			t.Errorf("upload %q escaped into the parent directory", name)
		}
		for _, n := range names(t, e.in("up")) {
			if n != "evil.txt" && n != "secret.txt" {
				t.Errorf("upload %q (status %d) created %q", name, w.Code, n)
			}
			fi, _ := os.Lstat(e.in("up", n))
			if !fi.Mode().IsRegular() {
				t.Errorf("upload %q created a non-regular entry %q", name, n)
			}
			os.RemoveAll(e.in("up", n))
		}
	}

	// NUL and raw control bytes in the header.
	for _, name := range []string{"a\x00b.txt", "evil.txt\x00.png", "\x00"} {
		w := e.upload(e.in("up"), name, []byte("data"), true)
		if w.Code == http.StatusCreated {
			t.Errorf("upload with NUL in the file name was accepted: %s", w.Body)
		}
		if got := names(t, e.in("up")); len(got) != 0 {
			t.Errorf("upload with NUL in the name left %v", got)
			os.RemoveAll(e.in("up"))
			e.mkdir(e.in("up"))
		}
	}

	// Legitimate awkward names work.
	for _, name := range []string{"rapor 2024 (son).pdf", "türkçe-ğüşıöç.txt", ".gizli", "a'b;c&d.txt", `quo"te.txt`, strings.Repeat("n", 255)} {
		w := e.upload(e.in("up"), name, []byte("data"), false)
		if w.Code != http.StatusCreated {
			t.Errorf("upload %.40q: %d %s", name, w.Code, w.Body)
			continue
		}
		if readFile(t, e.in("up", name)) != "data" {
			t.Errorf("upload %.40q: wrong content", name)
		}
	}
	if l := leftovers(t, e.root); len(l) > 0 {
		t.Errorf("temporary files left: %v", l)
	}
	check()
}

func TestUploadTargets(t *testing.T) {
	e := newEnv(t)
	e.write(e.in("up", "file.txt"), "x")
	e.mkdir(e.in("up", "dir"))
	refused(t, "upload into a file", e.upload(e.in("up", "file.txt"), "a.txt", []byte("d"), true), 400, 404)
	refused(t, "upload into a missing directory", e.upload(e.in("missing"), "a.txt", []byte("d"), true), 404)
	refused(t, "upload onto a directory", e.upload(e.in("up"), "dir", []byte("d"), true), 409)
	if fi, err := os.Stat(e.in("up", "dir")); err != nil || !fi.IsDir() {
		t.Error("an upload replaced a directory")
	}
	// No file part at all.
	body := "--" + testBoundary + "\r\nContent-Disposition: form-data; name=\"note\"\r\n\r\nhello\r\n--" + testBoundary + "--\r\n"
	refused(t, "upload without a file", e.uploadRaw(e.admin, e.in("up"), false, strings.NewReader(body), int64(len(body))), 400)
	// Not multipart.
	r := httptest.NewRequest("POST", apiURL("/upload", url.Values{"path": {e.in("up")}}), strings.NewReader("raw"))
	r.Header.Set("Content-Type", "application/octet-stream")
	refused(t, "upload that is not multipart", e.send(e.admin, r), 400)
	if l := leftovers(t, e.root); len(l) > 0 {
		t.Errorf("temporary files left: %v", l)
	}
}

func TestUploadOverwrite(t *testing.T) {
	e := newEnv(t)
	e.write(e.in("up", "doc.txt"), "original")
	os.Chmod(e.in("up", "doc.txt"), 0o600)

	w := e.upload(e.in("up"), "doc.txt", []byte("replacement"), false)
	if w.Code != http.StatusConflict {
		t.Errorf("upload onto an existing file: %d, want 409", w.Code)
	}
	if got := readFile(t, e.in("up", "doc.txt")); got != "original" {
		t.Fatalf("DATA LOSS: the existing file was overwritten without being asked: %q", got)
	}
	// Any value other than "true" is not a request to overwrite.
	for _, v := range []string{"1", "yes", "TRUE", "false", ""} {
		b := multipartBody("doc.txt", []byte("replacement"))
		r := httptest.NewRequest("POST", apiURL("/upload", url.Values{"path": {e.in("up")}, "overwrite": {v}}), bytes.NewReader(b))
		r.Header.Set("Content-Type", "multipart/form-data; boundary="+testBoundary)
		if w := e.send(e.admin, r); w.Code != http.StatusConflict && v != "TRUE" && v != "1" && v != "yes" {
			t.Errorf("overwrite=%q: %d, want 409", v, w.Code)
		}
	}
	if got := readFile(t, e.in("up", "doc.txt")); got != "original" && got != "replacement" {
		t.Fatalf("unexpected content %q", got)
	}
	e.write(e.in("up", "doc.txt"), "original")

	w = e.upload(e.in("up"), "doc.txt", []byte("replacement"), true)
	if w.Code != http.StatusCreated {
		t.Fatalf("upload with overwrite: %d %s", w.Code, w.Body)
	}
	if got := readFile(t, e.in("up", "doc.txt")); got != "replacement" {
		t.Errorf("after overwrite the file reads %q", got)
	}
	var res []uploadResult
	json.Unmarshal(decode(t, w).Data, &res)
	if len(res) != 1 || res[0].Size != int64(len("replacement")) || res[0].Path != e.in("up", "doc.txt") {
		t.Errorf("upload result: %+v", res)
	}

	// Two parts, the second collides: the first is stored, the second is
	// refused and the existing file survives.
	e.write(e.in("up2", "b.txt"), "original")
	var b bytes.Buffer
	for _, n := range []string{"a.txt", "b.txt"} {
		b.WriteString("--" + testBoundary + "\r\nContent-Disposition: form-data; name=\"file\"; filename=\"" + n + "\"\r\n\r\nnew\r\n")
	}
	b.WriteString("--" + testBoundary + "--\r\n")
	w = e.uploadRaw(e.admin, e.in("up2"), false, bytes.NewReader(b.Bytes()), int64(b.Len()))
	if w.Code != http.StatusConflict {
		t.Errorf("colliding second part: %d, want 409", w.Code)
	}
	if readFile(t, e.in("up2", "b.txt")) != "original" {
		t.Error("DATA LOSS: colliding part overwrote the file")
	}
	if l := leftovers(t, e.root); len(l) > 0 {
		t.Errorf("temporary files left: %v", l)
	}
}

func TestUploadSizeLimit(t *testing.T) {
	e := newEnv(t)
	e.setting(KeyMaxUploadMB, "1")
	e.mkdir(e.in("up"))
	const mb = 1 << 20

	if w := e.upload(e.in("up"), "exact.bin", bytes.Repeat([]byte("x"), mb), false); w.Code != http.StatusCreated {
		t.Errorf("upload of exactly the limit: %d %s", w.Code, w.Body)
	}
	os.Remove(e.in("up", "exact.bin"))

	for name, size := range map[string]int{"one byte over": mb + 1, "1.5x": mb + mb/2, "3x": 3 * mb} {
		data := bytes.Repeat([]byte("x"), size)
		// With a declared length.
		w := e.upload(e.in("up"), "big.bin", data, true)
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s: status %d, want 413", name, w.Code)
		}
		// Chunked, so nothing announces the size.
		w = e.uploadRaw(e.admin, e.in("up"), true, io.MultiReader(bytes.NewReader(multipartBody("big.bin", data))), -1)
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s chunked: status %d, want 413", name, w.Code)
		}
		// With a length that lies.
		w = e.uploadRaw(e.admin, e.in("up"), true, io.MultiReader(bytes.NewReader(multipartBody("big.bin", data))), 10)
		if w.Code == http.StatusCreated {
			t.Errorf("%s with a false Content-Length was stored", name)
		}
		if got := names(t, e.in("up")); len(got) != 0 {
			t.Fatalf("%s: a refused upload left %v", name, got)
		}
	}

	// The limit covers the whole request, not each part.
	var b bytes.Buffer
	for _, n := range []string{"p1.bin", "p2.bin", "p3.bin"} {
		b.WriteString("--" + testBoundary + "\r\nContent-Disposition: form-data; name=\"file\"; filename=\"" + n + "\"\r\n\r\n")
		b.Write(bytes.Repeat([]byte("x"), mb/2-100))
		b.WriteString("\r\n")
	}
	b.WriteString("--" + testBoundary + "--\r\n")
	w := e.uploadRaw(e.admin, e.in("up"), false, bytes.NewReader(b.Bytes()), int64(b.Len()))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("three parts exceeding the limit together: %d, want 413", w.Code)
	}
	if exists(e.in("up", "p3.bin")) {
		t.Error("the part that exceeded the limit was stored")
	}
	if l := leftovers(t, e.root); len(l) > 0 {
		t.Errorf("temporary files left: %v", l)
	}

	// An existing file survives a too-large overwrite.
	e.write(e.in("keep", "big.bin"), "original")
	e.upload(e.in("keep"), "big.bin", bytes.Repeat([]byte("x"), 2*mb), true)
	if readFile(t, e.in("keep", "big.bin")) != "original" {
		t.Error("DATA LOSS: a refused upload destroyed the existing file")
	}
}

// brokenReader delivers data and then fails like a dropped connection.
type brokenReader struct {
	data []byte
	err  error
}

func (b *brokenReader) Read(p []byte) (int, error) {
	if len(b.data) == 0 {
		return 0, b.err
	}
	n := copy(p, b.data)
	if n > 64<<10 {
		n = 64 << 10
	}
	b.data = b.data[n:]
	return n, nil
}

func TestUploadCutOff(t *testing.T) {
	e := newEnv(t)
	e.write(e.in("up", "existing.bin"), "original")
	full := multipartBody("existing.bin", bytes.Repeat([]byte("payload-"), 100000))
	fresh := multipartBody("fresh.bin", bytes.Repeat([]byte("payload-"), 100000))

	for name, errv := range map[string]error{
		"connection reset": errors.New("connection reset by peer"),
		"unexpected eof":   io.ErrUnexpectedEOF,
		"clean eof":        io.EOF,
	} {
		for _, cut := range []int{200, len(full) / 2, len(full) - 10} {
			for _, body := range [][]byte{full, fresh} {
				w := e.uploadRaw(e.admin, e.in("up"), true, &brokenReader{data: body[:cut], err: errv}, int64(len(body)))
				if w.Code < 400 || w.Code >= 500 {
					t.Errorf("%s at %d: status %d, want a 4xx", name, cut, w.Code)
				}
				if got := names(t, e.in("up")); len(got) != 1 || got[0] != "existing.bin" {
					t.Fatalf("%s at %d: directory now holds %v", name, cut, got)
				}
				if readFile(t, e.in("up", "existing.bin")) != "original" {
					t.Fatalf("DATA LOSS: %s at %d: an interrupted upload replaced the existing file", name, cut)
				}
			}
		}
	}
}

/* ---------- downloads ---------- */

func TestDownloadHeaders(t *testing.T) {
	e := newEnv(t)
	payloads := map[string]string{
		"page.html":                 "<html><script>alert(1)</script></html>",
		"page.HTM":                  "<script>alert(1)</script>",
		"image.svg":                 `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`,
		"doc.xml":                   "<?xml version='1.0'?><x/>",
		"app.js":                    "alert(1)",
		"doc.pdf":                   "%PDF-1.4",
		"noext":                     "<html><script>alert(1)</script>",
		`with "quotes".txt`:         "q",
		"new\nline.txt":             "n",
		"cr\r\nSet-Cookie: x=1.txt": "c",
		"semi;colon.txt":            "s",
		"türkçe dosya.txt":          "t",
		"日本語.html":                  "<script>1</script>",
		"per%cent.txt":              "p",
		"back\\slash.txt":           "b",
	}
	for name, content := range payloads {
		e.write(e.in("dl", name), content)
	}
	for name, content := range payloads {
		w := e.get(e.admin, "/download", e.in("dl", name))
		if w.Code != 200 || w.Body.String() != content {
			t.Errorf("%q: status %d body %q", name, w.Code, w.Body)
			continue
		}
		h := w.Header()
		if got := h.Get("Content-Type"); got != "application/octet-stream" {
			t.Errorf("%q: Content-Type %q, want application/octet-stream", name, got)
		}
		if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%q: X-Content-Type-Options %q", name, got)
		}
		if got := h.Get("Content-Security-Policy"); !strings.Contains(got, "sandbox") || !strings.Contains(got, "default-src 'none'") {
			t.Errorf("%q: Content-Security-Policy %q", name, got)
		}
		if got := h.Get("Set-Cookie"); got != "" {
			t.Errorf("%q: header injection, Set-Cookie = %q", name, got)
		}
		for k, vs := range h {
			for _, v := range vs {
				if strings.ContainsAny(v, "\r\n") {
					t.Errorf("%q: header %s contains a line break: %q", name, k, v)
				}
			}
		}
		kind, params, err := mime.ParseMediaType(h.Get("Content-Disposition"))
		if err != nil || kind != "attachment" || params["filename"] != name {
			t.Errorf("%q: Content-Disposition %q parsed as %q %v %v", name, h.Get("Content-Disposition"), kind, params, err)
		}
	}

	// Preview refuses anything that could run.
	for name := range payloads {
		w := e.get(e.admin, "/preview", e.in("dl", name))
		if w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("preview of %q: status %d, want 415", name, w.Code)
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("preview of %q answered with Content-Type %q", name, ct)
		}
	}

	// An HTML document disguised as an image is served as that image type
	// with sniffing disabled, never as HTML.
	e.write(e.in("dl", "fake.png"), "<html><script>alert(1)</script></html>")
	e.write(e.in("dl", "fake.html.JPG"), "<html><script>alert(1)</script></html>")
	for name, want := range map[string]string{"fake.png": "image/png", "fake.html.JPG": "image/jpeg"} {
		w := e.get(e.admin, "/preview", e.in("dl", name))
		if w.Code != 200 {
			t.Errorf("preview %s: %d", name, w.Code)
			continue
		}
		h := w.Header()
		if h.Get("Content-Type") != want || h.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("preview %s: Content-Type %q nosniff %q", name, h.Get("Content-Type"), h.Get("X-Content-Type-Options"))
		}
		if !strings.Contains(h.Get("Content-Security-Policy"), "sandbox") {
			t.Errorf("preview %s: no sandbox policy", name)
		}
		if kind, _, _ := mime.ParseMediaType(h.Get("Content-Disposition")); kind != "inline" {
			t.Errorf("preview %s: disposition %q", name, h.Get("Content-Disposition"))
		}
	}

	// A directory download is a ZIP with safe headers too.
	e.write(e.in("evil\"dir;x", "f.txt"), "f")
	w := e.get(e.admin, "/download", e.in("evil\"dir;x"))
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/zip" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("directory download: %d %v", w.Code, w.Header())
	}
	if _, params, err := mime.ParseMediaType(w.Header().Get("Content-Disposition")); err != nil || params["filename"] != "evil\"dir;x.zip" {
		t.Errorf("directory download disposition: %q", w.Header().Get("Content-Disposition"))
	}
	assertZipClean(t, w.Body.Bytes(), "evil\"dir;x/f.txt")
}

func TestDownloadRange(t *testing.T) {
	e := newEnv(t)
	content := "0123456789abcdefghijklmnopqrstuvwxyz"
	e.write(e.in("r.bin"), content)
	do := func(endpoint, p, rng string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", apiURL(endpoint, url.Values{"path": {p}}), nil)
		if rng != "" {
			r.Header.Set("Range", rng)
		}
		return e.send(e.admin, r)
	}
	cases := []struct {
		rng, body, contentRange string
		status                  int
	}{
		{"", content, "", 200},
		{"bytes=0-9", "0123456789", "bytes 0-9/36", 206},
		{"bytes=10-", content[10:], "bytes 10-35/36", 206},
		{"bytes=-6", "uvwxyz", "bytes 30-35/36", 206},
		{"bytes=30-999", "uvwxyz", "bytes 30-35/36", 206},
		{"bytes=5-5", "5", "bytes 5-5/36", 206},
		{"bytes=36-40", "", "bytes */36", 416},
		{"bytes=9-2", "", "", 416},
	}
	for _, c := range cases {
		w := do("/download", e.in("r.bin"), c.rng)
		if w.Code != c.status {
			t.Errorf("Range %q: status %d, want %d", c.rng, w.Code, c.status)
			continue
		}
		if c.status < 400 && w.Body.String() != c.body {
			t.Errorf("Range %q: body %q, want %q", c.rng, w.Body, c.body)
		}
		if c.contentRange != "" && w.Header().Get("Content-Range") != c.contentRange {
			t.Errorf("Range %q: Content-Range %q, want %q", c.rng, w.Header().Get("Content-Range"), c.contentRange)
		}
		if w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("Range %q: nosniff missing", c.rng)
		}
		if c.status == 206 && w.Header().Get("Content-Type") != "application/octet-stream" {
			t.Errorf("Range %q: Content-Type %q", c.rng, w.Header().Get("Content-Type"))
		}
	}
	if w := do("/download", e.in("r.bin"), ""); w.Header().Get("Accept-Ranges") != "bytes" {
		t.Errorf("Accept-Ranges: %q", w.Header().Get("Accept-Ranges"))
	}
	// Multiple ranges produce multipart/byteranges; the parts must not
	// reintroduce a sniffable type.
	w := do("/download", e.in("r.bin"), "bytes=0-1,4-5")
	if w.Code == 206 && strings.Contains(strings.ToLower(w.Body.String()), "text/html") {
		t.Errorf("multi-range response: %s", w.Body)
	}
	e.write(e.in("clip.mp4"), content)
	w = do("/preview", e.in("clip.mp4"), "bytes=2-5")
	if w.Code != 206 || w.Body.String() != "2345" || w.Header().Get("Content-Type") != "video/mp4" {
		t.Errorf("preview range: %d %q %q", w.Code, w.Body, w.Header().Get("Content-Type"))
	}
}

/* ---------- text editor ---------- */

func TestTextReadWrite(t *testing.T) {
	e := newEnv(t)
	e.write(e.in("t", "a.txt"), "merhaba")
	os.Chmod(e.in("t", "a.txt"), 0o600)
	e.write(e.in("t", "bin.dat"), "a\x00b")
	e.write(e.in("t", "big.txt"), strings.Repeat("x", textMaxBytes+1))

	w := e.get(e.admin, "/text", e.in("t", "a.txt"))
	var rd struct {
		Content    string `json:"content"`
		ModifiedAt int64  `json:"modified_at"`
	}
	json.Unmarshal(decode(t, w).Data, &rd)
	if w.Code != 200 || rd.Content != "merhaba" {
		t.Fatalf("read: %d %s", w.Code, w.Body)
	}
	refused(t, "binary", e.get(e.admin, "/text", e.in("t", "bin.dat")), 415)
	refused(t, "too large", e.get(e.admin, "/text", e.in("t", "big.txt")), 413)
	refused(t, "directory", e.get(e.admin, "/text", e.in("t")), 400)

	put := func(body map[string]any) *httptest.ResponseRecorder {
		return e.call(e.admin, "PUT", "/text", body)
	}
	if w := put(map[string]any{"path": e.in("t", "a.txt"), "content": "dünya", "modified_at": rd.ModifiedAt}); w.Code != 200 {
		t.Fatalf("write: %d %s", w.Code, w.Body)
	}
	if readFile(t, e.in("t", "a.txt")) != "dünya" {
		t.Error("the edit was not stored")
	}
	if fi, _ := os.Stat(e.in("t", "a.txt")); fi.Mode().Perm() != 0o600 {
		t.Errorf("the edit changed the permissions to %o", fi.Mode().Perm())
	}
	// Stale editor: someone else changed the file.
	stale := rd.ModifiedAt - 100
	if w := put(map[string]any{"path": e.in("t", "a.txt"), "content": "lost update", "modified_at": stale}); w.Code != 409 {
		t.Errorf("stale write: %d, want 409", w.Code)
	}
	if readFile(t, e.in("t", "a.txt")) != "dünya" {
		t.Error("DATA LOSS: a stale edit overwrote the file")
	}
	refused(t, "write too large", put(map[string]any{"path": e.in("t", "a.txt"), "content": strings.Repeat("x", textMaxBytes+1)}), 413)
	refused(t, "write binary", put(map[string]any{"path": e.in("t", "a.txt"), "content": "a\x00b"}), 400)
	refused(t, "write onto a directory", put(map[string]any{"path": e.in("t"), "content": "x"}), 400, 409)
	refused(t, "write onto the root", put(map[string]any{"path": e.root, "content": "x"}), 400)
	refused(t, "write into a missing directory", put(map[string]any{"path": e.in("none", "a.txt"), "content": "x"}), 404)
	if readFile(t, e.in("t", "a.txt")) != "dünya" {
		t.Error("a refused write changed the file")
	}
	if fi, err := os.Stat(e.in("t")); err != nil || !fi.IsDir() {
		t.Error("a write replaced a directory")
	}
	if l := leftovers(t, e.root); len(l) > 0 {
		t.Errorf("temporary files left: %v", l)
	}
}

/* ---------- simple operations ---------- */

func TestMkdirRenameChmodValidation(t *testing.T) {
	e := newEnv(t)
	e.write(e.in("d", "f.txt"), "x")
	e.write(e.in("d", "g.txt"), "g")
	check := e.guard()
	before := snapshot(t, e.root)
	for _, name := range []string{"", ".", "..", "a/b", "../x", "/abs", "a\\b", "a\x00b", "a\nb", strings.Repeat("x", 256), "../../outside/pwned", "g.txt"} {
		if w := e.post("/mkdir", map[string]string{"path": e.in("d"), "name": name}); w.Code == 201 || w.Code == 200 {
			t.Errorf("mkdir %.30q accepted", name)
		}
		if w := e.post("/rename", map[string]string{"path": e.in("d", "f.txt"), "name": name}); w.Code == 200 {
			t.Errorf("rename to %.30q accepted", name)
		}
	}
	for _, mode := range []string{"", "7777", "4755", "2755", "1777", "888", "abc", "-1", "0o755", "75", "07777", " 755", "755 ", "+x", "u+s"} {
		if w := e.post("/chmod", map[string]string{"path": e.in("d", "f.txt"), "mode": mode}); w.Code == 200 {
			t.Errorf("chmod %q accepted", mode)
		}
	}
	if got := snapshot(t, e.root); got != before {
		t.Errorf("refused operations changed the tree:\n%s", got)
	}
	if readFile(t, e.in("d", "g.txt")) != "g" {
		t.Error("DATA LOSS: rename replaced an existing file")
	}
	check()
	if w := e.post("/chmod", map[string]string{"path": e.in("d", "f.txt"), "mode": "0640"}); w.Code != 200 {
		t.Errorf("chmod 0640: %d %s", w.Code, w.Body)
	}
	if fi, _ := os.Stat(e.in("d", "f.txt")); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode is %o", fi.Mode().Perm())
	}
}

func TestMoveAndCopyConflicts(t *testing.T) {
	e := newEnv(t)
	reset := func() {
		os.RemoveAll(e.in("src"))
		os.RemoveAll(e.in("dst"))
		e.write(e.in("src", "a.txt"), "new")
		e.write(e.in("dst", "a.txt"), "old")
	}
	for _, ep := range []string{"/copy", "/move"} {
		reset()
		e.mustRun(ep, map[string]any{"paths": []string{e.in("src", "a.txt")}, "dest": e.in("dst")})
		if readFile(t, e.in("dst", "a.txt")) != "old" || readFile(t, e.in("dst", "a (2).txt")) != "new" {
			t.Errorf("%s default conflict handling: %v", ep, names(t, e.in("dst")))
		}
		reset()
		v := e.mustRun(ep, map[string]any{"paths": []string{e.in("src", "a.txt")}, "dest": e.in("dst"), "conflict": "skip"})
		if readFile(t, e.in("dst", "a.txt")) != "old" || v.Skipped != 1 || !exists(e.in("src", "a.txt")) {
			t.Errorf("%s skip: skipped %d", ep, v.Skipped)
		}
		reset()
		e.mustRun(ep, map[string]any{"paths": []string{e.in("src", "a.txt")}, "dest": e.in("dst"), "conflict": "overwrite"})
		if readFile(t, e.in("dst", "a.txt")) != "new" {
			t.Errorf("%s overwrite did not replace", ep)
		}
		if exists(e.in("src", "a.txt")) != (ep == "/copy") {
			t.Errorf("%s: source existence is wrong", ep)
		}
		reset()
		refused(t, ep+" bad conflict", e.post(ep, map[string]any{"paths": []string{e.in("src", "a.txt")}, "dest": e.in("dst"), "conflict": "destroy"}), 400)

		// A file must never replace a directory, nor the other way round.
		reset()
		e.write(e.in("dst", "thing", "inner.txt"), "precious")
		e.write(e.in("src", "thing"), "file")
		e.mustNotRun(ep+" file over directory", ep, map[string]any{"paths": []string{e.in("src", "thing")}, "dest": e.in("dst"), "conflict": "overwrite"})
		if readFile(t, e.in("dst", "thing", "inner.txt")) != "precious" {
			t.Fatalf("DATA LOSS: %s replaced a directory by a file", ep)
		}
		if !exists(e.in("src", "thing")) {
			t.Errorf("DATA LOSS: %s failed but removed the source", ep)
		}
		reset()
		e.write(e.in("dst", "thing"), "precious")
		e.write(e.in("src", "thing", "inner.txt"), "dir")
		e.mustNotRun(ep+" directory over file", ep, map[string]any{"paths": []string{e.in("src", "thing")}, "dest": e.in("dst"), "conflict": "overwrite"})
		if readFile(t, e.in("dst", "thing")) != "precious" {
			t.Fatalf("DATA LOSS: %s replaced a file by a directory", ep)
		}
		if !exists(e.in("src", "thing", "inner.txt")) {
			t.Errorf("DATA LOSS: %s failed but removed the source", ep)
		}
	}
	// Merging a moved directory keeps what only the destination has.
	reset()
	e.write(e.in("src", "m", "both.txt"), "new")
	e.write(e.in("src", "m", "only-src.txt"), "s")
	e.write(e.in("dst", "m", "both.txt"), "old")
	e.write(e.in("dst", "m", "only-dst.txt"), "d")
	e.mustRun("/move", map[string]any{"paths": []string{e.in("src", "m")}, "dest": e.in("dst"), "conflict": "overwrite"})
	if readFile(t, e.in("dst", "m", "both.txt")) != "new" || readFile(t, e.in("dst", "m", "only-dst.txt")) != "d" ||
		readFile(t, e.in("dst", "m", "only-src.txt")) != "s" || exists(e.in("src", "m")) {
		t.Errorf("merge move: dst %v", names(t, e.in("dst", "m")))
	}
	if l := leftovers(t, e.root); len(l) > 0 {
		t.Errorf("temporary files left: %v", l)
	}
}

// Moving between two allowed roots is a copy followed by a delete. The
// source may only disappear after everything arrived.
func TestMoveAcrossRoots(t *testing.T) {
	e := newEnv(t, "allowed-second")
	second := e.base + "/allowed-second"
	e.mkdir(second)
	e.write(e.in("m", "a.txt"), "a")
	e.write(e.in("m", "sub", "b.txt"), "b")
	e.symlink("a.txt", e.in("m", "link"))
	os.Chmod(e.in("m", "sub", "b.txt"), 0o600)
	check := e.guard()

	e.mustRun("/move", map[string]any{"paths": []string{e.in("m")}, "dest": second})
	if exists(e.in("m")) {
		t.Error("the source still exists after the move")
	}
	if readFile(t, second+"/m/a.txt") != "a" || readFile(t, second+"/m/sub/b.txt") != "b" {
		t.Fatal("DATA LOSS: moved files are missing")
	}
	if fi, _ := os.Stat(second + "/m/sub/b.txt"); fi.Mode().Perm() != 0o600 {
		t.Errorf("the move changed permissions to %o", fi.Mode().Perm())
	}
	if target, err := os.Readlink(second + "/m/link"); err != nil || target != "a.txt" {
		t.Errorf("moved link: %q %v", target, err)
	}

	// The copy fails half way: "z" is a directory in the source and a file
	// in the destination. Nothing may be removed from the source.
	e.write(e.in("m", "a.txt"), "a2")
	e.write(e.in("m", "z", "deep.txt"), "deep")
	os.RemoveAll(second + "/m")
	e.write(second+"/m/z", "a file")
	e.mustNotRun("merge with a type clash", "/move", map[string]any{"paths": []string{e.in("m")}, "dest": second, "conflict": "overwrite"})
	if readFile(t, e.in("m", "a.txt")) != "a2" || readFile(t, e.in("m", "z", "deep.txt")) != "deep" {
		t.Fatal("DATA LOSS: a failed move removed files from the source")
	}
	if readFile(t, second+"/m/z") != "a file" {
		t.Error("DATA LOSS: a failed move replaced a file in the destination")
	}
	if l := leftovers(t, e.base); len(l) > 0 {
		t.Errorf("temporary files left: %v", l)
	}
	check()
}

// Deleting a link to a directory must leave the directory's content alone.
func TestDeleteLinkKeepsTarget(t *testing.T) {
	e := newEnv(t)
	e.write(e.in("real", "precious.txt"), "precious")
	e.symlink("real", e.in("alias"))
	e.symlink("../real", e.in("box", "inner-alias"))
	e.symlink("real/precious.txt", e.in("filealias"))
	e.mustRun("/delete", map[string]any{"paths": []string{e.in("alias"), e.in("box"), e.in("filealias")}})
	if exists(e.in("alias")) || exists(e.in("box")) || exists(e.in("filealias")) {
		t.Error("the links were not removed")
	}
	if !exists(e.in("real", "precious.txt")) || readFile(t, e.in("real", "precious.txt")) != "precious" {
		t.Fatal("DATA LOSS: deleting a link deleted the content of its target")
	}
	// A trailing slash must not turn the link into its target.
	e.symlink("real", e.in("alias"))
	e.mustRun("/delete", map[string]any{"paths": []string{e.in("alias") + "/"}})
	if !exists(e.in("real", "precious.txt")) {
		t.Fatal("DATA LOSS: deleting \"link/\" deleted the content of its target")
	}
}
