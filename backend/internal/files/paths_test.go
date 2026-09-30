package files

import (
	"mime"
	"strings"
	"testing"
)

func TestResolveAccepts(t *testing.T) {
	roots := []string{"/home", "/data", "/data/media"}
	cases := []struct{ in, root, rel, path string }{
		{"/home", "/home", ".", "/home"},
		{"/home/", "/home", ".", "/home"},
		{"/home/./", "/home", ".", "/home"},
		{"/home/ali/belge.txt", "/home", "ali/belge.txt", "/home/ali/belge.txt"},
		{"/home//ali///x", "/home", "ali/x", "/home/ali/x"},
		{"/data/media/film.mkv", "/data/media", "film.mkv", "/data/media/film.mkv"},
		{"/data/mediax", "/data", "mediax", "/data/mediax"},
		{"/home/a..b/..c/d..", "/home", "a..b/..c/d..", "/home/a..b/..c/d.."},
		{"/home/ğüşıöç/日本語", "/home", "ğüşıöç/日本語", "/home/ğüşıöç/日本語"},
	}
	for _, c := range cases {
		loc, err := resolve(roots, c.in)
		if err != nil {
			t.Errorf("resolve(%q): unexpected error %v", c.in, err)
			continue
		}
		if loc.Root != c.root || loc.Rel != c.rel || loc.Path != c.path {
			t.Errorf("resolve(%q) = %+v, want root %q rel %q path %q", c.in, loc, c.root, c.rel, c.path)
		}
	}
}

func TestResolveRejects(t *testing.T) {
	roots := []string{"/home", "/data"}
	long := "/home/" + strings.Repeat("a/", 2100)
	cases := map[string]string{
		"empty":                   "",
		"relative":                "home/ali",
		"relative dot":            "./home",
		"dotdot first":            "/../home",
		"dotdot middle":           "/home/../etc/passwd",
		"dotdot last":             "/home/ali/..",
		"dotdot returning inside": "/home/ali/../veli",
		"dotdot trailing slash":   "/home/ali/../",
		"outside":                 "/etc/passwd",
		"filesystem root":         "/",
		"prefix sibling":          "/homes",
		"prefix sibling child":    "/homes/ali",
		"prefix sibling 2":        "/home2/x",
		"parent of root":          "/dat",
		"nul":                     "/home/a\x00b",
		"nul then dotdot":         "/home\x00/../etc",
		"invalid utf8":            "/home/\xff\xfe",
		"backslash start":         "\\home\\ali",
		"backslash dotdot":        "/home\\..\\etc",
		"too long":                long,
		"segment too long":        "/home/" + strings.Repeat("x", 256),
		"windows drive":           "C:/home",
		"url":                     "file:///home",
	}
	for name, p := range cases {
		if loc, err := resolve(roots, p); err == nil {
			t.Errorf("%s: resolve(%.60q) accepted as %+v", name, p, loc)
		}
	}
	if _, err := resolve(nil, "/home"); err == nil {
		t.Error("a path was accepted although no root is configured")
	}
	if _, err := resolve([]string{"/"}, "/etc/passwd"); err == nil {
		t.Error("the filesystem root was accepted as an allowed root")
	}
	if _, err := resolve([]string{"relative", "/a/../etc", "", "/x\x00"}, "/etc/passwd"); err == nil {
		t.Error("an invalid root configuration allowed access")
	}
}

func TestResolveSegmentLimit(t *testing.T) {
	if _, err := resolve([]string{"/home"}, "/home/"+strings.Repeat("x", 255)); err != nil {
		t.Errorf("a 255 byte name must be accepted: %v", err)
	}
	p := "/home/" + strings.Repeat(strings.Repeat("y", 200)+"/", 30)
	if len(p) <= maxPathLen {
		t.Fatal("test path is not long enough")
	}
	if _, err := resolve([]string{"/home"}, p); err == nil {
		t.Error("a path longer than 4096 bytes was accepted")
	}
}

