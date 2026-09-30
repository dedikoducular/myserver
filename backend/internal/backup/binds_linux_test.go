//go:build linux

package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func needRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root (run in the test container)")
	}
}

// snapshot describes every entry below dir: type, mode, owner, target or
// content, and (for files) the modification time.
func snapshot(t *testing.T, dir string, withDirTimes bool) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(dir, func(p string, fi fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		st := fi.Sys().(*syscall.Stat_t)
		desc := fmt.Sprintf("%v uid=%d gid=%d", fi.Mode(), st.Uid, st.Gid)
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			desc += " -> " + target
		case fi.Mode().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			desc += fmt.Sprintf(" size=%d sha=%s mtime=%d", len(b), sha(b)[:16], fi.ModTime().UnixNano())
		case fi.IsDir() && withDirTimes:
			desc += fmt.Sprintf(" mtime=%d", fi.ModTime().UnixNano())
		}
		out[rel] = desc
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", dir, err)
	}
	return out
}

func diff(a, b map[string]string) []string {
	var out []string
	for k, v := range a {
		if w, ok := b[k]; !ok {
			out = append(out, "missing: "+k+" ("+v+")")
		} else if v != w {
			out = append(out, "differs: "+k+"\n    was "+v+"\n    is  "+w)
		}
	}
	for k, v := range b {
		if _, ok := a[k]; !ok {
			out = append(out, "new: "+k+" ("+v+")")
		}
	}
	sort.Strings(out)
	return out
}

