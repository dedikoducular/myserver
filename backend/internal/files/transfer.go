package files

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"myserver/internal/httpx"
)

/* ---------- response headers ---------- */

// rfc5987 percent-encodes a value for the filename* parameter.
func rfc5987(s string) string {
	const hexdigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexdigits[c>>4])
		b.WriteByte(hexdigits[c&15])
	}
	return b.String()
}

// contentDisposition builds a header value that is safe for any file name:
// an ASCII fallback plus the RFC 5987 encoded real name.
func contentDisposition(kind, name string) string {
	var fb strings.Builder
	for _, r := range name {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' || r == '%' || r == ';' || r == '/' {
			fb.WriteByte('_')
		} else {
			fb.WriteRune(r)
		}
	}
	fallback := fb.String()
	if strings.Trim(fallback, "_. ") == "" {
		fallback = "dosya"
	}
	return kind + `; filename="` + fallback + `"; filename*=UTF-8''` + rfc5987(name)
}

// fileHeaders makes a response carrying user data inert: the browser must
// not sniff the type and must not run anything from it, even if it were
// opened directly as a document in the panel's origin.
func fileHeaders(w http.ResponseWriter, contentType, disposition string) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Disposition", disposition)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Cache-Control", "private, no-store")
}

/* ---------- download ---------- */

// openRegular opens loc for reading, following symlinks that stay inside
// the root, and requires the result to be a regular file.
func openRegular(rt *os.Root, loc location) (*os.File, os.FileInfo, error) {
	fi, err := rt.Stat(loc.Rel)
	if err != nil {
		return nil, nil, fsError(err, "Dosya bulunamadı.")
	}
	if fi.IsDir() {
		return nil, nil, httpx.BadRequest("Bu yol bir dizin.")
	}
	if !fi.Mode().IsRegular() {
		return nil, nil, httpx.BadRequest("Yalnızca normal dosyalar açılabilir.")
	}
	f, err := rt.OpenFile(loc.Rel, os.O_RDONLY|openNonblock, 0)
	if err != nil {
		return nil, nil, fsError(err, "Dosya açılamadı.")
	}
	fi, err = f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, nil, httpx.BadRequest("Yalnızca normal dosyalar açılabilir.")
	}
	return f, fi, nil
}

func (m *Module) handleDownload(w http.ResponseWriter, r *http.Request) error {
	paths, err := queryPaths(r)
	if err != nil {
		return err
	}
	if len(paths) == 1 {
		rt, loc, err := m.open(paths[0])
		if err != nil {
			return err
		}
		fi, err := rt.Stat(loc.Rel)
		if err != nil {
			rt.Close()
			return fsError(err, "Dosya bulunamadı.")
		}
		if !fi.IsDir() {
			defer rt.Close()
			f, fi, err := openRegular(rt, loc)
			if err != nil {
				return err
			}
			defer f.Close()
			fileHeaders(w, "application/octet-stream", contentDisposition("attachment", loc.Name()))
			http.ServeContent(w, r, "", fi.ModTime(), f)
			return nil
		}
		rt.Close()
	}
	return m.streamZip(w, r, paths)
}

type zipSource struct {
	rt  *os.Root
	loc location
}

// streamZip sends a selection as a ZIP written directly to the response.
func (m *Module) streamZip(w http.ResponseWriter, r *http.Request, paths []string) error {
	set := m.newRootSet()
	defer set.close()
	sources := make([]zipSource, 0, len(paths))
	for _, p := range paths {
		rt, loc, err := set.open(p)
		if err != nil {
			return err
		}
		if _, err := rt.Lstat(loc.Rel); err != nil {
			return fsError(err, "Öğe bulunamadı: "+loc.Path)
		}
		sources = append(sources, zipSource{rt, loc})
	}
	name := "dosyalar.zip"
	if len(sources) == 1 {
		name = sources[0].loc.Name() + ".zip"
	}
	fileHeaders(w, "application/zip", contentDisposition("attachment", name))
	w.WriteHeader(http.StatusOK)
	zw := zip.NewWriter(w)
	err := writeZip(r.Context(), zw, sources, nil, nil)
	if err == nil {
		err = zw.Close()
	}
	if err != nil && r.Context().Err() == nil {
		// Headers are already sent; the truncated archive is the signal.
		slog.Warn("zip indirme yarıda kesildi", "error", err.Error())
	}
	return nil
}

