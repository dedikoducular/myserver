package files

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

/* ---------- path confinement over HTTP ---------- */

func TestHTTPPathConfinement(t *testing.T) {
	e := newEnv(t)
	e.write(e.in("a", "inside.txt"), "inside")
	var okZip bytes.Buffer
	zw := zip.NewWriter(&okZip)
	zf, _ := zw.Create("pwned.txt")
	zf.Write([]byte("pwned"))
	zw.Close()
	e.write(e.in("ok.zip"), okZip.String())
	check := e.guard()

	bad := map[string]string{
		"dotdot to outside":       e.root + "/../outside",
		"dotdot to secret":        e.root + "/../outside/secret.txt",
		"dotdot deep":             e.root + "/a/../../outside/secret.txt",
		"dotdot staying inside":   e.root + "/a/../a/inside.txt",
		"dotdot at start":         "/.." + e.root,
		"dotdot at end":           e.root + "/a/..",
		"absolute outside":        e.outside,
		"absolute outside file":   e.outside + "/secret.txt",
		"etc passwd":              "/etc/passwd",
		"filesystem root":         "/",
		"parent of root":          e.base,
		"prefix sibling":          e.sibling,
		"prefix sibling file":     e.sibling + "/secret.txt",
		"nul":                     e.root + "/a\x00",
		"nul hiding suffix":       e.outside + "/secret.txt\x00" + e.root,
		"invalid utf8":            e.root + "/\xff\xfe",
		"backslash traversal":     e.root + "\\..\\outside\\secret.txt",
		"backslash in segment":    e.root + "/a\\..\\..\\outside\\secret.txt",
		"relative":                "root/a",
		"relative dotdot":         "../outside",
		"empty":                   "",
		"over-long path":          e.root + "/" + strings.Repeat("d/", 2100),
		"over-long segment":       e.root + "/" + strings.Repeat("s", 256),
		"percent encoded dotdot":  e.root + "/%2e%2e/outside/secret.txt",
		"double encoded traverse": e.root + "/..%2foutside%2fsecret.txt",
	}
	// These look like traversal but, once decoded, are ordinary (odd) names
	// INSIDE the root: a backslash and a percent sign are plain characters
	// on Linux, and JSON cannot carry invalid UTF-8 (the decoder replaces
	// it). For them the only requirement is that nothing outside the root
	// is read or changed.
	lookalike := map[string]bool{
		"invalid utf8": true, "backslash in segment": true,
		"percent encoded dotdot": true, "double encoded traverse": true,
	}
	for name, p := range bad {
		strict := !lookalike[name]
		direct := func(what string, w *httptest.ResponseRecorder, allowed ...int) {
			t.Helper()
			if strict {
				refused(t, name+" "+what, w, allowed...)
				return
			}
			noSecret(t, name+" "+what, w)
			e.job(w)
		}
		run := func(what, endpoint string, body any) {
			t.Helper()
			if strict {
				e.mustNotRun(name+" "+what, endpoint, body)
				return
			}
			w := e.post(endpoint, body)
			noSecret(t, name+" "+what, w)
			e.job(w)
		}
		for _, ep := range []string{"/list", "/stat", "/download", "/preview", "/text", "/count"} {
			refused(t, name+" GET "+ep, e.get(e.admin, ep, p), 400, 403, 404, 415)
		}
		direct("mkdir", e.post("/mkdir", map[string]string{"path": p, "name": "pwned"}))
		direct("rename", e.post("/rename", map[string]string{"path": p, "name": "pwned"}))
		direct("chmod", e.post("/chmod", map[string]string{"path": p, "mode": "777"}))
		direct("text write", e.call(e.admin, http.MethodPut, "/text", map[string]any{"path": p, "content": "pwned"}))
		refused(t, name+" upload", e.upload(p, "pwned.txt", []byte("pwned"), true))
		direct("size", e.post("/size", map[string]string{"path": p}))
		run("delete", "/delete", map[string]any{"paths": []string{p}})
		run("copy from", "/copy", map[string]any{"paths": []string{p}, "dest": e.in("a")})
		run("copy to", "/copy", map[string]any{"paths": []string{e.in("a", "inside.txt")}, "dest": p})
		run("move from", "/move", map[string]any{"paths": []string{p}, "dest": e.in("a")})
		run("move to", "/move", map[string]any{"paths": []string{e.in("a", "inside.txt")}, "dest": p})
		run("zip from", "/zip", map[string]any{"paths": []string{p}, "dest": e.root, "name": "z-" + strings.ReplaceAll(name, " ", "-")})
		run("zip to", "/zip", map[string]any{"paths": []string{e.in("a")}, "dest": p, "name": "pwned"})
		run("extract to", "/extract", map[string]any{"path": e.in("ok.zip"), "dest": p})
		run("extract from", "/extract", map[string]any{"path": p, "dest": e.root})
		check()
		if !exists(e.in("a", "inside.txt")) {
			e.write(e.in("a", "inside.txt"), "inside")
		}
	}
	if got := readFile(t, e.in("a", "inside.txt")); got != "inside" {
		t.Errorf("a refused request modified a file inside the root: %q", got)
	}
	if found := regularFilesWith(t, e.root, secret); len(found) > 0 {
		t.Errorf("the secret was copied into the root: %v", found)
	}

	// Sanity: the same endpoints do work for a legitimate path, so the
	// refusals above are not an artefact of a broken harness.
	if w := e.get(e.admin, "/list", e.in("a")); w.Code != 200 {
		t.Fatalf("list of a legitimate directory failed: %d %s", w.Code, w.Body)
	}
	if w := e.get(e.admin, "/download", e.in("a", "inside.txt")); w.Code != 200 || w.Body.String() != "inside" {
		t.Fatalf("download of a legitimate file failed: %d %s", w.Code, w.Body)
	}
}

/* ---------- symlinks ---------- */

