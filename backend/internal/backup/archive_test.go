package backup

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
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"myserver/internal/apps"
)

/* ---------- building archives by hand ---------- */

// rawEntry is one entry of the outer tar archive of a backup file.
type rawEntry struct {
	name string
	data []byte
	typ  byte   // 0 means regular file
	link string // for link entries
}

type streamSpec struct {
	kind   string
	source string
	tar    []byte
}

// blueprint describes a backup file; build turns it into entries that form
// a valid archive, which tests then damage in one specific way.
type blueprint struct {
	cfg     *apps.Config
	streams []streamSpec
	// partLen cuts the streams into parts of this size (default partSize).
	partLen int
}

func testConfig(slug string, mounts ...apps.Mount) *apps.Config {
	if len(mounts) == 0 {
		mounts = []apps.Mount{{Type: apps.VolumeNamed, Key: "data", Source: "myserver-" + slug + "-data", Target: "/data"}}
	}
	return &apps.Config{
		Slug: slug, Name: "Fotolar", Version: "1.2.3", Network: "myserver-" + slug, BindAddress: apps.BindAll,
		Services: []apps.ServiceConfig{{
			Name: "app", ContainerName: "myserver-" + slug, Image: "ghcr.io/ornek/fotolar:1.2.3",
			Restart: "unless-stopped", NetworkMode: "bridge",
			Env: []apps.EnvValue{
				{Name: "DB_PASSWORD", Key: "db_password", Value: "cok-gizli-veritabani-parolasi", Secret: true},
				{Name: "TZ", Key: "tz", Value: "Europe/Istanbul"},
			},
			Ports:   []apps.PortBinding{{Key: "web", HostIP: "0.0.0.0", Host: 8081, Container: 80, Protocol: "tcp"}},
			Volumes: mounts,
		}},
	}
}

type tarFile struct {
	name    string
	body    string
	typ     byte
	link    string
	mode    int64
	uid     int
	gid     int
	modTime time.Time
}

