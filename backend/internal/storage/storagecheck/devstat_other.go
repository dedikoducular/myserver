//go:build !linux

package storagecheck

import "errors"

var errUnsupported = errors.New("storagecheck: only supported on Linux")

// DevNumber is only available on Linux.
func DevNumber(string) (string, error) { return "", errUnsupported }

// Usage is only available on Linux.
func Usage(string) (total, free, avail uint64, err error) { return 0, 0, 0, errUnsupported }

// Lock is only available on Linux.
func Lock(string) (func(), error) { return nil, errUnsupported }
