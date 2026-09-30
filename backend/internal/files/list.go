package files

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/user"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"myserver/internal/httpx"
	"myserver/internal/settings"
)

const (
	listHardCap     = 20000 // entries read from one directory
	listDefaultPage = 1000
	listMaxPage     = 5000
	countCap        = 200000
	countDeadline   = 4 * time.Second
	sizeCap         = 2000000
)

// Entry describes one file, directory or symlink.
type Entry struct {
	Name       string  `json:"name"`
	Path       string  `json:"path"`
	Type       string  `json:"type"`        // file | directory | symlink | other
	LinkTarget *string `json:"link_target"` // symlinks only
	LinkKind   *string `json:"link_kind"`   // file | directory | other | unreachable
	Size       int64   `json:"size"`
	Mode       string  `json:"mode"`       // "drwxr-xr-x"
	ModeOctal  string  `json:"mode_octal"` // "0755"
	UID        *int    `json:"uid"`
	GID        *int    `json:"gid"`
	Owner      *string `json:"owner"`
	Group      *string `json:"group"`
	ModifiedAt int64   `json:"modified_at"`
	Mime       string  `json:"mime"`
}

func (e *Entry) isDirLike() bool {
	return e.Type == "directory" || (e.Type == "symlink" && e.LinkKind != nil && *e.LinkKind == "directory")
}

func kindOf(mode os.FileMode) string {
	switch {
	case mode&os.ModeSymlink != 0:
		return "symlink"
	case mode.IsDir():
		return "directory"
	case mode.IsRegular():
		return "file"
	}
	return "other"
}

// modeString renders permissions the way ls does.
func modeString(mode os.FileMode) string {
	b := []byte("----------")
	switch {
	case mode&os.ModeSymlink != 0:
		b[0] = 'l'
	case mode.IsDir():
		b[0] = 'd'
	case mode&os.ModeNamedPipe != 0:
		b[0] = 'p'
	case mode&os.ModeSocket != 0:
		b[0] = 's'
	case mode&os.ModeCharDevice != 0:
		b[0] = 'c'
	case mode&os.ModeDevice != 0:
		b[0] = 'b'
	}
	const rwx = "rwxrwxrwx"
	perm := mode.Perm()
	for i := 0; i < 9; i++ {
		if perm&(1<<uint(8-i)) != 0 {
			b[i+1] = rwx[i]
		}
	}
	special := func(flag os.FileMode, pos int, set, unset byte) {
		if mode&flag == 0 {
			return
		}
		if b[pos] == 'x' {
			b[pos] = set
		} else {
			b[pos] = unset
		}
	}
	special(os.ModeSetuid, 3, 's', 'S')
	special(os.ModeSetgid, 6, 's', 'S')
	special(os.ModeSticky, 9, 't', 'T')
	return string(b)
}

func modeOctal(mode os.FileMode) string {
	v := uint32(mode.Perm())
	if mode&os.ModeSetuid != 0 {
		v |= 0o4000
	}
	if mode&os.ModeSetgid != 0 {
		v |= 0o2000
	}
	if mode&os.ModeSticky != 0 {
		v |= 0o1000
	}
	s := strconv.FormatUint(uint64(v), 8)
	for len(s) < 4 {
		s = "0" + s
	}
	return s
}

// ownerCache resolves uid/gid to names, briefly cached.
type ownerCache struct {
	mu     sync.Mutex
	users  map[int]string
	groups map[int]string
	at     time.Time
}

func newOwnerCache() *ownerCache {
	return &ownerCache{users: map[int]string{}, groups: map[int]string{}}
}

func (c *ownerCache) names(uid, gid int) (string, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.at) > 2*time.Minute || len(c.users) > 4096 || len(c.groups) > 4096 {
		c.users, c.groups, c.at = map[int]string{}, map[int]string{}, time.Now()
	}
	u, ok := c.users[uid]
	if !ok {
		u = strconv.Itoa(uid)
		if lu, err := user.LookupId(u); err == nil && lu.Username != "" {
			u = lu.Username
		}
		c.users[uid] = u
	}
	g, ok := c.groups[gid]
	if !ok {
		g = strconv.Itoa(gid)
		if lg, err := user.LookupGroupId(g); err == nil && lg.Name != "" {
			g = lg.Name
		}
		c.groups[gid] = g
	}
	return u, g
}

