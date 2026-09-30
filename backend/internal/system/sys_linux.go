//go:build linux

package system

import "golang.org/x/sys/unix"

// statFS returns the size, used and available bytes of the filesystem
// mounted at path.
func statFS(path string) (total, used, avail uint64, err error) {
	var st unix.Statfs_t
	if err = unix.Statfs(path, &st); err != nil {
		return 0, 0, 0, err
	}
	bs := uint64(st.Bsize)
	total = st.Blocks * bs
	if st.Blocks >= st.Bfree {
		used = (st.Blocks - st.Bfree) * bs
	}
	avail = st.Bavail * bs
	return total, used, avail, nil
}

// timeSynced reads the kernel clock state. known is false when the state
// cannot be read.
func timeSynced() (synced, known bool) {
	var tx unix.Timex
	state, err := unix.Adjtimex(&tx)
	if err != nil {
		return false, false
	}
	if state == unix.TIME_ERROR || tx.Status&unix.STA_UNSYNC != 0 {
		return false, true
	}
	return true, true
}