// symlinkEnv adds to the root:
//
//	linkdir  -> <outside>            (absolute)
//	reldir   -> ../outside           (relative)
//	linkfile -> <outside>/secret.txt (absolute)
//	relfile  -> ../outside/secret.txt
//	pic.png  -> <outside>/secret.txt
//	x.zip    -> <outside>/archive.zip
//	real/    a normal directory with a file
func symlinkEnv(t *testing.T) *testEnv {
	e := newEnv(t)
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	f, _ := zw.Create("from-outside.txt")
	f.Write([]byte(secret))
	zw.Close()
	e.write(filepath.Join(e.outside, "archive.zip"), zb.String())
	e.mkdir(filepath.Join(e.outside, "sub"))

	e.symlink(e.outside, e.in("linkdir"))
	e.symlink("../outside", e.in("reldir"))
	e.symlink(filepath.Join(e.outside, "secret.txt"), e.in("linkfile"))
	e.symlink("../outside/secret.txt", e.in("relfile"))
	e.symlink(filepath.Join(e.outside, "secret.txt"), e.in("pic.png"))
	e.symlink(filepath.Join(e.outside, "archive.zip"), e.in("x.zip"))
	e.write(e.in("real", "file.txt"), "inside")
	e.mkdir(e.in("dst"))
	return e
}

func TestSymlinkOutsideCannotBeRead(t *testing.T) {
	e := symlinkEnv(t)
	check := e.guard()

	for _, dir := range []string{"linkdir", "reldir"} {
		refused(t, "list "+dir, e.get(e.admin, "/list", e.in(dir)))
		refused(t, "list below "+dir, e.get(e.admin, "/list", e.in(dir, "sub")))
		refused(t, "stat below "+dir, e.get(e.admin, "/stat", e.in(dir, "secret.txt")))
		refused(t, "download below "+dir, e.get(e.admin, "/download", e.in(dir, "secret.txt")))
		refused(t, "text below "+dir, e.get(e.admin, "/text", e.in(dir, "secret.txt")))
		refused(t, "count below "+dir, e.get(e.admin, "/count", e.in(dir, "secret.txt")))
		refused(t, "download dir "+dir, e.get(e.admin, "/download", e.in(dir)))
	}
	for _, f := range []string{"linkfile", "relfile", "pic.png"} {
		refused(t, "download "+f, e.get(e.admin, "/download", e.in(f)))
		refused(t, "text "+f, e.get(e.admin, "/text", e.in(f)))
		refused(t, "preview "+f, e.get(e.admin, "/preview", e.in(f)), 400, 403, 404, 415)
	}

	// Listing the root shows the links as links, marked unreachable, and
	// reveals neither size nor content of what they point to.
	w := e.get(e.admin, "/list", e.root)
	if w.Code != 200 {
		t.Fatalf("list root: %d %s", w.Code, w.Body)
	}
	noSecret(t, "list root", w)
	var data struct {
		Entries []Entry `json:"entries"`
	}
	if err := json.Unmarshal(decode(t, w).Data, &data); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, en := range data.Entries {
		switch en.Name {
		case "linkdir", "reldir", "linkfile", "relfile", "pic.png", "x.zip":
			seen++
			if en.Type != "symlink" || en.LinkKind == nil || *en.LinkKind != "unreachable" {
				t.Errorf("%s: type %q kind %v, want an unreachable symlink", en.Name, en.Type, en.LinkKind)
			}
			if en.Size != 0 {
				t.Errorf("%s: size %d of the outside target is disclosed", en.Name, en.Size)
			}
		}
	}
	if seen != 6 {
		t.Errorf("expected the 6 links in the listing, saw %d", seen)
	}

	// A multi-selection download is a ZIP; links must not be followed.
	w = e.get(e.admin, "/download", e.in("linkfile"), e.in("linkdir"), e.in("reldir"), e.in("real"))
	if w.Code != 200 {
		t.Fatalf("zip download: %d %s", w.Code, w.Body)
	}
	assertZipClean(t, w.Body.Bytes(), "real/file.txt")

	// The size job must not walk through the link either.
	v, ok := e.job(e.post("/size", map[string]string{"path": e.root}))
	if !ok || v.Status != jobDone {
		t.Fatalf("size job: %+v", v)
	}
	var totals treeTotals
	b, _ := json.Marshal(v.Result)
	json.Unmarshal(b, &totals)
	if totals.Bytes != int64(len("inside")) {
		t.Errorf("size of the root counts %d bytes, want %d (links must not be followed)", totals.Bytes, len("inside"))
	}
	check()
}

// assertZipClean checks that an archive carries no data from outside and
// contains the wanted member.
func assertZipClean(t *testing.T, b []byte, want string) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	found := false
	for _, zf := range zr.File {
		if zf.Mode()&os.ModeSymlink != 0 {
			t.Errorf("zip member %q is a symlink", zf.Name)
		}
		rc, err := zf.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, _ := io.ReadAll(rc)
		rc.Close()
		if bytes.Contains(content, []byte(secret)) {
			t.Errorf("LEAK: zip member %q contains data from outside the root", zf.Name)
		}
		if strings.Contains(zf.Name, "secret") || strings.Contains(zf.Name, "archive.zip") {
			t.Errorf("LEAK: zip member %q names a file from outside the root", zf.Name)
		}
		if zf.Name == want {
			found = true
		}
	}
	if want != "" && !found {
		t.Errorf("zip lacks member %q", want)
	}
}