// tarStream builds the tar stream of one volume.
func tarStream(t testing.TB, files ...tarFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		h := &tar.Header{Name: f.name, Typeflag: f.typ, Linkname: f.link, Mode: f.mode, Uid: f.uid, Gid: f.gid,
			ModTime: f.modTime, Format: tar.FormatPAX}
		if h.Typeflag == 0 {
			h.Typeflag = tar.TypeReg
		}
		if h.Mode == 0 {
			h.Mode = 0o644
			if h.Typeflag == tar.TypeDir {
				h.Mode = 0o755
			}
		}
		if h.ModTime.IsZero() {
			h.ModTime = time.Unix(1_700_000_000, 0)
		}
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(f.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatalf("tar header %q: %v", f.name, err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(f.body)); err != nil {
				t.Fatalf("tar body %q: %v", f.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	return buf.Bytes()
}

func simpleVolume(t testing.TB) []byte {
	return tarStream(t,
		tarFile{name: "./", typ: tar.TypeDir},
		tarFile{name: "foto.jpg", body: "JPEG-verisi"},
		tarFile{name: "alt/", typ: tar.TypeDir},
		tarFile{name: "alt/kasa.kdbx", body: "parola kasası", mode: 0o600},
	)
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// build returns the manifest and the entries of a valid archive.
func (b *blueprint) build(t testing.TB) (*Manifest, []rawEntry) {
	t.Helper()
	if b.cfg == nil {
		b.cfg = testConfig("fotolar")
	}
	if b.streams == nil {
		b.streams = []streamSpec{{kind: kindVolume, source: "myserver-" + b.cfg.Slug + "-data", tar: simpleVolume(t)}}
	}
	partLen := b.partLen
	if partLen == 0 {
		partLen = partSize
	}
	cfgJSON, err := json.MarshalIndent(b.cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	man := &Manifest{
		Format: formatName, FormatVersion: formatVersion, CreatedAt: 1_750_000_000, PanelVersion: "1.0.0",
		App:    ManifestApp{Slug: b.cfg.Slug, Name: b.cfg.Name, Version: b.cfg.Version},
		Images: map[string]apps.ImageInfo{}, Consistency: consistencyStopped, Trigger: triggerManual,
		IncludesBinds: true,
		Files:         []FileEntry{{Name: nameConfig, Size: int64(len(cfgJSON)), SHA256: sha(cfgJSON)}},
		Entries:       []DataEntry{}, Warnings: []string{},
	}
	entries := []rawEntry{{name: nameConfig, data: cfgJSON}}
	for i, s := range b.streams {
		base := dataArchiveName(i+1, s.kind, filepath.Base(s.source))
		parts := 0
		for off := 0; off < len(s.tar) || parts == 0; off += partLen {
			end := min(off+partLen, len(s.tar))
			parts++
			entries = append(entries, rawEntry{name: fmt.Sprintf("%s.part-%06d", base, parts), data: s.tar[off:end]})
		}
		st, err := readTar(context.Background(), bytes.NewReader(s.tar), "", nil)
		if err != nil {
			// Hostile streams are described as they claim to be.
			st = tarStats{}
		}
		man.Entries = append(man.Entries, DataEntry{
			Archive: base, Kind: s.kind, Source: s.source, Service: "app", Target: "/data",
			Parts: parts, Size: int64(len(s.tar)), SHA256: sha(s.tar),
			Files: st.Files, Items: st.Items, ContentBytes: st.Bytes,
		})
	}
	return man, entries
}

func manifestEntry(t testing.TB, man *Manifest) rawEntry {
	t.Helper()
	b, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return rawEntry{name: nameManifest, data: b}
}

// packEntries writes the outer archive: tar, gzip and, with a key, the
// encryption layer.
func packEntries(t testing.TB, km *keyMaterial, entries []rawEntry) []byte {
	t.Helper()
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Linkname: e.link, Mode: 0o600,
			ModTime: time.Unix(1_750_000_000, 0), Format: tar.FormatPAX}
		if h.Typeflag == 0 {
			h.Typeflag = tar.TypeReg
		}
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(e.data))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatalf("outer header %q: %v", e.name, err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write(e.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(tarBuf.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if km == nil {
		return gz.Bytes()
	}
	return encryptBytes(t, km, gz.Bytes())
}

// pack builds a complete valid archive.
func (b *blueprint) pack(t testing.TB, km *keyMaterial) []byte {
	t.Helper()
	man, entries := b.build(t)
	return packEntries(t, km, append(entries, manifestEntry(t, man)))
}

func writeTemp(t testing.TB, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "yedek.tar.gz")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

type scanned struct {
	res     *scanResult
	streams map[string][]byte
}

// scanBytes runs the full verification the module applies to a backup file:
// structure, checksums, every inner tar stream and the manifest rules.
func scanBytes(t testing.TB, data []byte, km *keyMaterial, pass string) (*scanned, error) {
	t.Helper()
	ar, err := openArchive(writeTemp(t, data), km, pass)
	if err != nil {
		return nil, err
	}
	defer ar.close()
	out := &scanned{streams: map[string][]byte{}}
	res, err := scanArchive(context.Background(), ar, func(name string, r io.Reader) error {
		var buf bytes.Buffer
		_, err := readTar(context.Background(), io.TeeReader(r, &buf), "", nil)
		if err == nil {
			_, err = io.Copy(&buf, r)
		}
		out.streams[name] = buf.Bytes()
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := validateBackup(res, []string{"/data", "/home", "/tmp"}); err != nil {
		return nil, err
	}
	out.res = res
	return out, nil
}

func wantRejected(t *testing.T, what string, data []byte, km *keyMaterial) error {
	t.Helper()
	got, err := scanBytes(t, data, km, "")
	if err == nil {
		t.Fatalf("%s: the archive was accepted (manifest %+v)", what, got.res.Manifest.App)
	}
	var ae *archiveError
	var ue *unsafeEntryError
	if !errors.As(err, &ae) && !errors.As(err, &ue) && !errors.Is(err, errEncCorrupt) && !errors.Is(err, errCorruptTar) &&
		!errors.Is(err, errEncHeader) && !errors.Is(err, errPassphraseRequired) {
		t.Fatalf("%s: rejected with an error that has no user message: %T %v", what, err, err)
	}
	if msg := messageOf(err); strings.HasPrefix(msg, "Beklenmeyen") {
		t.Fatalf("%s: the user would see the generic message for %v", what, err)
	}
	return err
}

/* ---------- tests ---------- */

func TestArchiveRoundTripWithTheProductionWriter(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		t.Run(fmt.Sprintf("encrypted=%v", encrypted), func(t *testing.T) {
			var km *keyMaterial
			if encrypted {
				km = keyFor(t, testPass)
			}
			path := filepath.Join(t.TempDir(), "a.tar.gz")
			aw, err := newArchiveWriter(path, km, time.Unix(1_750_000_000, 0))
			if err != nil {
				t.Fatalf("newArchiveWriter: %v", err)
			}
			fi, err := os.Lstat(path)
			if err != nil || fi.Mode().Perm() != 0o600 {
				t.Fatalf("archive mode = %v (err %v), want 0600", fi.Mode(), err)
			}
			cfg := testConfig("fotolar")
			cfg.Services[0].Volumes = append(cfg.Services[0].Volumes,
				apps.Mount{Type: apps.VolumeNamed, Key: "bos", Source: "myserver-fotolar-bos", Target: "/bos"})
			cfgJSON, _ := json.Marshal(cfg)
			fe, err := aw.addFile(nameConfig, cfgJSON)
			if err != nil {
				t.Fatal(err)
			}
			// A stream of several parts: 9 MiB and a bit.
			big := strings.Repeat("0123456789abcdef", (9<<20)/16) + "son"
			stream := tarStream(t, tarFile{name: "./", typ: tar.TypeDir}, tarFile{name: "buyuk.bin", body: big}, tarFile{name: "kucuk.txt", body: "merhaba"})
			empty := tarStream(t, tarFile{name: "./", typ: tar.TypeDir})
			man := &Manifest{Format: formatName, FormatVersion: formatVersion, CreatedAt: 1_750_000_000,
				App: ManifestApp{Slug: cfg.Slug, Name: cfg.Name, Version: cfg.Version}, Consistency: consistencyStopped,
				Trigger: triggerManual, Files: []FileEntry{fe}, Warnings: []string{"bir uyarı"}}
			for i, s := range []struct {
				src  string
				data []byte
			}{{"myserver-fotolar-data", stream}, {"myserver-fotolar-bos", empty}} {
				base := dataArchiveName(i+1, kindVolume, s.src)
				pw := aw.stream(base)
				// Written in uneven pieces.
				for off := 0; off < len(s.data); off += 777_777 {
					if _, err := pw.Write(s.data[off:min(off+777_777, len(s.data))]); err != nil {
						t.Fatal(err)
					}
				}
				parts, size, sum, err := pw.close()
				if err != nil {
					t.Fatal(err)
				}
				wantParts := (len(s.data) + partSize - 1) / partSize
				if parts != wantParts || size != int64(len(s.data)) || sum != sha(s.data) {
					t.Fatalf("stream %s: parts=%d size=%d, want %d parts of %d bytes", s.src, parts, size, wantParts, len(s.data))
				}
				man.Entries = append(man.Entries, DataEntry{Archive: base, Kind: kindVolume, Source: s.src,
					Parts: parts, Size: size, SHA256: sum})
			}
			manJSON, _ := json.Marshal(man)
			if err := aw.writeEntry(nameManifest, manJSON); err != nil {
				t.Fatal(err)
			}
			if err := aw.finish(); err != nil {
				t.Fatalf("finish: %v", err)
			}
			fi, _ = os.Stat(path)
			if aw.written() != fi.Size() {
				t.Fatalf("written() = %d, file size %d", aw.written(), fi.Size())
			}
			head := make([]byte, 8)
			f, _ := os.Open(path)
			io.ReadFull(f, head)
			f.Close()
			if isEncrypted(head) != encrypted {
				t.Fatalf("isEncrypted = %v", isEncrypted(head))
			}

			ar, err := openArchive(path, km, "")
			if err != nil {
				t.Fatalf("openArchive: %v", err)
			}
			defer ar.close()
			got := map[string][]byte{}
			res, err := scanArchive(context.Background(), ar, func(name string, r io.Reader) error {
				b, err := io.ReadAll(r)
				got[name] = b
				return err
			})
			if err != nil {
				t.Fatalf("scanArchive: %v", err)
			}
			if res.Encrypted != encrypted {
				t.Fatalf("Encrypted = %v", res.Encrypted)
			}
			if !bytes.Equal(got["data/01-volume-myserver-fotolar-data.tar"], stream) {
				t.Fatal("the large stream differs")
			}
			if !bytes.Equal(got["data/02-volume-myserver-fotolar-bos.tar"], empty) {
				t.Fatal("the small stream differs")
			}
			wantMan, _ := json.Marshal(man)
			gotMan, _ := json.Marshal(res.Manifest)
			if !bytes.Equal(wantMan, gotMan) {
				t.Fatalf("manifest differs:\n%s\n%s", wantMan, gotMan)
			}
			if res.Config.Services[0].Env[0].Value != "cok-gizli-veritabani-parolasi" {
				t.Fatal("the configuration lost a secret value; the application could not be recreated")
			}
			if encrypted {
				raw, _ := os.ReadFile(path)
				if bytes.Contains(raw, []byte("cok-gizli")) || bytes.Contains(raw, []byte("manifest.json")) {
					t.Fatal("the encrypted archive shows plaintext")
				}
			}
		})
	}
}

func TestNewArchiveWriterDoesNotOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "var.tar.gz")
	if err := os.WriteFile(path, []byte("mevcut yedek"), 0o600); err != nil {
		t.Fatal(err)
	}
	if aw, err := newArchiveWriter(path, nil, time.Now()); err == nil {
		aw.abort()
		t.Fatal("an existing file was opened for writing")
	}
	if b, _ := os.ReadFile(path); string(b) != "mevcut yedek" {
		t.Fatal("the existing file was changed")
	}
}

func TestValidArchiveIsAccepted(t *testing.T) {
	b := &blueprint{partLen: 1000}
	for _, km := range []*keyMaterial{nil, keyFor(t, testPass)} {
		got, err := scanBytes(t, b.pack(t, km), km, "")
		if err != nil {
			t.Fatalf("valid archive rejected: %v", err)
		}
		if got.res.Manifest.App.Slug != "fotolar" || len(got.streams) != 1 {
			t.Fatalf("unexpected result: %+v", got.res.Manifest)
		}
		if !bytes.Equal(got.streams["data/01-volume-myserver-fotolar-data.tar"], simpleVolume(t)) {
			t.Fatal("stream differs")
		}
		if got.res.Manifest.Entries[0].Parts < 2 {
			t.Fatalf("the test archive should have several parts, has %d", got.res.Manifest.Entries[0].Parts)
		}
	}
}

// damage describes one way of breaking a valid archive.
type damage struct {
	name string
	// apply receives the manifest and the entries without the manifest and
	// returns the entries to write (it adds the manifest itself).
	apply func(t *testing.T, man *Manifest, entries []rawEntry) []rawEntry
}

func withManifest(t *testing.T, man *Manifest, entries []rawEntry) []rawEntry {
	return append(entries, manifestEntry(t, man))
}

func partIndex(entries []rawEntry, n int) int {
	suffix := fmt.Sprintf(".part-%06d", n)
	for i, e := range entries {
		if strings.HasSuffix(e.name, suffix) {
			return i
		}
	}
	return -1
}

func TestDamagedArchivesAreRejected(t *testing.T) {
	without := func(entries []rawEntry, i int) []rawEntry {
		out := append([]rawEntry{}, entries[:i]...)
		return append(out, entries[i+1:]...)
	}
	cases := []damage{
		{"modified part", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			i := partIndex(e, 2)
			e[i].data = bytes.Clone(e[i].data)
			e[i].data[10] ^= 0x01
			return withManifest(t, man, e)
		}},
		{"modified first part", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			i := partIndex(e, 1)
			e[i].data = bytes.Clone(e[i].data)
			e[i].data[len(e[i].data)-1] ^= 0x80
			return withManifest(t, man, e)
		}},
		{"part one byte longer", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			i := partIndex(e, 3)
			e[i].data = append(bytes.Clone(e[i].data), 0)
			return withManifest(t, man, e)
		}},
		{"part emptied", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			e[partIndex(e, 2)].data = nil
			return withManifest(t, man, e)
		}},
		{"modified config", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			e[0].data = bytes.Replace(e[0].data, []byte(`"privileged": false`), []byte(`"privileged": true `), 1)
			return withManifest(t, man, e)
		}},
		{"missing middle part", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			return withManifest(t, man, without(e, partIndex(e, 2)))
		}},
		{"missing first part", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			return withManifest(t, man, without(e, partIndex(e, 1)))
		}},
		{"missing last part", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			return withManifest(t, man, without(e, partIndex(e, man.Entries[0].Parts)))
		}},
		{"all parts missing", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			return withManifest(t, man, e[:1])
		}},
		{"extra part at the end", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			base := man.Entries[0].Archive
			e = append(e, rawEntry{name: fmt.Sprintf("%s.part-%06d", base, man.Entries[0].Parts+1), data: []byte("fazladan")})
			return withManifest(t, man, e)
		}},
		{"extra empty part at the end", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			base := man.Entries[0].Archive
			e = append(e, rawEntry{name: fmt.Sprintf("%s.part-%06d", base, man.Entries[0].Parts+1)})
			return withManifest(t, man, e)
		}},
		{"part duplicated", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			i := partIndex(e, 2)
			out := append([]rawEntry{}, e[:i+1]...)
			out = append(out, e[i])
			return withManifest(t, man, append(out, e[i+1:]...))
		}},
		{"parts swapped", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			i, j := partIndex(e, 2), partIndex(e, 3)
			e[i], e[j] = e[j], e[i]
			return withManifest(t, man, e)
		}},
		{"parts reversed", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			out := []rawEntry{e[0]}
			for i := len(e) - 1; i >= 1; i-- {
				out = append(out, e[i])
			}
			return withManifest(t, man, out)
		}},
		{"part contents swapped, names in order", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			i, j := partIndex(e, 1), partIndex(e, 2)
			e[i].data, e[j].data = e[j].data, e[i].data
			return withManifest(t, man, e)
		}},
		{"second stream interleaved", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			// part 1, a foreign entry, then part 2 of the same stream.
			i := partIndex(e, 2)
			out := append([]rawEntry{}, e[:i]...)
			out = append(out, rawEntry{name: "data/09-volume-araya-giren.tar.part-000001", data: []byte("x")})
			return withManifest(t, man, append(out, e[i:]...))
		}},
		{"part count too high in manifest", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			man.Entries[0].Parts++
			return withManifest(t, man, e)
		}},
		{"part count too low in manifest", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			man.Entries[0].Parts--
			return withManifest(t, man, e)
		}},
		{"part count zero in manifest", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			man.Entries[0].Parts = 0
			return withManifest(t, man, e)
		}},
		{"size wrong in manifest", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			man.Entries[0].Size++
			return withManifest(t, man, e)
		}},
		{"checksum wrong in manifest", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			man.Entries[0].SHA256 = sha([]byte("baska"))
			return withManifest(t, man, e)
		}},
		{"checksum empty in manifest", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			man.Entries[0].SHA256 = ""
			return withManifest(t, man, e)
		}},
		{"checksum upper case", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			man.Entries[0].SHA256 = strings.ToUpper(man.Entries[0].SHA256) + " "
			return withManifest(t, man, e)
		}},
		{"config checksum wrong in manifest", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			man.Files[0].SHA256 = sha([]byte("baska"))
			return withManifest(t, man, e)
		}},
		{"manifest missing", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry { return e }},
		{"manifest duplicated", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			return withManifest(t, man, withManifest(t, man, e))
		}},
		{"manifest first", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			return append([]rawEntry{manifestEntry(t, man)}, e...)
		}},
		{"manifest in the middle", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			out := append([]rawEntry{e[0], manifestEntry(t, man)}, e[1:]...)
			return out
		}},
		{"manifest followed by a second, different manifest", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			e = withManifest(t, man, e)
			other := *man
			other.App.Slug = "baska"
			return append(e, manifestEntry(t, &other))
		}},
		{"data after the manifest", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			e = withManifest(t, man, e)
			return append(e, rawEntry{name: "data/02-volume-sonradan.tar.part-000001", data: []byte("x")})
		}},
		{"manifest is not json", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			return append(e, rawEntry{name: nameManifest, data: []byte("manifest değil")})
		}},
		{"manifest empty", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			return append(e, rawEntry{name: nameManifest})
		}},
		{"manifest is a directory entry", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			return append(e, rawEntry{name: nameManifest, typ: tar.TypeDir})
		}},
		{"unknown format version 2", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			man.FormatVersion = 2
			return withManifest(t, man, e)
		}},
		{"format version 0", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			man.FormatVersion = 0
			return withManifest(t, man, e)
		}},
		{"format version negative", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			man.FormatVersion = -1
			return withManifest(t, man, e)
		}},
		{"foreign format name", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			man.Format = "casaos-backup"
			return withManifest(t, man, e)
		}},
		{"config missing", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			return withManifest(t, man, e[1:])
		}},
		{"config missing and not listed", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			man.Files = nil
			return withManifest(t, man, e[1:])
		}},
		{"config not listed", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			man.Files = []FileEntry{}
			return withManifest(t, man, e)
		}},
		{"config duplicated", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			return withManifest(t, man, append([]rawEntry{e[0]}, e...))
		}},
		{"config listed twice", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			man.Files = append(man.Files, man.Files[0])
			return withManifest(t, man, e)
		}},
		{"config is not json", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			e[0].data = []byte("yapılandırma değil")
			man.Files[0] = FileEntry{Name: nameConfig, Size: int64(len(e[0].data)), SHA256: sha(e[0].data)}
			return withManifest(t, man, e)
		}},
		{"manifest lists itself", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			man.Files = append(man.Files, FileEntry{Name: nameManifest})
			return withManifest(t, man, e)
		}},
		{"unlisted app manifest", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			return withManifest(t, man, append(e, rawEntry{name: nameAppManifest, data: []byte("slug: x")}))
		}},
		{"unlisted data stream", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			return withManifest(t, man, append(e, rawEntry{name: "data/02-volume-gizli.tar.part-000001", data: simpleVolume(t)}))
		}},
		{"stream listed twice", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			man.Entries = append(man.Entries, man.Entries[0])
			return withManifest(t, man, e)
		}},
		{"stream present twice", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			return withManifest(t, man, append(e, e[1:]...))
		}},
		{"unknown kind in manifest", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			man.Entries[0].Kind = "system"
			return withManifest(t, man, e)
		}},
		{"archive name with path in manifest", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			man.Entries[0].Archive = "data/../../etc/01-volume-x.tar"
			return withManifest(t, man, e)
		}},
		{"unknown entry", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			return withManifest(t, man, append(e, rawEntry{name: "notlar.txt", data: []byte("x")}))
		}},
		{"entry with parent reference", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			return withManifest(t, man, append(e, rawEntry{name: "../../etc/cron.d/x", data: []byte("x")}))
		}},
		{"entry with absolute name", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			return withManifest(t, man, append(e, rawEntry{name: "/etc/passwd", data: []byte("x")}))
		}},
		{"data name with parent reference", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			return withManifest(t, man, append(e, rawEntry{name: "data/../01-volume-x.tar.part-000001", data: []byte("x")}))
		}},
		{"config as ./config.json", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			e[0].name = "./config.json"
			return withManifest(t, man, e)
		}},
		{"symlink entry", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			return withManifest(t, man, append(e, rawEntry{name: "data/02-volume-x.tar.part-000001", typ: tar.TypeSymlink, link: "/etc/shadow"}))
		}},
		{"hard link entry named config.json", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			e[0] = rawEntry{name: nameConfig, typ: tar.TypeLink, link: "/etc/shadow"}
			return withManifest(t, man, e)
		}},
		{"directory entry", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			return withManifest(t, man, append([]rawEntry{{name: "data/", typ: tar.TypeDir}}, e...))
		}},
		{"device entry", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			return withManifest(t, man, append(e, rawEntry{name: "data/02-volume-x.tar.part-000001", typ: tar.TypeBlock}))
		}},
		{"part larger than the limit", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			big := make([]byte, partSize+1)
			e = append(e, rawEntry{name: "data/02-volume-myserver-fotolar-buyuk.tar.part-000001", data: big})
			man.Entries = append(man.Entries, DataEntry{Archive: "data/02-volume-myserver-fotolar-buyuk.tar", Kind: kindVolume,
				Source: "myserver-fotolar-buyuk", Parts: 1, Size: int64(len(big)), SHA256: sha(big)})
			return withManifest(t, man, e)
		}},
		{"later part larger than the limit", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			first, big := []byte("a"), make([]byte, partSize+1)
			e = append(e, rawEntry{name: "data/02-volume-myserver-fotolar-buyuk.tar.part-000001", data: first},
				rawEntry{name: "data/02-volume-myserver-fotolar-buyuk.tar.part-000002", data: big})
			all := append(bytes.Clone(first), big...)
			man.Entries = append(man.Entries, DataEntry{Archive: "data/02-volume-myserver-fotolar-buyuk.tar", Kind: kindVolume,
				Source: "myserver-fotolar-buyuk", Parts: 2, Size: int64(len(all)), SHA256: sha(all)})
			return withManifest(t, man, e)
		}},
		{"oversized config", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			e[0].data = append(bytes.Clone(e[0].data), bytes.Repeat([]byte(" "), maxSmallFile)...)
			man.Files[0] = FileEntry{Name: nameConfig, Size: int64(len(e[0].data)), SHA256: sha(e[0].data)}
			return withManifest(t, man, e)
		}},
		{"slug differs between manifest and config", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			man.App.Slug = "baska-uygulama"
			return withManifest(t, man, e)
		}},
		{"invalid slug", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			cfg := testConfig("../etc")
			e[0].data, _ = json.Marshal(cfg)
			man.Files[0] = FileEntry{Name: nameConfig, Size: int64(len(e[0].data)), SHA256: sha(e[0].data)}
			man.App.Slug = "../etc"
			return withManifest(t, man, e)
		}},
		{"volume of another application", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			cfg := testConfig("fotolar", apps.Mount{Type: apps.VolumeNamed, Source: "myserver-parolalar-data", Target: "/data"})
			e[0].data, _ = json.Marshal(cfg)
			man.Files[0] = FileEntry{Name: nameConfig, Size: int64(len(e[0].data)), SHA256: sha(e[0].data)}
			man.Entries[0].Source = "myserver-parolalar-data"
			return withManifest(t, man, e)
		}},
		{"data location not in the configuration", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			man.Entries[0].Source = "myserver-fotolar-baska"
			return withManifest(t, man, e)
		}},
		{"unknown consistency", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			man.Consistency = ""
			return withManifest(t, man, e)
		}},
		{"invalid image name", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			cfg := testConfig("fotolar")
			cfg.Services[0].Image = "--privileged alpine"
			e[0].data, _ = json.Marshal(cfg)
			man.Files[0] = FileEntry{Name: nameConfig, Size: int64(len(e[0].data)), SHA256: sha(e[0].data)}
			return withManifest(t, man, e)
		}},
		{"no services", func(t *testing.T, man *Manifest, e []rawEntry) []rawEntry {
			cfg := testConfig("fotolar")
			cfg.Services = nil
			e[0].data, _ = json.Marshal(cfg)
			man.Files[0] = FileEntry{Name: nameConfig, Size: int64(len(e[0].data)), SHA256: sha(e[0].data)}
			man.Entries = nil
			return withManifest(t, man, e[:1])
		}},
	}
	for _, c := range cases {
		for _, encrypted := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/encrypted=%v", c.name, encrypted), func(t *testing.T) {
				var km *keyMaterial
				if encrypted {
					km = keyFor(t, testPass)
				}
				b := &blueprint{partLen: 1000}
				man, entries := b.build(t)
				if man.Entries[0].Parts < 3 {
					t.Fatalf("test archive needs at least 3 parts, has %d", man.Entries[0].Parts)
				}
				wantRejected(t, c.name, packEntries(t, km, c.apply(t, man, entries)), km)
			})
		}
	}
}

