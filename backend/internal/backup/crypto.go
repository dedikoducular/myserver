package backup

// Streaming authenticated encryption of a backup archive.
//
// File layout (all integers big endian):
//
//	offset size
//	0      8    magic "MYSBKENC"
//	8      1    format version (1)
//	9      1    KDF id (1 = Argon2id)
//	10     4    Argon2id time (passes)
//	14     4    Argon2id memory (KiB)
//	18     1    Argon2id threads
//	19     16   KDF salt
//	35     4    plaintext chunk size
//	39     7    nonce prefix (random per file)
//	46     12   nonce of the wrapped data key
//	58     48   data key (32 bytes) sealed with AES-256-GCM under the key
//	            derived from the passphrase; AAD = header bytes 0..45
//	106    ...  chunks
//
// The archive is split into chunks of "chunk size" plaintext bytes; each is
// sealed with AES-256-GCM under the random data key. The nonce of a chunk is
// prefix(7) || counter(4) || last(1), where last is 1 only for the final
// chunk, and the AAD is the SHA-256 of the 106 header bytes. Every chunk but
// the last is full. This is the STREAM construction: a modified, reordered,
// duplicated or dropped chunk, a truncated file and a modified header all
// fail authentication.

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"runtime/debug"

	"golang.org/x/crypto/argon2"
)

const (
	encMagic     = "MYSBKENC"
	encVersion   = 1
	kdfArgon2id  = 1
	encChunkSize = 64 * 1024
	encSaltLen   = 16
	encKeyLen    = 32
	encHeaderLen = 106
	encWrapStart = 46
	gcmTagLen    = 16
	gcmNonceLen  = 12
)

var (
	errPassphraseRequired = errors.New("backup: passphrase required")
	errWrongPassphrase    = errors.New("backup: wrong passphrase")
	errEncCorrupt         = errors.New("backup: encrypted data is corrupt or truncated")
	errEncHeader          = errors.New("backup: invalid encryption header")
)

// kdfParams are the Argon2id parameters stored in every header.
type kdfParams struct {
	Time      uint32 `json:"time"`
	MemoryKiB uint32 `json:"memory_kib"`
	Threads   uint8  `json:"threads"`
}

var defaultKDF = kdfParams{Time: 3, MemoryKiB: 64 * 1024, Threads: 4}

// valid bounds the cost of a header read from an untrusted file. The key is
// derived with the parameters of the file whenever a passphrase is supplied,
// so the upper bounds are what a hostile file can make the panel spend:
// at most 256 MiB of memory and 8 passes (the panel itself writes 64 MiB and
// 3 passes).
func (p kdfParams) valid() bool {
	return p.Time >= 1 && p.Time <= 8 &&
		p.MemoryKiB >= 8*1024 && p.MemoryKiB <= 256*1024 &&
		p.Threads >= 1 && p.Threads <= 16
}

