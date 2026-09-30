package backup

// The backup archive.
//
// A backup is one file: a gzip-compressed tar archive, optionally wrapped in
// the encryption layer of crypto.go (then the file name ends in ".enc").
// The tar archive holds, in this order:
//
//	config.json                      the resolved configuration of the
//	                                 application (apps.Config, with secrets)
//	app-manifest.yaml                the catalog manifest of the application,
//	                                 when it could be read (informational)
//	data/NN-volume-<name>.tar.part-000001 ...
//	data/NN-bind-<key>.tar.part-000001 ...
//	                                 one tar stream per Docker volume or host
//	                                 folder, cut into parts of at most 4 MiB
//	                                 because a tar entry needs its size in
//	                                 advance and the streams are not held in
//	                                 memory or spooled to disk. Concatenating
//	                                 the parts in order gives the tar stream.
//	manifest.json                    written last: describes the backup and
//	                                 lists every entry with its size and
//	                                 SHA-256 checksum
//
// To restore by hand: tar xzf backup.tar.gz; cat data/01-volume-x.tar.part-*
// | tar x -C <target>.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"regexp"
	"strconv"
	"time"

	"myserver/internal/apps"
)

const (
	formatName    = "myserver-backup"
	formatVersion = 1

	nameConfig      = "config.json"
	nameAppManifest = "app-manifest.yaml"
	nameManifest    = "manifest.json"

	partSize     = 4 << 20
	maxSmallFile = 8 << 20
	maxEntries   = 64

	kindVolume = "volume"
	kindBind   = "bind"
)

var (
	dataNameRe = regexp.MustCompile(`^data/[0-9]{2}-(?:volume|bind)-[A-Za-z0-9_.-]{1,100}\.tar$`)
	partNameRe = regexp.MustCompile(`^(data/[0-9]{2}-(?:volume|bind)-[A-Za-z0-9_.-]{1,100}\.tar)\.part-([0-9]{6})$`)
	unsafeChar = regexp.MustCompile(`[^A-Za-z0-9_.-]`)
)

// Manifest describes a backup. It is stored as manifest.json.
type Manifest struct {
	Format        string                    `json:"format"`
	FormatVersion int                       `json:"format_version"`
	CreatedAt     int64                     `json:"created_at"`
	PanelVersion  string                    `json:"panel_version"`
	App           ManifestApp               `json:"app"`
	Images        map[string]apps.ImageInfo `json:"images"`
	// Consistency is "stopped" when the application was not running
	// while its data was copied, "live" otherwise.
	Consistency string `json:"consistency"`
	// AppStopped reports whether the backup itself stopped the application.
	AppStopped    bool        `json:"app_stopped"`
	Trigger       string      `json:"trigger"`
	IncludesBinds bool        `json:"includes_binds"`
	Files         []FileEntry `json:"files"`
	Entries       []DataEntry `json:"entries"`
	Warnings      []string    `json:"warnings"`
}

type ManifestApp struct {
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

// FileEntry is a small file of the archive.
type FileEntry struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// DataEntry is the tar stream of one volume or host folder.
type DataEntry struct {
	Archive  string `json:"archive"`
	Kind     string `json:"kind"`
	Source   string `json:"source"`
	Service  string `json:"service"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only"`
	Parts    int    `json:"parts"`
	// Size and SHA256 are those of the tar stream.
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	// Files is the number of regular files, Items of all entries and
	// ContentBytes the sum of the file sizes.
	Files        int64 `json:"files"`
	Items        int64 `json:"items"`
	ContentBytes int64 `json:"content_bytes"`
}

func dataArchiveName(index int, kind, name string) string {
	safe := unsafeChar.ReplaceAllString(name, "_")
	if len(safe) > 100 {
		safe = safe[:100]
	}
	if safe == "" {
		safe = "x"
	}
	return fmt.Sprintf("data/%02d-%s-%s.tar", index, kind, safe)
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

/* ---------- writing ---------- */

type archiveWriter struct {
	file  *os.File
	count *countWriter
	enc   *encWriter
	gz    *gzip.Writer
	tw    *tar.Writer
	mtime time.Time
}

// newArchiveWriter creates path (which must not exist) with mode 0600.
func newArchiveWriter(path string, km *keyMaterial, now time.Time) (*archiveWriter, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	a := &archiveWriter{file: f, count: &countWriter{w: f}, mtime: now}
	var out io.Writer = a.count
	if km != nil {
		a.enc, err = newEncWriter(a.count, km)
		if err != nil {
			f.Close()
			os.Remove(path)
			return nil, err
		}
		out = a.enc
	}
	a.gz, err = gzip.NewWriterLevel(out, gzip.BestSpeed)
	if err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	a.tw = tar.NewWriter(a.gz)
	return a, nil
}

func (a *archiveWriter) writeEntry(name string, data []byte) error {
	if err := a.tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg, Name: name, Mode: 0o600, Size: int64(len(data)),
		ModTime: a.mtime, Format: tar.FormatPAX,
	}); err != nil {
		return err
	}
	_, err := a.tw.Write(data)
	return err
}

