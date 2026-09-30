//go:build !linux

package system

import "errors"

// The panel only runs on Linux; these keep the package compiling on a
// development machine and report that nothing can be measured.

func statFS(string) (total, used, avail uint64, err error) {
	return 0, 0, 0, errors.New("statfs is only available on Linux")
}

func timeSynced() (synced, known bool) { return false, false }
