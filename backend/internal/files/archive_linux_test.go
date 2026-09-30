package files

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

/* ---------- archive builders ---------- */

type member struct {
	name    string
	body    string
	mode    os.FileMode // zip: full mode incl. type bits; tar: permission bits
	tarType byte
	link    string
}

func buildZip(t *testing.T, members []member) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, m := range members {
		h := &zip.FileHeader{Name: m.name, Method: zip.Deflate, Modified: time.Unix(1700000000, 0)}
		mode := m.mode
		if mode == 0 {
			mode = 0o644
		}
		h.SetMode(mode)
		// SetMode on a directory mode appends nothing to the name; keep
		// the name exactly as the attacker wrote it.
		h.Name = m.name
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(m.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// lyingZip declares `declared` bytes for a member that really inflates to
// len(data).
func lyingZip(t *testing.T, name string, data []byte, declared uint64) []byte {
	t.Helper()
	var comp bytes.Buffer
	fw, _ := flate.NewWriter(&comp, flate.BestCompression)
	fw.Write(data)
	fw.Close()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	h := &zip.FileHeader{Name: name, Method: zip.Deflate}
	h.SetMode(0o644)
	h.CRC32 = crc32.ChecksumIEEE(data)
	h.CompressedSize64 = uint64(comp.Len())
	h.UncompressedSize64 = declared
	w, err := zw.CreateRaw(h)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(comp.Bytes())
	// A harmless second member, to see whether extraction carried on.
	w2, _ := zw.Create("after.txt")
	w2.Write([]byte("after"))
	zw.Close()
	return buf.Bytes()
}

func buildTar(t *testing.T, members []member) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, m := range members {
		typ := m.tarType
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := int64(m.mode & 0o7777) // tar carries the type in Typeflag
		if mode == 0 {
			mode = 0o644
		}
		h := &tar.Header{Name: m.name, Typeflag: typ, Mode: mode, Linkname: m.link, ModTime: time.Unix(1700000000, 0),
			Format: tar.FormatPAX}
		if typ == tar.TypeReg {
			h.Size = int64(len(m.body))
		}
		if typ == tar.TypeChar || typ == tar.TypeBlock {
			h.Devmajor, h.Devminor = 1, 3
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatalf("tar header %q: %v", m.name, err)
		}
		if typ == tar.TypeReg {
			tw.Write([]byte(m.body))
		}
	}
	tw.Close()
	return buf.Bytes()
}

func gz(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

// archiveEnv has root/in (archives) and root/dest (extraction target).
func archiveEnv(t *testing.T) *testEnv {
	e := newEnv(t)
	e.mkdir(e.in("in"))
	e.mkdir(e.in("dest"))
	return e
}

func (e *testEnv) extract(name string, data []byte, conflict string) (JobView, bool) {
	e.t.Helper()
	e.write(e.in("in", name), string(data))
	w := e.post("/extract", map[string]any{"path": e.in("in", name), "dest": e.in("dest"), "conflict": conflict})
	noSecret(e.t, "extract "+name, w)
	v, ok := e.job(w)
	if !ok && (w.Code < 400 || w.Code >= 500) {
		e.t.Errorf("extract %s: status %d %s", name, w.Code, w.Body)
	}
	return v, ok
}

// assertConfined verifies the invariants that hold after ANY extraction,
// successful or not.
func (e *testEnv) assertConfined(what string, check func(), inBefore string) {
	e.t.Helper()
	check()
	if got := snapshot(e.t, e.in("in")); inBefore != "" && got != inBefore {
		e.t.Errorf("%s: ESCAPE: the sibling directory of the destination changed:\n%s", what, got)
	}
	for _, n := range names(e.t, e.root) {
		if n != "in" && n != "dest" {
			e.t.Errorf("%s: ESCAPE: %q appeared next to the destination", what, n)
		}
	}
	filepath.Walk(e.in("dest"), func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !fi.IsDir() && !fi.Mode().IsRegular() {
			e.t.Errorf("%s: extraction created a special entry: %s (%s)", what, p, fi.Mode())
		}
		if fi.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			e.t.Errorf("%s: extraction kept set-id/sticky bits: %s (%s)", what, p, fi.Mode())
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && fi.Mode().IsRegular() && st.Nlink > 1 {
			e.t.Errorf("%s: extraction created a hard link: %s", what, p)
		}
		return nil
	})
	if l := leftovers(e.t, e.root); len(l) > 0 {
		e.t.Errorf("%s: temporary files left: %v", what, l)
	}
	for _, p := range []string{"/abs-evil.txt", "/tmp/abs-evil.txt", "/evil.txt"} {
		if exists(p) {
			e.t.Errorf("%s: ESCAPE: %s was created", what, p)
			os.Remove(p)
		}
	}
	if found := regularFilesWith(e.t, e.in("dest"), secret); len(found) > 0 {
		e.t.Errorf("%s: LEAK: outside data reachable in the destination: %v", what, found)
	}
}

/* ---------- hostile member names ---------- */

func hostileNames(e *testEnv) []string {
	return []string{
		"../evil.txt",
		"../../evil.txt",
		"../../../../../../../../tmp/abs-evil.txt",
		"../in/evil.txt",
		"../outside/evil.txt",
		"../../outside/secret.txt",
		"good/../../evil.txt",
		"good/../../../outside/evil.txt",
		"good/..",
		"..",
		"/abs-evil.txt",
		"/tmp/abs-evil.txt",
		"//tmp/abs-evil.txt",
		e.outside + "/evil.txt",
		e.outside + "/secret.txt",
		e.in("in") + "/evil.txt",
		"C:\\evil.txt",
		"C:/evil.txt",
		"c:evil.txt",
		"..\\evil.txt",
		"..\\..\\outside\\evil.txt",
		"good\\..\\..\\evil.txt",
		"\\abs-evil.txt",
		"\\\\host\\share\\evil.txt",
		"nul\x00/evil.txt",
		"evil.txt\x00.png",
		"ctl\n/evil.txt",
		"bad-utf8-\xff\xfe.txt",
		strings.Repeat("x", 300),
		"deep/" + strings.Repeat("y", 300) + "/evil.txt",
	}
}

func TestZipSlip(t *testing.T) {
	e := archiveEnv(t)
	check := e.guard()
	hostile := hostileNames(e)
	members := []member{{name: "good/first.txt", body: "first"}}
	for _, n := range hostile {
		members = append(members, member{name: n, body: "pwned"})
	}
	// Directory members with hostile names.
	members = append(members,
		member{name: "../evildir/", mode: os.ModeDir | 0o755},
		member{name: "/abs-evildir/", mode: os.ModeDir | 0o755},
		member{name: "..\\evildir2\\", mode: os.ModeDir | 0o755},
		member{name: "good/last.txt", body: "last"},
	)
	data := buildZip(t, members)
	e.write(e.in("in", "slip.zip"), string(data))
	inBefore := snapshot(t, e.in("in"))

	for _, conflict := range []string{"rename", "overwrite", "skip"} {
		v, ok := e.extract("slip.zip", data, conflict)
		e.assertConfined("zip slip/"+conflict, check, inBefore)
		if !ok || v.Status != jobDone {
			t.Errorf("%s: the archive was not processed to the end: %+v", conflict, v)
			continue
		}
		if v.Status == jobDone {
			if readFile(t, e.in("dest", "good", "first.txt")) != "first" || readFile(t, e.in("dest", "good", "last.txt")) != "last" {
				t.Errorf("%s: legitimate members were not extracted", conflict)
			}
			if conflict == "rename" && v.Skipped != int64(len(hostile)+3) {
				t.Errorf("skipped %d members, want %d", v.Skipped, len(hostile)+3)
			}
		}
		filepath.Walk(e.in("dest"), func(p string, fi os.FileInfo, err error) error {
			if err == nil && (strings.Contains(fi.Name(), "evil") || strings.Contains(fi.Name(), "secret")) {
				t.Errorf("%s: a hostile member was extracted as %s", conflict, p)
			}
			return nil
		})
	}
	if exists("/abs-evildir") || exists(e.in("evildir")) || exists(e.in("evildir2")) {
		t.Error("ESCAPE: a hostile directory member was created")
	}
}

func tarSlipMembers(e *testEnv) (members []member, refusedCount int) {
	members = []member{{name: "good/first.txt", body: "first"}}
	for _, n := range hostileNames(e) {
		if !tarCanEncode(n) {
			continue // e.g. NUL cannot be written by archive/tar
		}
		members = append(members, member{name: n, body: "pwned"})
		refusedCount++
	}
	special := []member{
		{name: "../evildir/", tarType: tar.TypeDir, mode: 0o755},
		{name: "/abs-evildir/", tarType: tar.TypeDir, mode: 0o755},
		{name: "sym-out", tarType: tar.TypeSymlink, link: e.outside},
		{name: "sym-rel", tarType: tar.TypeSymlink, link: "../../outside"},
		{name: "sym-file", tarType: tar.TypeSymlink, link: e.outside + "/secret.txt"},
		{name: "sym-in", tarType: tar.TypeSymlink, link: "good"},
		{name: "hard-out", tarType: tar.TypeLink, link: e.outside + "/secret.txt"},
		{name: "hard-rel", tarType: tar.TypeLink, link: "../../outside/secret.txt"},
		{name: "hard-in", tarType: tar.TypeLink, link: "good/first.txt"},
		{name: "hard-passwd", tarType: tar.TypeLink, link: "/etc/passwd"},
		{name: "dev-char", tarType: tar.TypeChar, mode: 0o666},
		{name: "dev-block", tarType: tar.TypeBlock, mode: 0o666},
		{name: "fifo", tarType: tar.TypeFifo, mode: 0o666},
	}
	members = append(members, special...)
	refusedCount += len(special)
	members = append(members,
		// Written THROUGH the names of the refused links.
		member{name: "sym-out/pwned.txt", body: "pwned"},
		member{name: "sym-rel/pwned.txt", body: "pwned"},
		member{name: "sym-file", body: "pwned"},
		member{name: "hard-out", body: "pwned"},
		member{name: "suid", body: "#!/bin/sh", mode: 0o4755},
		member{name: "good/last.txt", body: "last"},
	)
	return members, refusedCount
}

func tarCanEncode(name string) bool {
	tw := tar.NewWriter(io.Discard)
	return tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Format: tar.FormatPAX}) == nil
}

