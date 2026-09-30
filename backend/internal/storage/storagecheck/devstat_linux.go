//go:build linux

package storagecheck

import (
	"errors"
	"strconv"

	"golang.org/x/sys/unix"
)

// DevNumber returns "major:minor" of a block device node. It fails when
// the path is not a block device.
func DevNumber(devPath string) (string, error) {
	var st unix.Stat_t
	if err := unix.Stat(devPath, &st); err != nil {
		return "", err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFBLK {
		return "", errors.New("not a block device")
	}
	rdev := uint64(st.Rdev)
	return strconv.FormatUint(uint64(unix.Major(rdev)), 10) + ":" +
		strconv.FormatUint(uint64(unix.Minor(rdev)), 10), nil
}

// Usage returns the size, free and user-available bytes of a mounted
// filesystem.
func Usage(mountPoint string) (total, free, avail uint64, err error) {
	var st unix.Statfs_t
	if err = unix.Statfs(mountPoint, &st); err != nil {
		return 0, 0, 0, err
	}
	bs := uint64(st.Bsize)
	return st.Blocks * bs, st.Bfree * bs, st.Bavail * bs, nil
}

// Lock takes an exclusive advisory lock so that two storage operations
// never run at the same time. The returned function releases it.
func Lock(path string) (func(), error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return func() {
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = unix.Close(fd)
	}, nil
}