func TestSymlinkOutsideCannotBeWritten(t *testing.T) {
	e := symlinkEnv(t)
	check := e.guard()

	for _, dir := range []string{"linkdir", "reldir"} {
		d := e.in(dir)
		refused(t, "mkdir in "+dir, e.post("/mkdir", map[string]string{"path": d, "name": "pwned"}))
		refused(t, "mkdir below "+dir, e.post("/mkdir", map[string]string{"path": d + "/sub", "name": "pwned"}))
		refused(t, "upload to "+dir, e.upload(d, "pwned.txt", []byte("pwned"), false))
		refused(t, "upload overwrite to "+dir, e.upload(d, "secret.txt", []byte("pwned"), true))
		refused(t, "text write new in "+dir, e.call(e.admin, http.MethodPut, "/text", map[string]any{"path": d + "/pwned.txt", "content": "pwned"}))
		refused(t, "text overwrite in "+dir, e.call(e.admin, http.MethodPut, "/text", map[string]any{"path": d + "/secret.txt", "content": "pwned"}))
		refused(t, "rename in "+dir, e.post("/rename", map[string]string{"path": d + "/secret.txt", "name": "pwned"}))
		refused(t, "chmod in "+dir, e.post("/chmod", map[string]string{"path": d + "/secret.txt", "mode": "777"}))
		e.mustNotRun("delete below "+dir, "/delete", map[string]any{"paths": []string{d + "/secret.txt"}})
		e.mustNotRun("delete dir below "+dir, "/delete", map[string]any{"paths": []string{d + "/sub"}})
		e.mustNotRun("copy from below "+dir, "/copy", map[string]any{"paths": []string{d + "/secret.txt"}, "dest": e.in("dst")})
		e.mustNotRun("copy into "+dir, "/copy", map[string]any{"paths": []string{e.in("real")}, "dest": d})
		e.mustNotRun("copy into below "+dir, "/copy", map[string]any{"paths": []string{e.in("real")}, "dest": d + "/sub"})
		e.mustNotRun("move from below "+dir, "/move", map[string]any{"paths": []string{d + "/secret.txt"}, "dest": e.in("dst")})
		e.mustNotRun("move into "+dir, "/move", map[string]any{"paths": []string{e.in("real")}, "dest": d})
		e.mustNotRun("zip from below "+dir, "/zip", map[string]any{"paths": []string{d + "/secret.txt"}, "dest": e.in("dst"), "name": "a-" + dir})
		e.mustNotRun("zip into "+dir, "/zip", map[string]any{"paths": []string{e.in("real")}, "dest": d, "name": "pwned"})
		e.mustNotRun("extract from below "+dir, "/extract", map[string]any{"path": d + "/archive.zip", "dest": e.in("dst")})
		check()
	}
	if !exists(e.in("real", "file.txt")) {
		t.Error("a refused move removed its source")
	}

	// Archive inside the root, destination through a link.
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	f, _ := zw.Create("pwned.txt")
	f.Write([]byte("pwned"))
	zw.Close()
	e.write(e.in("ok.zip"), zb.String())
	for _, dir := range []string{"linkdir", "reldir"} {
		e.mustNotRun("extract into "+dir, "/extract", map[string]any{"path": e.in("ok.zip"), "dest": e.in(dir)})
	}
	e.mustNotRun("extract a linked archive", "/extract", map[string]any{"path": e.in("x.zip"), "dest": e.in("dst")})

	for _, f := range []string{"linkfile", "relfile"} {
		refused(t, "text write through "+f, e.call(e.admin, http.MethodPut, "/text", map[string]any{"path": e.in(f), "content": "pwned"}))
		refused(t, "chmod "+f, e.post("/chmod", map[string]string{"path": e.in(f), "mode": "777"}))
	}
	check()
	if found := regularFilesWith(t, e.root, secret); len(found) > 0 {
		t.Errorf("LEAK: data from outside was copied into the root: %v", found)
	}
	if got := names(t, e.in("dst")); len(got) != 0 {
		t.Errorf("refused operations left entries in the destination: %v", got)
	}
}

// Operations on the link itself act on the link and never on its target.
func TestSymlinkItselfIsHandledAsLink(t *testing.T) {
	e := symlinkEnv(t)
	check := e.guard()

	// Copying links copies them as links; no outside data is materialised.
	e.mustRun("/copy", map[string]any{"paths": []string{e.in("linkfile"), e.in("linkdir"), e.in("relfile")}, "dest": e.in("dst")})
	for _, n := range []string{"linkfile", "linkdir", "relfile"} {
		fi, err := os.Lstat(e.in("dst", n))
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("copy of %s: want a symlink in the destination, got %v %v", n, fi, err)
		}
	}
	if found := regularFilesWith(t, e.root, secret); len(found) > 0 {
		t.Fatalf("LEAK: copy followed a link: %v", found)
	}
	check()

	// A directory that contains links.
	e.symlink(e.outside, e.in("tree", "deep", "out"))
	e.symlink(filepath.Join(e.outside, "secret.txt"), e.in("tree", "s.txt"))
	e.write(e.in("tree", "plain.txt"), "inside")
	e.mustRun("/copy", map[string]any{"paths": []string{e.in("tree")}, "dest": e.in("dst")})
	if found := regularFilesWith(t, e.root, secret); len(found) > 0 {
		t.Fatalf("LEAK: recursive copy followed a link: %v", found)
	}
	if readFile(t, e.in("dst", "tree", "plain.txt")) != "inside" {
		t.Error("recursive copy lost a regular file")
	}
	check()

	// Zip skips links.
	e.mustRun("/zip", map[string]any{"paths": []string{e.in("tree"), e.in("linkfile"), e.in("linkdir")}, "dest": e.in("real"), "name": "out"})
	zb, err := os.ReadFile(e.in("real", "out.zip"))
	if err != nil {
		t.Fatal(err)
	}
	assertZipClean(t, zb, "tree/plain.txt")
	check()

	// Uploading over a link replaces the link, not the target.
	if w := e.upload(e.root, "linkfile", []byte("new content"), false); w.Code != http.StatusConflict {
		t.Errorf("upload onto an existing link without overwrite: %d, want 409", w.Code)
	}
	check()
	if w := e.upload(e.root, "linkfile", []byte("new content"), true); w.Code == http.StatusCreated {
		fi, err := os.Lstat(e.in("linkfile"))
		if err != nil || !fi.Mode().IsRegular() {
			t.Errorf("after overwriting a link the entry should be a regular file: %v %v", fi, err)
		}
	}
	check()

	// Copy with overwrite onto a link in the destination.
	e.write(e.in("src2", "relfile"), "replacement")
	e.mustRun("/copy", map[string]any{"paths": []string{e.in("src2", "relfile")}, "dest": e.root, "conflict": "overwrite"})
	if readFile(t, filepath.Join(e.outside, "secret.txt")) != secret {
		t.Fatal("ESCAPE: copy wrote through a symlink")
	}
	check()

	// Moving into a directory whose name is taken by a link to a directory.
	e.write(e.in("src3", "linkdir", "pwned.txt"), "pwned")
	e.mustNotRun("merge into a linked directory", "/move",
		map[string]any{"paths": []string{e.in("src3", "linkdir")}, "dest": e.root, "conflict": "overwrite"})
	e.mustNotRun("copy-merge into a linked directory", "/copy",
		map[string]any{"paths": []string{e.in("src3", "linkdir")}, "dest": e.root, "conflict": "overwrite"})
	check()

	// Moving and renaming links moves the link.
	e.mustRun("/move", map[string]any{"paths": []string{e.in("reldir")}, "dest": e.in("real")})
	if fi, err := os.Lstat(e.in("real", "reldir")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("moved link: %v %v", fi, err)
	}
	if w := e.post("/rename", map[string]string{"path": e.in("linkdir"), "name": "renamed"}); w.Code != 200 {
		t.Errorf("rename of a link: %d %s", w.Code, w.Body)
	}
	check()

	// Deleting links, and trees containing links, removes only the links.
	e.mustRun("/delete", map[string]any{"paths": []string{e.in("renamed"), e.in("pic.png"), e.in("tree"), e.in("dst"), e.in("real")}})
	for _, n := range []string{"renamed", "pic.png", "tree", "dst", "real"} {
		if exists(e.in(n)) {
			t.Errorf("%s was not deleted", n)
		}
	}
	check()
	if readFile(t, filepath.Join(e.outside, "secret.txt")) != secret {
		t.Fatal("ESCAPE: the outside file changed")
	}
}

