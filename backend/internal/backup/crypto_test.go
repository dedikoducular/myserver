package backup

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	mrand "math/rand"
	"runtime"
	"testing"
	"time"
)

// fastKDF is the cheapest parameter set the format accepts. Tests derive
// their keys with it so that the suite does not spend minutes in Argon2; the
// production parameters are exercised once, in TestProductionKDFRoundTrip.
var fastKDF = kdfParams{Time: 1, MemoryKiB: 8 * 1024, Threads: 1}

const testPass = "dogru-parola-icin-uzun-bir-metin"

func randomBytes(t testing.TB, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return b
}

func keyFor(t testing.TB, pass string) *keyMaterial {
	t.Helper()
	salt := randomBytes(t, encSaltLen)
	return &keyMaterial{KDF: fastKDF, Salt: salt, KEK: deriveKEK(pass, salt, fastKDF)}
}

func encryptBytes(t testing.TB, km *keyMaterial, plain []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := newEncWriter(&out, km)
	if err != nil {
		t.Fatalf("newEncWriter: %v", err)
	}
	if _, err := w.Write(plain); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return out.Bytes()
}

// decryptBytes returns everything the reader released and the error that
// ended the stream (nil when it ended with io.EOF).
func decryptBytes(data []byte, stored *keyMaterial, pass string) ([]byte, error) {
	r, err := newEncReader(bytes.NewReader(data), stored, pass)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	buf := make([]byte, 7919) // deliberately not aligned with the chunk size
	for {
		n, err := r.Read(buf)
		out.Write(buf[:n])
		if errors.Is(err, io.EOF) {
			return out.Bytes(), nil
		}
		if err != nil {
			return out.Bytes(), err
		}
	}
}

// mustReject asserts that data cannot be decrypted: an error is reported,
// and whatever was released before the error is a true prefix of the
// original, so nothing forged ever reaches a consumer and the stream never
// ends with a clean EOF.
func mustReject(t *testing.T, what string, data []byte, km *keyMaterial, plain []byte) {
	t.Helper()
	got, err := decryptBytes(data, km, "")
	if err == nil {
		t.Fatalf("%s: decryption succeeded (%d bytes released)", what, len(got))
	}
	if !bytes.HasPrefix(plain, got) {
		t.Fatalf("%s: released %d bytes that are not a prefix of the original", what, len(got))
	}
	if len(got) == len(plain) && len(plain) > 0 {
		t.Fatalf("%s: the whole plaintext was released before the error %v", what, err)
	}
}

func TestEncryptionRoundTripSizes(t *testing.T) {
	km := keyFor(t, testPass)
	sizes := []int{0, 1, 15, 16, encChunkSize - 1, encChunkSize, encChunkSize + 1, 2 * encChunkSize, 2*encChunkSize + 1, 3*encChunkSize - 1}
	for _, n := range sizes {
		t.Run(fmt.Sprintf("%d", n), func(t *testing.T) {
			plain := randomBytes(t, n)
			enc := encryptBytes(t, km, plain)
			chunks := (n + encChunkSize - 1) / encChunkSize
			if chunks == 0 {
				chunks = 1
			}
			if want := encHeaderLen + n + chunks*gcmTagLen; len(enc) != want {
				t.Fatalf("encrypted size = %d, want %d", len(enc), want)
			}
			if n >= 32 && bytes.Contains(enc, plain[:32]) {
				t.Fatal("ciphertext contains plaintext")
			}
			got, err := decryptBytes(enc, km, "")
			if err != nil {
				t.Fatalf("decrypt with stored key: %v", err)
			}
			if !bytes.Equal(got, plain) {
				t.Fatal("stored key: plaintext differs")
			}
			got, err = decryptBytes(enc, nil, testPass)
			if err != nil {
				t.Fatalf("decrypt with passphrase: %v", err)
			}
			if !bytes.Equal(got, plain) {
				t.Fatal("passphrase: plaintext differs")
			}
		})
	}
}

// patternReader produces n deterministic pseudo-random bytes without holding
// them in memory.
type patternReader struct {
	rnd  *mrand.Rand
	left int64
}

func newPattern(seed, n int64) *patternReader {
	return &patternReader{rnd: mrand.New(mrand.NewSource(seed)), left: n}
}

