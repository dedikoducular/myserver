//go:build linux

package backup

import "golang.org/x/sys/unix"

// Flags for opening files inside a host folder: never follow a symbolic
// link in the last path element and never block on a FIFO.
const (
	openNoFollow = unix.O_NOFOLLOW
	openNonBlock = unix.O_NONBLOCK
)

// diskSpace returns the space available to the panel user and the size of
// the file system holding path.
func diskSpace(path string) (free, total uint64, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)
	return st.Bavail * bs, st.Blocks * bs, nil
}
