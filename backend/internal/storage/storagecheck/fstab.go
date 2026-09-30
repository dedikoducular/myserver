package storagecheck

import (
	"errors"
	"strings"
)

// ManagedOption marks the fstab entries written by the panel. Options that
// start with "x-" are comments to mount(8). Only entries that carry it are
// ever modified or removed.
const ManagedOption = "x-myserver"

// FstabPath is the file edited by the helper.
const FstabPath = "/etc/fstab"

// FstabEntry is one parsed fstab line.
type FstabEntry struct {
	Spec       string
	MountPoint string
	FSType     string
	Options    string
	Managed    bool
}

// UUID returns the UUID of a "UUID=..." spec, or "".
func (e FstabEntry) UUID() string {
	u, ok := strings.CutPrefix(e.Spec, "UUID=")
	if !ok {
		return ""
	}
	return strings.Trim(u, `"`)
}

func parseFstabLine(line string) (FstabEntry, bool) {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") {
		return FstabEntry{}, false
	}
	f := strings.Fields(t)
	if len(f) < 3 {
		return FstabEntry{}, false
	}
	e := FstabEntry{Spec: f[0], MountPoint: unescape(f[1]), FSType: f[2]}
	if len(f) > 3 {
		e.Options = f[3]
	}
	e.Managed = HasOption(e.Options, ManagedOption)
	return e, true
}

// ParseFstab returns the entries of an fstab file.
func ParseFstab(content []byte) []FstabEntry {
	var out []FstabEntry
	for _, line := range strings.Split(string(content), "\n") {
		if e, ok := parseFstabLine(line); ok {
			out = append(out, e)
		}
	}
	return out
}

// Errors returned by FstabAdd.
var (
	ErrFstabConflict = errors.New("fstab: entry exists")
	ErrFstabInvalid  = errors.New("fstab: invalid entry")
)

// FstabAdd appends a managed entry, mounted by UUID with nofail so that a
// missing disk never blocks booting. It replaces an existing managed entry
// for the same UUID and refuses to touch entries it did not write.
func FstabAdd(content []byte, uuid, mountPoint, fstype, options string) ([]byte, error) {
	if !ValidUUID(uuid) || !ManagedMountPoint(mountPoint) || !MountableFS(fstype) {
		return nil, ErrFstabInvalid
	}
	for _, o := range strings.Split(options, ",") {
		switch o {
		case "nosuid", "nodev", "noexec":
		default:
			return nil, ErrFstabInvalid
		}
	}
	var kept []string
	for _, line := range fstabLines(content) {
		e, ok := parseFstabLine(line)
		if ok && (strings.EqualFold(e.UUID(), uuid) || e.MountPoint == mountPoint) {
			if !e.Managed {
				return nil, ErrFstabConflict
			}
			continue // replaced below
		}
		kept = append(kept, line)
	}
	entry := "UUID=" + uuid + " " + mountPoint + " " + fstype + " " + options +
		",nofail,x-systemd.device-timeout=10s," + ManagedOption + " 0 0"
	kept = append(kept, entry)
	return []byte(strings.Join(kept, "\n") + "\n"), nil
}

// FstabRemove deletes the managed entries of a UUID.
func FstabRemove(content []byte, uuid string) ([]byte, bool) {
	var kept []string
	removed := false
	for _, line := range fstabLines(content) {
		e, ok := parseFstabLine(line)
		if ok && e.Managed && strings.EqualFold(e.UUID(), uuid) {
			removed = true
			continue
		}
		kept = append(kept, line)
	}
	if !removed {
		return content, false
	}
	if len(kept) == 0 {
		return []byte{}, true
	}
	return []byte(strings.Join(kept, "\n") + "\n"), true
}

// fstabLines splits the file into lines without losing anything: only the
// newline that terminates the last line is dropped, so blank lines at the
// end of the file survive an edit, and an empty file has no lines.
func fstabLines(content []byte) []string {
	s := string(content)
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
