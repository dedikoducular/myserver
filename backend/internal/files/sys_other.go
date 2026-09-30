//go:build !linux

package files

import "os"

// The product runs on Linux only; these exist so the portable parts of the
// package compile and can be unit-tested on a development machine.

const openNonblock = 0

func ownerIDs(os.FileInfo) (uid, gid int, ok bool) { return 0, 0, false }

func diskUsageIn(*os.Root, string) (total, free int64, ok bool) { return 0, 0, false }
