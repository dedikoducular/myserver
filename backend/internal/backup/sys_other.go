//go:build !linux

package backup

import "errors"

const (
	openNoFollow = 0
	openNonBlock = 0
)

func diskSpace(string) (free, total uint64, err error) {
	return 0, 0, errors.New("backup: disk space is only available on Linux")
}
