package backup

// Storage of the encryption key.
//
// The passphrase itself is never stored. When the administrator sets a
// passphrase, a key-encryption key (KEK) is derived from it with Argon2id
// and a random salt, and the KEK, the salt and the parameters are written to
// <data dir>/backup-key.json (mode 0600, owned by the panel user). The KEK
// is what scheduled backups need in order to encrypt without asking.
//
// Consequence: whoever can read that file (root, or the panel user) can
// decrypt every backup made with that passphrase, but does not learn the
// passphrase. Encryption therefore protects archives that leave the server
// (downloads, copies on other disks, off-site storage), not archives read by
// somebody who already controls the server.

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type keyFile struct {
	Version int       `json:"version"`
	KDF     kdfParams `json:"kdf"`
	Salt    string    `json:"salt"`
	KEK     string    `json:"kek"`
	SetAt   int64     `json:"set_at"`
}

type keyring struct {
	path string

	mu     sync.Mutex
	loaded bool
	km     *keyMaterial
	setAt  int64
}

func newKeyring(dataDir string) *keyring {
	return &keyring{path: filepath.Join(dataDir, "backup-key.json")}
}

func (k *keyring) load() error {
	if k.loaded {
		return nil
	}
	b, err := os.ReadFile(k.path)
	if errors.Is(err, fs.ErrNotExist) {
		k.loaded, k.km, k.setAt = true, nil, 0
		return nil
	}
	if err != nil {
		return err
	}
	var f keyFile
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	salt, err1 := base64.StdEncoding.DecodeString(f.Salt)
	kek, err2 := base64.StdEncoding.DecodeString(f.KEK)
	if err1 != nil || err2 != nil || f.Version != 1 || len(salt) != encSaltLen || len(kek) != encKeyLen || !f.KDF.valid() {
		return errors.New("backup: key file is invalid")
	}
	k.loaded, k.km, k.setAt = true, &keyMaterial{KDF: f.KDF, Salt: salt, KEK: kek}, f.SetAt
	return nil
}

// current returns the stored key, or nil when encryption is not configured.
func (k *keyring) current() (*keyMaterial, int64, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := k.load(); err != nil {
		return nil, 0, err
	}
	return k.km, k.setAt, nil
}

// set derives a new key from the passphrase and stores it.
func (k *keyring) set(passphrase string) error {
	salt := make([]byte, encSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	km := &keyMaterial{KDF: defaultKDF, Salt: salt, KEK: deriveKEK(passphrase, salt, defaultKDF)}
	now := time.Now().Unix()
	b, err := json.Marshal(keyFile{
		Version: 1, KDF: km.KDF,
		Salt:  base64.StdEncoding.EncodeToString(km.Salt),
		KEK:   base64.StdEncoding.EncodeToString(km.KEK),
		SetAt: now,
	})
	if err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	tmp := k.path + ".tmp"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, k.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	k.loaded, k.km, k.setAt = true, km, now
	return nil
}

// clear removes the stored key; later backups are not encrypted.
func (k *keyring) clear() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := os.Remove(k.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	k.loaded, k.km, k.setAt = true, nil, 0
	return nil
}
