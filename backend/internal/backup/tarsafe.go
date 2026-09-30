package backup

// Validation of tar streams. Every tar stream the module handles, whether
// it comes from Docker or from a backup file (which may have been uploaded
// or tampered with), passes through readTar: entry names are normalized to
// paths relative to the volume or folder root, and anything absolute,
// containing "..", of an unknown type or reaching through a symbolic link
// is refused.

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// unsafeEntryError reports a tar entry that must not be extracted.
type unsafeEntryError struct{ Name, Reason string }

func (e *unsafeEntryError) Error() string {
	return fmt.Sprintf("backup: unsafe tar entry %q: %s", e.Name, e.Reason)
}

var errCorruptTar = errors.New("backup: tar stream is corrupt")

const maxEntryPath = 4096

// cleanEntryName normalizes a tar entry name to a slash-separated path
// relative to the root, without "." components. The root itself is "".
func cleanEntryName(name string) (string, error) {
	if name == "" {
		return "", &unsafeEntryError{name, "empty name"}
	}
	if len(name) > maxEntryPath {
		return "", &unsafeEntryError{name[:64], "name too long"}
	}
	if strings.ContainsRune(name, 0) {
		return "", &unsafeEntryError{name, "NUL in name"}
	}
	if strings.HasPrefix(name, "/") {
		return "", &unsafeEntryError{name, "absolute path"}
	}
	parts := strings.Split(name, "/")
	out := parts[:0]
	for _, p := range parts {
		switch p {
		case "", ".":
		case "..":
			return "", &unsafeEntryError{name, "parent directory reference"}
		default:
			out = append(out, p)
		}
	}
	return strings.Join(out, "/"), nil
}

// stripPrefix removes the leading directory that Docker puts in front of
// every entry when a directory is copied out of a container.
func stripPrefix(name, prefix, original string) (string, error) {
	if name == prefix {
		return "", nil
	}
	if strings.HasPrefix(name, prefix+"/") {
		return name[len(prefix)+1:], nil
	}
	return "", &unsafeEntryError{original, "outside the expected directory"}
}

func underSymlink(name string, symlinks map[string]struct{}) bool {
	if len(symlinks) == 0 {
		return false
	}
	for i := 0; i < len(name); i++ {
		if name[i] == '/' {
			if _, ok := symlinks[name[:i]]; ok {
				return true
			}
		}
	}
	return false
}

// Keys that the tar writer regenerates from the header fields, and sparse
// file bookkeeping that no longer applies once the reader expanded the file.
func cleanPAX(records map[string]string) map[string]string {
	if len(records) == 0 {
		return nil
	}
	out := make(map[string]string, len(records))
	for k, v := range records {
		switch k {
		case "path", "linkpath", "size", "uid", "gid", "uname", "gname", "mtime", "atime", "ctime":
			continue
		}
		if strings.HasPrefix(k, "GNU.sparse.") {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// tarStats counts what a tar stream contains.
type tarStats struct {
	Files int64 // regular files
	Items int64 // every entry
	Bytes int64 // content bytes of regular files
}

// entryFunc receives one validated entry. hdr.Name is relative to the root
// ("./" for the root directory, directories end in "/"); hdr.Linkname of a
// hard link is normalized the same way. body holds the file content.
type entryFunc func(hdr *tar.Header, body io.Reader) error

// readTar reads a tar stream and calls fn for every entry after validating
// it. With a non-empty prefix every entry must be that directory or lie
// inside it, and the prefix is removed. fn may be nil to validate only.
func readTar(ctx context.Context, src io.Reader, prefix string, fn entryFunc) (tarStats, error) {
	var st tarStats
	tr := tar.NewReader(src)
	symlinks := map[string]struct{}{}
	for {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return st, nil
		}
		if err != nil {
			var ae *archiveError
			if errors.Is(err, errEncCorrupt) || errors.As(err, &ae) || errors.Is(err, context.Canceled) {
				return st, err
			}
			return st, fmt.Errorf("%w: %v", errCorruptTar, err)
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name, err := cleanEntryName(hdr.Name)
		if err != nil {
			return st, err
		}
		if prefix != "" {
			if name, err = stripPrefix(name, prefix, hdr.Name); err != nil {
				return st, err
			}
		}
		h := *hdr
		switch h.Typeflag {
		case tar.TypeRegA: //nolint:staticcheck // old archives use it for regular files
			h.Typeflag = tar.TypeReg
		case tar.TypeReg, tar.TypeDir, tar.TypeSymlink, tar.TypeLink, tar.TypeFifo, tar.TypeChar, tar.TypeBlock:
		default:
			return st, &unsafeEntryError{hdr.Name, "unsupported entry type"}
		}
		if name == "" && h.Typeflag != tar.TypeDir {
			return st, &unsafeEntryError{hdr.Name, "root is not a directory"}
		}
		if underSymlink(name, symlinks) {
			return st, &unsafeEntryError{hdr.Name, "path passes through a symbolic link"}
		}
		switch h.Typeflag {
		case tar.TypeLink:
			ln, err := cleanEntryName(h.Linkname)
			if err == nil && prefix != "" {
				ln, err = stripPrefix(ln, prefix, h.Linkname)
			}
			if err != nil || ln == "" || ln == name || underSymlink(ln, symlinks) {
				return st, &unsafeEntryError{hdr.Name, "invalid hard link target"}
			}
			if _, isLink := symlinks[ln]; isLink {
				return st, &unsafeEntryError{hdr.Name, "hard link to a symbolic link"}
			}
			h.Linkname = ln
		case tar.TypeSymlink:
			if h.Linkname == "" || len(h.Linkname) > maxEntryPath || strings.ContainsRune(h.Linkname, 0) {
				return st, &unsafeEntryError{hdr.Name, "invalid symbolic link target"}
			}
			symlinks[name] = struct{}{}
		}
		if h.Typeflag != tar.TypeSymlink {
			delete(symlinks, name)
		}
		switch {
		case name == "":
			h.Name = "./"
		case h.Typeflag == tar.TypeDir:
			h.Name = name + "/"
		default:
			h.Name = name
		}
		if h.Typeflag != tar.TypeReg {
			h.Size = 0
		}
		if h.Size < 0 {
			return st, &unsafeEntryError{hdr.Name, "negative size"}
		}
		h.Format = tar.FormatPAX
		h.PAXRecords = cleanPAX(h.PAXRecords)
		h.Xattrs = nil //nolint:staticcheck // carried in PAXRecords

		st.Items++
		if h.Typeflag == tar.TypeReg {
			st.Files++
			st.Bytes += h.Size
		}
		if fn != nil {
			if err := fn(&h, tr); err != nil {
				return st, err
			}
		}
	}
}

// copyTar validates src and writes it, normalized, to dst.
func copyTar(ctx context.Context, dst *tar.Writer, src io.Reader, prefix string, progress func(int64)) (tarStats, error) {
	buf := make([]byte, 256*1024)
	return readTar(ctx, src, prefix, func(h *tar.Header, body io.Reader) error {
		if err := dst.WriteHeader(h); err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg || h.Size == 0 {
			return nil
		}
		return copyBody(ctx, dst, body, buf, progress)
	})
}

// copyBody copies one file body, reporting progress and honouring ctx.
func copyBody(ctx context.Context, dst io.Writer, src io.Reader, buf []byte, progress func(int64)) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, err := dst.Write(buf[:n]); err != nil {
				return err
			}
			if progress != nil {
				progress(int64(n))
			}
		}
		if errors.Is(rerr, io.EOF) {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}