func mustWrite(t *testing.T, p, body string, mode fs.FileMode, uid, gid int, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Lchown(p, uid, gid); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

type sandbox struct {
	base    string // everything lives below
	allowed string // the allowed root
	outside string // must never change
	roots   []string
}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	base := t.TempDir()
	s := &sandbox{base: base, allowed: filepath.Join(base, "izinli"), outside: filepath.Join(base, "disari")}
	s.roots = []string{s.allowed}
	for _, d := range []string{s.allowed, s.outside, filepath.Join(s.outside, "alt")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Unix(1_500_000_000, 0)
	mustWrite(t, filepath.Join(s.outside, "gizli.txt"), "dışarıdaki gizli dosya", 0o600, 0, 0, old)
	mustWrite(t, filepath.Join(s.outside, "alt", "kasa.kdbx"), "dışarıdaki kasa", 0o600, 0, 0, old)
	return s
}

func exportToBytes(t *testing.T, dir string) ([]byte, tarStats, *bindReport) {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	st, rep, err := exportBind(context.Background(), dir, tw, nil)
	if err != nil {
		t.Fatalf("exportBind: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), st, rep
}

func TestBindBackupAndRestoreOfARealTree(t *testing.T) {
	needRoot(t)
	s := newSandbox(t)
	src := filepath.Join(s.allowed, "kaynak")
	t1 := time.Unix(1_600_000_000, 123_456_789)
	t2 := time.Unix(946_684_800, 0) // year 2000
	big := strings.Repeat("büyük dosya içeriği\n", 60_000)

	mustWrite(t, filepath.Join(src, "foto.jpg"), "JPEG", 0o644, 1234, 2345, t1)
	mustWrite(t, filepath.Join(src, "kasa.kdbx"), "parola kasası", 0o600, 1234, 2345, t2)
	mustWrite(t, filepath.Join(src, "betik.sh"), "#!/bin/sh\n", 0o755, 0, 0, t1)
	mustWrite(t, filepath.Join(src, "bos"), "", 0o640, 65534, 65534, t2)
	mustWrite(t, filepath.Join(src, "büyük.bin"), big, 0o444, 1234, 100, t1)
	mustWrite(t, filepath.Join(src, "alt", "derin", "daha derin", "dosya adı boşluklu.txt"), "x", 0o664, 1000, 1000, t1)
	mustWrite(t, filepath.Join(src, "alt", `ters\bölü`), "y", 0o644, 1000, 1000, t1)
	mustWrite(t, filepath.Join(src, "alt", "satır\nsonu"), "z", 0o644, 1000, 1000, t1)
	mustWrite(t, filepath.Join(src, ".gizli", ".env"), "SECRET=1", 0o400, 999, 999, t2)
	if err := os.Mkdir(filepath.Join(src, "bos klasör"), 0o700); err != nil {
		t.Fatal(err)
	}
	for p, m := range map[string]fs.FileMode{"alt": 0o750, "alt/derin": 0o2775 | fs.ModeSetgid, ".gizli": 0o700} {
		if err := os.Lchown(filepath.Join(src, p), 1234, 2345); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(src, p), m&(fs.ModePerm|fs.ModeSetgid)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(src, "alt", "derin"), 0o775|fs.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		"bağ-içeri":      "foto.jpg",
		"bağ-klasör":     "alt/derin",
		"bağ-dışarı":     filepath.Join(s.outside, "gizli.txt"),
		"bağ-dışarı-dir": s.outside,
		"bağ-göreli-dış": "../../disari/alt",
		"bağ-kırık":      "/yok/boyle/bir/yer",
		"alt/bağ-yukarı": "../kasa.kdbx",
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(src, name)); err != nil {
			t.Fatal(err)
		}
		if err := os.Lchown(filepath.Join(src, name), 1234, 2345); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Lchown(src, 1234, 2345); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(src, 0o751); err != nil {
		t.Fatal(err)
	}
	// Special files.
	if err := unix.Mkfifo(filepath.Join(src, "boru"), 0o644); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", filepath.Join(src, "soket"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	specials := 2
	if err := unix.Mknod(filepath.Join(src, "aygıt"), unix.S_IFCHR|0o600, int(unix.Mkdev(1, 3))); err == nil {
		specials++
	} else {
		t.Logf("mknod unavailable: %v", err)
	}

	before := snapshot(t, src, false)
	outsideBefore := snapshot(t, s.outside, true)

	done := make(chan struct{})
	var stream []byte
	var st tarStats
	var rep *bindReport
	go func() {
		defer close(done)
		stream, st, rep = exportToBytes(t, src)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the export blocked, probably on the FIFO")
	}

	if rep.Skipped != int64(specials) || rep.Changed != 0 || rep.Vanished != 0 {
		t.Fatalf("report %+v, want %d skipped special files", rep, specials)
	}
	warnings := rep.warnings(src)
	if len(warnings) != 1 || !strings.Contains(warnings[0], fmt.Sprint(specials)) || !strings.Contains(warnings[0], "özel dosya") {
		t.Fatalf("warnings %q", warnings)
	}
	if st.Files != 9 {
		t.Fatalf("stats %+v, want 9 regular files", st)
	}
	entries := listTar(t, stream)
	for _, special := range []string{"boru", "soket", "aygıt"} {
		if _, ok := entries[special]; ok {
			t.Fatalf("the special file %q is in the backup", special)
		}
	}
	for name, target := range links {
		h := entries[name]
		if h == nil || h.Typeflag != tar.TypeSymlink || h.Linkname != target {
			t.Fatalf("symlink %q stored as %+v, want a link to %q", name, h, target)
		}
	}
	for name, h := range entries {
		if strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
			t.Fatalf("entry name %q", name)
		}
		body := h.PAXRecords["test.body"]
		if strings.Contains(body, "dışarıdaki") {
			t.Fatalf("entry %q holds the content of a file outside the folder: a symlink was followed", name)
		}
	}
	if h := entries["foto.jpg"]; h == nil || h.Uid != 1234 || h.Gid != 2345 || h.Mode&0o7777 != 0o644 || !h.ModTime.Equal(t1) {
		t.Fatalf("foto.jpg stored as %+v", h)
	}

	// Restore over a folder that has other content.
	dst := filepath.Join(s.allowed, "hedef")
	mustWrite(t, filepath.Join(dst, "eski.txt"), "eski", 0o644, 0, 0, t1)
	mustWrite(t, filepath.Join(dst, "foto.jpg"), "eski foto", 0o600, 0, 0, t2)
	mustWrite(t, filepath.Join(dst, "alt", "eski-alt.txt"), "eski", 0o644, 0, 0, t1)
	_, irep, err := importBind(context.Background(), dst, s.roots, bytes.NewReader(stream), nil)
	if err != nil {
		t.Fatalf("importBind: %v", err)
	}
	if irep.Meta != 0 || irep.Skipped != 0 {
		t.Fatalf("import report %+v", irep)
	}
	after := snapshot(t, dst, false)
	for _, special := range []string{"boru", "soket", "aygıt"} {
		delete(before, special)
	}
	if d := diff(before, after); len(d) != 0 {
		t.Fatalf("the restored tree differs from the original:\n%s", strings.Join(d, "\n"))
	}
	if d := diff(outsideBefore, snapshot(t, s.outside, true)); len(d) != 0 {
		t.Fatalf("files outside the folder changed:\n%s", strings.Join(d, "\n"))
	}

	// Restoring into the folder the backup was taken from gives the same.
	if err := os.Remove(filepath.Join(src, "foto.jpg")); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(src, "kasa.kdbx"), "bozulmuş", 0o666, 0, 0, t1)
	if _, _, err := importBind(context.Background(), src, s.roots, bytes.NewReader(stream), nil); err != nil {
		t.Fatalf("importBind in place: %v", err)
	}
	if d := diff(before, snapshot(t, src, false)); len(d) != 0 {
		t.Fatalf("restore in place differs:\n%s", strings.Join(d, "\n"))
	}
}

func TestBindDirectoryTimesAreRestored(t *testing.T) {
	needRoot(t)
	s := newSandbox(t)
	src := filepath.Join(s.allowed, "kaynak")
	when := time.Unix(1_400_000_000, 0)
	mustWrite(t, filepath.Join(src, "alt", "a.txt"), "a", 0o644, 0, 0, when)
	for _, d := range []string{filepath.Join(src, "alt"), src} {
		if err := os.Chtimes(d, when, when); err != nil {
			t.Fatal(err)
		}
	}
	stream, _, _ := exportToBytes(t, src)
	dst := filepath.Join(s.allowed, "hedef")
	if _, _, err := importBind(context.Background(), dst, s.roots, bytes.NewReader(stream), nil); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"alt", "."} {
		fi, err := os.Stat(filepath.Join(dst, d))
		if err != nil {
			t.Fatal(err)
		}
		if !fi.ModTime().Equal(when) {
			t.Errorf("directory %q: modification time %v, want %v", d, fi.ModTime().UTC(), when.UTC())
		}
	}
}