func (p *patternReader) Read(b []byte) (int, error) {
	if p.left == 0 {
		return 0, io.EOF
	}
	if int64(len(b)) > p.left {
		b = b[:p.left]
	}
	p.rnd.Read(b)
	p.left -= int64(len(b))
	return len(b), nil
}

func TestEncryptionRoundTripManyChunksStreamed(t *testing.T) {
	const size = 5<<20 + 12345
	km := keyFor(t, testPass)
	pr, pw := io.Pipe()
	want := sha256.New()
	go func() {
		w, err := newEncWriter(pw, km)
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		// Odd write sizes so that chunk boundaries fall inside writes.
		_, err = io.CopyBuffer(struct{ io.Writer }{w}, io.TeeReader(newPattern(1, size), want), make([]byte, 100003))
		if err == nil {
			err = w.Close()
		}
		pw.CloseWithError(err)
	}()
	r, err := newEncReader(pr, km, "")
	if err != nil {
		t.Fatalf("newEncReader: %v", err)
	}
	got := sha256.New()
	n, err := io.CopyBuffer(got, struct{ io.Reader }{r}, make([]byte, 33331))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if n != size {
		t.Fatalf("read %d bytes, want %d", n, size)
	}
	if !bytes.Equal(got.Sum(nil), want.Sum(nil)) {
		t.Fatal("checksum differs")
	}
}

func TestProductionKDFRoundTrip(t *testing.T) {
	if !defaultProductionKDF.valid() {
		t.Fatalf("production KDF parameters are outside the accepted bounds: %+v", defaultProductionKDF)
	}
	if defaultProductionKDF.MemoryKiB < 19*1024 || defaultProductionKDF.Time < 2 {
		t.Fatalf("production KDF parameters are weaker than the OWASP minimum for Argon2id: %+v", defaultProductionKDF)
	}
	salt := randomBytes(t, encSaltLen)
	km := &keyMaterial{KDF: defaultProductionKDF, Salt: salt, KEK: deriveKEK(testPass, salt, defaultProductionKDF)}
	plain := []byte("aile fotoğrafları")
	enc := encryptBytes(t, km, plain)
	got, err := decryptBytes(enc, nil, testPass)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("round trip with production parameters: %v", err)
	}
	if _, err := decryptBytes(enc, nil, testPass+"x"); !errors.Is(err, errWrongPassphrase) {
		t.Fatalf("wrong passphrase: err = %v", err)
	}
}

func TestWrongPassphrase(t *testing.T) {
	km := keyFor(t, testPass)
	plain := randomBytes(t, encChunkSize+10)
	enc := encryptBytes(t, km, plain)
	for _, pass := range []string{"yanlis-parola-12345", testPass + " ", " " + testPass, testPass[:len(testPass)-1], "DOGRU-PAROLA-ICIN-UZUN-BIR-METIN"} {
		r, err := newEncReader(bytes.NewReader(enc), nil, pass)
		if !errors.Is(err, errWrongPassphrase) {
			t.Fatalf("passphrase %q: err = %v, want errWrongPassphrase", pass, err)
		}
		if r != nil {
			t.Fatalf("passphrase %q: a reader was returned", pass)
		}
	}
	if _, err := newEncReader(bytes.NewReader(enc), nil, ""); !errors.Is(err, errPassphraseRequired) {
		t.Fatalf("no key and no passphrase: err = %v, want errPassphraseRequired", err)
	}
	// A key derived from another passphrase (different salt) is not tried.
	other := keyFor(t, "baska-bir-parola-123")
	if _, err := newEncReader(bytes.NewReader(enc), other, ""); !errors.Is(err, errPassphraseRequired) {
		t.Fatalf("foreign stored key: err = %v, want errPassphraseRequired", err)
	}
	// A stored key with the right salt but the wrong key bytes.
	bad := &keyMaterial{KDF: km.KDF, Salt: km.Salt, KEK: randomBytes(t, encKeyLen)}
	if _, err := newEncReader(bytes.NewReader(enc), bad, ""); err == nil {
		t.Fatal("stored key with wrong key bytes was accepted")
	}
}

