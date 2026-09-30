package files

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"myserver/internal/audit"
	"myserver/internal/auth"
	"myserver/internal/httpx"
)

const (
	conflictRename    = "rename"
	conflictOverwrite = "overwrite"
	conflictSkip      = "skip"

	tempPrefix = ".myserver-"
	tempSuffix = ".tmp"
)

func actorOf(r *http.Request) audit.Actor { return auth.ActorFrom(r) }

func (m *Module) audit(r *http.Request, action, target, detail string, ok bool) {
	m.deps.Audit.Log(r.Context(), actorOf(r), action, truncate(target, 500), truncate(detail, 300), ok)
}

func tempName() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return tempPrefix + hex.EncodeToString(b) + tempSuffix
}

func parseConflict(v string) (string, error) {
	switch v {
	case "", conflictRename:
		return conflictRename, nil
	case conflictOverwrite, conflictSkip:
		return v, nil
	}
	return "", httpx.BadRequest("Çakışma seçeneği geçersiz.")
}

// uniqueName finds a free "name (2).ext" style name inside dirRel.
func uniqueName(rt *os.Root, dirRel, name string) (string, error) {
	base, ext := splitExt(name)
	for i := 2; i < 10000; i++ {
		suffix := " (" + strconv.Itoa(i) + ")"
		b := base
		if over := len(b) + len(suffix) + len(ext) - maxNameLen; over > 0 {
			if over >= len(b) {
				return "", errors.New("name too long")
			}
			b = truncateBytes(b, len(b)-over)
		}
		candidate := b + suffix + ext
		if _, err := rt.Lstat(path.Join(dirRel, candidate)); errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
	}
	return "", errors.New("no free name")
}