// describe builds the entry for loc from its Lstat information.
func (m *Module) describe(rt *os.Root, loc location, fi os.FileInfo) Entry {
	e := Entry{
		Name: loc.Name(), Path: loc.Path, Type: kindOf(fi.Mode()),
		Mode: modeString(fi.Mode()), ModeOctal: modeOctal(fi.Mode()),
		ModifiedAt: fi.ModTime().Unix(),
	}
	if e.Type == "file" {
		e.Size = fi.Size()
		e.Mime = mimeByName(e.Name)
	}
	if uid, gid, ok := ownerIDs(fi); ok {
		u, g := m.owners.names(uid, gid)
		e.UID, e.GID, e.Owner, e.Group = &uid, &gid, &u, &g
	}
	if e.Type == "symlink" {
		if target, err := rt.Readlink(loc.Rel); err == nil {
			e.LinkTarget = &target
		}
		kind := "unreachable" // broken, or pointing outside the allowed root
		if tfi, err := rt.Stat(loc.Rel); err == nil {
			kind = kindOf(tfi.Mode())
			if kind == "file" {
				e.Size = tfi.Size()
				e.Mime = mimeByName(e.Name)
			}
		}
		e.LinkKind = &kind
	}
	return e
}

// readNames returns the names inside a directory, at most limit of them
// (limit <= 0 means all). expect, when given, must be the Lstat result of
// the directory: the opened handle is compared with it so a directory that
// was swapped for a symlink in between is not entered.
func readNames(rt *os.Root, rel string, expect os.FileInfo, limit int) (names []string, truncated bool, err error) {
	f, err := rt.Open(rel)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	if expect != nil {
		fi, err := f.Stat()
		if err != nil {
			return nil, false, err
		}
		if !os.SameFile(fi, expect) {
			return nil, false, errors.New("path escapes: directory changed during operation")
		}
	}
	for {
		batch, err := f.Readdirnames(1024)
		names = append(names, batch...)
		if limit > 0 && len(names) > limit {
			return names[:limit], true, nil
		}
		if err == io.EOF {
			return names, false, nil
		}
		if err != nil {
			return names, false, err
		}
		if len(batch) == 0 {
			return names, false, nil
		}
	}
}

// openSame opens a regular file and verifies it is still the file that was
// examined with Lstat (not a symlink or special file swapped in since).
func openSame(rt *os.Root, rel string, expect os.FileInfo) (*os.File, error) {
	f, err := rt.OpenFile(rel, os.O_RDONLY|openNonblock, 0)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !os.SameFile(fi, expect) || !fi.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("path escapes: file changed during operation")
	}
	return f, nil
}

type rootView struct {
	Path       string `json:"path"`
	Name       string `json:"name"`
	Exists     bool   `json:"exists"`
	TotalBytes *int64 `json:"total_bytes"`
	FreeBytes  *int64 `json:"free_bytes"`
}

func (m *Module) handleRoots(w http.ResponseWriter, _ *http.Request) error {
	roots := m.roots()
	out := make([]rootView, 0, len(roots))
	for _, p := range roots {
		v := rootView{Path: p, Name: path.Base(p)}
		if settings.CheckRootReal(p) != nil {
			out = append(out, v)
			continue
		}
		if rt, err := os.OpenRoot(p); err == nil {
			v.Exists = true
			if total, free, ok := diskUsageIn(rt, "."); ok {
				v.TotalBytes, v.FreeBytes = &total, &free
			}
			rt.Close()
		}
		out = append(out, v)
	}
	httpx.OK(w, map[string]any{
		"roots":            out,
		"max_upload_bytes": m.maxUploadBytes(),
		"text_max_bytes":   textMaxBytes,
	})
	return nil
}

func intParam(r *http.Request, key string, def int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil {
		return def
	}
	return n
}

func (m *Module) handleList(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	rt, loc, err := m.open(q.Get("path"))
	if err != nil {
		return err
	}
	defer rt.Close()

	dfi, err := rt.Stat(loc.Rel)
	if err != nil {
		return fsError(err, "Dizin okunamadı.")
	}
	if !dfi.IsDir() {
		return httpx.BadRequest("Bu yol bir dizin değil.")
	}
	names, truncated, err := readNames(rt, loc.Rel, nil, listHardCap)
	if err != nil {
		return fsError(err, "Dizin okunamadı.")
	}
	dirsOnly := q.Get("dirs_only") == "true"
	entries := make([]Entry, 0, len(names))
	for _, name := range names {
		if r.Context().Err() != nil {
			return nil
		}
		child := loc.Child(name)
		fi, err := rt.Lstat(child.Rel)
		if err != nil {
			continue // vanished while listing
		}
		e := m.describe(rt, child, fi)
		if dirsOnly && !e.isDirLike() {
			continue
		}
		entries = append(entries, e)
	}
	sortEntries(entries, q.Get("sort"), q.Get("order") == "desc", q.Get("dirs_first") != "false")

	total := len(entries)
	offset := intParam(r, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	limit := intParam(r, "limit", listDefaultPage)
	if limit <= 0 || limit > listMaxPage {
		limit = listDefaultPage
	}
	end := offset + limit
	if end > total {
		end = total
	}
	var parent *string
	if !loc.IsRoot() {
		p := loc.Parent().Path
		parent = &p
	}
	self := m.describe(rt, loc, dfi)
	httpx.OK(w, map[string]any{
		"path":      loc.Path,
		"root":      loc.Root,
		"parent":    parent,
		"directory": self,
		"entries":   entries[offset:end],
		"total":     total,
		"offset":    offset,
		"limit":     limit,
		"truncated": truncated,
	})
	return nil
}

func sortEntries(list []Entry, key string, desc, dirsFirst bool) {
	less := func(a, b *Entry) bool {
		switch key {
		case "size":
			if a.Size != b.Size {
				return a.Size < b.Size
			}
		case "modified":
			if a.ModifiedAt != b.ModifiedAt {
				return a.ModifiedAt < b.ModifiedAt
			}
		case "mode":
			if a.ModeOctal != b.ModeOctal {
				return a.ModeOctal < b.ModeOctal
			}
		}
		la, lb := strings.ToLower(a.Name), strings.ToLower(b.Name)
		if la != lb {
			return la < lb
		}
		return a.Name < b.Name
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := &list[i], &list[j]
		if dirsFirst {
			ad, bd := a.isDirLike(), b.isDirLike()
			if ad != bd {
				return ad
			}
		}
		if desc {
			return less(b, a)
		}
		return less(a, b)
	})
}

