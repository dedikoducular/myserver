//go:build linux

package files

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// openNonblock is added when opening a file for reading whose type was
// checked a moment ago. Should the entry have been replaced by a FIFO or a
// device since, the open returns at once instead of blocking the panel; the
// type check on the opened descriptor then refuses it. It has no effect on
// regular files and directories.
const openNonblock = syscall.O_NONBLOCK

// ownerIDs returns the owner and group of a file.
func ownerIDs(fi os.FileInfo) (uid, gid int, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}

// diskUsageIn returns the size and the space available to unprivileged
// users of the filesystem holding rel, measured on a descriptor opened
// through the root rather than on a path string.
func diskUsageIn(rt *os.Root, rel string) (total, free int64, ok bool) {
	f, err := rt.Open(rel)
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	rc, err := f.SyscallConn()
	if err != nil {
		return 0, 0, false
	}
	var st unix.Statfs_t
	var serr error
	if err := rc.Control(func(fd uintptr) { serr = unix.Fstatfs(int(fd), &st) }); err != nil || serr != nil {
		return 0, 0, false
	}
	bs := uint64(st.Bsize)
	return int64(uint64(st.Blocks) * bs), int64(uint64(st.Bavail) * bs), true
}
