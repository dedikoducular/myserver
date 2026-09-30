package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"
)

/* ---------- hand-made tar blocks ---------- */

// rawHeader builds one 512-byte ustar header without any of the checks a
// tar writer applies, so that hostile headers can be produced.
func rawHeader(name string, typ byte, size int64, link string) []byte {
	b := make([]byte, 512)
	copy(b[0:100], name)
	copy(b[100:108], "0000644\x00")
	copy(b[108:116], "0000000\x00")
	copy(b[116:124], "0000000\x00")
	if size >= 0 && size < 1<<33 {
		copy(b[124:136], fmt.Sprintf("%011o\x00", size))
	} else {
		// base-256 encoding for large and negative values
		v := uint64(size)
		for i := 135; i >= 124; i-- {
			b[i] = byte(v)
			v >>= 8
		}
		if size < 0 {
			for i := 124; i < 128; i++ {
				b[i] = 0xff
			}
		}
		b[124] |= 0x80
	}
	copy(b[136:148], "14000000000\x00")
	b[156] = typ
	copy(b[157:257], link)
	copy(b[257:263], "ustar\x00")
	copy(b[263:265], "00")
	for i := 148; i < 156; i++ {
		b[i] = ' '
	}
	sum := 0
	for _, c := range b {
		sum += int(c)
	}
	copy(b[148:156], fmt.Sprintf("%06o\x00 ", sum))
	return b
}

func padded(data []byte) []byte {
	out := bytes.Clone(data)
	if r := len(out) % 512; r != 0 {
		out = append(out, make([]byte, 512-r)...)
	}
	return out
}

func rawFile(name string, body string) []byte {
	return append(rawHeader(name, tar.TypeReg, int64(len(body)), ""), padded([]byte(body))...)
}

// paxHeader builds an extended header carrying the given records for the
// entry that follows.
func paxHeader(records map[string]string) []byte {
	var data []byte
	for k, v := range records {
		rec := fmt.Sprintf(" %s=%s\n", k, v)
		n := len(rec) + 1
		for len(fmt.Sprint(n))+len(rec) != n {
			n = len(fmt.Sprint(n)) + len(rec)
		}
		data = append(data, fmt.Sprintf("%d%s", n, rec)...)
	}
	return append(rawHeader("PaxHeaders.0/x", tar.TypeXHeader, int64(len(data)), ""), padded(data)...)
}

// gnuLong builds a GNU long name ('L') or long link ('K') header.
func gnuLong(typ byte, value string) []byte {
	data := append([]byte(value), 0)
	return append(rawHeader("././@LongLink", typ, int64(len(data)), ""), padded(data)...)
}

func endOfTar(blocks ...[]byte) []byte {
	var out []byte
	for _, b := range blocks {
		out = append(out, b...)
	}
	return append(out, make([]byte, 1024)...)
}

func validate(stream []byte, prefix string) (tarStats, []string, error) {
	var names []string
	st, err := readTar(context.Background(), bytes.NewReader(stream), prefix, func(h *tar.Header, body io.Reader) error {
		names = append(names, h.Name)
		_, err := io.Copy(io.Discard, body)
		return err
	})
	return st, names, err
}

func wantUnsafe(t *testing.T, what string, stream []byte, prefix string) {
	t.Helper()
	_, names, err := validate(stream, prefix)
	if err == nil {
		t.Fatalf("%s: the stream was accepted; entries %q", what, names)
	}
	var ue *unsafeEntryError
	// A body that ends early surfaces in the consumer's own read.
	if !errors.As(err, &ue) && !errors.Is(err, errCorruptTar) && !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("%s: err = %T %v, want an unsafe entry or corrupt stream error", what, err, err)
	}
	// Validation alone (no consumer), as the verification of a backup does.
	if _, err := readTar(context.Background(), bytes.NewReader(stream), prefix, nil); err == nil {
		t.Fatalf("%s: accepted when only validated", what)
	} else if !errors.As(err, &ue) && !errors.Is(err, errCorruptTar) {
		t.Fatalf("%s: validation err = %T %v", what, err, err)
	}
	for _, n := range names {
		if strings.HasPrefix(n, "/") || strings.Contains("/"+n+"/", "/../") || strings.ContainsRune(n, 0) {
			t.Fatalf("%s: the hostile name %q was handed to the consumer before the refusal", what, n)
		}
	}
}

