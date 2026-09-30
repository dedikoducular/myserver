package files

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"myserver/internal/httpx"
)

const maxArchiveEntries = 200000

/* ---------- create ---------- */

func (m *Module) handleZip(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Paths []string `json:"paths"`
		Dest  string   `json:"dest"`
		Name  string   `json:"name"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	if _, err := checkPaths(req.Paths); err != nil {
		return err
	}
	name := strings.TrimSpace(req.Name)
	if name != "" && !strings.HasSuffix(strings.ToLower(name), ".zip") {
		name += ".zip"
	}
	if err := validName(name); err != nil {
		return err
	}
	set := m.newRootSet()
	fail := func(err error) error {
		set.close()
		return err
	}
	dstRt, dst, err := set.open(req.Dest)
	if err != nil {
		return fail(err)
	}
	dfi, err := dstRt.Stat(dst.Rel)
	if err != nil {
		return fail(fsError(err, "Hedef dizin bulunamadı."))
	}
	if !dfi.IsDir() {
		return fail(httpx.BadRequest("Hedef bir dizin değil."))
	}
	target := dst.Child(name)
	if len(target.Path) > maxPathLen {
		return fail(errPathTooLong)
	}
	if _, err := dstRt.Lstat(target.Rel); err == nil {
		return fail(httpx.Conflict("Bu adda bir öğe zaten var: " + name))
	}
	sources := make([]zipSource, 0, len(req.Paths))
	for _, p := range req.Paths {
		rt, loc, err := set.open(p)
		if err != nil {
			return fail(err)
		}
		if _, err := rt.Lstat(loc.Rel); err != nil {
			return fail(fsError(err, "Öğe bulunamadı: "+loc.Path))
		}
		sources = append(sources, zipSource{rt, loc})
	}
	uid, gid, own := ownerIDs(dfi)

	j, err := m.jobs.start(jobSpec{
		kind: "zip", title: target.Path, actor: actorOf(r),
		action: "files.zip", target: target.Path,
		cleanup: set.close,
		run: func(ctx context.Context, j *Job) (any, error) {
			j.setPhase("scanning")
			var t treeTotals
			for _, s := range sources {
				if err := scanTree(ctx, s.rt, s.loc.Rel, 0, &t, nil); err != nil {
					return nil, opErr(s.loc.Path, err)
				}
			}
			if t.Items > maxArchiveEntries {
				return nil, opMsg(target.Path, "Seçim bir arşiv için çok fazla öğe içeriyor.")
			}
			j.itemsTotal.Store(t.Items)
			j.bytesTotal.Store(t.Bytes)
			j.setPhase("working")

			tmp := path.Join(dst.Rel, tempName())
			out, err := dstRt.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return nil, opErr(target.Path, err)
			}
			self, _ := out.Stat()
			zw := zip.NewWriter(out)
			err = writeZip(ctx, zw, sources, j, self)
			if err == nil {
				err = zw.Close()
			}
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err == nil {
				adopt(dstRt, tmp, uid, gid, own)
				_ = dstRt.Chmod(tmp, 0o644)
				// Link refuses to replace a file created meanwhile.
				if lerr := dstRt.Link(tmp, target.Rel); lerr == nil {
					_ = dstRt.Remove(tmp)
				} else if errors.Is(lerr, os.ErrExist) {
					err = opMsg(target.Path, "Bu adda bir öğe zaten var.")
				} else {
					err = dstRt.Rename(tmp, target.Rel)
				}
			}
			if err != nil {
				_ = dstRt.Remove(tmp)
				return nil, opErr(target.Path, err)
			}
			return map[string]any{"path": target.Path}, nil
		},
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusAccepted, j.view())
	return nil
}

/* ---------- extract ---------- */

// extractor writes archive members into a directory. out is an os.Root
// opened on the DESTINATION directory itself, so even a member name that
// slipped past validation could not be written outside it.
type extractor struct {
	ctx      context.Context
	job      *Job
	out      *os.Root
	destPath string
	conflict string
	left     int64 // bytes still allowed
	entries  int
	uid, gid int
	chown    bool
	buf      []byte
	made     map[string]bool
}

func (x *extractor) count() error {
	x.entries++
	if x.entries > maxArchiveEntries {
		return opMsg(x.destPath, "Arşiv çok fazla öğe içeriyor; açma işlemi durduruldu.")
	}
	return x.ctx.Err()
}

// mkdirs creates rel and its missing parents inside the destination.
func (x *extractor) mkdirs(rel string) error {
	if rel == "." || rel == "" || x.made[rel] {
		return nil
	}
	if err := x.mkdirs(path.Dir(rel)); err != nil {
		return err
	}
	err := x.out.Mkdir(rel, 0o755)
	switch {
	case err == nil:
		adopt(x.out, rel, x.uid, x.gid, x.chown)
		_ = x.out.Chmod(rel, 0o755)
	case errors.Is(err, os.ErrExist):
		fi, serr := x.out.Stat(rel)
		if serr != nil || !fi.IsDir() {
			return opMsg(path.Join(x.destPath, rel), "Arşivdeki bir dizinle aynı adda bir dosya var.")
		}
	default:
		return opErr(path.Join(x.destPath, rel), err)
	}
	x.made[rel] = true
	return nil
}

func (x *extractor) dir(name string, mode os.FileMode, mod time.Time) error {
	if err := x.mkdirs(name); err != nil {
		return err
	}
	x.job.itemsDone.Add(1)
	return nil
}

// file writes one regular member. The size in the archive header is not
// trusted: the data is read through a limit tied to the remaining budget.
func (x *extractor) file(name string, mode os.FileMode, mod time.Time, src io.Reader) error {
	full := path.Join(x.destPath, name)
	if err := x.mkdirs(path.Dir(name)); err != nil {
		return err
	}
	if fi, err := x.out.Lstat(name); err == nil {
		if fi.IsDir() {
			return opMsg(full, "Arşivdeki bir dosyayla aynı adda bir dizin var.")
		}
		switch x.conflict {
		case conflictSkip:
			x.job.skipped.Add(1)
			x.job.itemsDone.Add(1)
			return nil
		case conflictRename:
			free, err := uniqueName(x.out, path.Dir(name), path.Base(name))
			if err != nil {
				return opMsg(full, "Kullanılabilir bir ad bulunamadı.")
			}
			name = path.Join(path.Dir(name), free)
			full = path.Join(x.destPath, name)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return opErr(full, err)
	}
	x.job.setCurrent(full)

	tmp := path.Join(path.Dir(name), tempName())
	out, err := x.out.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return opErr(full, err)
	}
	var n counterInt
	in := &sourceReader{r: src}
	err = copyStream(x.ctx, out, &limitedReader{r: in, left: x.left}, x.buf, &n)
	x.job.bytesDone.Add(int64(n))
	x.left -= int64(n)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		perm := mode.Perm() // set-id and sticky bits are dropped
		if perm == 0 {
			perm = 0o644
		}
		perm |= 0o600
		adopt(x.out, tmp, x.uid, x.gid, x.chown)
		_ = x.out.Chmod(tmp, perm)
		if !mod.IsZero() && mod.Year() > 1979 {
			_ = x.out.Chtimes(tmp, mod, mod)
		}
		err = x.out.Rename(tmp, name)
	}
	if err != nil {
		_ = x.out.Remove(tmp)
		if errors.Is(err, errTooLarge) {
			return opMsg(full, "Arşivin açılmış boyutu izin verilen sınırı aşıyor; açma işlemi durduruldu.")
		}
		if in.err != nil && x.ctx.Err() == nil {
			return opMsg(full, "Arşiv bozuk: içindeki dosya okunamadı; açma işlemi durduruldu.")
		}
		return opErr(full, err)
	}
	x.job.itemsDone.Add(1)
	return nil
}

// skip records a member that is refused: symlinks, hard links, devices and
// names that are absolute or contain "..".
func (x *extractor) skip() {
	x.job.skipped.Add(1)
	x.job.itemsDone.Add(1)
}

func (x *extractor) zip(f *os.File, size int64) error {
	zr, err := zip.NewReader(f, size)
	if err != nil {
		return opMsg(x.destPath, "ZIP dosyası bozuk veya desteklenmiyor.")
	}
	if len(zr.File) > maxArchiveEntries {
		return opMsg(x.destPath, "Arşiv çok fazla öğe içeriyor.")
	}
	var declared uint64
	for _, zf := range zr.File {
		declared += zf.UncompressedSize64
	}
	if declared > uint64(x.left) {
		return opMsg(x.destPath, "Arşivin açılmış boyutu izin verilen sınırı aşıyor.")
	}
	x.job.itemsTotal.Store(int64(len(zr.File)))
	x.job.bytesTotal.Store(int64(declared))
	for _, zf := range zr.File {
		if err := x.count(); err != nil {
			return err
		}
		name, ok := archiveEntryName(zf.Name)
		mode := zf.Mode()
		isDir := strings.HasSuffix(zf.Name, "/") || strings.HasSuffix(zf.Name, "\\") || mode.IsDir()
		if !ok || (!isDir && !mode.IsRegular()) {
			x.skip()
			continue
		}
		if isDir {
			if err := x.dir(name, mode, zf.Modified); err != nil {
				return err
			}
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return opMsg(path.Join(x.destPath, name), "Arşivdeki dosya açılamadı (şifreli veya desteklenmeyen sıkıştırma).")
		}
		err = x.file(name, mode, zf.Modified, rc)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (x *extractor) tar(src io.Reader) error {
	tr := tar.NewReader(src)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			if x.ctx.Err() != nil {
				return x.ctx.Err()
			}
			return opMsg(x.destPath, "TAR arşivi bozuk veya desteklenmiyor.")
		}
		if err := x.count(); err != nil {
			return err
		}
		x.job.itemsTotal.Add(1)
		name, ok := archiveEntryName(hdr.Name)
		mode := os.FileMode(hdr.Mode & 0o777)
		switch {
		case hdr.Typeflag == tar.TypeXGlobalHeader || hdr.Typeflag == tar.TypeXHeader:
			x.job.itemsTotal.Add(-1)
		case !ok:
			x.skip()
		case hdr.Typeflag == tar.TypeDir:
			if err := x.dir(name, mode, hdr.ModTime); err != nil {
				return err
			}
		case hdr.Typeflag == tar.TypeReg:
			if err := x.file(name, mode, hdr.ModTime, tr); err != nil {
				return err
			}
		default:
			x.skip()
		}
	}
}

func (m *Module) handleExtract(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Path     string `json:"path"`
		Dest     string `json:"dest"`
		Conflict string `json:"conflict"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	policy, err := parseConflict(req.Conflict)
	if err != nil {
		return err
	}
	set := m.newRootSet()
	fail := func(err error) error {
		set.close()
		return err
	}
	srcRt, src, err := set.open(req.Path)
	if err != nil {
		return fail(err)
	}
	kind := archiveKind(src.Name())
	if kind == "" {
		return fail(httpx.BadRequest("Yalnızca .zip, .tar, .tar.gz ve .tgz arşivleri açılabilir."))
	}
	if probe, _, err := openRegular(srcRt, src); err != nil {
		return fail(err)
	} else {
		probe.Close()
	}
	dstRt, dst, err := set.open(req.Dest)
	if err != nil {
		return fail(err)
	}
	dfi, err := dstRt.Stat(dst.Rel)
	if err != nil {
		return fail(fsError(err, "Hedef dizin bulunamadı."))
	}
	if !dfi.IsDir() {
		return fail(httpx.BadRequest("Hedef bir dizin değil."))
	}
	limit := m.maxExtractBytes()

	j, err := m.jobs.start(jobSpec{
		kind: "extract", title: src.Path, actor: actorOf(r),
		action: "files.extract", target: src.Path + " → " + dst.Path,
		cleanup: set.close,
		run: func(ctx context.Context, j *Job) (any, error) {
			j.setPhase("working")
			f, fi, err := openRegular(srcRt, src)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			out, err := dstRt.OpenRoot(dst.Rel)
			if err != nil {
				return nil, opErr(dst.Path, err)
			}
			defer out.Close()
			if _, free, ok := diskUsageIn(out, "."); ok && free < limit {
				// Never fill the disk completely: leave the budget at
				// what is actually available.
				limit = free
			}
			x := &extractor{
				ctx: ctx, job: j, out: out, destPath: dst.Path, conflict: policy,
				left: limit, buf: make([]byte, 1<<20), made: map[string]bool{},
			}
			x.uid, x.gid, x.chown = ownerIDs(dfi)
			switch kind {
			case "zip":
				err = x.zip(f, fi.Size())
			case "tar":
				err = x.tar(f)
			case "tgz":
				gz, gerr := gzip.NewReader(f)
				if gerr != nil {
					return nil, opMsg(src.Path, "Arşiv bozuk: gzip başlığı okunamadı.")
				}
				err = x.tar(gz)
				gz.Close()
			}
			if err != nil {
				return nil, err
			}
			return map[string]any{"path": dst.Path}, nil
		},
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusAccepted, j.view())
	return nil
}
