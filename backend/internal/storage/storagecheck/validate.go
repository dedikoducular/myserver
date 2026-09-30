// Package storagecheck holds the portable validation and parsing code of the
// storage module. It is shared by the panel (internal/storage) and by the
// root helper (internal/helper), which repeats every check independently
// because it is the security boundary. It must not import module, auth or
// server.
package storagecheck

import (
	"regexp"
	"strings"
)

const diskPattern = `(sd[a-z]{1,4}|vd[a-z]{1,4}|xvd[a-z]{1,4}|nvme[0-9]{1,3}n[0-9]{1,3}|mmcblk[0-9]{1,3})`

var (
	diskRe    = regexp.MustCompile(`^` + diskPattern + `$`)
	deviceRe  = regexp.MustCompile(`^` + diskPattern + `(p?[0-9]{1,3})?$`)
	virtualRe = regexp.MustCompile(`^(md[0-9]{1,4}(p[0-9]{1,3})?|dm-[0-9]{1,5})$`)
	nameRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,47}$`)
	uuidRe    = regexp.MustCompile(`^[A-Fa-f0-9][A-Fa-f0-9-]{3,35}$`)
	// A label never starts with '-', so it cannot be read as an option.
	labelRe = regexp.MustCompile(`^([A-Za-z0-9_][A-Za-z0-9_-]*)?$`)
)

// ValidDevice reports whether name is the kernel name of a physical disk or
// one of its partitions (sdb, sdb1, nvme0n1p2, mmcblk0p1). Only these may
// be formatted.
func ValidDevice(name string) bool { return deviceRe.MatchString(name) }

// ValidDiskName reports whether name can be a whole physical disk.
func ValidDiskName(name string) bool { return diskRe.MatchString(name) }

// ValidMountDevice additionally accepts software RAID and device-mapper
// nodes (md0, dm-3), which can be mounted and unmounted but not formatted.
func ValidMountDevice(name string) bool {
	return deviceRe.MatchString(name) || virtualRe.MatchString(name)
}

// ValidMountName validates the directory name used under the mount base.
func ValidMountName(name string) bool { return nameRe.MatchString(name) }

// ValidUUID validates a filesystem UUID as printed by lsblk/blkid
// ("1234-ABCD" for FAT, 36 characters for ext4/xfs/btrfs).
func ValidUUID(uuid string) bool { return uuidRe.MatchString(uuid) }

// ValidSerial is a sanity check for a serial number that is only ever
// compared, never passed to a command.
func ValidSerial(s string) bool {
	if len(s) > 128 {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// Mount base directories. Removable media go under /media.
const (
	MountBaseFixed     = "/mnt"
	MountBaseRemovable = "/media"
)

// MountBase returns the base directory for a device.
func MountBase(removable bool) string {
	if removable {
		return MountBaseRemovable
	}
	return MountBaseFixed
}

// ManagedMountPoint reports whether path is exactly <base>/<valid name>.
func ManagedMountPoint(path string) bool {
	for _, base := range []string{MountBaseFixed, MountBaseRemovable} {
		if rest, ok := strings.CutPrefix(path, base+"/"); ok && ValidMountName(rest) {
			return true
		}
	}
	return false
}

// MountOptions returns the safe default mount options.
func MountOptions(removable bool) string {
	if removable {
		return "nosuid,nodev,noexec"
	}
	return "nosuid,nodev"
}

// mountable is the allow-list of filesystem types that may be mounted.
var mountable = map[string]bool{
	"ext4": true, "ext3": true, "ext2": true, "xfs": true, "btrfs": true,
	"vfat": true, "exfat": true, "ntfs": true, "ntfs3": true,
}

// MountableFS reports whether a filesystem type may be mounted.
func MountableFS(fstype string) bool { return mountable[fstype] }

// FormatFS describes one filesystem that can be created.
type FormatFS struct {
	Type     string
	Binary   string
	MaxLabel int
	// GPT partition type used when a whole disk is formatted.
	PartType string
}

const (
	gptLinux     = "0FC63DAF-8483-4772-8E79-3D69D8477DE4"
	gptMicrosoft = "EBD0A0A2-B9E5-4433-87C0-68B6B72699C7"
)

// FormatFilesystems lists the filesystems the panel can create, in the
// order they are offered.
var FormatFilesystems = []FormatFS{
	{Type: "ext4", Binary: "/usr/sbin/mkfs.ext4", MaxLabel: 16, PartType: gptLinux},
	{Type: "xfs", Binary: "/usr/sbin/mkfs.xfs", MaxLabel: 12, PartType: gptLinux},
	{Type: "btrfs", Binary: "/usr/sbin/mkfs.btrfs", MaxLabel: 48, PartType: gptLinux},
	{Type: "exfat", Binary: "/usr/sbin/mkfs.exfat", MaxLabel: 15, PartType: gptMicrosoft},
	{Type: "vfat", Binary: "/usr/sbin/mkfs.vfat", MaxLabel: 11, PartType: gptMicrosoft},
}

// LookupFormatFS returns the description of a creatable filesystem.
func LookupFormatFS(fstype string) (FormatFS, bool) {
	for _, f := range FormatFilesystems {
		if f.Type == fstype {
			return f, true
		}
	}
	return FormatFS{}, false
}

// ValidLabel validates an optional filesystem label for a filesystem type.
func ValidLabel(fs FormatFS, label string) bool {
	return len(label) <= fs.MaxLabel && labelRe.MatchString(label)
}

// MkfsArgs returns the arguments of the mkfs tool for a filesystem, ending
// with "--" and the device path.
func MkfsArgs(fs FormatFS, label, devPath string) []string {
	var args []string
	switch fs.Type {
	case "ext4":
		args = []string{"-F", "-q"}
	case "xfs", "btrfs":
		args = []string{"-f"}
	case "vfat":
		args = []string{"-F", "32"}
		label = strings.ToUpper(label)
	}
	if label != "" {
		if fs.Type == "vfat" {
			args = append(args, "-n", label)
		} else {
			args = append(args, "-L", label)
		}
	}
	return append(args, "--", devPath)
}

// DeriveMountName proposes a mount directory name from the label, falling
// back to the UUID and then the device name.
func DeriveMountName(label, uuid, device string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(label) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ' || r == '.':
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-_")
	if len(name) > 48 {
		name = name[:48]
	}
	if ValidMountName(name) {
		return name
	}
	if ValidMountName(uuid) {
		return uuid
	}
	return device
}

// PartitionName returns the kernel name of partition n of a disk: sdb1, but
// nvme0n1p1 and mmcblk0p1 for names that end in a digit.
func PartitionName(disk string, n int) string {
	sep := ""
	if disk != "" && disk[len(disk)-1] >= '0' && disk[len(disk)-1] <= '9' {
		sep = "p"
	}
	return disk + sep + itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