func TestSymlinkInsideRootWorks(t *testing.T) {
	e := newEnv(t)
	e.write(e.in("real", "file.txt"), "inside")
	e.write(e.in("real", "pic.png"), "\x89PNG fake")
	e.symlink("real", e.in("alias"))
	e.symlink("real/file.txt", e.in("filealias.txt"))
	e.symlink("missing", e.in("broken"))
	check := e.guard()

	w := e.get(e.admin, "/list", e.in("alias"))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "file.txt") {
		t.Errorf("list through an inside link: %d %s", w.Code, w.Body)
	}
	if w := e.get(e.admin, "/download", e.in("alias", "file.txt")); w.Code != 200 || w.Body.String() != "inside" {
		t.Errorf("download through an inside link: %d %q", w.Code, w.Body)
	}
	if w := e.get(e.admin, "/download", e.in("filealias.txt")); w.Code != 200 || w.Body.String() != "inside" {
		t.Errorf("download of an inside file link: %d %q", w.Code, w.Body)
	}
	if w := e.get(e.admin, "/text", e.in("filealias.txt")); w.Code != 200 || !strings.Contains(w.Body.String(), "inside") {
		t.Errorf("text of an inside file link: %d %q", w.Code, w.Body)
	}
	if w := e.get(e.admin, "/preview", e.in("alias", "pic.png")); w.Code != 200 {
		t.Errorf("preview through an inside link: %d", w.Code)
	}
	refused(t, "download of a broken link", e.get(e.admin, "/download", e.in("broken")))

	w = e.get(e.admin, "/stat", e.in("alias"))
	var st struct {
		Entry Entry `json:"entry"`
	}
	json.Unmarshal(decode(t, w).Data, &st)
	if st.Entry.Type != "symlink" || st.Entry.LinkKind == nil || *st.Entry.LinkKind != "directory" ||
		st.Entry.LinkTarget == nil || *st.Entry.LinkTarget != "real" {
		t.Errorf("stat of an inside link: %+v", st.Entry)
	}

	if w := e.upload(e.in("alias"), "up.txt", []byte("uploaded"), false); w.Code != http.StatusCreated {
		t.Errorf("upload through an inside link: %d %s", w.Code, w.Body)
	} else if readFile(t, e.in("real", "up.txt")) != "uploaded" {
		t.Error("upload through an inside link landed elsewhere")
	}
	if w := e.post("/mkdir", map[string]string{"path": e.in("alias"), "name": "made"}); w.Code != http.StatusCreated {
		t.Errorf("mkdir through an inside link: %d %s", w.Code, w.Body)
	}

	// Editing through a file link must not destroy data: afterwards both
	// names must show the new text.
	w = e.call(e.admin, http.MethodPut, "/text", map[string]any{"path": e.in("filealias.txt"), "content": "edited"})
	if w.Code != 200 {
		t.Fatalf("edit through an inside file link: %d %s", w.Code, w.Body)
	}
	if got := readFile(t, e.in("filealias.txt")); got != "edited" {
		t.Errorf("after the edit the link name reads %q", got)
	}

	// Copying a directory onto itself through its alias must be refused,
	// not recurse.
	e.mustNotRun("copy into itself through a link", "/copy", map[string]any{"paths": []string{e.in("real")}, "dest": e.in("alias")})
	e.mustNotRun("move into itself through a link", "/move", map[string]any{"paths": []string{e.in("real")}, "dest": e.in("alias")})
	if !exists(e.in("real", "file.txt")) {
		t.Error("the source was damaged by a refused self-copy")
	}
	check()
}

