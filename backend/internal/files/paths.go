package files

import (
	"path"
	"strings"
	"unicode/utf8"

	"myserver/internal/httpx"
)

// Path handling is purely lexical here and deliberately portable: it decides
// WHICH allowed root a client path belongs to and what the path relative to
// that root is. It is not the security boundary on its own; every file
// operation afterwards goes through an os.Root opened on the allowed root,
// which is what stops symlinks and races from leaving it.

const (
	maxPathLen = 4096
	maxNameLen = 255
)

// location is a client path resolved against an allowed root.
type location struct {
	Root string // cleaned absolute allowed root, e.g. "/home"
	Rel  string // slash-separated path relative to Root; "." is the root itself
	Path string // cleaned absolute path as shown to the user
}

func (l location) IsRoot() bool { return l.Rel == "." }

func (l location) Name() string { return path.Base(l.Path) }

// Child returns the location of a direct child. name must be validated.
func (l location) Child(name string) location {
	return location{Root: l.Root, Rel: path.Join(l.Rel, name), Path: path.Join(l.Path, name)}
}

// Parent returns the containing directory. It must not be called on a root.
func (l location) Parent() location {
	return location{Root: l.Root, Rel: path.Dir(l.Rel), Path: path.Dir(l.Path)}
}

var (
	errPathInvalid = httpx.BadRequest("Dosya yolu geçersiz.")
	errPathTooLong = httpx.BadRequest("Dosya yolu çok uzun.")
	errPathOutside = httpx.NewError(403, "outside_allowed_roots", "Bu yol izin verilen dizinlerin dışında.")
)

// cleanRoots normalizes the configured roots and drops anything that is not
// an absolute path below "/".
func cleanRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	seen := map[string]bool{}
	for _, r := range roots {
		if !strings.HasPrefix(r, "/") || strings.ContainsRune(r, 0) || hasDotDot(r) {
			continue
		}
		c := path.Clean(r)
		if c == "/" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

func hasDotDot(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// resolve maps a client-supplied absolute path to an allowed root. Paths
// containing "..", NUL bytes, relative paths and paths outside every root
// are rejected. When roots are nested the longest one wins.
func resolve(roots []string, p string) (location, error) {
	if p == "" || !utf8.ValidString(p) || strings.ContainsRune(p, 0) {
		return location{}, errPathInvalid
	}
	if len(p) > maxPathLen {
		return location{}, errPathTooLong
	}
	if !strings.HasPrefix(p, "/") || hasDotDot(p) {
		return location{}, errPathInvalid
	}
	c := path.Clean(p)
	for _, seg := range strings.Split(c, "/") {
		if len(seg) > maxNameLen {
			return location{}, errPathTooLong
		}
	}
	best := ""
	for _, r := range cleanRoots(roots) {
		if (c == r || strings.HasPrefix(c, r+"/")) && len(r) > len(best) {
			best = r
		}
	}
	if best == "" {
		return location{}, errPathOutside
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(c, best), "/")
	if rel == "" {
		rel = "."
	}
	return location{Root: best, Rel: rel, Path: c}, nil
}

// validName checks a single file or directory name supplied by the client.
func validName(name string) error {
	switch {
	case name == "":
		return httpx.BadRequest("Ad boş olamaz.")
	case name == "." || name == "..":
		return httpx.BadRequest("Bu ad kullanılamaz.")
	case len(name) > maxNameLen:
		return httpx.BadRequest("Ad en fazla 255 bayt olabilir.")
	case !utf8.ValidString(name):
		return httpx.BadRequest("Ad geçersiz karakter içeriyor.")
	}
	for _, r := range name {
		if r == '/' || r == '\\' || r == 0 || r < 0x20 || r == 0x7f {
			return httpx.BadRequest("Ad '/' , '\\' veya kontrol karakteri içeremez.")
		}
	}
	if strings.TrimSpace(name) != name {
		return httpx.BadRequest("Ad boşlukla başlayamaz veya bitemez.")
	}
	return nil
}

// within reports whether p is dir itself or lies below it (lexically).
func within(dir, p string) bool {
	return p == dir || strings.HasPrefix(p, dir+"/")
}

// archiveEntryName sanitizes the name of an archive member. It returns the
// cleaned relative slash path, or false when the entry must be refused
// (absolute, escaping, empty or otherwise unusable).
func archiveEntryName(name string) (string, bool) {
	if name == "" || !utf8.ValidString(name) || strings.ContainsRune(name, 0) || len(name) > maxPathLen {
		return "", false
	}
	n := strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(n, "/") || hasDotDot(n) {
		return "", false
	}
	if len(n) >= 2 && n[1] == ':' {
		return "", false // Windows drive letter
	}
	c := path.Clean(n)
	if c == "." || c == "" || strings.HasPrefix(c, "/") || strings.HasPrefix(c, "../") {
		return "", false
	}
	for _, seg := range strings.Split(c, "/") {
		if seg == "" || seg == "." || seg == ".." || len(seg) > maxNameLen {
			return "", false
		}
		for _, r := range seg {
			if r < 0x20 || r == 0x7f {
				return "", false
			}
		}
	}
	return c, true
}

// splitExt splits "a.tar.gz" into ("a", ".tar.gz") and "b.txt" into
// ("b", ".txt"); dot files keep their whole name.
func splitExt(name string) (string, string) {
	lower := strings.ToLower(name)
	for _, double := range []string{".tar.gz", ".tar.bz2", ".tar.xz"} {
		if strings.HasSuffix(lower, double) && len(name) > len(double) {
			return name[:len(name)-len(double)], name[len(name)-len(double):]
		}
	}
	ext := path.Ext(name)
	if ext == name || ext == "" {
		return name, ""
	}
	return name[:len(name)-len(ext)], ext
}
