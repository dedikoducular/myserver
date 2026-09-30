package storagecheck

import (
	"strconv"
	"strings"
)

// Mount is one line of /proc/<pid>/mountinfo.
type Mount struct {
	MajMin     string
	Root       string
	MountPoint string
	Options    string
	FSType     string
	Source     string
}

// ReadOnly reports whether the mount is read-only.
func (m Mount) ReadOnly() bool { return HasOption(m.Options, "ro") }

// HasOption reports whether a comma-separated option list contains opt.
func HasOption(options, opt string) bool {
	for _, o := range strings.Split(options, ",") {
		if o == opt {
			return true
		}
	}
	return false
}

// ParseMountInfo parses the content of a mountinfo file.
func ParseMountInfo(data []byte) []Mount {
	var out []Mount
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 10 {
			continue
		}
		sep := -1
		for i := 6; i < len(f); i++ {
			if f[i] == "-" {
				sep = i
				break
			}
		}
		if sep < 0 || sep+2 >= len(f) {
			continue
		}
		out = append(out, Mount{
			MajMin:     f[2],
			Root:       unescape(f[3]),
			MountPoint: unescape(f[4]),
			Options:    f[5],
			FSType:     f[sep+1],
			Source:     unescape(f[sep+2]),
		})
	}
	return out
}

// unescape decodes the octal escapes (\040 for space) used in mountinfo,
// fstab and /proc/swaps.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// ParseSwaps returns the paths of active swap areas from /proc/swaps.
func ParseSwaps(data []byte) []string {
	var out []string
	for i, line := range strings.Split(string(data), "\n") {
		if i == 0 {
			continue // header
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		// The marker is removed before decoding: afterwards the escaped
		// space is a real one and the suffix would no longer match.
		out = append(out, unescape(strings.TrimSuffix(f[0], "\\040(deleted)")))
	}
	return out
}

// MergeMounts concatenates mount tables, dropping exact duplicates.
func MergeMounts(tables ...[]Mount) []Mount {
	seen := map[Mount]bool{}
	var out []Mount
	for _, t := range tables {
		for _, m := range t {
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	return out
}

// ContainingMount returns the mount with the longest mount point that
// contains path.
func ContainingMount(mounts []Mount, path string) (Mount, bool) {
	best := -1
	for i, m := range mounts {
		mp := m.MountPoint
		if mp == path || mp == "/" || strings.HasPrefix(path, mp+"/") {
			// Later entries shadow earlier ones at the same mount point.
			if best < 0 || len(mp) >= len(mounts[best].MountPoint) {
				best = i
			}
		}
	}
	if best < 0 {
		return Mount{}, false
	}
	return mounts[best], true
}