func deriveKEK(passphrase string, salt []byte, p kdfParams) []byte {
	key := argon2.IDKey([]byte(passphrase), salt, p.Time, p.MemoryKiB, p.Threads, encKeyLen)
	// Hand the KDF's working memory back to the OS; key derivation is rare.
	go debug.FreeOSMemory()
	return key
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// keyMaterial is a key-encryption key together with the parameters it was
// derived with.
type keyMaterial struct {
	KDF  kdfParams
	Salt []byte
	KEK  []byte
}

func chunkNonce(prefix []byte, counter uint32, last bool) []byte {
	n := make([]byte, gcmNonceLen)
	copy(n, prefix)
	binary.BigEndian.PutUint32(n[7:11], counter)
	if last {
		n[11] = 1
	}
	return n
}

type encWriter struct {
	w       io.Writer
	aead    cipher.AEAD
	prefix  []byte
	aad     []byte
	buf     []byte
	out     []byte
	n       int
	counter uint32
	closed  bool
}

// newEncWriter writes the header and returns a writer that encrypts what is
// written to it. Close must be called to write the final chunk.
func newEncWriter(w io.Writer, km *keyMaterial) (*encWriter, error) {
	if km == nil || len(km.KEK) != encKeyLen || len(km.Salt) != encSaltLen || !km.KDF.valid() {
		return nil, errors.New("backup: invalid key material")
	}
	random := make([]byte, encKeyLen+7+gcmNonceLen)
	if _, err := rand.Read(random); err != nil {
		return nil, err
	}
	dek, prefix, wrapNonce := random[:encKeyLen], random[encKeyLen:encKeyLen+7], random[encKeyLen+7:]

	hdr := make([]byte, 0, encHeaderLen)
	hdr = append(hdr, encMagic...)
	hdr = append(hdr, encVersion, kdfArgon2id)
	hdr = binary.BigEndian.AppendUint32(hdr, km.KDF.Time)
	hdr = binary.BigEndian.AppendUint32(hdr, km.KDF.MemoryKiB)
	hdr = append(hdr, km.KDF.Threads)
	hdr = append(hdr, km.Salt...)
	hdr = binary.BigEndian.AppendUint32(hdr, encChunkSize)
	hdr = append(hdr, prefix...)

	kek, err := newGCM(km.KEK)
	if err != nil {
		return nil, err
	}
	wrapped := kek.Seal(nil, wrapNonce, dek, hdr)
	hdr = append(hdr, wrapNonce...)
	hdr = append(hdr, wrapped...)
	if len(hdr) != encHeaderLen {
		return nil, errors.New("backup: header length mismatch")
	}
	aead, err := newGCM(dek)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(hdr); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(hdr)
	return &encWriter{
		w: w, aead: aead, prefix: append([]byte{}, prefix...), aad: sum[:],
		buf: make([]byte, encChunkSize), out: make([]byte, 0, encChunkSize+gcmTagLen),
	}, nil
}

func (e *encWriter) Write(p []byte) (int, error) {
	if e.closed {
		return 0, errors.New("backup: write after close")
	}
	total := 0
	for len(p) > 0 {
		// A full buffer is only sealed once more data arrives, so that
		// the final chunk can be marked as such.
		if e.n == len(e.buf) {
			if err := e.flush(false); err != nil {
				return total, err
			}
		}
		c := copy(e.buf[e.n:], p)
		e.n += c
		p = p[c:]
		total += c
	}
	return total, nil
}

func (e *encWriter) flush(last bool) error {
	ct := e.aead.Seal(e.out[:0], chunkNonce(e.prefix, e.counter, last), e.buf[:e.n], e.aad)
	if _, err := e.w.Write(ct); err != nil {
		return err
	}
	e.n = 0
	if !last {
		if e.counter == math.MaxUint32 {
			return errors.New("backup: archive too large for the encryption format")
		}
		e.counter++
	}
	return nil
}

// Close writes the final chunk. It does not close the underlying writer.
func (e *encWriter) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	return e.flush(true)
}

type encHeader struct {
	kdf       kdfParams
	salt      []byte
	chunk     int
	prefix    []byte
	wrapNonce []byte
	wrapped   []byte
	raw       []byte
}

func parseEncHeader(raw []byte) (*encHeader, error) {
	if len(raw) != encHeaderLen || string(raw[:8]) != encMagic {
		return nil, errEncHeader
	}
	if raw[8] != encVersion || raw[9] != kdfArgon2id {
		return nil, errEncHeader
	}
	h := &encHeader{
		kdf: kdfParams{
			Time:      binary.BigEndian.Uint32(raw[10:14]),
			MemoryKiB: binary.BigEndian.Uint32(raw[14:18]),
			Threads:   raw[18],
		},
		salt:      raw[19:35],
		prefix:    raw[39:46],
		wrapNonce: raw[46:58],
		wrapped:   raw[58:106],
		raw:       raw,
	}
	chunk := binary.BigEndian.Uint32(raw[35:39])
	if !h.kdf.valid() || chunk < 4096 || chunk > 4<<20 {
		return nil, errEncHeader
	}
	h.chunk = int(chunk)
	return h, nil
}