func TestTarSlip(t *testing.T) {
	for _, kind := range []string{"slip.tar", "slip.tar.gz", "slip.tgz", "SLIP.TAR.GZ"} {
		t.Run(kind, func(t *testing.T) {
			e := archiveEnv(t)
			check := e.guard()
			members, refusedCount := tarSlipMembers(e)
			data := buildTar(t, members)
			if strings.HasSuffix(strings.ToLower(kind), "gz") {
				data = gz(t, data)
			}
			e.write(e.in("in", kind), string(data))
			inBefore := snapshot(t, e.in("in"))
			v, ok := e.extract(kind, data, "overwrite")
			e.assertConfined(kind, check, inBefore)
			if !ok || v.Status != jobDone {
				t.Fatalf("extraction did not run to the end: %+v", v)
			}
			if v.Skipped != int64(refusedCount) {
				t.Errorf("skipped %d members, want %d", v.Skipped, refusedCount)
			}
			if readFile(t, e.in("dest", "good", "first.txt")) != "first" || readFile(t, e.in("dest", "good", "last.txt")) != "last" {
				t.Error("legitimate members were not extracted")
			}
			if readFile(t, filepath.Join(e.outside, "secret.txt")) != secret {
				t.Fatal("ESCAPE: the outside file was overwritten")
			}
			if exists(filepath.Join(e.outside, "pwned.txt")) {
				t.Fatal("ESCAPE: a member was written through a symlink member")
			}
			fi, err := os.Lstat(e.in("dest", "suid"))
			if err != nil || fi.Mode()&os.ModeSetuid != 0 || fi.Mode().Perm() != 0o755 {
				t.Errorf("suid member: %v %v", fi, err)
			}
		})
	}
}