func TestExportOfAFolderThatIsNotThere(t *testing.T) {
	s := newSandbox(t)
	var buf bytes.Buffer
	_, _, err := exportBind(context.Background(), filepath.Join(s.allowed, "yok"), tar.NewWriter(&buf), nil)
	var ue *userError
	if !errors.As(err, &ue) || !strings.Contains(ue.Message, "bulunamadı") {
		t.Fatalf("err = %v", err)
	}
	file := filepath.Join(s.allowed, "dosya")
	mustWrite(t, file, "x", 0o644, os.Getuid(), os.Getgid(), time.Now())
	if _, _, err := exportBind(context.Background(), file, tar.NewWriter(&buf), nil); err == nil {
		t.Fatal("a regular file was accepted as a folder")
	}
}

func TestBindSizeDoesNotFollowSymlinks(t *testing.T) {
	s := newSandbox(t)
	src := filepath.Join(s.allowed, "kaynak")
	mustWrite(t, filepath.Join(src, "a"), strings.Repeat("x", 1000), 0o644, os.Getuid(), os.Getgid(), time.Now())
	mustWrite(t, filepath.Join(src, "d", "b"), strings.Repeat("x", 500), 0o644, os.Getuid(), os.Getgid(), time.Now())
	mustWrite(t, filepath.Join(s.outside, "dev"), strings.Repeat("x", 1_000_000), 0o644, os.Getuid(), os.Getgid(), time.Now())
	if err := os.Symlink(s.outside, filepath.Join(src, "dis")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(s.outside, "dev"), filepath.Join(src, "dev")); err != nil {
		t.Fatal(err)
	}
	n, err := bindSize(context.Background(), src)
	if err != nil || n != 1500 {
		t.Fatalf("bindSize = %d, %v; want 1500", n, err)
	}
}