func truncateBytes(s string, n int) string {
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

// guardProtected refuses operations that would remove or relocate an
// allowed root, or a directory that contains one.
func (m *Module) guardProtected(loc location, verb string) error {
	if loc.IsRoot() {
		return httpx.NewError(http.StatusForbidden, "root_protected", "İzin verilen kök dizin "+verb+".")
	}
	for _, r := range m.roots() {
		if within(loc.Path, r) {
			return httpx.NewError(http.StatusForbidden, "root_protected",
				"Bu dizin izin verilen bir kök dizini ("+r+") içerdiği için "+verb+".")
		}
	}
	return nil
}

// rootSet keeps the allowed roots used by a job open for its duration.
type rootSet struct {
	m     *Module
	roots map[string]*os.Root
}

func (m *Module) newRootSet() *rootSet { return &rootSet{m: m, roots: map[string]*os.Root{}} }

func (s *rootSet) open(p string) (*os.Root, location, error) {
	loc, err := resolve(s.m.roots(), p)
	if err != nil {
		return nil, location{}, err
	}
	if rt, ok := s.roots[loc.Root]; ok {
		return rt, loc, nil
	}
	rt, loc, err := s.m.open(p)
	if err != nil {
		return nil, location{}, err
	}
	s.roots[loc.Root] = rt
	return rt, loc, nil
}

func (s *rootSet) close() {
	for _, rt := range s.roots {
		rt.Close()
	}
}

/* ---------- simple operations ---------- */

func (m *Module) handleMkdir(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	name := strings.TrimSpace(req.Name)
	if err := validName(name); err != nil {
		return err
	}
	rt, dir, err := m.open(req.Path)
	if err != nil {
		return err
	}
	defer rt.Close()
	target := dir.Child(name)
	if len(target.Path) > maxPathLen {
		return errPathTooLong
	}
	uid, gid, own := parentOwner(rt, dir.Rel)
	if err := rt.Mkdir(target.Rel, 0o755); err != nil {
		m.audit(r, "files.mkdir", target.Path, "başarısız", false)
		return fsError(err, "Klasör oluşturulamadı.")
	}
	adopt(rt, target.Rel, uid, gid, own)
	_ = rt.Chmod(target.Rel, 0o755) // undo the effect of the process umask
	m.audit(r, "files.mkdir", target.Path, "", true)
	fi, err := rt.Lstat(target.Rel)
	if err != nil {
		return fsError(err, "Klasör oluşturulamadı.")
	}
	httpx.JSON(w, http.StatusCreated, m.describe(rt, target, fi))
	return nil
}

func (m *Module) handleRename(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	name := strings.TrimSpace(req.Name)
	if err := validName(name); err != nil {
		return err
	}
	rt, loc, err := m.open(req.Path)
	if err != nil {
		return err
	}
	defer rt.Close()
	if err := m.guardProtected(loc, "yeniden adlandırılamaz"); err != nil {
		return err
	}
	target := loc.Parent().Child(name)
	if len(target.Path) > maxPathLen {
		return errPathTooLong
	}
	if target.Path == loc.Path {
		return httpx.BadRequest("Yeni ad eskisiyle aynı.")
	}
	if _, err := rt.Lstat(loc.Rel); err != nil {
		return fsError(err, "Öğe bulunamadı.")
	}
	if _, err := rt.Lstat(target.Rel); err == nil {
		return httpx.Conflict("Bu adda bir öğe zaten var.")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fsError(err, "Yeniden adlandırılamadı.")
	}
	if err := rt.Rename(loc.Rel, target.Rel); err != nil {
		m.audit(r, "files.rename", loc.Path, "başarısız", false)
		return fsError(err, "Yeniden adlandırılamadı.")
	}
	m.audit(r, "files.rename", loc.Path, "yeni ad: "+name, true)
	fi, err := rt.Lstat(target.Rel)
	if err != nil {
		return fsError(err, "Yeniden adlandırılamadı.")
	}
	httpx.OK(w, m.describe(rt, target, fi))
	return nil
}

var octalModeRe = regexp.MustCompile(`^0?[0-7]{3}$`)

func (m *Module) handleChmod(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Path string `json:"path"`
		Mode string `json:"mode"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	if !octalModeRe.MatchString(req.Mode) {
		return httpx.BadRequest("İzin değeri 644 veya 0755 gibi üç haneli sekizlik bir sayı olmalıdır.")
	}
	n, err := strconv.ParseUint(req.Mode, 8, 32)
	if err != nil || n > 0o777 {
		return httpx.BadRequest("İzin değeri geçersiz.")
	}
	rt, loc, err := m.open(req.Path)
	if err != nil {
		return err
	}
	defer rt.Close()
	fi, err := rt.Lstat(loc.Rel)
	if err != nil {
		return fsError(err, "Öğe bulunamadı.")
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return httpx.BadRequest("Sembolik bağlantıların izinleri değiştirilemez.")
	}
	if !fi.IsDir() && !fi.Mode().IsRegular() {
		return httpx.BadRequest("Bu öğe türünün izinleri değiştirilemez.")
	}
	if err := rt.Chmod(loc.Rel, os.FileMode(n)); err != nil {
		m.audit(r, "files.chmod", loc.Path, "başarısız", false)
		return fsError(err, "İzinler değiştirilemedi.")
	}
	m.audit(r, "files.chmod", loc.Path, fmt.Sprintf("%04o", n), true)
	fi, err = rt.Lstat(loc.Rel)
	if err != nil {
		return fsError(err, "İzinler değiştirilemedi.")
	}
	httpx.OK(w, m.describe(rt, loc, fi))
	return nil
}

/* ---------- delete ---------- */

func deleteTree(ctx context.Context, j *Job, rt *os.Root, loc location) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fi, err := rt.Lstat(loc.Rel)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return opErr(loc.Path, err)
	}
	if fi.IsDir() {
		j.setCurrent(loc.Path)
		names, _, err := readNames(rt, loc.Rel, fi, 0)
		if err != nil {
			return opErr(loc.Path, err)
		}
		for _, n := range names {
			if err := deleteTree(ctx, j, rt, loc.Child(n)); err != nil {
				return err
			}
		}
	}
	if err := rt.Remove(loc.Rel); err != nil && !errors.Is(err, os.ErrNotExist) {
		return opErr(loc.Path, err)
	}
	j.itemsDone.Add(1)
	return nil
}

func (m *Module) handleDelete(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Paths []string `json:"paths"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	if _, err := checkPaths(req.Paths); err != nil {
		return err
	}
	set := m.newRootSet()
	type item struct {
		rt  *os.Root
		loc location
	}
	items := make([]item, 0, len(req.Paths))
	for _, p := range req.Paths {
		rt, loc, err := set.open(p)
		if err == nil {
			err = m.guardProtected(loc, "silinemez")
		}
		if err != nil {
			set.close()
			return err
		}
		items = append(items, item{rt, loc})
	}
	j, err := m.jobs.start(jobSpec{
		kind: "delete", title: jobTitle(req.Paths), actor: actorOf(r),
		action: "files.delete", target: strings.Join(req.Paths, ", "),
		cleanup: set.close,
		run: func(ctx context.Context, j *Job) (any, error) {
			j.setPhase("scanning")
			var t treeTotals
			for _, it := range items {
				if err := scanTree(ctx, it.rt, it.loc.Rel, 0, &t, nil); err != nil && !errors.Is(err, os.ErrNotExist) {
					return nil, opErr(it.loc.Path, err)
				}
			}
			j.itemsTotal.Store(t.Items)
			j.setPhase("working")
			for _, it := range items {
				if err := deleteTree(ctx, j, it.rt, it.loc); err != nil {
					return nil, err
				}
			}
			return nil, nil
		},
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusAccepted, j.view())
	return nil
}

func jobTitle(paths []string) string {
	if len(paths) == 1 {
		return paths[0]
	}
	return fmt.Sprintf("%s ve %d öğe daha", paths[0], len(paths)-1)
}

/* ---------- copy / move ---------- */

// copier copies trees between two roots without following symlinks.
type copier struct {
	ctx      context.Context
	job      *Job
	src, dst *os.Root
	uid, gid int
	chown    bool
	buf      []byte
}

func (c *copier) finish(rel string, fi os.FileInfo) {
	adopt(c.dst, rel, c.uid, c.gid, c.chown)
	_ = c.dst.Chmod(rel, fi.Mode().Perm())
	_ = c.dst.Chtimes(rel, fi.ModTime(), fi.ModTime())
}

func (c *copier) tree(src, dst location) error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	fi, err := c.src.Lstat(src.Rel)
	if err != nil {
		return opErr(src.Path, err)
	}
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		target, err := c.src.Readlink(src.Rel)
		if err != nil {
			return opErr(src.Path, err)
		}
		if err := c.dst.Remove(dst.Rel); err != nil && !errors.Is(err, os.ErrNotExist) {
			return opErr(dst.Path, err)
		}
		// The link is recreated as a link, never followed. os.Root will
		// still refuse to follow it later if it points outside the root.
		if err := c.dst.Symlink(target, dst.Rel); err != nil {
			return opErr(dst.Path, err)
		}
		adopt(c.dst, dst.Rel, c.uid, c.gid, c.chown)
		c.job.itemsDone.Add(1)
	case fi.IsDir():
		if err := c.dst.Mkdir(dst.Rel, 0o700); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return opErr(dst.Path, err)
			}
			if dfi, e := c.dst.Lstat(dst.Rel); e != nil || !dfi.IsDir() {
				return opMsg(dst.Path, "Hedefte aynı adda, dizin olmayan bir öğe var.")
			}
		}
		c.job.itemsDone.Add(1)
		names, _, err := readNames(c.src, src.Rel, fi, 0)
		if err != nil {
			return opErr(src.Path, err)
		}
		for _, n := range names {
			if strings.HasPrefix(n, tempPrefix) && strings.HasSuffix(n, tempSuffix) {
				continue
			}
			if err := c.tree(src.Child(n), dst.Child(n)); err != nil {
				return err
			}
		}
		c.finish(dst.Rel, fi)
	case fi.Mode().IsRegular():
		if err := c.file(src, dst, fi); err != nil {
			return err
		}
		c.job.itemsDone.Add(1)
	default:
		// Devices, sockets and pipes are not copied.
		c.job.skipped.Add(1)
		c.job.itemsDone.Add(1)
	}
	return nil
}