// The decision implemented for the stored-key case: a passphrase that was
// supplied is always checked, even when the server could open the archive
// with its stored key; an empty passphrase means "use the stored key".
func TestSuppliedPassphraseIsCheckedEvenWithStoredKey(t *testing.T) {
	km := keyFor(t, testPass)
	plain := randomBytes(t, 1000)
	enc := encryptBytes(t, km, plain)

	if got, err := decryptBytes(enc, km, ""); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("empty passphrase with stored key: %v", err)
	}
	if got, err := decryptBytes(enc, km, testPass); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("right passphrase with stored key: %v", err)
	}
	got, err := decryptBytes(enc, km, "yanlis-parola-12345")
	if !errors.Is(err, errWrongPassphrase) {
		t.Fatalf("wrong passphrase with stored key: err = %v, want errWrongPassphrase", err)
	}
	if len(got) != 0 {
		t.Fatalf("wrong passphrase released %d bytes", len(got))
	}
}

func TestHeaderBitFlipsAreDetected(t *testing.T) {
	km := keyFor(t, testPass)
	plain := randomBytes(t, 2*encChunkSize+100)
	enc := encryptBytes(t, km, plain)
	// Every single bit of the header, opened with the stored key.
	for off := 0; off < encHeaderLen; off++ {
		for bit := 0; bit < 8; bit++ {
			mod := bytes.Clone(enc)
			mod[off] ^= 1 << bit
			mustReject(t, fmt.Sprintf("header byte %d bit %d", off, bit), mod, km, plain)
		}
	}
}

func TestHeaderBitFlipsAreDetectedWithPassphrase(t *testing.T) {
	km := keyFor(t, testPass)
	plain := randomBytes(t, encChunkSize+100)
	enc := encryptBytes(t, km, plain)
	// One flip per header field; the KDF fields are flipped in a way that
	// keeps them inside the accepted bounds, so that the key is derived and
	// the authentication of the header is what refuses the file.
	flips := map[string][2]int{
		"magic":           {3, 0},
		"version":         {8, 1},
		"kdf id":          {9, 1},
		"kdf time":        {13, 1}, // 1 -> 3
		"kdf memory":      {16, 2}, // 8 MiB -> 9 MiB
		"kdf threads":     {18, 1}, // 1 -> 3
		"salt":            {25, 4},
		"chunk size":      {37, 4}, // 64 KiB -> 68 KiB
		"nonce prefix":    {41, 0},
		"wrap nonce":      {50, 7},
		"wrapped key":     {70, 3},
		"wrapped key tag": {100, 5},
	}
	for name, f := range flips {
		mod := bytes.Clone(enc)
		mod[f[0]] ^= 1 << f[1]
		got, err := decryptBytes(mod, nil, testPass)
		if err == nil {
			t.Fatalf("%s: decryption succeeded", name)
		}
		if len(got) != 0 {
			t.Fatalf("%s: %d bytes were released", name, len(got))
		}
	}
}

func TestCiphertextAndTagBitFlipsAreDetected(t *testing.T) {
	km := keyFor(t, testPass)
	plain := randomBytes(t, 2*encChunkSize+100)
	enc := encryptBytes(t, km, plain)
	full := encChunkSize + gcmTagLen
	offsets := map[string]int{
		"first byte of chunk 0": encHeaderLen,
		"middle of chunk 0":     encHeaderLen + 1000,
		"tag of chunk 0":        encHeaderLen + encChunkSize + 3,
		"last tag byte chunk 0": encHeaderLen + full - 1,
		"first byte of chunk 1": encHeaderLen + full,
		"tag of chunk 1":        encHeaderLen + full + encChunkSize,
		"body of final chunk":   encHeaderLen + 2*full + 50,
		"tag of final chunk":    len(enc) - gcmTagLen,
		"last byte of the file": len(enc) - 1,
	}
	for name, off := range offsets {
		for _, bit := range []int{0, 7} {
			mod := bytes.Clone(enc)
			mod[off] ^= 1 << bit
			mustReject(t, fmt.Sprintf("%s bit %d", name, bit), mod, km, plain)
		}
	}
}

