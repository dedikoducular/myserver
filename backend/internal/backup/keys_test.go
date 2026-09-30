package backup

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readKeyFile(t *testing.T, k *keyring) (keyFile, []byte) {
	t.Helper()
	raw, err := os.ReadFile(k.path)
	if err != nil {
		t.Fatalf("read key file: %v", err)
	}
	var f keyFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("key file is not JSON: %v", err)
	}
	return f, raw
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestKeyFileIsPrivateAndHoldsNoPassphrase(t *testing.T) {
	dir := t.TempDir()
	k := newKeyring(dir)
	if km, at, err := k.current(); err != nil || km != nil || at != 0 {
		t.Fatalf("no key file: km=%v at=%d err=%v", km, at, err)
	}
	if err := k.set(testPass); err != nil {
		t.Fatalf("set: %v", err)
	}
	fi, err := os.Lstat(k.path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode = %v, want regular 0600", fi.Mode())
	}
	if got := listDir(t, dir); len(got) != 1 || got[0] != "backup-key.json" {
		t.Fatalf("data dir holds %v; a temporary file was left behind", got)
	}
	f, raw := readKeyFile(t, k)
	if bytes.Contains(raw, []byte(testPass)) {
		t.Fatal("the key file contains the passphrase")
	}
	if bytes.Contains(raw, []byte(base64.StdEncoding.EncodeToString([]byte(testPass)))) {
		t.Fatal("the key file contains the passphrase (base64)")
	}
	for _, field := range []string{"passphrase", "password", "parola"} {
		if strings.Contains(strings.ToLower(string(raw)), field) {
			t.Fatalf("the key file has a field named %q", field)
		}
	}
	salt, _ := base64.StdEncoding.DecodeString(f.Salt)
	kek, _ := base64.StdEncoding.DecodeString(f.KEK)
	if f.Version != 1 || len(salt) != encSaltLen || len(kek) != encKeyLen || f.SetAt == 0 {
		t.Fatalf("unexpected key file: %+v", f)
	}
	if !bytes.Equal(kek, deriveKEK(testPass, salt, f.KDF)) {
		t.Fatal("the stored key is not the key derived from the passphrase")
	}
}

func TestKeyFileUsesProductionParameters(t *testing.T) {
	old := defaultKDF
	defaultKDF = defaultProductionKDF
	defer func() { defaultKDF = old }()
	k := newKeyring(t.TempDir())
	if err := k.set(testPass); err != nil {
		t.Fatalf("set: %v", err)
	}
	f, _ := readKeyFile(t, k)
	want := kdfParams{Time: 3, MemoryKiB: 64 * 1024, Threads: 4}
	if f.KDF != want {
		t.Fatalf("key file parameters = %+v, want %+v", f.KDF, want)
	}
}

func TestKeySurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	k := newKeyring(dir)
	if err := k.set(testPass); err != nil {
		t.Fatal(err)
	}
	km, _, err := k.current()
	if err != nil || km == nil {
		t.Fatalf("current: %v", err)
	}
	plain := []byte("veri")
	enc := encryptBytes(t, km, plain)

	again, at, err := newKeyring(dir).current()
	if err != nil || again == nil || at == 0 {
		t.Fatalf("reload: km=%v err=%v", again, err)
	}
	if got, err := decryptBytes(enc, again, ""); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("decrypt with reloaded key: %v", err)
	}
}