func (c *copier) file(src, dst location, fi os.FileInfo) error {
	c.job.setCurrent(src.Path)
	if dfi, err := c.dst.Lstat(dst.Rel); err == nil && dfi.IsDir() {
		return opMsg(dst.Path, "Hedefte aynı adda bir dizin var.")
	}
	in, err := openSame(c.src, src.Rel, fi)
	if err != nil {
		return opErr(src.Path, err)
	}
	defer in.Close()
	tmp := path.Join(path.Dir(dst.Rel), tempName())
	out, err := c.dst.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return opErr(dst.Path, err)
	}
	err = copyStream(c.ctx, out, in, c.buf, &c.job.bytesDone)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		c.finish(tmp, fi)
		err = c.dst.Rename(tmp, dst.Rel)
	}
	if err != nil {
		_ = c.dst.Remove(tmp)
		return opErr(dst.Path, err)
	}
	return nil
}

type counter interface{ Add(int64) int64 }

// copyStream copies with cancellation and progress accounting.
func copyStream(ctx context.Context, dst io.Writer, src io.Reader, buf []byte, done counter) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
			if done != nil {
				done.Add(int64(n))
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}

type transferItem struct {
	rt  *os.Root
	loc location
}

type transferPlan struct {
	set      *rootSet
	items    []transferItem
	dstRt    *os.Root
	dst      location
	dstInfo  os.FileInfo
	conflict string
}