func TestTruncationIsDetected(t *testing.T) {
	km := keyFor(t, testPass)
	full := encChunkSize + gcmTagLen

	t.Run("every length of small files", func(t *testing.T) {
		for _, n := range []int{0, 1, 40} {
			plain := randomBytes(t, n)
			enc := encryptBytes(t, km, plain)
			for cut := 0; cut < len(enc); cut++ {
				mustReject(t, fmt.Sprintf("plain %d, file cut to %d of %d", n, cut, len(enc)), enc[:cut], km, plain)
			}
		}
	})
	for _, n := range []int{encChunkSize, 2 * encChunkSize, 2*encChunkSize + 100, 3 * encChunkSize} {
		t.Run(fmt.Sprintf("plain %d", n), func(t *testing.T) {
			plain := randomBytes(t, n)
			enc := encryptBytes(t, km, plain)
			cuts := map[string]int{
				"header only":           encHeaderLen,
				"inside the header":     50,
				"one byte short":        len(enc) - 1,
				"tag of final dropped":  len(enc) - gcmTagLen,
				"inside the first":      encHeaderLen + 1000,
				"after the first chunk": encHeaderLen + full,
			}
			if n > encChunkSize {
				cuts["inside the second"] = encHeaderLen + full + 77
			}
			if n > 2*encChunkSize {
				cuts["after the second chunk (final dropped)"] = encHeaderLen + 2*full
			}
			if n == 2*encChunkSize {
				// The final chunk is a full one here: dropping it leaves a
				// file made of whole chunks only.
				cuts["final full chunk dropped"] = encHeaderLen + full
			}
			for name, cut := range cuts {
				if cut >= len(enc) {
					continue
				}
				mustReject(t, name, enc[:cut], km, plain)
			}
		})
	}
}

// chunksOf splits the body of an encrypted file made from plain bytes.
func chunksOf(enc []byte) [][]byte {
	body := enc[encHeaderLen:]
	var out [][]byte
	full := encChunkSize + gcmTagLen
	for len(body) > full {
		out = append(out, body[:full])
		body = body[full:]
	}
	return append(out, body)
}

func assemble(header []byte, chunks ...[]byte) []byte {
	out := bytes.Clone(header)
	for _, c := range chunks {
		out = append(out, c...)
	}
	return out
}

func TestChunkManipulationIsDetected(t *testing.T) {
	km := keyFor(t, testPass)
	plain := randomBytes(t, 3*encChunkSize+500)
	enc := encryptBytes(t, km, plain)
	hdr := enc[:encHeaderLen]
	c := chunksOf(enc)
	if len(c) != 4 {
		t.Fatalf("expected 4 chunks, have %d", len(c))
	}
	// A second file with the same passphrase and key material.
	otherPlain := randomBytes(t, 3*encChunkSize+500)
	other := chunksOf(encryptBytes(t, km, otherPlain))
	// The same plaintext encrypted again.
	again := chunksOf(encryptBytes(t, km, plain))

	cases := map[string][]byte{
		"first two chunks swapped":          assemble(hdr, c[1], c[0], c[2], c[3]),
		"middle chunks swapped":             assemble(hdr, c[0], c[2], c[1], c[3]),
		"final chunk moved to the front":    assemble(hdr, c[3], c[0], c[1], c[2]),
		"chunk duplicated":                  assemble(hdr, c[0], c[1], c[1], c[2], c[3]),
		"chunk replaced by its neighbour":   assemble(hdr, c[0], c[0], c[2], c[3]),
		"final chunk duplicated":            assemble(hdr, c[0], c[1], c[2], c[3], c[3]),
		"middle chunk dropped":              assemble(hdr, c[0], c[2], c[3]),
		"first chunk dropped":               assemble(hdr, c[1], c[2], c[3]),
		"chunk of another file spliced in":  assemble(hdr, c[0], other[1], c[2], c[3]),
		"final chunk of another file":       assemble(hdr, c[0], c[1], c[2], other[3]),
		"same plaintext, other file, chunk": assemble(hdr, c[0], again[1], c[2], c[3]),
		"trailing garbage, one byte":        append(bytes.Clone(enc), 0),
		"trailing garbage, a tag's worth":   append(bytes.Clone(enc), randomBytes(t, gcmTagLen)...),
		"trailing garbage, a full chunk":    append(bytes.Clone(enc), randomBytes(t, encChunkSize+gcmTagLen)...),
		"trailing garbage, much":            append(bytes.Clone(enc), randomBytes(t, 3*encChunkSize)...),
		"second file appended":              append(bytes.Clone(enc), enc...),
	}
	for name, data := range cases {
		mustReject(t, name, data, km, plain)
	}

	// The same manipulations on a file whose final chunk is a full one.
	plain2 := randomBytes(t, 2*encChunkSize)
	enc2 := encryptBytes(t, km, plain2)
	c2 := chunksOf(enc2)
	if len(c2) != 2 {
		t.Fatalf("expected 2 chunks, have %d", len(c2))
	}
	hdr2 := enc2[:encHeaderLen]
	for name, data := range map[string][]byte{
		"swapped":             assemble(hdr2, c2[1], c2[0]),
		"final only":          assemble(hdr2, c2[1]),
		"first only":          assemble(hdr2, c2[0]),
		"first duplicated":    assemble(hdr2, c2[0], c2[0], c2[1]),
		"trailing byte":       append(bytes.Clone(enc2), 1),
		"chunk under header2": assemble(hdr, c2[0], c2[1]),
	} {
		mustReject(t, "full final chunk: "+name, data, km, plain2)
	}
}