// addFile stores a small file and returns its manifest entry.
func (a *archiveWriter) addFile(name string, data []byte) (FileEntry, error) {
	if err := a.writeEntry(name, data); err != nil {
		return FileEntry{}, err
	}
	sum := sha256.Sum256(data)
	return FileEntry{Name: name, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}, nil
}

// written is the number of bytes written to the file so far.
func (a *archiveWriter) written() int64 { return a.count.n }

// finish flushes every layer and closes the file.
func (a *archiveWriter) finish() error {
	err := a.tw.Close()
	if e := a.gz.Close(); err == nil {
		err = e
	}
	if a.enc != nil {
		if e := a.enc.Close(); err == nil {
			err = e
		}
	}
	if e := a.file.Sync(); err == nil {
		err = e
	}
	if e := a.file.Close(); err == nil {
		err = e
	}
	return err
}

// abort closes the file without completing the archive.
func (a *archiveWriter) abort() { _ = a.file.Close() }

// partWriter cuts a stream into archive entries of at most partSize bytes.
type partWriter struct {
	a     *archiveWriter
	base  string
	buf   []byte
	n     int
	parts int
	size  int64
	sum   hash.Hash
}

func (a *archiveWriter) stream(base string) *partWriter {
	return &partWriter{a: a, base: base, buf: make([]byte, partSize), sum: sha256.New()}
}

func (p *partWriter) Write(b []byte) (int, error) {
	total := 0
	for len(b) > 0 {
		if p.n == len(p.buf) {
			if err := p.flush(); err != nil {
				return total, err
			}
		}
		c := copy(p.buf[p.n:], b)
		p.n += c
		b = b[c:]
		total += c
	}
	return total, nil
}

func (p *partWriter) flush() error {
	p.parts++
	if p.parts > 999999 {
		return errors.New("backup: data stream has too many parts")
	}
	data := p.buf[:p.n]
	if err := p.a.writeEntry(fmt.Sprintf("%s.part-%06d", p.base, p.parts), data); err != nil {
		return err
	}
	p.sum.Write(data)
	p.size += int64(len(data))
	p.n = 0
	return nil
}

// close writes the last part; there is always at least one.
func (p *partWriter) close() (parts int, size int64, sha string, err error) {
	if p.n > 0 || p.parts == 0 {
		if err := p.flush(); err != nil {
			return 0, 0, "", err
		}
	}
	return p.parts, p.size, hex.EncodeToString(p.sum.Sum(nil)), nil
}

/* ---------- reading ---------- */

// archiveError is a problem with a backup file, with a Turkish message.
type archiveError struct {
	Message string
	Err     error
}

func (e *archiveError) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

func (e *archiveError) Unwrap() error { return e.Err }

func badArchive(msg string, err error) error { return &archiveError{Message: msg, Err: err} }

type archiveReader struct {
	file      *os.File
	enc       *encReader
	gz        *gzip.Reader
	tr        *tar.Reader
	pending   *tar.Header
	encrypted bool
}

// openArchive opens a backup file for reading.
func openArchive(path string, stored *keyMaterial, passphrase string) (*archiveReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, badArchive("Yedek dosyası açılamadı.", err)
	}
	head := make([]byte, len(encMagic))
	n, _ := io.ReadFull(f, head)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, badArchive("Yedek dosyası okunamadı.", err)
	}
	a := &archiveReader{file: f}
	var src io.Reader = f
	if isEncrypted(head[:n]) {
		a.encrypted = true
		a.enc, err = newEncReader(f, stored, passphrase)
		if err != nil {
			f.Close()
			return nil, err
		}
		src = a.enc
	}
	a.gz, err = gzip.NewReader(src)
	if err != nil {
		f.Close()
		if errors.Is(err, errEncCorrupt) {
			return nil, err
		}
		return nil, badArchive("Dosya bir MyServer yedeği değil veya bozuk (gzip başlığı okunamadı).", err)
	}
	a.tr = tar.NewReader(a.gz)
	return a, nil
}

