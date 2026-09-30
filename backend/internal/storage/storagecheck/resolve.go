package storagecheck

import "os"

// ResolvePath maps a device path (/dev/sda1, /dev/mapper/vg-lv,
// /dev/disk/by-uuid/...) to the kernel name, through the device number of
// the node so that symbolic links and aliases are followed by the kernel.
func (s Sys) ResolvePath(devPath string) (string, bool) {
	mm, err := DevNumber(devPath)
	if err != nil {
		return "", false
	}
	return s.NameByMajMin(mm)
}

// VerifyNode checks that /dev/<name> is a block device node whose device
// number belongs to the kernel device of the same name. It returns the
// path to operate on.
func (s Sys) VerifyNode(name string) (string, bool) {
	if !safeName(name) || !s.Exists(name) {
		return "", false
	}
	p := "/dev/" + name
	fi, err := os.Lstat(p)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 {
		return "", false
	}
	got, ok := s.ResolvePath(p)
	if !ok || got != name {
		return "", false
	}
	return p, true
}

// LoadState reads the mount tables and the swap list. extraMountInfo may
// name additional mountinfo files (the helper also reads PID 1's table).
func LoadState(s Sys, mountInfoFiles ...string) (Protection, []Mount, []string, error) {
	var tables [][]Mount
	var firstErr error
	for i, f := range mountInfoFiles {
		b, err := os.ReadFile(f)
		if err != nil {
			if i == 0 {
				firstErr = err
			}
			continue
		}
		tables = append(tables, ParseMountInfo(b))
	}
	if firstErr != nil {
		return Protection{}, nil, nil, firstErr
	}
	mounts := MergeMounts(tables...)
	var swaps []string
	b, err := os.ReadFile("/proc/swaps")
	if err != nil && !os.IsNotExist(err) {
		return Protection{}, nil, nil, err
	}
	swaps = ParseSwaps(b)
	return Detect(s, mounts, swaps, s.ResolvePath), mounts, swaps, nil
}

// MountsOf returns the mounts of a device.
func MountsOf(s Sys, mounts []Mount, name string) []Mount {
	var out []Mount
	for _, m := range mounts {
		if n, ok := MountDevice(s, m, s.ResolvePath); ok && n == name {
			out = append(out, m)
			continue
		}
		// A multi-device btrfs filesystem names only one member as source.
		if m.FSType == "btrfs" {
			if src, ok := s.ResolvePath(m.Source); ok {
				for _, member := range s.BtrfsMembers(src) {
					if member == name {
						out = append(out, m)
						break
					}
				}
			}
		}
	}
	return out
}

// SwapOn reports whether the device is an active swap area.
func SwapOn(s Sys, swaps []string, name string) bool {
	for _, sw := range swaps {
		if n, ok := s.ResolvePath(sw); ok && n == name {
			return true
		}
	}
	return false
}