func (m *Module) handleStat(w http.ResponseWriter, r *http.Request) error {
	rt, loc, err := m.open(r.URL.Query().Get("path"))
	if err != nil {
		return err
	}
	defer rt.Close()
	fi, err := rt.Lstat(loc.Rel)
	if err != nil {
		return fsError(err, "Öğe bilgileri okunamadı.")
	}
	e := m.describe(rt, loc, fi)
	httpx.OK(w, map[string]any{"entry": e, "is_root": loc.IsRoot(), "root": loc.Root})
	return nil
}

// treeTotals is the result of measuring a tree.
type treeTotals struct {
	Items     int64 `json:"items"`
	Bytes     int64 `json:"bytes"`
	Truncated bool  `json:"truncated"`
}

var errScanCap = errors.New("scan cap reached")

// scanTree counts the entries and bytes below rel without following
// symlinks. visit, when set, sees every entry and may abort the scan.
func scanTree(ctx context.Context, rt *os.Root, rel string, capItems int64, t *treeTotals, visit func(rel string, fi os.FileInfo) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fi, err := rt.Lstat(rel)
	if err != nil {
		return err
	}
	if capItems > 0 && t.Items >= capItems {
		t.Truncated = true
		return errScanCap
	}
	t.Items++
	if visit != nil {
		if err := visit(rel, fi); err != nil {
			return err
		}
	}
	if fi.Mode().IsRegular() {
		t.Bytes += fi.Size()
		return nil
	}
	if !fi.IsDir() {
		return nil
	}
	names, _, err := readNames(rt, rel, fi, 0)
	if err != nil {
		return err
	}
	for _, n := range names {
		err := scanTree(ctx, rt, path.Join(rel, n), capItems, t, visit)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func queryPaths(r *http.Request) ([]string, error) {
	paths := r.URL.Query()["path"]
	return checkPaths(paths)
}

func checkPaths(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, httpx.BadRequest("En az bir öğe seçilmelidir.")
	}
	if len(paths) > 1000 {
		return nil, httpx.BadRequest("Tek seferde en fazla 1000 öğe seçilebilir.")
	}
	return paths, nil
}

// handleCount reports how many items a selection contains, for the delete
// confirmation. It is bounded in items and time.
func (m *Module) handleCount(w http.ResponseWriter, r *http.Request) error {
	paths, err := queryPaths(r)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), countDeadline)
	defer cancel()
	var t treeTotals
	for _, p := range paths {
		rt, loc, err := m.open(p)
		if err != nil {
			return err
		}
		err = scanTree(ctx, rt, loc.Rel, countCap, &t, nil)
		rt.Close()
		if errors.Is(err, errScanCap) || errors.Is(err, context.DeadlineExceeded) {
			t.Truncated = true
			break
		}
		if err != nil {
			if r.Context().Err() != nil {
				return nil
			}
			return fsError(err, "Öğeler sayılamadı.")
		}
	}
	httpx.OK(w, t)
	return nil
}

// handleSize starts a background size calculation for the properties dialog.
func (m *Module) handleSize(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Path string `json:"path"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	rt, loc, err := m.open(req.Path)
	if err != nil {
		return err
	}
	user, _ := sessionUser(r)
	actor := actorOf(r)
	actor.Username = user
	j, err := m.jobs.start(jobSpec{
		kind: "size", title: loc.Path, actor: actor,
		cleanup: func() { rt.Close() },
		run: func(ctx context.Context, j *Job) (any, error) {
			j.setPhase("scanning")
			var t treeTotals
			err := scanTree(ctx, rt, loc.Rel, sizeCap, &t, func(string, os.FileInfo) error {
				j.itemsDone.Add(1)
				return nil
			})
			if errors.Is(err, errScanCap) {
				err = nil
			}
			if err != nil {
				return nil, opErr(loc.Path, err)
			}
			j.bytesDone.Store(t.Bytes)
			return t, nil
		},
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusAccepted, j.view())
	return nil
}