func (a *archiveReader) close() { _ = a.file.Close() }

func (a *archiveReader) next() (*tar.Header, error) {
	if h := a.pending; h != nil {
		a.pending = nil
		return h, nil
	}
	return a.tr.Next()
}

// partsReader reads the concatenated parts of one data stream.
type partsReader struct {
	a    *archiveReader
	base string
	next int
	done bool
}

func (p *partsReader) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	for {
		if p.done {
			return 0, io.EOF
		}
		n, err := p.a.tr.Read(b)
		if n > 0 || (err != nil && !errors.Is(err, io.EOF)) {
			if errors.Is(err, io.EOF) {
				err = nil
			}
			return n, err
		}
		if err == nil {
			continue
		}
		hdr, err := p.a.tr.Next()
		if errors.Is(err, io.EOF) {
			p.done = true
			return 0, io.EOF
		}
		if err != nil {
			return 0, err
		}
		if hdr.Typeflag == tar.TypeReg && hdr.Name == fmt.Sprintf("%s.part-%06d", p.base, p.next) {
			if hdr.Size > partSize {
				return 0, badArchive("Yedek dosyasında beklenenden büyük bir parça var.", nil)
			}
			p.next++
			continue
		}
		p.a.pending = hdr
		p.done = true
		return 0, io.EOF
	}
}

// zeroOnly accepts zero bytes and refuses everything else.
type zeroOnly struct{}

func (zeroOnly) Write(p []byte) (int, error) {
	for _, b := range p {
		if b != 0 {
			return 0, badArchive("Yedek dosyasında arşivin sonundan sonra veri var.", nil)
		}
	}
	return len(p), nil
}

// scanResult is what a pass over an archive found.
type scanResult struct {
	Manifest  *Manifest
	Config    *apps.Config
	Encrypted bool
}

// dataFunc consumes the tar stream of one data entry. It need not read the
// stream to the end.
type dataFunc func(archive string, r io.Reader) error

type measured struct {
	size  int64
	sha   string
	parts int
}

func readError(err error) error {
	var ae *archiveError
	var ue *unsafeEntryError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ae), errors.As(err, &ue),
		errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, errEncCorrupt), errors.Is(err, errCorruptTar):
		return err
	}
	return badArchive("Yedek dosyası okunamadı; dosya bozuk veya eksik.", err)
}