func TestZipSpecialMembers(t *testing.T) {
	e := archiveEnv(t)
	check := e.guard()
	members := []member{
		{name: "good/first.txt", body: "first"},
		{name: "sym-out", body: e.outside, mode: os.ModeSymlink | 0o777},
		{name: "sym-rel", body: "../../outside", mode: os.ModeSymlink | 0o777},
		{name: "sym-file", body: e.outside + "/secret.txt", mode: os.ModeSymlink | 0o777},
		{name: "sym-in", body: "good", mode: os.ModeSymlink | 0o777},
		{name: "dev-char", mode: os.ModeDevice | os.ModeCharDevice | 0o666},
		{name: "dev-block", mode: os.ModeDevice | 0o666},
		{name: "fifo", mode: os.ModeNamedPipe | 0o666},
		{name: "sock", mode: os.ModeSocket | 0o666},
	}
	refusedCount := len(members) - 1
	members = append(members,
		member{name: "sym-out/pwned.txt", body: "pwned"},
		member{name: "sym-rel/pwned.txt", body: "pwned"},
		member{name: "sym-file", body: "pwned"},
		member{name: "suid", body: "#!/bin/sh", mode: os.ModeSetuid | os.ModeSetgid | os.ModeSticky | 0o755},
		member{name: "noperm", body: "x", mode: 0o000 | os.ModeSetuid},
		member{name: "good/last.txt", body: "last"},
	)
	data := buildZip(t, members)
	e.write(e.in("in", "special.zip"), string(data))
	inBefore := snapshot(t, e.in("in"))
	v, ok := e.extract("special.zip", data, "overwrite")
	e.assertConfined("special zip", check, inBefore)
	if !ok || v.Status != jobDone {
		t.Fatalf("extraction did not run to the end: %+v", v)
	}
	if v.Skipped != int64(refusedCount) {
		t.Errorf("skipped %d members, want %d", v.Skipped, refusedCount)
	}
	if exists(filepath.Join(e.outside, "pwned.txt")) || readFile(t, filepath.Join(e.outside, "secret.txt")) != secret {
		t.Fatal("ESCAPE through a symlink member")
	}
	if readFile(t, e.in("dest", "good", "last.txt")) != "last" {
		t.Error("legitimate members were not extracted")
	}
}

