package backup

// Host folders (bind mounts). They are read and written by the panel
// process itself, always through an os.Root opened on the folder: no path
// can leave it, and symbolic links are stored and recreated as links and
// never followed.

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"myserver/internal/apps"
)

const maxDepth = 256

// bindReport collects what could not be handled exactly.
type bindReport struct {
	Skipped  int64 // sockets, devices, FIFOs
	Changed  int64 // files that changed while being read
	Vanished int64 // files that disappeared while being read
	Meta     int64 // owner/mode/time that could not be set
}

func (r *bindReport) warnings(where string) []string {
	var out []string
	if r.Skipped > 0 {
		out = append(out, where+": "+itoa(r.Skipped)+" özel dosya (soket, aygıt veya FIFO) yedeğe alınmadı.")
	}
	if r.Changed > 0 {
		out = append(out, where+": "+itoa(r.Changed)+" dosya okunurken değişti; bu dosyalar tutarsız olabilir.")
	}
	if r.Vanished > 0 {
		out = append(out, where+": "+itoa(r.Vanished)+" dosya okunurken silindi ve yedeğe alınmadı.")
	}
	if r.Meta > 0 {
		out = append(out, where+": "+itoa(r.Meta)+" öğenin sahibi, izinleri veya zamanı ayarlanamadı.")
	}
	return out
}

// exportBind writes the contents of a host folder to tw.
func exportBind(ctx context.Context, dir string, tw *tar.Writer, progress func(int64)) (tarStats, *bindReport, error) {
	rep := &bindReport{}
	var st tarStats
	root, err := os.OpenRoot(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return st, rep, userErr(dir+" klasörü bulunamadı.", err)
		}
		return st, rep, userErr(dir+" klasörü açılamadı.", err)
	}
	defer root.Close()
	w := &bindWalker{ctx: ctx, root: root, tw: tw, st: &st, rep: rep, progress: progress, buf: make([]byte, 256*1024)}
	if err := w.dir(".", 0); err != nil {
		return st, rep, err
	}
	return st, rep, nil
}

type bindWalker struct {
	ctx      context.Context
	root     *os.Root
	tw       *tar.Writer
	st       *tarStats
	rep      *bindReport
	progress func(int64)
	buf      []byte
}

func entryName(rel string, dir bool) string {
	if rel == "." {
		return "./"
	}
	if dir {
		return rel + "/"
	}
	return rel
}

func (w *bindWalker) header(fi fs.FileInfo, link, rel string) (*tar.Header, error) {
	h, err := tar.FileInfoHeader(fi, link)
	if err != nil {
		return nil, err
	}
	h.Name = entryName(rel, fi.IsDir())
	h.Format = tar.FormatPAX
	return h, nil
}

func (w *bindWalker) dir(rel string, depth int) error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	if depth > maxDepth {
		return userErr("Klasör yapısı çok derin: "+rel, nil)
	}
	d, err := w.root.OpenFile(rel, os.O_RDONLY|openNoFollow, 0)
	if err != nil {
		if depth > 0 && errors.Is(err, fs.ErrNotExist) {
			w.rep.Vanished++
			return nil
		}
		return userErr("Klasör okunamadı: "+rel, err)
	}
	defer d.Close()
	fi, err := d.Stat()
	if err != nil {
		return userErr("Klasör okunamadı: "+rel, err)
	}
	if !fi.IsDir() {
		if depth == 0 {
			return userErr("Yedeklenecek yol bir klasör değil.", nil)
		}
		w.rep.Changed++
		return nil
	}
	h, err := w.header(fi, "", rel)
	if err != nil {
		return err
	}
	if err := w.tw.WriteHeader(h); err != nil {
		return err
	}
	w.st.Items++
	for {
		entries, err := d.ReadDir(256)
		for _, e := range entries {
			child := e.Name()
			if rel != "." {
				child = rel + "/" + e.Name()
			}
			if err := w.entry(child, e, depth); err != nil {
				return err
			}
		}
		if errors.Is(err, io.EOF) || (err == nil && len(entries) == 0) {
			return nil
		}
		if err != nil {
			return userErr("Klasör okunamadı: "+rel, err)
		}
	}
}