// writeZip adds the sources to zw. Symlinks and special files are skipped,
// so an archive never contains data from outside the selection. exclude,
// when set, is the archive file itself (it may live inside a source).
func writeZip(ctx context.Context, zw *zip.Writer, sources []zipSource, j *Job, exclude os.FileInfo) error {
	buf := make([]byte, 1<<20)
	used := map[string]bool{}
	var add func(src zipSource, name string) error
	add = func(src zipSource, name string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		fi, err := src.rt.Lstat(src.loc.Rel)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return opErr(src.loc.Path, err)
		}
		done := func() {
			if j != nil {
				j.itemsDone.Add(1)
			}
		}
		switch {
		case fi.IsDir():
			hdr := &zip.FileHeader{Name: name + "/", Modified: fi.ModTime()}
			hdr.SetMode(os.ModeDir | fi.Mode().Perm())
			if _, err := zw.CreateHeader(hdr); err != nil {
				return err
			}
			done()
			names, _, err := readNames(src.rt, src.loc.Rel, fi, 0)
			if err != nil {
				return opErr(src.loc.Path, err)
			}
			for _, n := range names {
				child := zipSource{src.rt, src.loc.Child(n)}
				if err := add(child, name+"/"+n); err != nil {
					return err
				}
			}
		case fi.Mode().IsRegular():
			if exclude != nil && os.SameFile(fi, exclude) {
				return nil
			}
			if j != nil {
				j.setCurrent(src.loc.Path)
			}
			f, err := openSame(src.rt, src.loc.Rel, fi)
			if err != nil {
				return opErr(src.loc.Path, err)
			}
			hdr := &zip.FileHeader{Name: name, Modified: fi.ModTime(), Method: zip.Deflate}
			hdr.SetMode(fi.Mode().Perm())
			if isCompressed(name) {
				hdr.Method = zip.Store
			}
			out, err := zw.CreateHeader(hdr)
			if err == nil {
				var c counter
				if j != nil {
					c = &j.bytesDone
				}
				err = copyStream(ctx, out, f, buf, c)
			}
			f.Close()
			if err != nil {
				return opErr(src.loc.Path, err)
			}
			done()
		default:
			if j != nil {
				j.skipped.Add(1)
			}
			done()
		}
		return nil
	}
	for _, s := range sources {
		name := s.loc.Name()
		if used[name] {
			base, ext := splitExt(name)
			for i := 2; ; i++ {
				name = base + " (" + itoa(i) + ")" + ext
				if !used[name] {
					break
				}
			}
		}
		used[name] = true
		if err := add(s, name); err != nil {
			return err
		}
	}
	return nil
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// isCompressed reports formats that do not benefit from deflate.
func isCompressed(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".zip", ".gz", ".tgz", ".bz2", ".xz", ".zst", ".7z", ".rar",
		".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif", ".heic",
		".mp4", ".m4v", ".mkv", ".webm", ".avi", ".mov",
		".mp3", ".m4a", ".aac", ".ogg", ".opus", ".flac":
		return true
	}
	return false
}

/* ---------- preview ---------- */

// handlePreview serves images, audio and video inline. Everything else,
// notably HTML and SVG, is refused so that nothing from the disk can run
// in the panel's origin.
func (m *Module) handlePreview(w http.ResponseWriter, r *http.Request) error {
	rt, loc, err := m.open(r.URL.Query().Get("path"))
	if err != nil {
		return err
	}
	defer rt.Close()
	ctype, ok := previewType(loc.Name())
	if !ok {
		return httpx.NewError(http.StatusUnsupportedMediaType, "preview_unsupported", "Bu dosya türü önizlenemez.")
	}
	f, fi, err := openRegular(rt, loc)
	if err != nil {
		return err
	}
	defer f.Close()
	fileHeaders(w, ctype, contentDisposition("inline", loc.Name()))
	http.ServeContent(w, r, "", fi.ModTime(), f)
	return nil
}

/* ---------- text ---------- */

func isText(b []byte) bool {
	return utf8.Valid(b) && !bytes.ContainsRune(b, 0)
}

func (m *Module) handleTextRead(w http.ResponseWriter, r *http.Request) error {
	rt, loc, err := m.open(r.URL.Query().Get("path"))
	if err != nil {
		return err
	}
	defer rt.Close()
	f, fi, err := openRegular(rt, loc)
	if err != nil {
		return err
	}
	defer f.Close()
	if fi.Size() > textMaxBytes {
		return httpx.NewError(http.StatusRequestEntityTooLarge, "too_large", "Dosya metin olarak açmak için çok büyük (en fazla 1 MB).")
	}
	data, err := io.ReadAll(io.LimitReader(f, textMaxBytes+1))
	if err != nil {
		return fsError(err, "Dosya okunamadı.")
	}
	if len(data) > textMaxBytes {
		return httpx.NewError(http.StatusRequestEntityTooLarge, "too_large", "Dosya metin olarak açmak için çok büyük (en fazla 1 MB).")
	}
	if !isText(data) {
		return httpx.NewError(http.StatusUnsupportedMediaType, "binary", "Bu dosya metin dosyası değil.")
	}
	httpx.OK(w, map[string]any{
		"path":        loc.Path,
		"content":     string(data),
		"size":        len(data),
		"modified_at": fi.ModTime().Unix(),
	})
	return nil
}