// A directory is replaced by a symlink after it has been examined. Each
// operation must notice and stay inside.
func TestSymlinkSwapBetweenCheckAndUse(t *testing.T) {
	e := newEnv(t)
	e.write(filepath.Join(e.outside, "sub", "deep.txt"), secret)
	check := e.guard()

	swap := func(p string) {
		t.Helper()
		if err := os.RemoveAll(p); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(e.outside, p); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("http after swap", func(t *testing.T) {
		e.write(e.in("work", "a.txt"), "inside")
		if w := e.get(e.admin, "/list", e.in("work")); w.Code != 200 {
			t.Fatalf("list before swap: %d", w.Code)
		}
		swap(e.in("work"))
		refused(t, "list", e.get(e.admin, "/list", e.in("work")))
		refused(t, "download", e.get(e.admin, "/download", e.in("work", "secret.txt")))
		refused(t, "upload", e.upload(e.in("work"), "pwned.txt", []byte("x"), true))
		refused(t, "mkdir", e.post("/mkdir", map[string]string{"path": e.in("work"), "name": "pwned"}))
		e.mustNotRun("delete below", "/delete", map[string]any{"paths": []string{e.in("work", "secret.txt")}})
		check()
		os.Remove(e.in("work"))
	})

	rt, err := os.OpenRoot(e.root)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	loc := func(rel string) location {
		return location{Root: e.root, Rel: rel, Path: e.root + "/" + rel}
	}
	job := func() *Job { return &Job{} }

	t.Run("readNames", func(t *testing.T) {
		e.write(e.in("d1", "a.txt"), "inside")
		e.write(e.in("other", "b.txt"), "inside")
		fi, _ := rt.Lstat("d1")
		// Swapped for a link that stays INSIDE the root: os.Root alone
		// would follow it, so the identity check has to catch it.
		os.RemoveAll(e.in("d1"))
		os.Symlink("other", e.in("d1"))
		if got, _, err := readNames(rt, "d1", fi, 0); err == nil {
			t.Errorf("readNames entered a directory that was swapped for a link: %v", got)
		}
		os.Remove(e.in("d1"))
		swap(e.in("d1"))
		if got, _, err := readNames(rt, "d1", fi, 0); err == nil {
			t.Errorf("readNames entered an outside directory: %v", got)
		}
		os.Remove(e.in("d1"))
	})

	t.Run("openSame", func(t *testing.T) {
		e.write(e.in("f1.txt"), "inside")
		e.write(e.in("f2.txt"), "other inside file")
		fi, _ := rt.Lstat("f1.txt")
		os.Remove(e.in("f1.txt"))
		os.Symlink("f2.txt", e.in("f1.txt"))
		if f, err := openSame(rt, "f1.txt", fi); err == nil {
			f.Close()
			t.Error("openSame opened a file that was swapped for a link inside the root")
		}
		os.Remove(e.in("f1.txt"))
		os.Symlink(filepath.Join(e.outside, "secret.txt"), e.in("f1.txt"))
		if f, err := openSame(rt, "f1.txt", fi); err == nil {
			f.Close()
			t.Error("openSame opened an outside file")
		}
		os.Remove(e.in("f1.txt"))
		// A FIFO swapped in must not block the panel.
		if err := syscall.Mkfifo(e.in("f1.txt"), 0o644); err == nil {
			done := make(chan error, 1)
			go func() {
				f, err := openSame(rt, "f1.txt", fi)
				if err == nil {
					f.Close()
				}
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Error("openSame accepted a FIFO")
				}
			case <-timeout(3):
				t.Error("openSame blocks forever on a FIFO swapped in for a file")
				// Unblock the goroutine.
				if w, err := os.OpenFile(e.in("f1.txt"), os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
					w.Close()
				}
			}
			os.Remove(e.in("f1.txt"))
		}
	})

	t.Run("deleteTree", func(t *testing.T) {
		e.write(e.in("victim", "sub", "x.txt"), "inside")
		swap(e.in("victim", "sub"))
		if err := deleteTree(context.Background(), job(), rt, loc("victim")); err != nil {
			t.Errorf("deleteTree: %v", err)
		}
		if exists(e.in("victim")) {
			t.Error("the tree was not deleted")
		}
		check()
	})

	t.Run("scanTree", func(t *testing.T) {
		e.write(e.in("scan", "sub", "x.txt"), "12345")
		swap(e.in("scan", "sub"))
		var tt treeTotals
		var visited []string
		err := scanTree(context.Background(), rt, "scan", 0, &tt, func(rel string, _ os.FileInfo) error {
			visited = append(visited, rel)
			return nil
		})
		if err != nil {
			t.Errorf("scanTree: %v", err)
		}
		for _, v := range visited {
			if strings.Contains(v, "secret") || strings.Contains(v, "deep") {
				t.Errorf("scanTree walked outside: %v", visited)
			}
		}
		if tt.Bytes != 0 {
			t.Errorf("scanTree counted %d bytes from outside", tt.Bytes)
		}
		os.RemoveAll(e.in("scan"))
	})

	t.Run("copier", func(t *testing.T) {
		e.write(e.in("csrc", "sub", "x.txt"), "inside")
		e.mkdir(e.in("cdst"))
		swap(e.in("csrc", "sub"))
		c := &copier{ctx: context.Background(), job: job(), src: rt, dst: rt, buf: make([]byte, 4096)}
		if err := c.tree(loc("csrc"), loc("cdst/csrc")); err != nil {
			t.Errorf("copy: %v", err)
		}
		if found := regularFilesWith(t, e.root, secret); len(found) > 0 {
			t.Errorf("LEAK: copier followed a swapped link: %v", found)
		}
		// Destination directory swapped for a link to outside while the
		// copy is under way.
		e.write(e.in("csrc2", "sub", "pwned.txt"), "pwned")
		e.mkdir(e.in("cdst2", "csrc2"))
		swap(e.in("cdst2", "csrc2", "sub"))
		err := c.tree(loc("csrc2"), loc("cdst2/csrc2"))
		if err == nil {
			t.Error("copy into a directory swapped for an outside link reported success")
		}
		check()
		os.RemoveAll(e.in("csrc"))
		os.RemoveAll(e.in("cdst"))
		os.RemoveAll(e.in("csrc2"))
		os.RemoveAll(e.in("cdst2"))
	})

	t.Run("writeZip", func(t *testing.T) {
		e.write(e.in("zsrc", "sub", "x.txt"), "inside")
		e.write(e.in("zsrc", "keep.txt"), "inside")
		swap(e.in("zsrc", "sub"))
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		if err := writeZip(context.Background(), zw, []zipSource{{rt, loc("zsrc")}}, nil, nil); err != nil {
			t.Errorf("writeZip: %v", err)
		}
		zw.Close()
		assertZipClean(t, buf.Bytes(), "zsrc/keep.txt")
		os.RemoveAll(e.in("zsrc"))
	})

	t.Run("extractor", func(t *testing.T) {
		e.mkdir(e.in("xdst", "sub"))
		out, err := rt.OpenRoot("xdst")
		if err != nil {
			t.Fatal(err)
		}
		defer out.Close()
		x := &extractor{ctx: context.Background(), job: job(), out: out, destPath: e.in("xdst"),
			conflict: conflictOverwrite, left: 1 << 20, buf: make([]byte, 4096), made: map[string]bool{}}
		if err := x.file("sub/one.txt", 0o644, zeroTime, strings.NewReader("one")); err != nil {
			t.Fatalf("extract: %v", err)
		}
		// "sub" is now cached as created. Swap it.
		swap(e.in("xdst", "sub"))
		if err := x.file("sub/pwned.txt", 0o644, zeroTime, strings.NewReader("pwned")); err == nil {
			t.Error("extraction into a directory swapped for an outside link reported success")
		}
		check()
	})
	check()
}

