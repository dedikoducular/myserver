//go:build linux

package storage

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"

	sc "myserver/internal/storage/storagecheck"
)

// watchUevents listens on the kernel's kobject uevent netlink group and
// calls fn for every event. It returns an error at once when the socket
// cannot be opened (for example inside a restricted container), and nil
// when ctx ends.
func watchUevents(ctx context.Context, fn func(sc.Uevent)) error {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, unix.NETLINK_KOBJECT_UEVENT)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	// Group 1 carries the kernel's own events.
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: 1}); err != nil {
		return err
	}
	// A receive timeout lets the loop notice cancellation.
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 2}); err != nil {
		return err
	}
	buf := make([]byte, 64<<10)
	failures := 0
	for ctx.Err() == nil {
		n, from, err := unix.Recvfrom(fd, buf, 0)
		if err != nil {
			if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) || errors.Is(err, unix.ENOBUFS) {
				continue
			}
			if failures++; failures > 10 {
				return err
			}
			continue
		}
		failures = 0
		// Only the kernel (port 0) is a trusted sender.
		if nl, ok := from.(*unix.SockaddrNetlink); !ok || nl.Pid != 0 {
			continue
		}
		if ev, ok := sc.ParseUevent(buf[:n]); ok {
			fn(ev)
		}
	}
	return nil
}