// A bad final chunk is reported as an error after the earlier chunks were
// released: the reader authenticates chunk by chunk. Consumers therefore
// must not act on a stream before it ended without error; the restore path
// verifies the whole archive first (see TestRestoreDoesNotTouchAnythingWhenVerificationFails).
func TestReaderReportsBadFinalChunkAfterEarlierChunks(t *testing.T) {
	km := keyFor(t, testPass)
	plain := randomBytes(t, 2*encChunkSize+100)
	enc := encryptBytes(t, km, plain)
	enc[len(enc)-1] ^= 1

	r, err := newEncReader(bytes.NewReader(enc), km, "")
	if err != nil {
		t.Fatalf("newEncReader: %v", err)
	}
	got, err := io.ReadAll(r)
	if !errors.Is(err, errEncCorrupt) {
		t.Fatalf("err = %v, want errEncCorrupt", err)
	}
	if len(got) != 2*encChunkSize || !bytes.Equal(got, plain[:len(got)]) {
		t.Fatalf("released %d bytes; want exactly the two authenticated chunks", len(got))
	}
	// The error is sticky: no later read may report EOF.
	for i := 0; i < 3; i++ {
		n, err := r.Read(make([]byte, 10))
		if n != 0 || !errors.Is(err, errEncCorrupt) {
			t.Fatalf("read after failure: n=%d err=%v", n, err)
		}
	}
}

// decryptIndependently follows the documented format without using the
// reader, so that the nonce of every chunk is checked to be the documented
// one: prefix || counter || last.
func decryptIndependently(t *testing.T, enc []byte, km *keyMaterial) ([]byte, [][]byte) {
	t.Helper()
	hdr := enc[:encHeaderLen]
	kek, err := newGCM(km.KEK)
	if err != nil {
		t.Fatal(err)
	}
	dek, err := kek.Open(nil, hdr[46:58], hdr[58:106], hdr[:46])
	if err != nil {
		t.Fatalf("unwrap: %v", err)
	}
	aead, err := newGCM(dek)
	if err != nil {
		t.Fatal(err)
	}
	aad := sha256.Sum256(hdr)
	var plain []byte
	var nonces [][]byte
	chunks := chunksOf(enc)
	for i, c := range chunks {
		nonce := make([]byte, 12)
		copy(nonce, hdr[39:46])
		binary.BigEndian.PutUint32(nonce[7:11], uint32(i))
		if i == len(chunks)-1 {
			nonce[11] = 1
		}
		p, err := aead.Open(nil, nonce, c, aad[:])
		if err != nil {
			t.Fatalf("chunk %d does not open with the documented nonce: %v", i, err)
		}
		plain = append(plain, p...)
		nonces = append(nonces, nonce)
	}
	return plain, nonces
}