func TestKeyFileIsReplacedAtomically(t *testing.T) {
	dir := t.TempDir()
	k := newKeyring(dir)
	if err := k.set(testPass); err != nil {
		t.Fatal(err)
	}
	_, before := readKeyFile(t, k)
	kmBefore, _, _ := k.current()

	// The temporary file cannot be created: the existing key must stay
	// exactly as it was, on disk and in memory.
	tmp := k.path + ".tmp"
	if err := os.MkdirAll(filepath.Join(tmp, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := k.set("yeni-parola-1234567"); err == nil {
		t.Fatal("set succeeded although the temporary file could not be written")
	}
	_, after := readKeyFile(t, k)
	if !bytes.Equal(before, after) {
		t.Fatal("the key file changed although set failed")
	}
	kmAfter, _, err := k.current()
	if err != nil || !bytes.Equal(kmAfter.KEK, kmBefore.KEK) {
		t.Fatalf("the key in memory changed although set failed (err %v)", err)
	}
	if err := os.RemoveAll(tmp); err != nil {
		t.Fatal(err)
	}

	// A stale temporary file of a crashed run does not get in the way, and
	// a key file with loose permissions is replaced by a private one.
	if err := os.WriteFile(tmp, []byte("yarım"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(k.path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := k.set("yeni-parola-1234567"); err != nil {
		t.Fatalf("set: %v", err)
	}
	fi, err := os.Lstat(k.path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode after replace = %v (err %v)", fi.Mode(), err)
	}
	if got := listDir(t, dir); len(got) != 1 {
		t.Fatalf("data dir holds %v", got)
	}
	f, _ := readKeyFile(t, k)
	salt, _ := base64.StdEncoding.DecodeString(f.Salt)
	kek, _ := base64.StdEncoding.DecodeString(f.KEK)
	if !bytes.Equal(kek, deriveKEK("yeni-parola-1234567", salt, f.KDF)) {
		t.Fatal("the key file does not hold the new key")
	}
}

func TestKeyFileIsNotWrittenThroughASymlink(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(outside, []byte("dokunma"), 0o644); err != nil {
		t.Fatal(err)
	}
	k := newKeyring(dir)
	if err := os.Symlink(outside, k.path+".tmp"); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := k.set(testPass); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got, _ := os.ReadFile(outside); string(got) != "dokunma" {
		t.Fatalf("a file outside the data directory was overwritten: %q", got)
	}
}

func TestCorruptOrUnreadableKeyFileIsAnError(t *testing.T) {
	good := func() keyFile {
		return keyFile{
			Version: 1, KDF: fastKDF,
			Salt: base64.StdEncoding.EncodeToString(make([]byte, encSaltLen)),
			KEK:  base64.StdEncoding.EncodeToString(make([]byte, encKeyLen)),
		}
	}
	mk := func(change func(*keyFile)) []byte {
		f := good()
		change(&f)
		b, _ := json.Marshal(f)
		return b
	}
	cases := map[string][]byte{
		"empty file":      {},
		"not json":        []byte("bu bir anahtar değil"),
		"truncated json":  []byte(`{"version":1,"kdf":{"time":1`),
		"json null":       []byte("null"),
		"json array":      []byte("[]"),
		"version 0":       mk(func(f *keyFile) { f.Version = 0 }),
		"version 2":       mk(func(f *keyFile) { f.Version = 2 }),
		"short key":       mk(func(f *keyFile) { f.KEK = base64.StdEncoding.EncodeToString(make([]byte, 16)) }),
		"empty key":       mk(func(f *keyFile) { f.KEK = "" }),
		"key not base64":  mk(func(f *keyFile) { f.KEK = "!!!" }),
		"short salt":      mk(func(f *keyFile) { f.Salt = base64.StdEncoding.EncodeToString(make([]byte, 4)) }),
		"salt not base64": mk(func(f *keyFile) { f.Salt = "***" }),
		"kdf zero":        mk(func(f *keyFile) { f.KDF = kdfParams{} }),
		"kdf hostile":     mk(func(f *keyFile) { f.KDF = kdfParams{Time: 1 << 31, MemoryKiB: 1 << 31, Threads: 200} }),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			k := newKeyring(t.TempDir())
			if err := os.WriteFile(k.path, content, 0o600); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ { // the failure must not be cached as "no key"
				km, _, err := k.current()
				if err == nil {
					t.Fatalf("attempt %d: no error; key = %v. A backup would be written without encryption.", i, km)
				}
				if km != nil {
					t.Fatalf("attempt %d: a key was returned together with the error", i)
				}
			}
			// Setting the passphrase again repairs it.
			if err := k.set(testPass); err != nil {
				t.Fatalf("set: %v", err)
			}
			if km, _, err := k.current(); err != nil || km == nil {
				t.Fatalf("after set: km=%v err=%v", km, err)
			}
		})
	}
	t.Run("unreadable", func(t *testing.T) {
		k := newKeyring(t.TempDir())
		// Permission bits do not stop root (the tests run as root in the
		// container), a directory in place of the file fails for everybody.
		if err := os.Mkdir(k.path, 0o700); err != nil {
			t.Fatal(err)
		}
		if km, _, err := k.current(); err == nil || km != nil {
			t.Fatalf("km=%v err=%v", km, err)
		}
	})
}

func TestChangingThePassphraseKeepsOldArchivesReadable(t *testing.T) {
	const oldPass, newPass = "eski-parola-1234567", "yeni-parola-7654321"
	k := newKeyring(t.TempDir())
	if err := k.set(oldPass); err != nil {
		t.Fatal(err)
	}
	oldKey, _, _ := k.current()
	plain := []byte("eski yedek")
	oldArchive := encryptBytes(t, oldKey, plain)

	if err := k.set(newPass); err != nil {
		t.Fatal(err)
	}
	newKey, _, _ := k.current()
	if bytes.Equal(newKey.KEK, oldKey.KEK) || bytes.Equal(newKey.Salt, oldKey.Salt) {
		t.Fatal("the key or the salt did not change")
	}
	newArchive := encryptBytes(t, newKey, plain)

	if got, err := decryptBytes(oldArchive, newKey, oldPass); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("old archive with the old passphrase: %v", err)
	}
	if _, err := decryptBytes(oldArchive, newKey, ""); !errors.Is(err, errPassphraseRequired) {
		t.Fatalf("old archive without passphrase: err = %v, want errPassphraseRequired", err)
	}
	if _, err := decryptBytes(oldArchive, newKey, newPass); !errors.Is(err, errWrongPassphrase) {
		t.Fatalf("old archive with the new passphrase: err = %v, want errWrongPassphrase", err)
	}
	if got, err := decryptBytes(newArchive, newKey, ""); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("new archive with the stored key: %v", err)
	}
	if _, err := decryptBytes(newArchive, newKey, oldPass); !errors.Is(err, errWrongPassphrase) {
		t.Fatalf("new archive with the old passphrase: err = %v, want errWrongPassphrase", err)
	}

	// Encryption switched off: both remain readable with their passphrase.
	if err := k.clear(); err != nil {
		t.Fatal(err)
	}
	if km, _, err := k.current(); km != nil || err != nil {
		t.Fatalf("after clear: km=%v err=%v", km, err)
	}
	if _, err := os.Lstat(k.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("key file still exists after clear: %v", err)
	}
	if err := k.clear(); err != nil {
		t.Fatalf("second clear: %v", err)
	}
	if got, err := decryptBytes(newArchive, nil, newPass); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("new archive after clear: %v", err)
	}
	if got, err := decryptBytes(oldArchive, nil, oldPass); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("old archive after clear: %v", err)
	}
}