func TestRootThatIsSymlinkIsRefused(t *testing.T) {
	// allowed-link -> outside; allowed-via/root reaches the real root
	// through a linked parent.
	e := newEnv(t, "allowed-link", "allowed-via/root")
	e.symlink(e.outside, filepath.Join(e.base, "allowed-link"))
	e.symlink(e.base, filepath.Join(e.base, "allowed-via"))
	e.write(e.in("inside.txt"), "inside")
	check := e.guard()

	for _, root := range []string{filepath.Join(e.base, "allowed-link"), filepath.Join(e.base, "allowed-via", "root")} {
		file := root + "/secret.txt"
		if strings.HasSuffix(root, "/root") {
			file = root + "/inside.txt"
		}
		for _, ep := range []string{"/list", "/stat", "/count"} {
			w := e.get(e.admin, ep, root)
			refused(t, ep+" "+root, w, 403)
			if c := errCode(w); c != "root_is_link" {
				t.Errorf("%s %s: code %q, want root_is_link", ep, root, c)
			}
		}
		refused(t, "download", e.get(e.admin, "/download", file), 403)
		refused(t, "text", e.get(e.admin, "/text", file), 403)
		refused(t, "upload", e.upload(root, "pwned.txt", []byte("x"), true), 403)
		refused(t, "mkdir", e.post("/mkdir", map[string]string{"path": root, "name": "pwned"}), 403)
		refused(t, "text write", e.call(e.admin, http.MethodPut, "/text", map[string]any{"path": root + "/pwned.txt", "content": "x"}), 403)
		e.mustNotRun("copy to", "/copy", map[string]any{"paths": []string{e.in("inside.txt")}, "dest": root})
		e.mustNotRun("copy from", "/copy", map[string]any{"paths": []string{file}, "dest": e.root})
		e.mustNotRun("delete", "/delete", map[string]any{"paths": []string{file}})
		e.mustNotRun("zip", "/zip", map[string]any{"paths": []string{file}, "dest": e.root, "name": "z"})
		check()
	}

	// The roots overview must not report the linked roots as usable.
	w := e.get(e.admin, "/roots")
	var data struct {
		Roots []rootView `json:"roots"`
	}
	json.Unmarshal(decode(t, w).Data, &data)
	if len(data.Roots) != 3 {
		t.Fatalf("roots: %+v", data.Roots)
	}
	for _, r := range data.Roots {
		if r.Path == e.root {
			if !r.Exists {
				t.Error("the real root is reported as missing")
			}
		} else if r.Exists || r.TotalBytes != nil {
			t.Errorf("the linked root %s is reported as usable", r.Path)
		}
	}

	// A root that becomes a link after it was used.
	if w := e.get(e.admin, "/list", e.root); w.Code != 200 {
		t.Fatalf("list real root: %d", w.Code)
	}
	if err := os.Rename(e.root, e.root+"-moved"); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(e.root + "-moved")
	if err := os.Symlink(e.outside, e.root); err != nil {
		t.Fatal(err)
	}
	refused(t, "list of a root replaced by a link", e.get(e.admin, "/list", e.root), 403)
	refused(t, "download below a root replaced by a link", e.get(e.admin, "/download", e.root+"/secret.txt"), 403)
	os.Remove(e.root)
	os.Rename(e.root+"-moved", e.root)
}