func TestNoncesNeverRepeat(t *testing.T) {
	km := keyFor(t, testPass)
	plain := randomBytes(t, 20*encChunkSize+1)
	seen := map[string]bool{}
	wraps := map[string]bool{}
	for file := 0; file < 3; file++ {
		enc := encryptBytes(t, km, plain)
		got, nonces := decryptIndependently(t, enc, km)
		if !bytes.Equal(got, plain) {
			t.Fatal("independent decryption differs")
		}
		if len(nonces) != 21 {
			t.Fatalf("chunks = %d, want 21", len(nonces))
		}
		for _, n := range nonces {
			if seen[string(n)] {
				t.Fatalf("nonce %x used twice", n)
			}
			seen[string(n)] = true
		}
		wrap := string(enc[46:58])
		if wraps[wrap] {
			t.Fatal("the nonce of the wrapped key repeated under the same key-encryption key")
		}
		wraps[wrap] = true
	}
	// The nonce function itself: distinct for every counter and for the
	// final flag.
	set := map[string]bool{}
	prefix := []byte{1, 2, 3, 4, 5, 6, 7}
	for _, c := range []uint32{0, 1, 2, 255, 256, 65535, 65536, 1 << 24, 1<<32 - 2, 1<<32 - 1} {
		for _, last := range []bool{false, true} {
			n := chunkNonce(prefix, c, last)
			if len(n) != gcmNonceLen || set[string(n)] {
				t.Fatalf("nonce for counter %d last %v is not unique", c, last)
			}
			set[string(n)] = true
		}
	}
}

func TestSamePlaintextEncryptsDifferently(t *testing.T) {
	km := keyFor(t, testPass)
	plain := bytes.Repeat([]byte("ayni veri "), 20000)
	a := encryptBytes(t, km, plain)
	b := encryptBytes(t, km, plain)
	if bytes.Equal(a, b) {
		t.Fatal("two encryptions are identical")
	}
	if bytes.Equal(a[39:46], b[39:46]) {
		t.Fatal("nonce prefix repeated")
	}
	if bytes.Equal(a[58:106], b[58:106]) {
		t.Fatal("wrapped data key repeated: the data key is not random per file")
	}
	ca, cb := chunksOf(a), chunksOf(b)
	for i := range ca {
		if bytes.Equal(ca[i], cb[i]) {
			t.Fatalf("chunk %d is identical in both files", i)
		}
	}
	// No block of ciphertext repeats inside a file although the plaintext
	// is periodic.
	blocks := map[string]bool{}
	for off := encHeaderLen; off+16 <= len(a); off += 16 {
		k := string(a[off : off+16])
		if blocks[k] {
			t.Fatalf("ciphertext block at %d repeats", off)
		}
		blocks[k] = true
	}
}

func craftHeader(time, mem uint32, threads byte, chunk uint32) []byte {
	h := make([]byte, 0, encHeaderLen)
	h = append(h, encMagic...)
	h = append(h, encVersion, kdfArgon2id)
	h = binary.BigEndian.AppendUint32(h, time)
	h = binary.BigEndian.AppendUint32(h, mem)
	h = append(h, threads)
	h = append(h, make([]byte, encSaltLen)...)
	h = binary.BigEndian.AppendUint32(h, chunk)
	h = append(h, make([]byte, encHeaderLen-len(h))...)
	return h
}