// Links that already exist in the destination must not be written through.
func TestExtractOntoExistingLinks(t *testing.T) {
	for _, kind := range []string{"zip", "tar", "tar.gz"} {
		for _, conflict := range []string{"rename", "overwrite", "skip"} {
			t.Run(kind+"/"+conflict, func(t *testing.T) {
				e := archiveEnv(t)
				e.symlink(e.outside, e.in("dest", "outdir"))
				e.symlink("../../outside", e.in("dest", "reldir"))
				e.symlink(filepath.Join(e.outside, "secret.txt"), e.in("dest", "outfile"))
				e.mkdir(e.in("dest", "sub"))
				e.symlink(e.outside, e.in("dest", "sub", "nested"))
				check := e.guard()

				cases := [][]member{
					{{name: "outdir/pwned.txt", body: "pwned"}},
					{{name: "reldir/pwned.txt", body: "pwned"}},
					{{name: "outdir/secret.txt", body: "pwned"}},
					{{name: "outdir/sub/deeper/pwned.txt", body: "pwned"}},
					{{name: "sub/nested/pwned.txt", body: "pwned"}},
					{{name: "outfile", body: "pwned"}},
					{{name: "outfile/pwned.txt", body: "pwned"}},
					{{name: "outdir/", mode: os.ModeDir | 0o755, tarType: tar.TypeDir}, {name: "outdir/pwned.txt", body: "pwned"}},
					{{name: "outdir/newdir/", mode: os.ModeDir | 0o755, tarType: tar.TypeDir}},
				}
				for i, members := range cases {
					var data []byte
					switch kind {
					case "zip":
						data = buildZip(t, members)
					case "tar":
						data = buildTar(t, members)
					default:
						data = gz(t, buildTar(t, members))
					}
					e.extract("case."+kind, data, conflict)
					check()
					if readFile(t, filepath.Join(e.outside, "secret.txt")) != secret {
						t.Fatalf("case %d (%s): ESCAPE: outside file overwritten", i, members[0].name)
					}
					if l := leftovers(t, e.base); len(l) > 0 {
						t.Errorf("case %d: temporary files left: %v", i, l)
					}
				}
			})
		}
	}
}