/* ---------- names ---------- */

func TestCleanEntryName(t *testing.T) {
	good := map[string]string{
		"a":                               "a",
		"a/b":                             "a/b",
		"./a":                             "a",
		"./":                              "",
		".":                               "",
		"a/":                              "a",
		"a//b":                            "a/b",
		"a/./b":                           "a/b",
		".//a/.//b/":                      "a/b",
		"...":                             "...",
		"..a":                             "..a",
		"a..":                             "a..",
		"a/..b/c..":                       "a/..b/c..",
		"dosya adı.txt":                   "dosya adı.txt",
		`a\b`:                             `a\b`,
		`..\..\etc`:                       `..\..\etc`,
		strings.Repeat("a", maxEntryPath): strings.Repeat("a", maxEntryPath),
	}
	for in, want := range good {
		got, err := cleanEntryName(in)
		if err != nil || got != want {
			t.Errorf("cleanEntryName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{
		"", "/", "/a", "/etc/passwd", "//a", "..", "../", "../a", "a/..", "a/../", "a/../b", "a/b/../../../c",
		"./..", "./../a", "./../", "a/./../b", ".././a", "a//../b",
		"a\x00b", "\x00", "a/\x00/b", "a\x00/../b",
		strings.Repeat("a", maxEntryPath+1), strings.Repeat("a/", maxEntryPath),
	}
	for _, in := range bad {
		if got, err := cleanEntryName(in); err == nil {
			t.Errorf("cleanEntryName(%q) = %q, want an error", in, got)
		} else {
			var ue *unsafeEntryError
			if !errors.As(err, &ue) {
				t.Errorf("cleanEntryName(%q): error type %T", in, err)
			} else if len(ue.Name) > 200 {
				t.Errorf("cleanEntryName: the error carries %d bytes of the name", len(ue.Name))
			}
		}
	}
}

func TestHostileNamesAreRefused(t *testing.T) {
	long := strings.Repeat("uzun/", 1000)
	cases := map[string][]byte{
		"absolute":                     endOfTar(rawFile("/etc/passwd", "x")),
		"absolute, double slash":       endOfTar(rawFile("//etc/passwd", "x")),
		"parent first":                 endOfTar(rawFile("../x", "x")),
		"parent in the middle":         endOfTar(rawFile("a/../../x", "x")),
		"parent that stays inside":     endOfTar(rawFile("a/../b", "x")),
		"parent at the end":            endOfTar(rawFile("a/..", "x")),
		"parent only":                  endOfTar(rawHeader("..", tar.TypeDir, 0, "")),
		"parent with slash":            endOfTar(rawHeader("../", tar.TypeDir, 0, "")),
		"dot slash parent":             endOfTar(rawFile("./../x", "x")),
		"dot slash parent deep":        endOfTar(rawFile("./a/./../../x", "x")),
		"empty name":                   endOfTar(rawFile("", "x")),
		"dot as a file":                endOfTar(rawFile(".", "x")),
		"dot slash as a file":          endOfTar(rawFile("./", "x")),
		"after a good entry":           endOfTar(rawFile("iyi.txt", "x"), rawFile("../kotu", "x")),
		"ustar prefix field":           endOfTar(withPrefixField(rawHeader("x", tar.TypeReg, 0, ""), "../..")),
		"pax path absolute":            endOfTar(paxHeader(map[string]string{"path": "/etc/cron.d/x"}), rawFile("masum", "x")),
		"pax path parent":              endOfTar(paxHeader(map[string]string{"path": "a/../../../etc/x"}), rawFile("masum", "x")),
		"pax path long parent":         endOfTar(paxHeader(map[string]string{"path": long + "../" + strings.Repeat("../", 1001) + "x"}), rawFile("masum", "x")),
		"pax path too long":            endOfTar(paxHeader(map[string]string{"path": strings.Repeat("a/", 3000) + "x"}), rawFile("masum", "x")),
		"pax path with NUL":            endOfTar(paxHeader(map[string]string{"path": "a\x00/../../x"}), rawFile("masum", "x")),
		"pax path empty":               endOfTar(paxHeader(map[string]string{"path": ""}), rawFile("", "x")),
		"gnu long name absolute":       endOfTar(gnuLong(tar.TypeGNULongName, "/"+long+"x"), rawFile("masum", "x")),
		"gnu long name parent":         endOfTar(gnuLong(tar.TypeGNULongName, long+strings.Repeat("../", 1001)+"x"), rawFile("masum", "x")),
		"gnu long name short parent":   endOfTar(gnuLong(tar.TypeGNULongName, "../x"), rawFile("masum", "x")),
		"gnu long name too long":       endOfTar(gnuLong(tar.TypeGNULongName, strings.Repeat("a/", 3000)), rawFile("masum", "x")),
		"hard link absolute":           endOfTar(rawFile("a", "x"), rawHeader("b", tar.TypeLink, 0, "/etc/shadow")),
		"hard link parent":             endOfTar(rawFile("a", "x"), rawHeader("b", tar.TypeLink, 0, "../disari/gizli")),
		"hard link parent inside":      endOfTar(rawFile("a", "x"), rawHeader("b", tar.TypeLink, 0, "c/../a")),
		"hard link to itself":          endOfTar(rawHeader("b", tar.TypeLink, 0, "b")),
		"hard link to the root":        endOfTar(rawHeader("b", tar.TypeLink, 0, "./")),
		"hard link without target":     endOfTar(rawHeader("b", tar.TypeLink, 0, "")),
		"hard link to a symlink":       endOfTar(rawHeader("s", tar.TypeSymlink, 0, "/etc/shadow"), rawHeader("b", tar.TypeLink, 0, "s")),
		"hard link through a symlink":  endOfTar(rawHeader("s", tar.TypeSymlink, 0, "/etc"), rawHeader("b", tar.TypeLink, 0, "s/shadow")),
		"hard link pax linkpath":       endOfTar(paxHeader(map[string]string{"linkpath": "../../etc/shadow"}), rawHeader("b", tar.TypeLink, 0, "a")),
		"hard link gnu long link":      endOfTar(gnuLong(tar.TypeGNULongLink, "/etc/shadow"), rawHeader("b", tar.TypeLink, 0, "a")),
		"symlink without target":       endOfTar(rawHeader("s", tar.TypeSymlink, 0, "")),
		"symlink target too long":      endOfTar(paxHeader(map[string]string{"linkpath": strings.Repeat("a", maxEntryPath+1)}), rawHeader("s", tar.TypeSymlink, 0, "x")),
		"file through a symlink":       endOfTar(rawHeader("s", tar.TypeSymlink, 0, "/etc"), rawFile("s/cron.d/x", "x")),
		"file through relative link":   endOfTar(rawHeader("s", tar.TypeSymlink, 0, "../disari"), rawFile("s/x", "x")),
		"file through inner link":      endOfTar(rawHeader("d/", tar.TypeDir, 0, ""), rawHeader("d/s", tar.TypeSymlink, 0, "."), rawFile("d/s/x", "x")),
		"dir through a symlink":        endOfTar(rawHeader("s", tar.TypeSymlink, 0, "/etc"), rawHeader("s/yeni/", tar.TypeDir, 0, "")),
		"symlink through a symlink":    endOfTar(rawHeader("s", tar.TypeSymlink, 0, "/etc"), rawHeader("s/t", tar.TypeSymlink, 0, "x")),
		"file through link, dot form":  endOfTar(rawHeader("./s", tar.TypeSymlink, 0, "/etc"), rawFile(".//s/./x", "x")),
		"root is a symlink":            endOfTar(rawHeader("./", tar.TypeSymlink, 0, "/etc")),
		"root is a file":               endOfTar(rawFile("./", "x")),
		"type contiguous":              endOfTar(rawHeader("x", tar.TypeCont, 0, "")),
		"type gnu dumpdir":             endOfTar(rawHeader("x", 'D', 0, "")),
		"type gnu multivolume":         endOfTar(rawHeader("x", 'M', 0, "")),
		"type gnu volume header":       endOfTar(rawHeader("x", 'V', 0, "")),
		"type socket (non standard)":   endOfTar(rawHeader("x", 's', 0, "")),
		"type unknown":                 endOfTar(rawHeader("x", 'Z', 0, "")),
		"size larger than the stream":  append(rawHeader("x", tar.TypeReg, 5000, ""), []byte("kısa")...),
		"size larger, last of several": append(rawFile("a", "x"), append(rawHeader("b", tar.TypeReg, 1<<20, ""), make([]byte, 2048)...)...),
		"huge size":                    append(rawHeader("x", tar.TypeReg, 1<<40, ""), make([]byte, 4096)...),
		"huge size base 256":           append(rawHeader("x", tar.TypeReg, 1<<60, ""), make([]byte, 4096)...),
		"negative size":                endOfTar(rawHeader("x", tar.TypeReg, -1, "")),
		"pax size huge":                endOfTar(paxHeader(map[string]string{"size": "9223372036854775807"}), rawFile("x", "kısa")),
		"pax size negative":            endOfTar(paxHeader(map[string]string{"size": "-5"}), rawFile("x", "kısa")),
		"bad checksum":                 endOfTar(func() []byte { b := rawFile("x", "y"); b[150] ^= 1; return b }()),
		"garbage":                      bytes.Repeat([]byte("çöp veri "), 200),
		"cut inside a header":          rawFile("x", "y")[:300],
	}
	for name, stream := range cases {
		t.Run(name, func(t *testing.T) {
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			start := time.Now()
			wantUnsafe(t, name, stream, "")
			runtime.ReadMemStats(&after)
			if d := time.Since(start); d > 2*time.Second {
				t.Fatalf("refusal took %v", d)
			}
			if grown := after.TotalAlloc - before.TotalAlloc; grown > 8<<20 {
				t.Fatalf("refusal allocated %d bytes", grown)
			}
		})
	}
}

func withPrefixField(h []byte, prefix string) []byte {
	copy(h[345:500], prefix)
	for i := 148; i < 156; i++ {
		h[i] = ' '
	}
	sum := 0
	for _, c := range h {
		sum += int(c)
	}
	copy(h[148:156], fmt.Sprintf("%06o\x00 ", sum))
	return h
}

// Streams without the two end blocks are what a cut-off copy looks like.
func TestStreamWithoutEndMarker(t *testing.T) {
	// archive/tar accepts a stream that ends at an entry boundary; what
	// protects against truncation there is the checksum of the stream in
	// the manifest, which TestDamagedArchivesAreRejected covers. Inside an
	// entry the tar layer itself must notice.
	stream := rawFile("a", strings.Repeat("x", 3000))
	for _, cut := range []int{513, 1000, 512 + 3000 - 1} {
		wantUnsafe(t, fmt.Sprintf("cut at %d", cut), stream[:cut], "")
	}
}

func TestAcceptedStreams(t *testing.T) {
	t.Run("plain", func(t *testing.T) {
		st, names, err := validate(simpleVolume(t), "")
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"./", "foto.jpg", "alt/", "alt/kasa.kdbx"}
		if fmt.Sprint(names) != fmt.Sprint(want) || st.Files != 2 || st.Items != 4 || st.Bytes != int64(len("JPEG-verisi")+len("parola kasası")) {
			t.Fatalf("names %q stats %+v", names, st)
		}
	})
	t.Run("backslash is an ordinary character", func(t *testing.T) {
		_, names, err := validate(endOfTar(rawFile(`..\..\windows\system32`, "x"), rawFile(`a\b`, "x")), "")
		if err != nil {
			t.Fatalf("a name with backslashes was refused on Linux, where it is one file name: %v", err)
		}
		if len(names) != 2 || names[0] != `..\..\windows\system32` {
			t.Fatalf("names %q", names)
		}
	})
	t.Run("symlinks are kept as links whatever they point to", func(t *testing.T) {
		stream := endOfTar(rawHeader("a", tar.TypeSymlink, 0, "/etc/passwd"), rawHeader("b", tar.TypeSymlink, 0, "../../x"),
			rawHeader("c", tar.TypeSymlink, 0, "c"))
		var links []string
		_, err := readTar(context.Background(), bytes.NewReader(stream), "", func(h *tar.Header, _ io.Reader) error {
			if h.Typeflag != tar.TypeSymlink {
				t.Errorf("%s has type %c", h.Name, h.Typeflag)
			}
			links = append(links, h.Linkname)
			return nil
		})
		if err != nil || fmt.Sprint(links) != "[/etc/passwd ../../x c]" {
			t.Fatalf("links %q err %v", links, err)
		}
	})
	t.Run("an entry may replace an earlier symlink of the same name", func(t *testing.T) {
		_, _, err := validate(endOfTar(rawHeader("s", tar.TypeSymlink, 0, "/etc"), rawHeader("s/", tar.TypeDir, 0, ""), rawFile("s/x", "x")), "")
		if err != nil {
			t.Fatalf("refused: %v", err)
		}
	})
	t.Run("hard link inside", func(t *testing.T) {
		var link string
		_, err := readTar(context.Background(), bytes.NewReader(endOfTar(rawHeader("d/", tar.TypeDir, 0, ""), rawFile("d/a", "x"),
			rawHeader("b", tar.TypeLink, 0, "./d//a"))), "", func(h *tar.Header, _ io.Reader) error {
			if h.Typeflag == tar.TypeLink {
				link = h.Linkname
			}
			return nil
		})
		if err != nil || link != "d/a" {
			t.Fatalf("link %q err %v", link, err)
		}
	})
	t.Run("duplicates", func(t *testing.T) {
		st, names, err := validate(endOfTar(rawFile("a", "bir"), rawFile("a", "iki"), rawFile("./a", "üç")), "")
		if err != nil || st.Files != 3 || fmt.Sprint(names) != "[a a a]" {
			t.Fatalf("names %q stats %+v err %v", names, st, err)
		}
	})
	t.Run("old style regular file", func(t *testing.T) {
		var typ byte
		_, err := readTar(context.Background(), bytes.NewReader(endOfTar(rawHeader("a", 0, 0, ""))), "", func(h *tar.Header, _ io.Reader) error {
			typ = h.Typeflag
			return nil
		})
		if err != nil || typ != tar.TypeReg {
			t.Fatalf("type %q err %v", typ, err)
		}
	})
	t.Run("size of a non-file is ignored", func(t *testing.T) {
		var size int64 = -1
		_, err := readTar(context.Background(), bytes.NewReader(endOfTar(rawHeader("d/", tar.TypeDir, 0, ""), rawHeader("f", tar.TypeFifo, 0, ""))), "", func(h *tar.Header, _ io.Reader) error {
			size = h.Size
			return nil
		})
		if err != nil || size != 0 {
			t.Fatalf("size %d err %v", size, err)
		}
	})
	t.Run("global pax header cannot rename entries", func(t *testing.T) {
		data := []byte("20 path=../../etc/x\n")
		global := append(rawHeader("pax_global_header", tar.TypeXGlobalHeader, int64(len(data)), ""), padded(data)...)
		_, names, err := validate(endOfTar(global, rawFile("masum", "x")), "")
		if err != nil || fmt.Sprint(names) != "[masum]" {
			t.Fatalf("names %q err %v", names, err)
		}
	})
}

func TestModeBitsAreCarriedUnchanged(t *testing.T) {
	stream := tarStream(t,
		tarFile{name: "./", typ: tar.TypeDir},
		tarFile{name: "su", body: "x", mode: 0o4755},
		tarFile{name: "sg", body: "x", mode: 0o2755},
		tarFile{name: "tmp/", typ: tar.TypeDir, mode: 0o1777},
	)
	modes := map[string]int64{}
	_, err := readTar(context.Background(), bytes.NewReader(stream), "", func(h *tar.Header, _ io.Reader) error {
		modes[h.Name] = h.Mode
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// A backup is a faithful copy: the bits are part of the data. What the
	// restore of a host folder does with them is tested in
	// TestImportBindSpecialModeBits.
	if modes["su"] != 0o4755 || modes["sg"] != 0o2755 || modes["tmp/"] != 0o1777 {
		t.Fatalf("modes %v", modes)
	}
}

func TestDockerPrefixIsEnforced(t *testing.T) {
	p := helperPrefix
	good := endOfTar(rawHeader(p+"/", tar.TypeDir, 0, ""), rawFile(p+"/a", "x"), rawHeader(p+"/d/", tar.TypeDir, 0, ""),
		rawFile(p+"/d/b", "y"), rawHeader(p+"/l", tar.TypeLink, 0, p+"/a"))
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	st, err := copyTar(context.Background(), tw, bytes.NewReader(good), p, nil)
	if err != nil || tw.Close() != nil {
		t.Fatalf("copyTar: %v", err)
	}
	if st.Items != 5 || st.Files != 2 {
		t.Fatalf("stats %+v", st)
	}
	tr := tar.NewReader(&out)
	var names []string
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, h.Name+"→"+h.Linkname)
	}
	if fmt.Sprint(names) != "[./→ a→ d/→ d/b→ l→a]" {
		t.Fatalf("normalized names %q", names)
	}

	bad := map[string][]byte{
		"outside the prefix":      endOfTar(rawHeader(p+"/", tar.TypeDir, 0, ""), rawFile("etc/passwd", "x")),
		"prefix as a name prefix": endOfTar(rawFile(p+"-baska/a", "x")),
		"parent after the prefix": endOfTar(rawFile(p+"/../a", "x")),
		"absolute":                endOfTar(rawFile("/"+p+"/a", "x")),
		"hard link outside":       endOfTar(rawFile(p+"/a", "x"), rawHeader(p+"/l", tar.TypeLink, 0, "baska/a")),
		"hard link to the root":   endOfTar(rawHeader(p+"/l", tar.TypeLink, 0, p)),
		"prefix is a file":        endOfTar(rawFile(p, "x")),
	}
	for name, stream := range bad {
		wantUnsafe(t, name, stream, p)
	}
}

func TestReadTarHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	n := 0
	_, err := readTar(ctx, bytes.NewReader(endOfTar(rawFile("a", "x"), rawFile("b", "x"), rawFile("c", "x"))), "", func(*tar.Header, io.Reader) error {
		n++
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) || n != 1 {
		t.Fatalf("err %v after %d entries", err, n)
	}
}

func TestCleanPAXDropsWhatTheWriterRegenerates(t *testing.T) {
	got := cleanPAX(map[string]string{
		"path": "../../x", "linkpath": "/etc", "size": "1", "uid": "0", "mtime": "1", "GNU.sparse.major": "1",
		"SCHILY.xattr.user.etiket": "deger",
	})
	if len(got) != 1 || got["SCHILY.xattr.user.etiket"] != "deger" {
		t.Fatalf("cleanPAX = %v", got)
	}
	if cleanPAX(map[string]string{"path": "x"}) != nil || cleanPAX(nil) != nil {
		t.Fatal("expected nil")
	}
}