func TestSpecialFilesDoNotBlock(t *testing.T) {
	e := newEnv(t)
	e.write(e.in("d", "plain.txt"), "inside")
	if err := syscall.Mkfifo(e.in("d", "pipe"), 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	e.mkdir(e.in("dst"))
	done := make(chan struct{})
	go func() {
		defer close(done)
		refused(t, "download fifo", e.get(e.admin, "/download", e.in("d", "pipe")))
		refused(t, "text fifo", e.get(e.admin, "/text", e.in("d", "pipe")))
		refused(t, "text write fifo", e.call(e.admin, http.MethodPut, "/text", map[string]any{"path": e.in("d", "pipe"), "content": "x"}))
		refused(t, "chmod fifo", e.post("/chmod", map[string]string{"path": e.in("d", "pipe"), "mode": "777"}))
		if w := e.get(e.admin, "/list", e.in("d")); w.Code != 200 {
			t.Errorf("list: %d", w.Code)
		}
		if w := e.get(e.admin, "/download", e.in("d"), e.in("d", "pipe")); w.Code != 200 {
			t.Errorf("zip download: %d", w.Code)
		}
		v := e.mustRun("/copy", map[string]any{"paths": []string{e.in("d")}, "dest": e.in("dst")})
		if v.Skipped != 1 {
			t.Errorf("copy skipped %d entries, want 1", v.Skipped)
		}
		if exists(e.in("dst", "d", "pipe")) {
			t.Error("a FIFO was copied")
		}
		e.mustRun("/zip", map[string]any{"paths": []string{e.in("d")}, "dest": e.in("dst"), "name": "z"})
		e.mustRun("/delete", map[string]any{"paths": []string{e.in("d")}})
	}()
	select {
	case <-done:
	case <-timeout(20):
		t.Fatal("an operation on a FIFO blocks")
	}
}

/* ---------- protected operations ---------- */

func TestProtectedRoots(t *testing.T) {
	e := newEnv(t, "root/sub/inner")
	inner := e.in("sub", "inner")
	e.write(filepath.Join(inner, "data.txt"), "data")
	e.write(e.in("sub", "other.txt"), "x")
	e.write(e.in("plain", "f.txt"), "x")
	e.mkdir(e.in("dst"))
	before := snapshot(t, e.root)

	protected := []string{
		e.root, e.root + "/", e.root + "/.", e.root + "//",
		inner, inner + "/",
		e.in("sub"), e.in("sub") + "/", // contains the inner root
	}
	for _, p := range protected {
		w := e.post("/delete", map[string]any{"paths": []string{p}})
		refused(t, "delete "+p, w, 403)
		w = e.post("/delete", map[string]any{"paths": []string{e.in("plain"), p}})
		refused(t, "delete selection with "+p, w, 403)
		refused(t, "rename "+p, e.post("/rename", map[string]string{"path": p, "name": "renamed"}), 403)
		refused(t, "move "+p, e.post("/move", map[string]any{"paths": []string{p}, "dest": e.in("dst")}), 403)
		refused(t, "move overwrite "+p, e.post("/move", map[string]any{"paths": []string{p}, "dest": e.in("dst"), "conflict": "overwrite"}), 403)
		if got := snapshot(t, e.root); got != before {
			t.Fatalf("a refused operation on %s changed the tree:\n%s", p, got)
		}
	}

	// Things that are merely near a root stay usable.
	e.mustRun("/delete", map[string]any{"paths": []string{e.in("sub", "other.txt"), filepath.Join(inner, "data.txt")}})
	if w := e.post("/rename", map[string]string{"path": e.in("plain"), "name": "plain2"}); w.Code != 200 {
		t.Errorf("rename of an ordinary directory: %d %s", w.Code, w.Body)
	}
	if !exists(inner) || !exists(e.root) {
		t.Fatal("a root disappeared")
	}
}

func TestCopyOrMoveIntoItself(t *testing.T) {
	e := newEnv(t)
	e.write(e.in("d", "child", "f.txt"), "x")
	e.write(e.in("dd", "f.txt"), "x")
	before := snapshot(t, e.root)
	for _, ep := range []string{"/copy", "/move"} {
		for _, dest := range []string{e.in("d"), e.in("d", "child"), e.in("d") + "/", e.in("d", "child") + "/./"} {
			for _, conflict := range []string{"", "rename", "overwrite", "skip"} {
				w := e.post(ep, map[string]any{"paths": []string{e.in("d")}, "dest": dest, "conflict": conflict})
				refused(t, ep+" d into "+dest, w, 400)
			}
		}
		refused(t, ep+" root into itself", e.post(ep, map[string]any{"paths": []string{e.root}, "dest": e.in("d")}), 400, 403)
		if got := snapshot(t, e.root); got != before {
			t.Fatalf("%s into itself changed the tree:\n%s", ep, got)
		}
	}
	// A name that merely starts the same is a different directory.
	e.mustRun("/copy", map[string]any{"paths": []string{e.in("d")}, "dest": e.in("dd")})
	if readFile(t, e.in("dd", "d", "child", "f.txt")) != "x" {
		t.Error("copy into a directory with a similar name failed")
	}
}

// Copying onto itself with "overwrite" must never lose the data.
func TestCopyOntoItselfKeepsData(t *testing.T) {
	e := newEnv(t)
	e.write(e.in("d", "f.txt"), "precious")
	e.write(e.in("d", "sub", "g.txt"), "precious too")
	e.symlink("f.txt", e.in("d", "l"))
	w := e.post("/copy", map[string]any{"paths": []string{e.in("d", "f.txt"), e.in("d")}, "dest": e.in("d"), "conflict": "overwrite"})
	e.job(w)
	w = e.post("/copy", map[string]any{"paths": []string{e.in("d")}, "dest": e.root, "conflict": "overwrite"})
	e.job(w)
	w = e.post("/copy", map[string]any{"paths": []string{e.in("d", "f.txt")}, "dest": e.in("d"), "conflict": "overwrite"})
	e.job(w)
	if got := readFile(t, e.in("d", "f.txt")); got != "precious" {
		t.Errorf("f.txt now reads %q", got)
	}
	if got := readFile(t, e.in("d", "sub", "g.txt")); got != "precious too" {
		t.Errorf("g.txt now reads %q", got)
	}
	if l := leftovers(t, e.root); len(l) > 0 {
		t.Errorf("temporary files left: %v", l)
	}
}

/* ---------- authorisation ---------- */

func TestModifyingEndpointsRequireAdmin(t *testing.T) {
	e := newEnv(t)
	e.write(e.in("d", "f.txt"), "content")
	e.mkdir(e.in("dst"))
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	f, _ := zw.Create("x.txt")
	f.Write([]byte("x"))
	zw.Close()
	e.write(e.in("a.zip"), zb.String())
	before := snapshot(t, e.root)

	type call struct {
		method, endpoint string
		body             any
	}
	calls := []call{
		{"POST", "/mkdir", map[string]string{"path": e.root, "name": "new"}},
		{"POST", "/rename", map[string]string{"path": e.in("d"), "name": "renamed"}},
		{"POST", "/chmod", map[string]string{"path": e.in("d", "f.txt"), "mode": "600"}},
		{"POST", "/delete", map[string]any{"paths": []string{e.in("d")}}},
		{"POST", "/copy", map[string]any{"paths": []string{e.in("d")}, "dest": e.in("dst")}},
		{"POST", "/move", map[string]any{"paths": []string{e.in("d")}, "dest": e.in("dst")}},
		{"POST", "/zip", map[string]any{"paths": []string{e.in("d")}, "dest": e.in("dst"), "name": "z"}},
		{"POST", "/extract", map[string]any{"path": e.in("a.zip"), "dest": e.in("dst")}},
		{"PUT", "/text", map[string]any{"path": e.in("d", "f.txt"), "content": "changed"}},
		{"PUT", "/text", map[string]any{"path": e.in("d", "new.txt"), "content": "created"}},
	}
	who := []struct {
		name  string
		creds *testCreds
		want  int
	}{
		{"ordinary user", e.user, 403},
		{"anonymous", nil, 401},
		{"unknown session", &testCreds{token: "forged", csrf: "forged"}, 401},
	}
	for _, u := range who {
		for _, c := range calls {
			w := e.call(u.creds, c.method, c.endpoint, c.body)
			if w.Code != u.want {
				t.Errorf("%s %s %s: status %d, want %d", u.name, c.method, c.endpoint, w.Code, u.want)
			}
		}
		b := multipartBody("up.txt", []byte("uploaded"))
		for _, overwrite := range []bool{false, true} {
			if w := e.uploadRaw(u.creds, e.in("d"), overwrite, bytes.NewReader(b), int64(len(b))); w.Code != u.want {
				t.Errorf("%s upload: status %d, want %d", u.name, w.Code, u.want)
			}
		}
	}
	// Admin session without, or with a wrong, CSRF token.
	for _, c := range calls {
		body, _ := json.Marshal(c.body)
		for _, token := range []string{"", "wrong", e.user.csrf} {
			r := httptest.NewRequest(c.method, apiURL(c.endpoint, nil), bytes.NewReader(body))
			r.AddCookie(&http.Cookie{Name: "myserver_session", Value: e.admin.token})
			if token != "" {
				r.Header.Set("X-CSRF-Token", token)
			}
			w := httptest.NewRecorder()
			e.handler.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Errorf("admin with CSRF token %q %s %s: status %d, want 403", token, c.method, c.endpoint, w.Code)
			}
		}
	}
	// Modifying endpoints must not be reachable with a safe method, which
	// would bypass the CSRF check.
	for _, c := range calls {
		body, _ := json.Marshal(c.body)
		for _, method := range []string{"GET", "HEAD"} {
			r := httptest.NewRequest(method, apiURL(c.endpoint, url.Values{"path": {e.in("d", "f.txt")}}), bytes.NewReader(body))
			r.AddCookie(&http.Cookie{Name: "myserver_session", Value: e.admin.token})
			w := httptest.NewRecorder()
			e.handler.ServeHTTP(w, r)
			if c.endpoint == "/text" {
				continue // GET /text is the read endpoint
			}
			if w.Code != 405 && w.Code != 404 {
				t.Errorf("%s %s: status %d, want 405", method, c.endpoint, w.Code)
			}
		}
	}
	if len(e.mod.jobs.views("", true)) != 0 {
		t.Error("a refused request started a job")
	}
	if got := snapshot(t, e.root); got != before {
		t.Errorf("an unauthorised request changed the tree:\nbefore:\n%s\nafter:\n%s", before, got)
	}

	// An ordinary user can read, and sees only their own jobs.
	if w := e.get(e.user, "/list", e.root); w.Code != 200 {
		t.Errorf("ordinary user list: %d", w.Code)
	}
	if w := e.get(nil, "/list", e.root); w.Code != 401 {
		t.Errorf("anonymous list: %d, want 401", w.Code)
	}
	if w := e.get(nil, "/download", e.in("d", "f.txt")); w.Code != 401 || strings.Contains(w.Body.String(), "content") {
		t.Errorf("anonymous download: %d", w.Code)
	}
	v := e.mustRun("/delete", map[string]any{"paths": []string{e.in("dst")}})
	w := e.send(e.user, httptest.NewRequest("GET", apiURL("/jobs", nil), nil))
	if strings.Contains(w.Body.String(), v.ID) {
		t.Error("an ordinary user sees the administrator's jobs")
	}
	w = e.call(e.user, "POST", "/jobs/"+v.ID+"/cancel", map[string]string{})
	if w.Code != 404 {
		t.Errorf("an ordinary user cancelling an administrator's job: %d, want 404", w.Code)
	}
}