func TestValidName(t *testing.T) {
	good := []string{"a", "belge.txt", ".gizli", "a b", "ğüş.txt", "..a", "a..", "...", "-rf", strings.Repeat("x", 255), "a:b", "a*b?"}
	for _, n := range good {
		if err := validName(n); err != nil {
			t.Errorf("validName(%q) refused: %v", n, err)
		}
	}
	bad := []string{"", ".", "..", "a/b", "/a", "a/", "../a", "a\\b", "..\\a", "a\x00b", "\x00", "a\nb", "a\rb", "a\tb",
		"a\x7fb", "\x1b[31m", " a", "a ", "\ta", strings.Repeat("x", 256), "\xff\xfe", strings.Repeat("ğ", 128)}
	for _, n := range bad {
		if err := validName(n); err == nil {
			t.Errorf("validName(%.40q) accepted", n)
		}
	}
}

func TestArchiveEntryName(t *testing.T) {
	good := map[string]string{
		"a.txt":         "a.txt",
		"dir/a.txt":     "dir/a.txt",
		"dir/":          "dir",
		"./a.txt":       "a.txt",
		"a//b":          "a/b",
		"a/./b":         "a/b",
		"dir\\sub\\a":   "dir/sub/a",
		"..a/b..":       "..a/b..",
		"ğ/日本語.txt":     "ğ/日本語.txt",
		"a/.../b":       "a/.../b",
		"name with spc": "name with spc",
	}
	for in, want := range good {
		got, ok := archiveEntryName(in)
		if !ok || got != want {
			t.Errorf("archiveEntryName(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	bad := []string{
		"", ".", "./", "/", "..", "../", "../x", "a/../x", "a/../../x", "a/..", "x/../../../etc/passwd",
		"/abs/x", "//abs/x", "/etc/passwd",
		"..\\x", "a\\..\\..\\x", "\\abs\\x", "\\\\server\\share\\x",
		"C:\\x", "C:/x", "c:x", "C:",
		"a\x00b", "a/b\x00", "a\nb", "a/\x1b/b", "a\x7f",
		"\xff\xfe", strings.Repeat("x", 256), "a/" + strings.Repeat("x", 256) + "/b",
		strings.Repeat("a/", 2100) + "x",
	}
	for _, in := range bad {
		if got, ok := archiveEntryName(in); ok {
			t.Errorf("archiveEntryName(%.50q) accepted as %q", in, got)
		}
	}
	// Whatever is accepted must be a clean relative path.
	for in := range good {
		got, _ := archiveEntryName(in)
		if strings.HasPrefix(got, "/") || hasDotDot(got) || strings.Contains(got, "\\") && !strings.Contains(in, "\\") {
			t.Errorf("archiveEntryName(%q) produced unsafe %q", in, got)
		}
	}
}

func TestWithinIsSegmentAware(t *testing.T) {
	cases := []struct {
		dir, p string
		want   bool
	}{
		{"/home", "/home", true},
		{"/home", "/home/a", true},
		{"/home", "/homes", false},
		{"/home", "/homes/a", false},
		{"/home/a", "/home", false},
	}
	for _, c := range cases {
		if got := within(c.dir, c.p); got != c.want {
			t.Errorf("within(%q, %q) = %v, want %v", c.dir, c.p, got, c.want)
		}
	}
}

func TestContentDisposition(t *testing.T) {
	awkward := []string{
		`plain.txt`,
		`with "quotes".txt`,
		"new\nline.txt",
		"carriage\rreturn\r\nX-Injected: 1.txt",
		`semi;colon.txt`,
		`back\slash.txt`,
		`per%cent%22.txt`,
		`türkçe dosya ğüşıöç.txt`,
		`日本語.pdf`,
		`emoji 😀.png`,
		`'single'.txt`,
		`a=b&c=d.txt`,
		`tab	here.txt`,
		`ğüş`,
		`"`,
		"\x00nul.txt",
		`filename*=UTF-8''evil.exe`,
		`x"; filename="evil.exe`,
	}
	for _, kind := range []string{"attachment", "inline"} {
		for _, name := range awkward {
			v := contentDisposition(kind, name)
			for i := 0; i < len(v); i++ {
				if v[i] < 0x20 || v[i] > 0x7e {
					t.Errorf("%q: header value contains byte 0x%02x: %q", name, v[i], v)
					break
				}
			}
			gotKind, params, err := mime.ParseMediaType(v)
			if err != nil {
				t.Errorf("%q: header %q does not parse: %v", name, v, err)
				continue
			}
			if gotKind != kind {
				t.Errorf("%q: disposition %q, want %q", name, gotKind, kind)
			}
			if params["filename"] != name {
				t.Errorf("%q: decoded file name is %q (header %q)", name, params["filename"], v)
			}
			if len(params) != 1 {
				t.Errorf("%q: unexpected parameters %v in %q", name, params, v)
			}
			// The plain fallback must be a single quoted string with nothing
			// that could end it early.
			start := strings.Index(v, `filename="`) + len(`filename="`)
			end := strings.Index(v[start:], `"`)
			fallback := v[start : start+end]
			if strings.ContainsAny(fallback, "\\;%/") || fallback == "" {
				t.Errorf("%q: unsafe fallback %q", name, fallback)
			}
			if rest := v[start+end:]; !strings.HasPrefix(rest, `"; filename*=UTF-8''`) {
				t.Errorf("%q: the fallback was terminated early: %q", name, v)
			}
		}
	}
}

func TestPreviewTypesAreInert(t *testing.T) {
	for _, name := range []string{
		"a.html", "a.htm", "a.HTML", "a.svg", "a.SVG", "a.svgz", "a.xml", "a.xhtml", "a.pdf", "a.js", "a.mjs",
		"a.css", "a.txt", "a.json", "a.php", "a.swf", "a.shtml", "a.xsl", "a", "a.png.html", "a.html.", ".svg", "a.svg ",
	} {
		if ct, ok := previewType(name); ok {
			t.Errorf("%q would be served inline as %q", name, ct)
		}
	}
	for ext, ct := range previewTypes {
		major := strings.SplitN(ct, "/", 2)[0]
		if major != "image" && major != "audio" && major != "video" {
			t.Errorf("preview type for %s is %q", ext, ct)
		}
		if strings.Contains(ct, "svg") || strings.Contains(ct, "xml") || strings.Contains(ct, "html") {
			t.Errorf("preview type for %s is scriptable: %q", ext, ct)
		}
	}
}

func TestSplitExt(t *testing.T) {
	cases := []struct{ in, base, ext string }{
		{"a.txt", "a", ".txt"},
		{"a.tar.gz", "a", ".tar.gz"},
		{"A.TAR.GZ", "A", ".TAR.GZ"},
		{".bashrc", ".bashrc", ""},
		{"noext", "noext", ""},
		{".tar.gz", ".tar", ".gz"},
		{"a.b.c", "a.b", ".c"},
	}
	for _, c := range cases {
		b, e := splitExt(c.in)
		if b != c.base || e != c.ext {
			t.Errorf("splitExt(%q) = %q, %q; want %q, %q", c.in, b, e, c.base, c.ext)
		}
		if b+e != c.in {
			t.Errorf("splitExt(%q) loses characters", c.in)
		}
	}
}

func TestLimitedReader(t *testing.T) {
	for _, c := range []struct {
		size, limit int
		ok          bool
	}{{0, 0, true}, {10, 10, true}, {11, 10, false}, {1, 0, false}, {5000, 4096, false}, {4096, 4096, true}} {
		lr := &limitedReader{r: strings.NewReader(strings.Repeat("x", c.size)), left: int64(c.limit)}
		buf := make([]byte, 1000)
		total := 0
		var err error
		for err == nil {
			var n int
			n, err = lr.Read(buf)
			total += n
		}
		if c.ok && (err == errTooLarge || total != c.size) {
			t.Errorf("size %d limit %d: read %d, err %v", c.size, c.limit, total, err)
		}
		if !c.ok && err != errTooLarge {
			t.Errorf("size %d limit %d: err %v, want errTooLarge", c.size, c.limit, err)
		}
		if total > c.limit+1 {
			t.Errorf("size %d limit %d: %d bytes were let through", c.size, c.limit, total)
		}
	}
}