/* ---------- size limits ---------- */

func TestExtractSizeLimit(t *testing.T) {
	const mb = 1 << 20
	zeros := make([]byte, 5*mb)
	small := []member{{name: "small.txt", body: "small"}}

	bombs := map[string][]byte{
		"honest.zip":      buildZip(t, append(small, member{name: "bomb.bin", body: string(zeros)})),
		"lying.zip":       lyingZip(t, "bomb.bin", zeros, 10),
		"lying-zero.zip":  lyingZip(t, "bomb.bin", zeros, 0),
		"bomb.tar":        buildTar(t, append(small, member{name: "bomb.bin", body: string(zeros)})),
		"bomb.tar.gz":     gz(t, buildTar(t, append(small, member{name: "bomb.bin", body: string(zeros)}))),
		"bomb.tgz":        gz(t, buildTar(t, []member{{name: "bomb.bin", body: string(zeros)}})),
		"many-small.zip":  buildZip(t, manyMembers(12, 100<<10)),
		"many-small.tar":  buildTar(t, manyMembers(12, 100<<10)),
		"many-small.tgz":  gz(t, buildTar(t, manyMembers(12, 100<<10))),
		"nested-name.zip": buildZip(t, []member{{name: "a/b/c/bomb.bin", body: string(zeros)}}),
	}
	for name, data := range bombs {
		t.Run(name, func(t *testing.T) {
			e := archiveEnv(t)
			e.setting(KeyMaxExtractMB, "1")
			check := e.guard()
			v, ok := e.extract(name, data, "overwrite")
			if ok && v.Status != jobFailed {
				t.Errorf("an archive inflating beyond the limit ended as %q", v.Status)
			}
			if ok && !strings.Contains(v.Message, "sınır") && !strings.Contains(v.Message, "bozuk") {
				t.Errorf("failure message does not explain the limit: %q", v.Message)
			}
			var total int64
			filepath.Walk(e.in("dest"), func(p string, fi os.FileInfo, err error) error {
				if err == nil && fi.Mode().IsRegular() {
					total += fi.Size()
					if fi.Name() == "bomb.bin" {
						t.Errorf("the oversized member was kept: %s (%d bytes)", p, fi.Size())
					}
				}
				return nil
			})
			if total > mb {
				t.Errorf("%d bytes were written although the limit is %d", total, mb)
			}
			e.assertConfined(name, check, "")
		})
	}

	// A member whose declared size is wrong but which fits the budget must
	// not be accepted as if it were intact.
	t.Run("lying within budget", func(t *testing.T) {
		e := archiveEnv(t)
		e.setting(KeyMaxExtractMB, "1")
		check := e.guard()
		for name, declared := range map[string]uint64{"under.zip": 10, "over.zip": 900000} {
			v, ok := e.extract(name, lyingZip(t, "lie.bin", bytes.Repeat([]byte("A"), 300<<10), declared), "overwrite")
			if !ok || v.Status != jobFailed {
				t.Errorf("%s: a member with a false size ended as %+v", name, v)
			}
			if exists(e.in("dest", "lie.bin")) {
				t.Errorf("%s: the inconsistent member was kept", name)
			}
			e.assertConfined(name, check, "")
		}
	})

	// Within the limit everything works.
	t.Run("within limit", func(t *testing.T) {
		e := archiveEnv(t)
		e.setting(KeyMaxExtractMB, "1")
		data := buildZip(t, []member{{name: "ok.bin", body: strings.Repeat("z", mb-10)}})
		if v, ok := e.extract("ok.zip", data, ""); !ok || v.Status != jobDone {
			t.Fatalf("extract within the limit: %+v", v)
		}
		if fi, err := os.Stat(e.in("dest", "ok.bin")); err != nil || fi.Size() != mb-10 {
			t.Errorf("ok.bin: %v %v", fi, err)
		}
	})
}