/* ---------- ownership ---------- */

func TestNewEntriesTakeParentOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to create a directory owned by another user")
	}
	const uid, gid = 12345, 23456
	e := newEnv(t)
	parent := e.in("home")
	e.mkdir(parent)
	e.write(e.in("src", "tree", "a.txt"), "a")
	e.write(e.in("src", "single.txt"), "s")
	e.symlink("a.txt", e.in("src", "tree", "link"))
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	f, _ := zw.Create("xdir/deep/x.txt")
	f.Write([]byte("x"))
	zw.Close()
	e.write(e.in("src", "a.zip"), zb.String())
	if err := os.Chown(parent, uid, gid); err != nil {
		t.Fatal(err)
	}

	if w := e.post("/mkdir", map[string]string{"path": parent, "name": "made"}); w.Code != 201 {
		t.Fatalf("mkdir: %d %s", w.Code, w.Body)
	}
	if w := e.upload(parent, "up.txt", []byte("u"), false); w.Code != 201 {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	if w := e.upload(parent, "up.txt", []byte("u2"), true); w.Code != 201 {
		t.Fatalf("upload overwrite: %d %s", w.Code, w.Body)
	}
	if w := e.call(e.admin, "PUT", "/text", map[string]any{"path": parent + "/note.txt", "content": "n"}); w.Code != 200 {
		t.Fatalf("text: %d %s", w.Code, w.Body)
	}
	e.mustRun("/copy", map[string]any{"paths": []string{e.in("src", "tree"), e.in("src", "single.txt")}, "dest": parent})
	e.mustRun("/zip", map[string]any{"paths": []string{e.in("src", "tree")}, "dest": parent, "name": "packed"})
	e.mustRun("/extract", map[string]any{"path": e.in("src", "a.zip"), "dest": parent})

	want := []string{"made", "up.txt", "note.txt", "tree", "tree/a.txt", "tree/link", "single.txt", "packed.zip",
		"xdir", "xdir/deep", "xdir/deep/x.txt"}
	for _, rel := range want {
		fi, err := os.Lstat(filepath.Join(parent, rel))
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		st := fi.Sys().(*syscall.Stat_t)
		if st.Uid != uid || st.Gid != gid {
			t.Errorf("%s is owned by %d:%d, want the parent's owner %d:%d", rel, st.Uid, st.Gid, uid, gid)
		}
	}
	if l := leftovers(t, e.root); len(l) > 0 {
		t.Errorf("temporary files left: %v", l)
	}

	// Editing an existing file keeps that file's own owner and mode.
	own := filepath.Join(parent, "theirs.txt")
	e.write(own, "old")
	os.Chown(own, 777, 888)
	os.Chmod(own, 0o640)
	if w := e.call(e.admin, "PUT", "/text", map[string]any{"path": own, "content": "new"}); w.Code != 200 {
		t.Fatalf("edit: %d %s", w.Code, w.Body)
	}
	fi, _ := os.Lstat(own)
	st := fi.Sys().(*syscall.Stat_t)
	if st.Uid != 777 || st.Gid != 888 || fi.Mode().Perm() != 0o640 {
		t.Errorf("edited file is %d:%d %o, want 777:888 640", st.Uid, st.Gid, fi.Mode().Perm())
	}

	// A move keeps the owner of what is moved (like mv).
	e.write(e.in("mv", "m.txt"), "m")
	os.Chown(e.in("mv", "m.txt"), 4321, 4321)
	e.mustRun("/move", map[string]any{"paths": []string{e.in("mv", "m.txt")}, "dest": parent})
	fi, _ = os.Lstat(filepath.Join(parent, "m.txt"))
	if st := fi.Sys().(*syscall.Stat_t); st.Uid != 4321 {
		t.Errorf("moved file is owned by %d, want 4321", st.Uid)
	}
}