func TestRestoreClearsTheFolderWithoutFollowingSymlinks(t *testing.T) {
	needRoot(t)
	s := newSandbox(t)
	dst := filepath.Join(s.allowed, "hedef")
	now := time.Now()
	mustWrite(t, filepath.Join(dst, "eski.txt"), "eski", 0o644, 0, 0, now)
	mustWrite(t, filepath.Join(dst, ".gizli-eski"), "eski", 0o644, 0, 0, now)
	mustWrite(t, filepath.Join(dst, "alt", "derin", "eski.txt"), "eski", 0o400, 0, 0, now)
	if err := os.Chmod(filepath.Join(dst, "alt", "derin"), 0o500); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"dis-klasor":          s.outside,
		"dis-dosya":           filepath.Join(s.outside, "gizli.txt"),
		"alt/dis-goreli":      "../../../disari",
		"alt/derin/dis-derin": filepath.Join(s.outside, "alt"),
	} {
		p := filepath.Join(dst, name)
		os.Chmod(filepath.Dir(p), 0o755)
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	outsideBefore := snapshot(t, s.outside, true)

	stream := tarStream(t, tarFile{name: "./", typ: tar.TypeDir}, tarFile{name: "yeni.txt", body: "yeni"})
	if _, _, err := importBind(context.Background(), dst, s.roots, bytes.NewReader(stream), nil); err != nil {
		t.Fatalf("importBind: %v", err)
	}
	got := snapshot(t, dst, false)
	delete(got, ".")
	if len(got) != 1 || !strings.Contains(got["yeni.txt"], "size=4") {
		t.Fatalf("after the restore the folder holds %v; stale files must be gone", got)
	}
	if d := diff(outsideBefore, snapshot(t, s.outside, true)); len(d) != 0 {
		t.Fatalf("clearing the folder followed a symlink out of it:\n%s", strings.Join(d, "\n"))
	}
}