func manyMembers(n, size int) []member {
	out := make([]member, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, member{name: "part" + string(rune('a'+i)) + ".bin", body: strings.Repeat("m", size)})
	}
	return out
}

func TestExtractTooManyEntries(t *testing.T) {
	if testing.Short() {
		t.Skip("builds archives with more than 200000 members")
	}
	const n = maxArchiveEntries + 1

	t.Run("zip", func(t *testing.T) {
		e := archiveEnv(t)
		check := e.guard()
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		name := []byte("d0000000/")
		for i := 0; i < n; i++ {
			v := i
			for p := 7; p >= 1; p-- {
				name[p] = byte('0' + v%10)
				v /= 10
			}
			if _, err := zw.CreateHeader(&zip.FileHeader{Name: string(name)}); err != nil {
				t.Fatal(err)
			}
		}
		zw.Close()
		v, ok := e.extract("many.zip", buf.Bytes(), "")
		if ok && v.Status != jobFailed {
			t.Errorf("a zip with %d members ended as %q", n, v.Status)
		}
		if got := names(t, e.in("dest")); len(got) != 0 {
			t.Errorf("%d entries were created from an archive that is refused", len(got))
		}
		e.assertConfined("many.zip", check, "")
	})

	// For tar the members are refused link entries, so the test measures
	// the counter and not the speed of the filesystem.
	for _, kind := range []string{"many.tar", "many.tar.gz"} {
		t.Run(kind, func(t *testing.T) {
			e := archiveEnv(t)
			check := e.guard()
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			tw.WriteHeader(&tar.Header{Name: "first.txt", Typeflag: tar.TypeReg, Size: 5, Mode: 0o644})
			tw.Write([]byte("first"))
			for i := 0; i < n; i++ {
				if err := tw.WriteHeader(&tar.Header{Name: "l", Typeflag: tar.TypeSymlink, Linkname: "x", Mode: 0o777}); err != nil {
					t.Fatal(err)
				}
			}
			tw.WriteHeader(&tar.Header{Name: "beyond.txt", Typeflag: tar.TypeReg, Size: 6, Mode: 0o644})
			tw.Write([]byte("beyond"))
			tw.Close()
			data := buf.Bytes()
			if strings.HasSuffix(kind, ".gz") {
				data = gz(t, data)
			}
			v, ok := e.extract(kind, data, "")
			if !ok || v.Status != jobFailed {
				t.Errorf("a tar with %d members ended as %+v", n+2, v)
			}
			if exists(e.in("dest", "beyond.txt")) {
				t.Error("a member beyond the entry limit was extracted")
			}
			e.assertConfined(kind, check, "")
		})
	}
}

/* ---------- broken input ---------- */

