//go:build !linux

package storage

import (
	"context"
	"errors"

	sc "myserver/internal/storage/storagecheck"
)

// watchUevents is only available on Linux.
func watchUevents(context.Context, func(sc.Uevent)) error {
	return errors.New("uevent: only supported on Linux")
}