func (m *Module) handleTextWrite(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Path       string `json:"path"`
		Content    string `json:"content"`
		ModifiedAt *int64 `json:"modified_at"`
	}
	// JSON escaping can inflate the text, so the body cap is larger than
	// the content cap checked below.
	r.Body = http.MaxBytesReader(w, r.Body, 8*textMaxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return httpx.BadRequest("İstek gövdesi geçersiz.").Wrap(err)
	}
	if len(req.Content) > textMaxBytes {
		return httpx.NewError(http.StatusRequestEntityTooLarge, "too_large", "Metin en fazla 1 MB olabilir.")
	}
	if !isText([]byte(req.Content)) {
		return httpx.BadRequest("Metin geçersiz karakter içeriyor.")
	}
	rt, loc, err := m.open(req.Path)
	if err != nil {
		return err
	}
	defer rt.Close()
	if loc.IsRoot() {
		return httpx.BadRequest("Bu yol bir dizin.")
	}
	if err := validName(loc.Name()); err != nil {
		return err
	}
	dir := loc.Parent()
	uid, gid, own := parentOwner(rt, dir.Rel)
	mode := os.FileMode(0o644)
	fi, err := rt.Stat(loc.Rel)
	switch {
	case err == nil:
		if !fi.Mode().IsRegular() {
			return httpx.BadRequest("Yalnızca normal dosyalar düzenlenebilir.")
		}
		if req.ModifiedAt != nil && fi.ModTime().Unix() != *req.ModifiedAt {
			return httpx.Conflict("Dosya siz düzenlerken değişmiş. Yeniden açıp tekrar deneyin.")
		}
		// An existing file keeps its own owner and permissions.
		mode = fi.Mode().Perm()
		if u, g, ok := ownerIDs(fi); ok {
			uid, gid, own = u, g, true
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return fsError(err, "Dosya açılamadı.")
	}

	tmp := path.Join(dir.Rel, tempName())
	out, err := rt.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fsError(err, "Dosya yazılamadı.")
	}
	_, err = io.WriteString(out, req.Content)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		adopt(rt, tmp, uid, gid, own)
		_ = rt.Chmod(tmp, mode)
		// Rename replaces the directory entry; if loc is a symlink the
		// link itself is replaced by the file, inside the root.
		err = rt.Rename(tmp, loc.Rel)
	}
	if err != nil {
		_ = rt.Remove(tmp)
		m.audit(r, "files.edit", loc.Path, "başarısız", false)
		return fsError(err, "Dosya kaydedilemedi.")
	}
	m.audit(r, "files.edit", loc.Path, "", true)
	nfi, err := rt.Lstat(loc.Rel)
	if err != nil {
		return fsError(err, "Dosya kaydedilemedi.")
	}
	httpx.OK(w, m.describe(rt, loc, nfi))
	return nil
}

/* ---------- upload ---------- */

type uploadResult struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

var errTooLarge = errors.New("upload too large")