// planTransfer validates a copy or move request before a job is started.
func (m *Module) planTransfer(paths []string, dest, conflict string, move bool) (*transferPlan, error) {
	if _, err := checkPaths(paths); err != nil {
		return nil, err
	}
	policy, err := parseConflict(conflict)
	if err != nil {
		return nil, err
	}
	p := &transferPlan{set: m.newRootSet(), conflict: policy}
	fail := func(err error) (*transferPlan, error) {
		p.set.close()
		return nil, err
	}
	p.dstRt, p.dst, err = p.set.open(dest)
	if err != nil {
		return fail(err)
	}
	p.dstInfo, err = p.dstRt.Stat(p.dst.Rel)
	if err != nil {
		return fail(fsError(err, "Hedef dizin bulunamadı."))
	}
	if !p.dstInfo.IsDir() {
		return fail(httpx.BadRequest("Hedef bir dizin değil."))
	}
	seen := map[string]bool{}
	for _, sp := range paths {
		rt, loc, err := p.set.open(sp)
		if err != nil {
			return fail(err)
		}
		if seen[loc.Path] {
			continue
		}
		seen[loc.Path] = true
		if move {
			if err := m.guardProtected(loc, "taşınamaz"); err != nil {
				return fail(err)
			}
			if loc.Parent().Path == p.dst.Path {
				return fail(httpx.BadRequest("Öğe zaten bu dizinde: " + loc.Name()))
			}
		} else if loc.IsRoot() {
			// Copying a root is allowed only into a different root.
			if within(loc.Path, p.dst.Path) {
				return fail(httpx.BadRequest("Bir dizin kendi içine kopyalanamaz."))
			}
		}
		fi, err := rt.Lstat(loc.Rel)
		if err != nil {
			return fail(fsError(err, "Kaynak bulunamadı: "+loc.Path))
		}
		if fi.IsDir() && within(loc.Path, p.dst.Path) {
			if move {
				return fail(httpx.BadRequest("Bir dizin kendi içine taşınamaz."))
			}
			return fail(httpx.BadRequest("Bir dizin kendi içine kopyalanamaz."))
		}
		if len(p.dst.Path)+1+len(loc.Name()) > maxPathLen {
			return fail(errPathTooLong)
		}
		p.items = append(p.items, transferItem{rt, loc})
	}
	return p, nil
}