type encReader struct {
	r       *bufio.Reader
	aead    cipher.AEAD
	prefix  []byte
	aad     []byte
	buf     []byte
	plain   []byte
	off     int
	counter uint32
	done    bool
	err     error
}

// newEncReader reads the header from r and returns a reader of the
// plaintext.
//
// A passphrase that was supplied is always checked: the key is derived from
// it and the file must open with that key, otherwise the result is
// errWrongPassphrase, also when the stored key could have opened the file.
// An empty passphrase means "use the key stored on this server", which works
// when the file was made with it (same salt and parameters); otherwise the
// result is errPassphraseRequired.
func newEncReader(r io.Reader, stored *keyMaterial, passphrase string) (*encReader, error) {
	raw := make([]byte, encHeaderLen)
	if _, err := io.ReadFull(r, raw); err != nil {
		return nil, errEncHeader
	}
	h, err := parseEncHeader(raw)
	if err != nil {
		return nil, err
	}
	var dek []byte
	unwrap := func(kek []byte) bool {
		g, err := newGCM(kek)
		if err != nil {
			return false
		}
		out, err := g.Open(nil, h.wrapNonce, h.wrapped, raw[:encWrapStart])
		if err != nil || len(out) != encKeyLen {
			return false
		}
		dek = out
		return true
	}
	switch {
	case passphrase != "":
		if !unwrap(deriveKEK(passphrase, h.salt, h.kdf)) {
			return nil, errWrongPassphrase
		}
	case stored != nil && stored.KDF == h.kdf && bytes.Equal(stored.Salt, h.salt) && len(stored.KEK) == encKeyLen:
		if !unwrap(stored.KEK) {
			return nil, errPassphraseRequired
		}
	default:
		return nil, errPassphraseRequired
	}
	aead, err := newGCM(dek)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	return &encReader{
		r: bufio.NewReaderSize(r, h.chunk+gcmTagLen+1), aead: aead,
		prefix: append([]byte{}, h.prefix...), aad: sum[:],
		buf: make([]byte, h.chunk+gcmTagLen), plain: make([]byte, 0, h.chunk),
	}, nil
}

func (d *encReader) Read(p []byte) (int, error) {
	for d.off >= len(d.plain) {
		if d.err != nil {
			return 0, d.err
		}
		if d.done {
			return 0, io.EOF
		}
		if err := d.next(); err != nil {
			d.err = err
			return 0, err
		}
	}
	n := copy(p, d.plain[d.off:])
	d.off += n
	return n, nil
}

func (d *encReader) next() error {
	n, err := io.ReadFull(d.r, d.buf)
	last := false
	switch {
	case err == nil:
		if _, perr := d.r.Peek(1); perr == io.EOF {
			last = true
		} else if perr != nil {
			return perr
		}
	case errors.Is(err, io.ErrUnexpectedEOF):
		last = true
	case errors.Is(err, io.EOF):
		// The final chunk is always present, even when it is empty.
		return errEncCorrupt
	default:
		return err
	}
	if n < gcmTagLen {
		return errEncCorrupt
	}
	out, err := d.aead.Open(d.plain[:0], chunkNonce(d.prefix, d.counter, last), d.buf[:n], d.aad)
	if err != nil {
		return errEncCorrupt
	}
	d.plain, d.off = out, 0
	if last {
		d.done = true
		return nil
	}
	if d.counter == math.MaxUint32 {
		return errEncCorrupt
	}
	d.counter++
	return nil
}

// isEncrypted reports whether the first bytes of a file are the magic.
func isEncrypted(head []byte) bool {
	return len(head) >= len(encMagic) && string(head[:len(encMagic)]) == encMagic
}