func TestExtractBrokenArchives(t *testing.T) {
	e := archiveEnv(t)
	check := e.guard()
	goodZip := buildZip(t, []member{{name: "a.txt", body: strings.Repeat("a", 5000)}, {name: "b.txt", body: "b"}})
	goodTar := buildTar(t, []member{{name: "a.txt", body: strings.Repeat("a", 5000)}, {name: "b.txt", body: "b"}})
	// A tar header announcing far more data than the file holds.
	var huge bytes.Buffer
	tw := tar.NewWriter(&huge)
	tw.WriteHeader(&tar.Header{Name: "huge.bin", Typeflag: tar.TypeReg, Size: 1 << 40, Mode: 0o644})
	tw.Write(bytes.Repeat([]byte("h"), 4096))
	hugeTar := append([]byte{}, huge.Bytes()...)

	broken := map[string][]byte{
		"empty.zip":         {},
		"garbage.zip":       []byte("this is not an archive at all"),
		"truncated.zip":     goodZip[:len(goodZip)/2],
		"empty.tar":         {},
		"garbage.tar":       bytes.Repeat([]byte("garbage!"), 200),
		"truncated.tar":     goodTar[:700],
		"huge-claim.tar":    hugeTar,
		"huge-claim.tar.gz": gz(t, hugeTar),
		"garbage.tar.gz":    []byte("not gzip"),
		"truncated.tar.gz":  gz(t, goodTar)[:40],
		"zip-named.tar":     goodZip,
		"tar-named.zip":     goodTar,
	}
	// A zip whose data is corrupted after the headers were written.
	corrupt := append([]byte{}, goodZip...)
	for i := 40; i < 60; i++ {
		corrupt[i] ^= 0xff
	}
	broken["corrupt.zip"] = corrupt

	for name, data := range broken {
		v, ok := e.extract(name, data, "overwrite")
		if ok && v.Status == jobDone && name != "empty.tar" {
			t.Errorf("%s: a broken archive was reported as extracted", name)
		}
		if ok && v.Status == jobFailed && v.Message == "" {
			t.Errorf("%s: failure without a message", name)
		}
		e.assertConfined(name, check, "")
		for _, n := range []string{"huge.bin"} {
			if exists(e.in("dest", n)) {
				t.Errorf("%s: partial member %s was kept", name, n)
			}
		}
		os.RemoveAll(e.in("dest"))
		e.mkdir(e.in("dest"))
	}

	// Unsupported or misleading archive names are refused up front.
	for _, name := range []string{"a.rar", "a.7z", "a.txt", "a.zip.txt", "zip", "a.tar.bz2", "a.tar.xz"} {
		e.write(e.in("in", name), string(goodZip))
		refused(t, "extract "+name, e.post("/extract", map[string]any{"path": e.in("in", name), "dest": e.in("dest")}), 400)
	}
	refused(t, "extract a directory", e.post("/extract", map[string]any{"path": e.in("in"), "dest": e.in("dest")}), 400)
	e.mkdir(e.in("in", "dir.zip"))
	refused(t, "extract a directory named .zip", e.post("/extract", map[string]any{"path": e.in("in", "dir.zip"), "dest": e.in("dest")}), 400)
	e.write(e.in("in", "ok.zip"), string(goodZip))
	refused(t, "extract into a file", e.post("/extract", map[string]any{"path": e.in("in", "ok.zip"), "dest": e.in("in", "ok.zip")}), 400)
	refused(t, "extract into a missing directory", e.post("/extract", map[string]any{"path": e.in("in", "ok.zip"), "dest": e.in("nope")}), 404)
	refused(t, "extract with a bad conflict mode", e.post("/extract", map[string]any{"path": e.in("in", "ok.zip"), "dest": e.in("dest"), "conflict": "x"}), 400)
}