// target decides where an item lands, applying the conflict policy. skip is
// true when the item must be left alone.
func (p *transferPlan) target(name string) (loc location, exists os.FileInfo, skip bool, err error) {
	loc = p.dst.Child(name)
	fi, err := p.dstRt.Lstat(loc.Rel)
	if errors.Is(err, os.ErrNotExist) {
		return loc, nil, false, nil
	}
	if err != nil {
		return loc, nil, false, opErr(loc.Path, err)
	}
	switch p.conflict {
	case conflictSkip:
		return loc, fi, true, nil
	case conflictOverwrite:
		return loc, fi, false, nil
	}
	free, err := uniqueName(p.dstRt, p.dst.Rel, name)
	if err != nil {
		return loc, nil, false, opMsg(loc.Path, "Hedefte kullanılabilir bir ad bulunamadı.")
	}
	return p.dst.Child(free), nil, false, nil
}

// scan measures one source and refuses trees that contain the destination
// directory (which would make the copy recurse into itself, for example
// through a symlinked path that a lexical comparison cannot see).
func (p *transferPlan) scan(ctx context.Context, it transferItem, t *treeTotals, verb string) error {
	err := scanTree(ctx, it.rt, it.loc.Rel, 0, t, func(_ string, fi os.FileInfo) error {
		if fi.IsDir() && os.SameFile(fi, p.dstInfo) {
			return opMsg(it.loc.Path, "Bir dizin kendi içine "+verb+".")
		}
		return nil
	})
	return opErr(it.loc.Path, err)
}

func (p *transferPlan) copier(ctx context.Context, j *Job, src *os.Root) *copier {
	c := &copier{ctx: ctx, job: j, src: src, dst: p.dstRt, buf: make([]byte, 1<<20)}
	c.uid, c.gid, c.chown = ownerIDs(p.dstInfo)
	return c
}