func (w *bindWalker) entry(rel string, e fs.DirEntry, depth int) error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	switch t := e.Type(); {
	case t.IsDir():
		return w.dir(rel, depth+1)
	case t&fs.ModeSymlink != 0:
		target, err := w.root.Readlink(rel)
		if err != nil {
			w.rep.Vanished++
			return nil
		}
		fi, err := w.root.Lstat(rel)
		if err != nil {
			w.rep.Vanished++
			return nil
		}
		h, err := w.header(fi, target, rel)
		if err != nil {
			return err
		}
		w.st.Items++
		return w.tw.WriteHeader(h)
	case t.IsRegular():
		return w.file(rel)
	default:
		w.rep.Skipped++
		return nil
	}
}

func (w *bindWalker) file(rel string) error {
	f, err := w.root.OpenFile(rel, os.O_RDONLY|openNoFollow|openNonBlock, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			w.rep.Vanished++
			return nil
		}
		// Replaced by a symbolic link or special file in the meantime.
		w.rep.Changed++
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		w.rep.Changed++
		return nil
	}
	h, err := w.header(fi, "", rel)
	if err != nil {
		return err
	}
	if err := w.tw.WriteHeader(h); err != nil {
		return err
	}
	w.st.Items++
	w.st.Files++
	w.st.Bytes += h.Size
	remaining := h.Size
	for remaining > 0 {
		if err := w.ctx.Err(); err != nil {
			return err
		}
		chunk := w.buf
		if int64(len(chunk)) > remaining {
			chunk = chunk[:remaining]
		}
		n, rerr := f.Read(chunk)
		if n > 0 {
			if _, err := w.tw.Write(chunk[:n]); err != nil {
				return err
			}
			remaining -= int64(n)
			if w.progress != nil {
				w.progress(int64(n))
			}
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				return userErr("Dosya okunamadı: "+rel, rerr)
			}
			break
		}
	}
	if remaining > 0 {
		// The file shrank while it was read. The tar entry must still
		// have the announced length, so it is padded with zeros and
		// reported.
		w.rep.Changed++
		clear(w.buf)
		for remaining > 0 {
			chunk := w.buf
			if int64(len(chunk)) > remaining {
				chunk = chunk[:remaining]
			}
			if _, err := w.tw.Write(chunk); err != nil {
				return err
			}
			remaining -= int64(len(chunk))
		}
	} else if now, err := f.Stat(); err == nil && (now.Size() != fi.Size() || !now.ModTime().Equal(fi.ModTime())) {
		w.rep.Changed++
	}
	return nil
}

// bindSize sums the sizes of the regular files of a folder, for the
// free-space estimate.
func bindSize(ctx context.Context, dir string) (int64, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	var total int64
	err = fs.WalkDir(root.FS(), ".", func(_ string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				total += fi.Size()
			}
		}
		return nil
	})
	return total, err
}

// checkBindTarget validates a host folder of a backup before anything is
// written: it must be inside the allowed roots, also after resolving
// symbolic links.
func checkBindTarget(dir string, roots []string) error {
	if err := apps.CheckBindPath(dir, roots); err != nil {
		return bindPathErr(err, dir)
	}
	real := make([]string, 0, len(roots))
	for _, r := range roots {
		if rr, err := filepath.EvalSymlinks(r); err == nil {
			real = append(real, filepath.ToSlash(rr))
		}
	}
	existing := dir
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return userErr(dir+" klasörüne erişilemiyor.", err)
		}
		parent := path.Dir(existing)
		if parent == existing {
			break
		}
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return userErr(dir+" klasörüne erişilemiyor.", err)
	}
	full := path.Clean(filepath.ToSlash(resolved) + strings.TrimPrefix(dir, existing))
	if err := apps.CheckBindPath(full, real); err != nil {
		return userErr(dir+" izin verilen klasörlerin dışına işaret ediyor.", err)
	}
	if fi, err := os.Stat(dir); err == nil && !fi.IsDir() {
		return userErr(dir+" bir klasör değil.", nil)
	}
	return nil
}

func bindPathErr(err error, dir string) error {
	var ie *apps.InputError
	if errors.As(err, &ie) {
		return userErr(ie.Message, nil)
	}
	return userErr(dir+" klasörü kullanılamıyor.", err)
}