func TestExtractConflictModes(t *testing.T) {
	for _, kind := range []string{"zip", "tar"} {
		e := archiveEnv(t)
		members := []member{{name: "a.txt", body: "new"}, {name: "d/b.txt", body: "new"}, {name: "fresh.txt", body: "fresh"}}
		data := buildZip(t, members)
		if kind == "tar" {
			data = buildTar(t, members)
		}
		reset := func() {
			os.RemoveAll(e.in("dest"))
			e.write(e.in("dest", "a.txt"), "old")
			e.write(e.in("dest", "d", "b.txt"), "old")
		}
		reset()
		if v, _ := e.extract("c."+kind, data, ""); v.Status != jobDone {
			t.Fatalf("%s rename: %+v", kind, v)
		}
		if readFile(t, e.in("dest", "a.txt")) != "old" || readFile(t, e.in("dest", "a (2).txt")) != "new" ||
			readFile(t, e.in("dest", "d", "b.txt")) != "old" || readFile(t, e.in("dest", "d", "b (2).txt")) != "new" {
			t.Errorf("%s rename: %v %v", kind, names(t, e.in("dest")), names(t, e.in("dest", "d")))
		}
		reset()
		if v, _ := e.extract("c."+kind, data, "skip"); v.Status != jobDone || v.Skipped != 2 {
			t.Fatalf("%s skip: %+v", kind, v)
		}
		if readFile(t, e.in("dest", "a.txt")) != "old" || readFile(t, e.in("dest", "fresh.txt")) != "fresh" {
			t.Errorf("%s skip overwrote", kind)
		}
		reset()
		if v, _ := e.extract("c."+kind, data, "overwrite"); v.Status != jobDone {
			t.Fatalf("%s overwrite: %+v", kind, v)
		}
		if readFile(t, e.in("dest", "a.txt")) != "new" || readFile(t, e.in("dest", "d", "b.txt")) != "new" {
			t.Errorf("%s overwrite did not replace", kind)
		}
		// A member must not replace a directory, and a directory member
		// must not replace a file.
		reset()
		e.write(e.in("dest", "fresh.txt", "inner.txt"), "precious")
		e.extract("c."+kind, data, "overwrite")
		if readFile(t, e.in("dest", "fresh.txt", "inner.txt")) != "precious" {
			t.Errorf("DATA LOSS: %s member replaced a directory", kind)
		}
		reset()
		os.RemoveAll(e.in("dest", "d"))
		e.write(e.in("dest", "d"), "precious file")
		e.extract("c."+kind, data, "overwrite")
		if readFile(t, e.in("dest", "d")) != "precious file" {
			t.Errorf("DATA LOSS: %s directory member replaced a file", kind)
		}
		if l := leftovers(t, e.root); len(l) > 0 {
			t.Errorf("temporary files left: %v", l)
		}
	}
}

/* ---------- creating archives ---------- */

func TestZipCreate(t *testing.T) {
	e := newEnv(t)
	e.write(e.in("src", "a.txt"), "a")
	e.write(e.in("src", "sub", "b.txt"), "b")
	e.mkdir(e.in("out"))
	check := e.guard()

	e.mustRun("/zip", map[string]any{"paths": []string{e.in("src")}, "dest": e.in("out"), "name": "arsiv"})
	b, err := os.ReadFile(e.in("out", "arsiv.zip"))
	if err != nil {
		t.Fatal(err)
	}
	assertZipClean(t, b, "src/sub/b.txt")

	// No overwrite of an existing archive.
	e.write(e.in("out", "taken.zip"), "precious")
	refused(t, "zip onto an existing file", e.post("/zip", map[string]any{"paths": []string{e.in("src")}, "dest": e.in("out"), "name": "taken.zip"}), 409)
	if readFile(t, e.in("out", "taken.zip")) != "precious" {
		t.Error("DATA LOSS: zip replaced an existing file")
	}
	for _, name := range []string{"", "../evil", "a/b", "/abs", "a\\b", "x\x00y", strings.Repeat("n", 252)} {
		w := e.post("/zip", map[string]any{"paths": []string{e.in("src")}, "dest": e.in("out"), "name": name})
		if w.Code == 202 {
			e.job(w)
			t.Errorf("zip with name %.30q was accepted", name)
		}
	}
	check()
	if exists(e.in("evil.zip")) {
		t.Error("ESCAPE: archive created outside the destination")
	}

	// The archive is written into a directory that is itself archived: it
	// must not contain itself, nor temporary files.
	e.mustRun("/zip", map[string]any{"paths": []string{e.in("src")}, "dest": e.in("src"), "name": "self"})
	b, _ = os.ReadFile(e.in("src", "self.zip"))
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	for _, zf := range zr.File {
		if strings.Contains(zf.Name, tempPrefix) || strings.HasSuffix(zf.Name, "self.zip") {
			t.Errorf("the archive contains itself: %s", zf.Name)
		}
	}
	if l := leftovers(t, e.root); len(l) > 0 {
		t.Errorf("temporary files left: %v", l)
	}
}