// handleUpload streams multipart file parts to disk. Each file is written
// under a temporary name in the destination directory and renamed when
// complete; on any failure, including the client going away, the temporary
// file is removed.
func (m *Module) handleUpload(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	overwrite := q.Get("overwrite") == "true"
	limit := m.maxUploadBytes()
	tooLarge := httpx.NewError(http.StatusRequestEntityTooLarge, "too_large",
		"Dosya, izin verilen en büyük yükleme boyutunu aşıyor.")
	if r.ContentLength > limit+(1<<20) {
		return tooLarge
	}
	rt, dir, err := m.open(q.Get("path"))
	if err != nil {
		return err
	}
	defer rt.Close()
	dfi, err := rt.Stat(dir.Rel)
	if err != nil {
		return fsError(err, "Hedef dizin bulunamadı.")
	}
	if !dfi.IsDir() {
		return httpx.BadRequest("Hedef bir dizin değil.")
	}
	uid, gid, own := ownerIDs(dfi)

	r.Body = http.MaxBytesReader(w, r.Body, limit+(1<<20))
	mr, err := r.MultipartReader()
	if err != nil {
		return httpx.BadRequest("Yükleme isteği geçersiz.").Wrap(err)
	}
	results := []uploadResult{}
	buf := make([]byte, 1<<20)
	remaining := limit
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				return tooLarge
			}
			return httpx.BadRequest("Yükleme yarıda kesildi.").Wrap(err)
		}
		if part.FormName() != "file" && part.FileName() == "" {
			part.Close()
			continue
		}
		name := part.FileName() // already reduced to its base name
		if err := validName(name); err != nil {
			part.Close()
			return err
		}
		target := dir.Child(name)
		if len(target.Path) > maxPathLen {
			part.Close()
			return errPathTooLong
		}
		n, err := m.storeUpload(r.Context(), rt, target, part, buf, remaining, overwrite, uid, gid, own)
		part.Close()
		if err != nil {
			m.audit(r, "files.upload", target.Path, "başarısız", false)
			var mbe *http.MaxBytesError
			if errors.Is(err, errTooLarge) || errors.As(err, &mbe) {
				return tooLarge
			}
			if r.Context().Err() != nil {
				return httpx.BadRequest("Yükleme iptal edildi.")
			}
			var he *httpx.Error
			if errors.As(err, &he) {
				return he
			}
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return httpx.BadRequest("Yükleme yarıda kesildi.").Wrap(err)
			}
			return fsError(err, "Dosya yüklenemedi.")
		}
		remaining -= n
		m.audit(r, "files.upload", target.Path, itoa64(n)+" bayt", true)
		results = append(results, uploadResult{Name: name, Path: target.Path, Size: n})
	}
	if len(results) == 0 {
		return httpx.BadRequest("Yüklenecek dosya gönderilmedi.")
	}
	httpx.JSON(w, http.StatusCreated, results)
	return nil
}

func itoa64(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// limitedReader fails, instead of silently stopping, when the limit is
// exceeded.
type limitedReader struct {
	r    io.Reader
	left int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.left < 0 {
		return 0, errTooLarge
	}
	if int64(len(p)) > l.left+1 {
		p = p[:l.left+1]
	}
	n, err := l.r.Read(p)
	l.left -= int64(n)
	if l.left < 0 {
		return n, errTooLarge
	}
	return n, err
}

// sourceReader remembers a failure of the reader it wraps, so that broken
// input (a dropped connection, a corrupt archive member) can be told apart
// from a failure to write to the disk.
type sourceReader struct {
	r   io.Reader
	err error
}

func (s *sourceReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if err != nil && err != io.EOF {
		s.err = err
	}
	return n, err
}

func (m *Module) storeUpload(ctx context.Context, rt *os.Root, target location, src io.Reader, buf []byte,
	limit int64, overwrite bool, uid, gid int, own bool) (written int64, err error) {

	existing, lerr := rt.Lstat(target.Rel)
	if lerr == nil {
		if existing.IsDir() {
			return 0, httpx.Conflict("Aynı adda bir dizin var: " + target.Name())
		}
		if !overwrite {
			return 0, httpx.Conflict("Aynı adda bir dosya zaten var: " + target.Name())
		}
	} else if !errors.Is(lerr, os.ErrNotExist) {
		return 0, lerr
	}

	tmp := path.Join(path.Dir(target.Rel), tempName())
	out, err := rt.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = rt.Remove(tmp)
		}
	}()
	var count counterInt
	in := &sourceReader{r: src}
	err = copyStream(ctx, out, &limitedReader{r: in, left: limit}, buf, &count)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		if in.err != nil {
			// The request body failed, not the disk.
			return 0, httpx.BadRequest("Yükleme yarıda kesildi.").Wrap(in.err)
		}
		return 0, err
	}
	adopt(rt, tmp, uid, gid, own)
	if err := rt.Chmod(tmp, 0o644); err != nil {
		return 0, err
	}
	if overwrite {
		if err := rt.Rename(tmp, target.Rel); err != nil {
			return 0, err
		}
		committed = true
		return int64(count), nil
	}
	// Without overwrite the final step must not replace a file created in
	// the meantime: a hard link fails if the name exists.
	if err := rt.Link(tmp, target.Rel); err == nil {
		_ = rt.Remove(tmp)
		committed = true
		return int64(count), nil
	} else if errors.Is(err, os.ErrExist) {
		return 0, httpx.Conflict("Aynı adda bir dosya zaten var: " + target.Name())
	}
	// Filesystems without hard links (FAT, exFAT): check, then rename.
	if _, err := rt.Lstat(target.Rel); err == nil {
		return 0, httpx.Conflict("Aynı adda bir dosya zaten var: " + target.Name())
	}
	if err := rt.Rename(tmp, target.Rel); err != nil {
		return 0, err
	}
	committed = true
	return int64(count), nil
}

type counterInt int64

func (c *counterInt) Add(n int64) int64 {
	*c += counterInt(n)
	return int64(*c)
}