// importBind replaces the contents of a host folder by a tar stream.
func importBind(ctx context.Context, dir string, roots []string, src io.Reader, progress func(int64)) (tarStats, *bindReport, error) {
	rep := &bindReport{}
	if err := checkBindTarget(dir, roots); err != nil {
		return tarStats{}, rep, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return tarStats{}, rep, userErr(dir+" klasörü oluşturulamadı.", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return tarStats{}, rep, userErr(dir+" klasörü açılamadı.", err)
	}
	defer root.Close()
	if err := clearRoot(ctx, root); err != nil {
		return tarStats{}, rep, userErr(dir+" klasörünün mevcut içeriği silinemedi.", err)
	}
	buf := make([]byte, 256*1024)
	// Creating an entry changes the modification time of its directory, so
	// the times of directories are set last, children before parents.
	type dirTime struct {
		name string
		at   time.Time
	}
	var dirs []dirTime
	st, err := readTar(ctx, src, "", func(h *tar.Header, body io.Reader) error {
		if err := extractEntry(ctx, root, h, body, buf, rep, progress); err != nil {
			return err
		}
		if h.Typeflag == tar.TypeDir && !h.ModTime.IsZero() {
			name := strings.TrimSuffix(h.Name, "/")
			if h.Name == "./" {
				name = "."
			}
			dirs = append(dirs, dirTime{name, h.ModTime})
		}
		return nil
	})
	if err != nil {
		return st, rep, streamErr(err, dir+" klasörüne yazılırken hata oluştu.")
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		// Lstat first: an entry that replaced the directory later in the
		// stream must not receive the time.
		if fi, err := root.Lstat(dirs[i].name); err != nil || !fi.IsDir() {
			continue
		}
		if err := root.Chtimes(dirs[i].name, time.Time{}, dirs[i].at); err != nil {
			rep.Meta++
		}
	}
	return st, rep, nil
}

func clearRoot(ctx context.Context, root *os.Root) error {
	d, err := root.Open(".")
	if err != nil {
		return err
	}
	defer d.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		names, err := d.Readdirnames(256)
		for _, n := range names {
			if n == "." || n == ".." {
				continue
			}
			if err := root.RemoveAll(n); err != nil {
				return err
			}
		}
		if errors.Is(err, io.EOF) || (err == nil && len(names) == 0) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

const modeBits = fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky

func extractEntry(ctx context.Context, root *os.Root, h *tar.Header, body io.Reader, buf []byte, rep *bindReport, progress func(int64)) error {
	name := strings.TrimSuffix(h.Name, "/")
	if h.Name == "./" {
		name = "."
	}
	mode := h.FileInfo().Mode() & modeBits
	if name != "." {
		if parent := path.Dir(name); parent != "." {
			if _, err := root.Lstat(parent); errors.Is(err, fs.ErrNotExist) {
				if err := root.MkdirAll(parent, 0o755); err != nil {
					return userErr("Klasör oluşturulamadı: "+parent, err)
				}
			}
		}
	}
	switch h.Typeflag {
	case tar.TypeDir:
		if name != "." {
			if err := root.Mkdir(name, 0o700); err != nil {
				fi, serr := root.Lstat(name)
				if serr != nil || !fi.IsDir() {
					return userErr("Klasör oluşturulamadı: "+name, err)
				}
			}
		}
		meta := root.Lchown(name, h.Uid, h.Gid)
		if err := root.Chmod(name, mode); err != nil {
			meta = err
		}
		if meta != nil {
			rep.Meta++
		}
	case tar.TypeReg:
		f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|openNoFollow, 0o600)
		if err != nil {
			return userErr("Dosya oluşturulamadı: "+name, err)
		}
		cerr := copyBody(ctx, f, body, buf, progress)
		meta := f.Chown(h.Uid, h.Gid)
		if err := f.Chmod(mode); err != nil {
			meta = err
		}
		if err := f.Close(); cerr == nil {
			cerr = err
		}
		if cerr != nil {
			if errors.Is(cerr, context.Canceled) {
				return cerr
			}
			return userErr("Dosya yazılamadı: "+name, cerr)
		}
		if !h.ModTime.IsZero() {
			if err := root.Chtimes(name, time.Time{}, h.ModTime); err != nil {
				meta = err
			}
		}
		if meta != nil {
			rep.Meta++
		}
	case tar.TypeSymlink:
		_ = root.Remove(name)
		if err := root.Symlink(h.Linkname, name); err != nil {
			return userErr("Sembolik bağlantı oluşturulamadı: "+name, err)
		}
		if err := root.Lchown(name, h.Uid, h.Gid); err != nil {
			rep.Meta++
		}
	case tar.TypeLink:
		_ = root.Remove(name)
		if err := root.Link(h.Linkname, name); err != nil {
			return userErr("Sabit bağlantı oluşturulamadı: "+name, err)
		}
	default:
		rep.Skipped++
	}
	return nil
}