func TestRestoreTargetMustResolveInsideTheAllowedRoots(t *testing.T) {
	needRoot(t)
	s := newSandbox(t)
	if err := os.Symlink(s.outside, filepath.Join(s.allowed, "bag")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../disari", filepath.Join(s.allowed, "goreli")); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, s.outside, true)
	stream := tarStream(t, tarFile{name: "./", typ: tar.TypeDir}, tarFile{name: "yeni.txt", body: "yeni"})
	targets := []string{
		filepath.Join(s.allowed, "bag"),
		filepath.Join(s.allowed, "bag", "alt"),
		filepath.Join(s.allowed, "bag", "yeni", "klasor"),
		filepath.Join(s.allowed, "goreli"),
		filepath.Join(s.allowed, "goreli", "alt"),
		s.outside,
		s.allowed + "/../disari",
		s.allowed + "/./x",
		"/etc/myserver-test",
		"/",
		"goreli/yol",
		"",
	}
	for _, dir := range targets {
		_, _, err := importBind(context.Background(), dir, s.roots, bytes.NewReader(stream), nil)
		if err == nil {
			t.Fatalf("restore into %q was allowed", dir)
		}
		if msg := messageOf(err); strings.HasPrefix(msg, "Beklenmeyen") {
			t.Fatalf("%q: generic message for %v", dir, err)
		}
	}
	if d := diff(before, snapshot(t, s.outside, true)); len(d) != 0 {
		t.Fatalf("a folder outside the allowed roots changed:\n%s", strings.Join(d, "\n"))
	}
	if _, err := os.Lstat("/etc/myserver-test"); err == nil {
		t.Fatal("/etc/myserver-test was created")
	}
}

// Every hostile stream is restored into a real folder; whatever the outcome
// (refused or, for harmless variants, extracted), nothing outside the
// folder may appear or change.
func TestHostileStreamsCannotLeaveTheFolder(t *testing.T) {
	needRoot(t)
	probe := "/myserver-test-kacis"
	streams := func(s *sandbox) map[string][]byte {
		out := s.outside
		return map[string][]byte{
			"absolute":                   endOfTar(rawFile(probe, "x")),
			"absolute into outside":      endOfTar(rawFile(out+"/yeni", "x")),
			"parent":                     endOfTar(rawFile("../../disari/yeni", "x")),
			"parent overwrite":           endOfTar(rawFile("../../disari/gizli.txt", "üzerine yazıldı")),
			"dot slash parent":           endOfTar(rawFile("./../../disari/yeni", "x")),
			"pax parent":                 endOfTar(paxHeader(map[string]string{"path": "../../disari/yeni"}), rawFile("masum", "x")),
			"gnu long parent":            endOfTar(gnuLong(tar.TypeGNULongName, "../../disari/yeni"), rawFile("masum", "x")),
			"two step, absolute link":    endOfTar(rawHeader("s", tar.TypeSymlink, 0, out), rawFile("s/yeni", "x")),
			"two step, relative link":    endOfTar(rawHeader("s", tar.TypeSymlink, 0, "../../disari"), rawFile("s/yeni", "x")),
			"two step, overwrite":        endOfTar(rawHeader("s", tar.TypeSymlink, 0, out), rawFile("s/gizli.txt", "üzerine yazıldı")),
			"two step, nested":           endOfTar(rawHeader("d/", tar.TypeDir, 0, ""), rawHeader("d/s", tar.TypeSymlink, 0, "../../../disari"), rawFile("d/s/alt/yeni", "x")),
			"two step, chained links":    endOfTar(rawHeader("a", tar.TypeSymlink, 0, out), rawHeader("b", tar.TypeSymlink, 0, "a"), rawFile("b/yeni", "x")),
			"two step, dir in between":   endOfTar(rawHeader("s", tar.TypeSymlink, 0, out), rawHeader("s/", tar.TypeDir, 0, ""), rawFile("s/yeni", "x")),
			"two step, mkdir":            endOfTar(rawHeader("s", tar.TypeSymlink, 0, out), rawHeader("s/yeni-klasor/", tar.TypeDir, 0, "")),
			"file over a link to a file": endOfTar(rawHeader("s", tar.TypeSymlink, 0, out+"/gizli.txt"), rawFile("s", "üzerine yazıldı")),
			"link over link then file":   endOfTar(rawHeader("s", tar.TypeSymlink, 0, "x"), rawHeader("s", tar.TypeSymlink, 0, out+"/gizli.txt"), rawFile("s", "üzerine yazıldı")),
			"hard link absolute":         endOfTar(rawHeader("h", tar.TypeLink, 0, out+"/gizli.txt")),
			"hard link parent":           endOfTar(rawHeader("h", tar.TypeLink, 0, "../../disari/gizli.txt")),
			"hard link via symlink":      endOfTar(rawHeader("s", tar.TypeSymlink, 0, out), rawHeader("h", tar.TypeLink, 0, "s/gizli.txt")),
			"hard link to symlink":       endOfTar(rawHeader("s", tar.TypeSymlink, 0, out+"/gizli.txt"), rawHeader("h", tar.TypeLink, 0, "s"), rawFile("h", "üzerine yazıldı")),
			"root replaced by a link":    endOfTar(rawHeader("./", tar.TypeSymlink, 0, out), rawFile("yeni", "x")),
			"device":                     endOfTar(rawHeader("sda", tar.TypeBlock, 0, "")),
			"char device":                endOfTar(rawHeader("mem", tar.TypeChar, 0, "")),
			"fifo":                       endOfTar(rawHeader("boru", tar.TypeFifo, 0, "")),
			"backslashes":                endOfTar(rawFile(`..\..\disari\yeni`, "x")),
			"long component":             endOfTar(paxHeader(map[string]string{"path": strings.Repeat("a", 300)}), rawFile("masum", "x")),
			"short body":                 append(rawHeader("x", tar.TypeReg, 5000, ""), []byte("kısa")...),
			"huge size":                  append(rawHeader("x", tar.TypeReg, 1<<40, ""), make([]byte, 4096)...),
		}
	}
	for name := range streams(newSandbox(t)) {
		t.Run(name, func(t *testing.T) {
			s := newSandbox(t)
			dst := filepath.Join(s.allowed, "uygulama", "hedef")
			if err := os.MkdirAll(dst, 0o755); err != nil {
				t.Fatal(err)
			}
			mustWrite(t, filepath.Join(s.allowed, "komsu.txt"), "komşu uygulamanın verisi", 0o600, 0, 0, time.Unix(1_500_000_000, 0))
			before := snapshot(t, s.base, true)
			for k := range before {
				if k == "izinli/uygulama/hedef" || strings.HasPrefix(k, "izinli/uygulama/hedef/") {
					delete(before, k)
				}
			}
			etc := snapshot(t, "/etc", true)

			_, _, err := importBind(context.Background(), dst, s.roots, bytes.NewReader(streams(s)[name]), nil)
			t.Logf("result: %v", err)

			after := snapshot(t, s.base, true)
			for k := range after {
				if k == "izinli/uygulama/hedef" || strings.HasPrefix(k, "izinli/uygulama/hedef/") {
					delete(after, k)
				}
			}
			if d := diff(before, after); len(d) != 0 {
				t.Fatalf("something outside the folder changed:\n%s", strings.Join(d, "\n"))
			}
			if d := diff(etc, snapshot(t, "/etc", true)); len(d) != 0 {
				t.Fatalf("/etc changed:\n%s", strings.Join(d, "\n"))
			}
			if _, err := os.Lstat(probe); err == nil {
				os.Remove(probe)
				t.Fatalf("%s was created", probe)
			}
			// Nothing inside may be a device or FIFO, and no file inside may
			// share its inode with a file outside.
			filepath.Walk(dst, func(p string, fi fs.FileInfo, err error) error {
				if err != nil {
					return nil
				}
				if fi.Mode()&(fs.ModeDevice|fs.ModeCharDevice|fs.ModeNamedPipe|fs.ModeSocket) != 0 {
					t.Errorf("special file created: %s %v", p, fi.Mode())
				}
				if fi.Mode().IsRegular() && fi.Sys().(*syscall.Stat_t).Nlink > 1 {
					t.Errorf("%s is a hard link (nlink %d)", p, fi.Sys().(*syscall.Stat_t).Nlink)
				}
				return nil
			})
			switch name {
			case "backslashes":
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if b, rerr := os.ReadFile(filepath.Join(dst, `..\..\disari\yeni`)); rerr != nil || string(b) != "x" {
					t.Fatalf("the file with backslashes in its name is not in the folder: %v", rerr)
				}
			case "device", "char device", "fifo":
				// skipped, with a warning
			default:
				if err == nil {
					t.Fatal("the stream was accepted")
				}
			}
		})
	}
}

// The second line of defence: even when a hostile entry reaches the
// extraction without having been validated, the os.Root of the folder
// refuses to write outside.
func TestExtractionIsConfinedByTheRoot(t *testing.T) {
	needRoot(t)
	s := newSandbox(t)
	dst := filepath.Join(s.allowed, "hedef")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"dis": s.outside, "dis-dosya": filepath.Join(s.outside, "gizli.txt"), "goreli": "../../disari"} {
		if err := os.Symlink(target, filepath.Join(dst, name)); err != nil {
			t.Fatal(err)
		}
	}
	before := snapshot(t, s.outside, true)
	root, err := os.OpenRoot(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	entries := []*tar.Header{
		{Name: "../../disari/yeni", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "../kardes", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: s.outside + "/yeni", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "dis/yeni", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "dis/gizli.txt", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "goreli/yeni", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "dis-dosya", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "dis/yeni-klasor/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "dis/alt/derin/yeni", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "dis/", Typeflag: tar.TypeDir, Mode: 0o777, Uid: 4242, Gid: 4242},
		{Name: "dis/baglanti", Typeflag: tar.TypeSymlink, Linkname: "/etc"},
		{Name: "sabit", Typeflag: tar.TypeLink, Linkname: "dis/gizli.txt"},
		{Name: "sabit2", Typeflag: tar.TypeLink, Linkname: "../../disari/gizli.txt"},
		{Name: "sabit3", Typeflag: tar.TypeLink, Linkname: s.outside + "/gizli.txt"},
		{Name: "dis/sabit4", Typeflag: tar.TypeLink, Linkname: "dis-dosya"},
	}
	buf := make([]byte, 4096)
	for _, h := range entries {
		rep := &bindReport{}
		err := extractEntry(context.Background(), root, h, strings.NewReader("üzerine yazıldı"), buf, rep, nil)
		if err == nil && h.Typeflag != tar.TypeDir {
			t.Errorf("entry %q (%c): no error", h.Name, h.Typeflag)
		}
		if d := diff(before, snapshot(t, s.outside, true)); len(d) != 0 {
			t.Fatalf("entry %q (%c) changed something outside the folder:\n%s", h.Name, h.Typeflag, strings.Join(d, "\n"))
		}
	}
	if _, err := os.Lstat(filepath.Join(s.allowed, "kardes")); err == nil {
		t.Fatal("a file was created next to the folder")
	}
}

// Documents what a restore does with setuid, setgid and sticky bits: they
// are restored as stored, together with the owner. See the report: for an
// uploaded archive this creates whatever setuid files the archive describes.
func TestImportBindSpecialModeBits(t *testing.T) {
	needRoot(t)
	s := newSandbox(t)
	dst := filepath.Join(s.allowed, "hedef")
	stream := tarStream(t,
		tarFile{name: "./", typ: tar.TypeDir},
		tarFile{name: "su", body: "x", mode: 0o4755, uid: 0, gid: 0},
		tarFile{name: "su-user", body: "x", mode: 0o4755, uid: 1234, gid: 2345},
		tarFile{name: "sg", body: "x", mode: 0o2755, uid: 1234, gid: 2345},
		tarFile{name: "tmp/", typ: tar.TypeDir, mode: 0o1777, uid: 1234, gid: 2345},
		tarFile{name: "sgdir/", typ: tar.TypeDir, mode: 0o2775, uid: 1234, gid: 2345},
	)
	if _, _, err := importBind(context.Background(), dst, s.roots, bytes.NewReader(stream), nil); err != nil {
		t.Fatal(err)
	}
	want := map[string]fs.FileMode{
		"su":      0o755 | fs.ModeSetuid,
		"su-user": 0o755 | fs.ModeSetuid,
		"sg":      0o755 | fs.ModeSetgid,
		"tmp":     0o777 | fs.ModeSticky | fs.ModeDir,
		"sgdir":   0o775 | fs.ModeSetgid | fs.ModeDir,
	}
	for name, mode := range want {
		fi, err := os.Lstat(filepath.Join(dst, name))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode() != mode {
			t.Errorf("%s: mode %v, want %v", name, fi.Mode(), mode)
		}
	}
}

func TestImportStopsWhenCancelled(t *testing.T) {
	needRoot(t)
	s := newSandbox(t)
	dst := filepath.Join(s.allowed, "hedef")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stream := tarStream(t, tarFile{name: "./", typ: tar.TypeDir}, tarFile{name: "a", body: "x"})
	_, _, err := importBind(ctx, dst, s.roots, bytes.NewReader(stream), nil)
	if err == nil {
		t.Fatal("no error")
	}
}