// scanArchive reads a whole archive in one pass: it checks the structure,
// hands every data stream to onData, and finally compares sizes and
// checksums with the manifest. The result is only returned when everything
// matched.
func scanArchive(ctx context.Context, a *archiveReader, onData dataFunc) (*scanResult, error) {
	res := &scanResult{Encrypted: a.encrypted}
	files := map[string]measured{}
	data := map[string]measured{}
	var manifestRaw, configRaw []byte
	buf := make([]byte, 256*1024)

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hdr, err := a.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, readError(err)
		}
		if manifestRaw != nil {
			return nil, badArchive("Yedek dosyasında manifest.json dosyasından sonra veri var.", nil)
		}
		if hdr.Typeflag != tar.TypeReg {
			return nil, badArchive("Yedek dosyasında beklenmeyen bir kayıt türü var.", nil)
		}
		switch name := hdr.Name; {
		case name == nameConfig || name == nameAppManifest || name == nameManifest:
			if _, dup := files[name]; dup || hdr.Size > maxSmallFile {
				return nil, badArchive("Yedek dosyasının yapısı geçersiz ("+name+").", nil)
			}
			b, err := io.ReadAll(io.LimitReader(a.tr, maxSmallFile+1))
			if err != nil {
				return nil, readError(err)
			}
			sum := sha256.Sum256(b)
			files[name] = measured{size: int64(len(b)), sha: hex.EncodeToString(sum[:])}
			switch name {
			case nameManifest:
				manifestRaw = b
			case nameConfig:
				configRaw = b
			}
		default:
			m := partNameRe.FindStringSubmatch(name)
			if m == nil || m[2] != "000001" || hdr.Size > partSize {
				return nil, badArchive("Yedek dosyasında tanınmayan bir kayıt var.", nil)
			}
			base := m[1]
			if _, dup := data[base]; dup || len(data) >= maxEntries {
				return nil, badArchive("Yedek dosyasının yapısı geçersiz (yinelenen veri kaydı).", nil)
			}
			pr := &partsReader{a: a, base: base, next: 2}
			sum := sha256.New()
			cw := &countWriter{w: sum}
			tee := io.TeeReader(pr, cw)
			if onData != nil {
				if err := onData(base, tee); err != nil {
					return nil, readError(err)
				}
			}
			if err := copyBody(ctx, io.Discard, tee, buf, nil); err != nil {
				return nil, readError(err)
			}
			data[base] = measured{size: cw.n, sha: hex.EncodeToString(sum.Sum(nil)), parts: pr.next - 1}
		}
	}
	// Read to the very end so that the gzip checksum and the final
	// encrypted chunk are verified.
	// Only the zero padding of the tar format may follow the end marker:
	// anything else (a second archive appended to the file, for example)
	// is data that nothing described or checked.
	if err := copyBody(ctx, zeroOnly{}, a.gz, buf, nil); err != nil {
		return nil, readError(err)
	}
	if a.enc != nil {
		if err := copyBody(ctx, io.Discard, a.enc, buf, nil); err != nil {
			return nil, readError(err)
		}
	}

	if manifestRaw == nil {
		return nil, badArchive("Yedek dosyasında manifest.json yok; dosya bir MyServer yedeği değil veya yarım kalmış.", nil)
	}
	if configRaw == nil {
		return nil, badArchive("Yedek dosyasında uygulama yapılandırması (config.json) yok.", nil)
	}
	var man Manifest
	dec := json.NewDecoder(bytes.NewReader(manifestRaw))
	if err := dec.Decode(&man); err != nil {
		return nil, badArchive("manifest.json okunamadı.", err)
	}
	if man.Format != formatName {
		return nil, badArchive("Dosya bir MyServer yedeği değil.", nil)
	}
	if man.FormatVersion != formatVersion {
		return nil, badArchive("Yedek biçimi sürümü ("+strconv.Itoa(man.FormatVersion)+") bu panel sürümü tarafından desteklenmiyor.", nil)
	}
	if len(man.Entries) > maxEntries {
		return nil, badArchive("manifest.json geçersiz.", nil)
	}
	listed := map[string]bool{}
	for _, f := range man.Files {
		got, ok := files[f.Name]
		if !ok || f.Name == nameManifest || listed[f.Name] {
			return nil, badArchive("Yedekte "+f.Name+" dosyası eksik.", nil)
		}
		listed[f.Name] = true
		if got.size != f.Size || got.sha != f.SHA256 {
			return nil, badArchive("Sağlama toplamı uyuşmuyor: "+f.Name+". Yedek dosyası bozulmuş veya değiştirilmiş.", nil)
		}
	}
	if !listed[nameConfig] {
		return nil, badArchive("manifest.json, config.json dosyasını listelemiyor.", nil)
	}
	for name := range files {
		if name != nameManifest && !listed[name] {
			return nil, badArchive("Yedekte manifest.json içinde listelenmeyen bir dosya var: "+name, nil)
		}
	}
	seen := map[string]bool{}
	for _, e := range man.Entries {
		if !dataNameRe.MatchString(e.Archive) || seen[e.Archive] || (e.Kind != kindVolume && e.Kind != kindBind) {
			return nil, badArchive("manifest.json geçersiz bir veri kaydı içeriyor.", nil)
		}
		seen[e.Archive] = true
		got, ok := data[e.Archive]
		if !ok {
			return nil, badArchive("Yedekte "+e.Source+" verisi eksik.", nil)
		}
		if got.size != e.Size || got.sha != e.SHA256 || got.parts != e.Parts {
			return nil, badArchive("Sağlama toplamı uyuşmuyor: "+e.Source+". Yedek dosyası bozulmuş veya değiştirilmiş.", nil)
		}
	}
	for name := range data {
		if !seen[name] {
			return nil, badArchive("Yedekte manifest.json içinde listelenmeyen veri var.", nil)
		}
	}
	var cfg apps.Config
	if err := json.Unmarshal(configRaw, &cfg); err != nil {
		return nil, badArchive("Uygulama yapılandırması (config.json) okunamadı.", err)
	}
	res.Manifest, res.Config = &man, &cfg
	return res, nil
}