func TestHostileKDFParametersAreRefusedFast(t *testing.T) {
	cases := map[string][]byte{
		"time max":         craftHeader(1<<32-1, 64*1024, 4, encChunkSize),
		"time 1000":        craftHeader(1000, 64*1024, 4, encChunkSize),
		"time 0":           craftHeader(0, 64*1024, 4, encChunkSize),
		"memory max":       craftHeader(3, 1<<32-1, 4, encChunkSize),
		"memory 64 GiB":    craftHeader(3, 64<<20, 4, encChunkSize),
		"memory 4 GiB":     craftHeader(3, 4<<20, 4, encChunkSize),
		"memory 2 GiB":     craftHeader(3, 2<<20, 4, encChunkSize),
		"memory 0":         craftHeader(3, 0, 4, encChunkSize),
		"threads 255":      craftHeader(3, 64*1024, 255, encChunkSize),
		"threads 0":        craftHeader(3, 64*1024, 0, encChunkSize),
		"everything max":   craftHeader(1<<32-1, 1<<32-1, 255, 1<<32-1),
		"chunk size max":   craftHeader(3, 64*1024, 4, 1<<32-1),
		"chunk size 1 GiB": craftHeader(3, 64*1024, 4, 1<<30),
		"chunk size 0":     craftHeader(3, 64*1024, 4, 0),
		"chunk size 1":     craftHeader(3, 64*1024, 4, 1),
		"unknown version":  append([]byte(encMagic+"\x02\x01"), craftHeader(3, 64*1024, 4, encChunkSize)[10:]...),
		"unknown kdf":      append([]byte(encMagic+"\x01\x02"), craftHeader(3, 64*1024, 4, encChunkSize)[10:]...),
		"short header":     craftHeader(3, 64*1024, 4, encChunkSize)[:60],
		"magic only":       []byte(encMagic),
		"empty":            {},
	}
	for name, hdr := range cases {
		t.Run(name, func(t *testing.T) {
			data := append(bytes.Clone(hdr), make([]byte, 4096)...)
			if len(hdr) < encHeaderLen {
				data = hdr
			}
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			start := time.Now()
			r, err := newEncReader(bytes.NewReader(data), nil, testPass)
			elapsed := time.Since(start)
			runtime.ReadMemStats(&after)
			if !errors.Is(err, errEncHeader) {
				t.Fatalf("err = %v, want errEncHeader", err)
			}
			if r != nil {
				t.Fatal("a reader was returned")
			}
			if elapsed > 500*time.Millisecond {
				t.Fatalf("refusal took %v", elapsed)
			}
			if grown := after.TotalAlloc - before.TotalAlloc; grown > 1<<20 {
				t.Fatalf("refusal allocated %d bytes", grown)
			}
		})
	}
}

// The most expensive parameters a file may carry are still derived when a
// passphrase is supplied, so the upper bounds themselves must be affordable
// on a small server.
func TestAcceptedKDFParametersAreAffordable(t *testing.T) {
	maxOf := func(set func(*kdfParams, uint32)) uint32 {
		var best uint32
		for shift := 0; shift < 32; shift++ {
			for _, v := range []uint32{1 << shift, 1<<shift + 1<<shift/2} {
				p := defaultProductionKDF
				set(&p, v)
				if p.valid() && v > best {
					best = v
				}
			}
		}
		return best
	}
	mem := maxOf(func(p *kdfParams, v uint32) { p.MemoryKiB = v })
	passes := maxOf(func(p *kdfParams, v uint32) { p.Time = v })
	if mem > 256*1024 {
		t.Errorf("a header may demand %d MiB of memory for the key derivation; more than 256 MiB can exhaust a small server", mem/1024)
	}
	if passes > 8 {
		t.Errorf("a header may demand %d passes", passes)
	}
	if uint64(mem)*uint64(passes) > 8*256*1024 {
		t.Errorf("worst case cost %d KiB-passes", uint64(mem)*uint64(passes))
	}
}

func TestEncWriterRefusesBadKeyMaterial(t *testing.T) {
	good := keyFor(t, testPass)
	cases := map[string]*keyMaterial{
		"nil":          nil,
		"short key":    {KDF: good.KDF, Salt: good.Salt, KEK: good.KEK[:16]},
		"empty key":    {KDF: good.KDF, Salt: good.Salt},
		"short salt":   {KDF: good.KDF, Salt: good.Salt[:8], KEK: good.KEK},
		"invalid kdf":  {KDF: kdfParams{}, Salt: good.Salt, KEK: good.KEK},
		"hostile cost": {KDF: kdfParams{Time: 1 << 30, MemoryKiB: 1 << 30, Threads: 4}, Salt: good.Salt, KEK: good.KEK},
	}
	for name, km := range cases {
		var out bytes.Buffer
		w, err := newEncWriter(&out, km)
		if err == nil || w != nil {
			t.Errorf("%s: accepted", name)
		}
		if out.Len() != 0 {
			t.Errorf("%s: %d bytes were written", name, out.Len())
		}
	}
}

func TestEncWriterAfterClose(t *testing.T) {
	var out bytes.Buffer
	w, err := newEncWriter(&out, keyFor(t, testPass))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	size := out.Len()
	if err := w.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := w.Write([]byte("x")); err == nil {
		t.Fatal("write after close succeeded")
	}
	if out.Len() != size {
		t.Fatal("bytes were written after close")
	}
}