func TestBindLocationsOfABackupMustBeAllowed(t *testing.T) {
	for _, dir := range []string{"/etc", "/etc/cron.d", "/", "/root/.ssh", "/var/lib/docker/volumes", "/var/lib/myserver",
		"/data/../etc", "data", "/opt/izinsiz", "/var/run/docker.sock", "/data/x/docker.sock", "/usr/local/bin", ""} {
		cfg := testConfig("fotolar", apps.Mount{Type: apps.VolumeBind, Key: "medya", Source: dir, Target: "/medya"})
		b := &blueprint{cfg: cfg, streams: []streamSpec{{kind: kindBind, source: dir, tar: simpleVolume(t)}}}
		if dir == "" {
			// collectSources leaves an empty source out, so the entry
			// refers to a location the configuration does not have.
			b.streams[0].source = "/"
		}
		wantRejected(t, "bind "+dir, b.pack(t, nil), nil)
	}
	// A system mount is not data: an entry that claims to be its contents
	// is refused.
	cfg := testConfig("fotolar", apps.Mount{Type: apps.VolumeSystem, Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"})
	b := &blueprint{cfg: cfg, streams: []streamSpec{{kind: kindBind, source: "/var/run/docker.sock", tar: simpleVolume(t)}}}
	wantRejected(t, "system mount as data", b.pack(t, nil), nil)

	ok := testConfig("fotolar", apps.Mount{Type: apps.VolumeBind, Key: "medya", Source: "/data/medya", Target: "/medya"})
	if _, err := scanBytes(t, (&blueprint{cfg: ok, streams: []streamSpec{{kind: kindBind, source: "/data/medya", tar: simpleVolume(t)}}}).pack(t, nil), nil, ""); err != nil {
		t.Fatalf("a folder inside the allowed roots was refused: %v", err)
	}
}

func TestFilesThatAreNotBackups(t *testing.T) {
	gz := func(b []byte) []byte {
		var out bytes.Buffer
		zw := gzip.NewWriter(&out)
		zw.Write(b)
		zw.Close()
		return out.Bytes()
	}
	valid := (&blueprint{}).pack(t, nil)
	cases := map[string][]byte{
		"empty file":              {},
		"text":                    []byte("bu bir yedek değil"),
		"gzip magic only":         {0x1f, 0x8b},
		"gzip of text":            gz([]byte("bu bir yedek değil")),
		"gzip of nothing":         gz(nil),
		"gzip of an empty tar":    gz(make([]byte, 1024)),
		"gzip of a foreign tar":   gz(tarStream(t, tarFile{name: "etc/passwd", body: "root:x:0:0"})),
		"zip":                     []byte("PK\x03\x04aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		"two archives in one":     append(bytes.Clone(valid), valid...),
		"archive plus garbage":    append(bytes.Clone(valid), []byte("çöp")...),
		"encryption magic only":   []byte(encMagic),
		"encryption magic + junk": append([]byte(encMagic), make([]byte, 500)...),
	}
	for name, data := range cases {
		wantRejected(t, name, data, nil)
	}
}

func TestTruncatedArchivesAreRejected(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		var km *keyMaterial
		if encrypted {
			km = keyFor(t, testPass)
		}
		// Incompressible content, so that the file is long enough to cut
		// in many places.
		body := string(randomBytes(t, 300_000))
		b := &blueprint{partLen: 100_000, streams: []streamSpec{{kind: kindVolume, source: "myserver-fotolar-data",
			tar: tarStream(t, tarFile{name: "./", typ: tar.TypeDir}, tarFile{name: "a.bin", body: body})}}}
		data := b.pack(t, km)
		if _, err := scanBytes(t, data, km, ""); err != nil {
			t.Fatalf("valid archive: %v", err)
		}
		cuts := []int{0, 1, 2, 9, 10, 100, encHeaderLen, encHeaderLen + 1, 1000, len(data) / 3, len(data) / 2,
			len(data) - 100_000, len(data) - 4096, len(data) - 512, len(data) - 17, len(data) - 16, len(data) - 9,
			len(data) - 8, len(data) - 4, len(data) - 1}
		for _, cut := range cuts {
			wantRejected(t, fmt.Sprintf("encrypted=%v cut at %d of %d", encrypted, cut, len(data)), data[:cut], km)
		}
	}
}

// Whatever single byte of an archive changes, the result is a refusal or
// exactly the original content; never different content that passes.
func TestSingleByteCorruptionNeverYieldsDifferentContent(t *testing.T) {
	body := string(randomBytes(t, 20_000))
	b := &blueprint{partLen: 8000, streams: []streamSpec{{kind: kindVolume, source: "myserver-fotolar-data",
		tar: tarStream(t, tarFile{name: "./", typ: tar.TypeDir}, tarFile{name: "a.bin", body: body})}}}
	for _, encrypted := range []bool{false, true} {
		var km *keyMaterial
		if encrypted {
			km = keyFor(t, testPass)
		}
		data := b.pack(t, km)
		orig, err := scanBytes(t, data, km, "")
		if err != nil {
			t.Fatal(err)
		}
		origMan, _ := json.Marshal(orig.res.Manifest)
		origCfg, _ := json.Marshal(orig.res.Config)
		accepted := 0
		step := 37
		for off := 0; off < len(data); off += step {
			mod := bytes.Clone(data)
			mod[off] ^= 0x20
			got, err := scanBytes(t, mod, km, "")
			if err != nil {
				continue
			}
			accepted++
			if encrypted {
				t.Fatalf("encrypted archive accepted with byte %d changed", off)
			}
			man, _ := json.Marshal(got.res.Manifest)
			cfg, _ := json.Marshal(got.res.Config)
			if !bytes.Equal(man, origMan) || !bytes.Equal(cfg, origCfg) ||
				!bytes.Equal(got.streams[orig.res.Manifest.Entries[0].Archive], orig.streams[orig.res.Manifest.Entries[0].Archive]) {
				t.Fatalf("byte %d changed: the archive was accepted with different content", off)
			}
		}
		// Only the unauthenticated fields of the gzip header (time, flags
		// of the operating system) may change without effect.
		if accepted > 10/step+1 {
			t.Fatalf("encrypted=%v: %d corrupted files were accepted", encrypted, accepted)
		}
	}
}

func TestEncryptedArchiveNeedsTheKey(t *testing.T) {
	km := keyFor(t, testPass)
	data := (&blueprint{}).pack(t, km)
	if _, err := scanBytes(t, data, nil, ""); !errors.Is(err, errPassphraseRequired) {
		t.Fatalf("no key: err = %v", err)
	}
	if _, err := scanBytes(t, data, nil, "yanlis-parola-12345"); !errors.Is(err, errWrongPassphrase) {
		t.Fatalf("wrong passphrase: err = %v", err)
	}
	if _, err := scanBytes(t, data, km, "yanlis-parola-12345"); !errors.Is(err, errWrongPassphrase) {
		t.Fatalf("wrong passphrase with stored key: err = %v", err)
	}
	if _, err := scanBytes(t, data, nil, testPass); err != nil {
		t.Fatalf("right passphrase: %v", err)
	}
}

func TestTooManyDataEntries(t *testing.T) {
	cfg := testConfig("fotolar")
	cfg.Services[0].Volumes = nil
	b := &blueprint{cfg: cfg}
	for i := 0; i < maxEntries+1; i++ {
		src := fmt.Sprintf("myserver-fotolar-v%d", i)
		cfg.Services[0].Volumes = append(cfg.Services[0].Volumes, apps.Mount{Type: apps.VolumeNamed, Source: src, Target: fmt.Sprintf("/v%d", i)})
		b.streams = append(b.streams, streamSpec{kind: kindVolume, source: src, tar: simpleVolume(t)})
	}
	wantRejected(t, "65 data entries", b.pack(t, nil), nil)
}

func TestScanHonoursCancellation(t *testing.T) {
	data := (&blueprint{}).pack(t, nil)
	ar, err := openArchive(writeTemp(t, data), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer ar.close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := scanArchive(ctx, ar, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

/* ---------- memory ---------- */

type heapWatch struct {
	base uint64
	peak uint64
	n    int64
	next int64
}

func newHeapWatch() *heapWatch {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return &heapWatch{base: m.HeapAlloc, peak: m.HeapAlloc}
}

// add samples the heap every 4 MiB of processed data.
func (h *heapWatch) add(n int) {
	h.n += int64(n)
	if h.n < h.next {
		return
	}
	h.next = h.n + 4<<20
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	if m.HeapAlloc > h.peak {
		h.peak = m.HeapAlloc
	}
}

func (h *heapWatch) growth() uint64 { return h.peak - min(h.peak, h.base) }

type watchedReader struct {
	r io.Reader
	h *heapWatch
}

func (w *watchedReader) Read(b []byte) (int, error) {
	n, err := w.r.Read(b)
	w.h.add(n)
	return n, err
}

// A volume far larger than any buffer of the module is written and read
// back while the heap is watched: the data must flow through, not pile up.
func TestLargeDataIsStreamedWithBoundedMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("writes a 256 MiB archive")
	}
	const (
		fileSize = 256 << 20
		limit    = 48 << 20
	)
	km := keyFor(t, testPass)
	path := filepath.Join(t.TempDir(), "buyuk.tar.gz.enc")
	aw, err := newArchiveWriter(path, km, time.Unix(1_750_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig("fotolar")
	cfgJSON, _ := json.Marshal(cfg)
	fe, err := aw.addFile(nameConfig, cfgJSON)
	if err != nil {
		t.Fatal(err)
	}

	// The inner tar stream is generated on the fly and validated on its
	// way into the archive, exactly as a volume read from Docker is.
	pr, pw := io.Pipe()
	srcSum := sha256.New()
	go func() {
		tw := tar.NewWriter(pw)
		err := tw.WriteHeader(&tar.Header{Name: helperPrefix + "/", Typeflag: tar.TypeDir, Mode: 0o755})
		if err == nil {
			err = tw.WriteHeader(&tar.Header{Name: helperPrefix + "/video.mkv", Typeflag: tar.TypeReg, Mode: 0o644, Size: fileSize})
		}
		if err == nil {
			_, err = io.Copy(tw, io.TeeReader(newPattern(7, fileSize), srcSum))
		}
		if err == nil {
			err = tw.Close()
		}
		pw.CloseWithError(err)
	}()

	watch := newHeapWatch()
	base := dataArchiveName(1, kindVolume, "myserver-fotolar-data")
	part := aw.stream(base)
	tw := tar.NewWriter(part)
	st, err := copyTar(context.Background(), tw, &watchedReader{r: pr, h: watch}, helperPrefix, nil)
	if err != nil {
		t.Fatalf("copyTar: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	parts, size, sum, err := part.close()
	if err != nil {
		t.Fatal(err)
	}
	if st.Files != 1 || st.Bytes != fileSize || parts < fileSize/partSize {
		t.Fatalf("stats %+v, parts %d", st, parts)
	}
	man := &Manifest{Format: formatName, FormatVersion: formatVersion, CreatedAt: 1_750_000_000,
		App: ManifestApp{Slug: cfg.Slug, Name: cfg.Name}, Consistency: consistencyStopped, Trigger: triggerManual,
		Files: []FileEntry{fe}, Entries: []DataEntry{{Archive: base, Kind: kindVolume, Source: "myserver-fotolar-data",
			Parts: parts, Size: size, SHA256: sum, Files: st.Files, Items: st.Items, ContentBytes: st.Bytes}}}
	manJSON, _ := json.Marshal(man)
	if err := aw.writeEntry(nameManifest, manJSON); err != nil {
		t.Fatal(err)
	}
	if err := aw.finish(); err != nil {
		t.Fatal(err)
	}
	if g := watch.growth(); g > limit {
		t.Fatalf("writing a %d MiB volume grew the heap by %d MiB (limit %d MiB)", fileSize>>20, g>>20, limit>>20)
	}
	t.Logf("write: heap growth %d KiB for %d MiB of data", watch.growth()>>10, fileSize>>20)
	fi, _ := os.Stat(path)
	if fi.Size() < fileSize {
		t.Fatalf("archive has %d bytes; the random data cannot have been compressed below %d", fi.Size(), fileSize)
	}

	// Reading: verification and extraction of the file content.
	watch = newHeapWatch()
	ar, err := openArchive(path, km, "")
	if err != nil {
		t.Fatal(err)
	}
	defer ar.close()
	gotSum := sha256.New()
	res, err := scanArchive(context.Background(), ar, func(_ string, r io.Reader) error {
		_, err := readTar(context.Background(), &watchedReader{r: r, h: watch}, "", func(h *tar.Header, body io.Reader) error {
			if h.Typeflag != tar.TypeReg {
				return nil
			}
			if h.Name != "video.mkv" {
				return fmt.Errorf("unexpected entry %q", h.Name)
			}
			_, err := io.Copy(gotSum, body)
			return err
		})
		return err
	})
	if err != nil {
		t.Fatalf("scanArchive: %v", err)
	}
	if res.Manifest.Entries[0].Parts != parts {
		t.Fatal("manifest differs")
	}
	if !bytes.Equal(gotSum.Sum(nil), srcSum.Sum(nil)) {
		t.Fatal("the content read back differs from what was written")
	}
	if g := watch.growth(); g > limit {
		t.Fatalf("reading a %d MiB volume grew the heap by %d MiB (limit %d MiB)", fileSize>>20, g>>20, limit>>20)
	}
	t.Logf("read: heap growth %d KiB", watch.growth()>>10)
}