func (m *Module) handleCopy(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Paths    []string `json:"paths"`
		Dest     string   `json:"dest"`
		Conflict string   `json:"conflict"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	plan, err := m.planTransfer(req.Paths, req.Dest, req.Conflict, false)
	if err != nil {
		return err
	}
	j, err := m.jobs.start(jobSpec{
		kind: "copy", title: jobTitle(req.Paths), actor: actorOf(r),
		action: "files.copy", target: strings.Join(req.Paths, ", ") + " → " + plan.dst.Path,
		cleanup: plan.set.close,
		run: func(ctx context.Context, j *Job) (any, error) {
			j.setPhase("scanning")
			var t treeTotals
			for _, it := range plan.items {
				if err := plan.scan(ctx, it, &t, "kopyalanamaz"); err != nil {
					return nil, err
				}
			}
			j.itemsTotal.Store(t.Items)
			j.bytesTotal.Store(t.Bytes)
			j.setPhase("working")
			for _, it := range plan.items {
				dst, _, skip, err := plan.target(it.loc.Name())
				if err != nil {
					return nil, err
				}
				if skip {
					j.skipped.Add(1)
					continue
				}
				if err := plan.copier(ctx, j, it.rt).tree(it.loc, dst); err != nil {
					return nil, err
				}
			}
			return nil, nil
		},
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusAccepted, j.view())
	return nil
}

func (m *Module) handleMove(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Paths    []string `json:"paths"`
		Dest     string   `json:"dest"`
		Conflict string   `json:"conflict"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	plan, err := m.planTransfer(req.Paths, req.Dest, req.Conflict, true)
	if err != nil {
		return err
	}
	j, err := m.jobs.start(jobSpec{
		kind: "move", title: jobTitle(req.Paths), actor: actorOf(r),
		action: "files.move", target: strings.Join(req.Paths, ", ") + " → " + plan.dst.Path,
		cleanup: plan.set.close,
		run: func(ctx context.Context, j *Job) (any, error) {
			j.setPhase("working")
			j.itemsTotal.Store(int64(len(plan.items)))
			for _, it := range plan.items {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if err := moveOne(ctx, j, plan, it); err != nil {
					return nil, err
				}
			}
			return nil, nil
		},
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusAccepted, j.view())
	return nil
}

func moveOne(ctx context.Context, j *Job, plan *transferPlan, it transferItem) error {
	j.setCurrent(it.loc.Path)
	sfi, err := it.rt.Lstat(it.loc.Rel)
	if err != nil {
		return opErr(it.loc.Path, err)
	}
	dst, existing, skip, err := plan.target(it.loc.Name())
	if err != nil {
		return err
	}
	if skip {
		j.skipped.Add(1)
		j.itemsDone.Add(1)
		return nil
	}
	if existing != nil && existing.IsDir() != sfi.IsDir() {
		return opMsg(dst.Path, "Hedefte aynı adda, farklı türde bir öğe var.")
	}
	merge := existing != nil && existing.IsDir()
	if it.rt == plan.dstRt && !merge {
		err := it.rt.Rename(it.loc.Rel, dst.Rel)
		if err == nil {
			j.itemsDone.Add(1)
			return nil
		}
		if errors.Is(err, syscall.EINVAL) {
			return opMsg(it.loc.Path, "Bir dizin kendi içine taşınamaz.")
		}
		if !errors.Is(err, syscall.EXDEV) {
			return opErr(it.loc.Path, err)
		}
	}
	// Different filesystem, different root or a merge: copy, then delete.
	var t treeTotals
	if err := plan.scan(ctx, it, &t, "taşınamaz"); err != nil {
		return err
	}
	j.itemsTotal.Add(2*t.Items - 1)
	j.bytesTotal.Add(t.Bytes)
	c := plan.copier(ctx, j, it.rt)
	// A move keeps the original owner, like mv does.
	c.chown = false
	mc := &moveCopier{copier: c}
	if err := mc.tree(it.loc, dst); err != nil {
		return err
	}
	return deleteTree(ctx, j, it.rt, it.loc)
}

// moveCopier copies like copier but keeps each entry's own owner.
type moveCopier struct{ *copier }

func (mc *moveCopier) tree(src, dst location) error {
	err := mc.copier.tree(src, dst)
	if err != nil {
		return err
	}
	return mc.restoreOwners(src, dst)
}

func (mc *moveCopier) restoreOwners(src, dst location) error {
	if err := mc.ctx.Err(); err != nil {
		return err
	}
	fi, err := mc.src.Lstat(src.Rel)
	if err != nil {
		return nil
	}
	if uid, gid, ok := ownerIDs(fi); ok {
		_ = mc.dst.Lchown(dst.Rel, uid, gid)
		if fi.Mode()&os.ModeSymlink == 0 {
			// chown clears set-id bits and may change mode; reapply.
			_ = mc.dst.Chmod(dst.Rel, fi.Mode().Perm())
			_ = mc.dst.Chtimes(dst.Rel, fi.ModTime(), fi.ModTime())
		}
	}
	if !fi.IsDir() {
		return nil
	}
	names, _, err := readNames(mc.src, src.Rel, fi, 0)
	if err != nil {
		return nil
	}
	for _, n := range names {
		if err := mc.restoreOwners(src.Child(n), dst.Child(n)); err != nil {
			return err
		}
	}
	// Children changed the directory's modification time.
	_ = mc.dst.Chtimes(dst.Rel, fi.ModTime(), fi.ModTime())
	return nil
}
