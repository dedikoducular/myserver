package storagecheck

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Sys reads block device facts from sysfs. Root is "/sys" in production.
type Sys struct{ Root string }

// DefaultSys is the real sysfs.
var DefaultSys = Sys{Root: "/sys"}

func (s Sys) class(name string, elem ...string) string {
	return filepath.Join(append([]string{s.Root, "class", "block", name}, elem...)...)
}

func safeName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00")
}

// Exists reports whether the kernel knows the block device.
func (s Sys) Exists(name string) bool {
	if !safeName(name) {
		return false
	}
	_, err := os.Stat(s.class(name))
	return err == nil
}

// IsPartition reports whether the device is a partition of a disk.
func (s Sys) IsPartition(name string) bool {
	if !safeName(name) {
		return false
	}
	_, err := os.Stat(s.class(name, "partition"))
	return err == nil
}

// Parent returns the disk a partition belongs to.
func (s Sys) Parent(name string) (string, bool) {
	if !s.IsPartition(name) {
		return "", false
	}
	target, err := os.Readlink(s.class(name))
	if err != nil {
		return "", false
	}
	p := path.Base(path.Dir(filepath.ToSlash(target)))
	if !safeName(p) || p == "block" {
		return "", false
	}
	return p, true
}

func (s Sys) list(name, sub string) []string {
	if !safeName(name) {
		return nil
	}
	entries, err := os.ReadDir(s.class(name, sub))
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// Slaves returns the devices a stacked device (LVM, LUKS, RAID) is built on.
func (s Sys) Slaves(name string) []string { return s.list(name, "slaves") }

// Holders returns the stacked devices built on top of a device.
func (s Sys) Holders(name string) []string { return s.list(name, "holders") }

// Partitions returns the partitions of a whole disk.
func (s Sys) Partitions(disk string) []string {
	if !safeName(disk) {
		return nil
	}
	entries, err := os.ReadDir(s.class(disk))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if _, err := os.Stat(s.class(disk, e.Name(), "partition")); err == nil {
			out = append(out, e.Name())
		}
	}
	return out
}

// NameByMajMin resolves "8:1" to the kernel name "sda1".
func (s Sys) NameByMajMin(majmin string) (string, bool) {
	maj, min, ok := strings.Cut(majmin, ":")
	if !ok {
		return "", false
	}
	a, err1 := strconv.ParseUint(maj, 10, 32)
	b, err2 := strconv.ParseUint(min, 10, 32)
	if err1 != nil || err2 != nil || a == 0 {
		// Major 0 is an anonymous device (btrfs, tmpfs, overlay).
		return "", false
	}
	_ = b
	target, err := os.Readlink(filepath.Join(s.Root, "dev", "block", majmin))
	if err != nil {
		return "", false
	}
	name := path.Base(filepath.ToSlash(target))
	if !s.Exists(name) {
		return "", false
	}
	return name, true
}

func (s Sys) readTrim(name, file string) string {
	if !safeName(name) {
		return ""
	}
	b, err := os.ReadFile(s.class(name, file))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// SizeBytes returns the device size; sysfs counts 512-byte sectors.
func (s Sys) SizeBytes(name string) int64 {
	n, err := strconv.ParseInt(s.readTrim(name, "size"), 10, 64)
	if err != nil {
		return 0
	}
	return n * 512
}

// ReadOnly reports the kernel's read-only flag of the device.
func (s Sys) ReadOnly(name string) bool { return s.readTrim(name, "ro") == "1" }

// BtrfsMembers returns every member device of the btrfs filesystem that
// contains name, or nil.
func (s Sys) BtrfsMembers(name string) []string {
	base := filepath.Join(s.Root, "fs", "btrfs")
	fss, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	for _, fs := range fss {
		devs, err := os.ReadDir(filepath.Join(base, fs.Name(), "devices"))
		if err != nil {
			continue
		}
		var names []string
		found := false
		for _, d := range devs {
			names = append(names, d.Name())
			if d.Name() == name {
				found = true
			}
		}
		if found {
			return names
		}
	}
	return nil
}

// DiskNames lists /sys/block (directory listing only; very cheap).
func (s Sys) DiskNames() []string {
	entries, err := os.ReadDir(filepath.Join(s.Root, "block"))
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// BaseDisks walks from a device down to the physical disks it lives on:
// through btrfs members, device-mapper/RAID slaves and from partitions to
// their disk. chain receives every node visited, including name itself.
func (s Sys) BaseDisks(name string) (disks, chain []string) {
	seen := map[string]bool{}
	var walk func(n string, depth int)
	walk = func(n string, depth int) {
		if seen[n] || depth > 16 || !safeName(n) {
			return
		}
		seen[n] = true
		chain = append(chain, n)
		for _, m := range s.BtrfsMembers(n) {
			walk(m, depth+1)
		}
		slaves := s.Slaves(n)
		for _, sl := range slaves {
			walk(sl, depth+1)
		}
		if p, ok := s.Parent(n); ok {
			walk(p, depth+1)
			return
		}
		if len(slaves) == 0 {
			disks = append(disks, n)
		}
	}
	walk(name, 0)
	return disks, chain
}

// Family returns a device together with its partitions and everything
// stacked on top of them (holders), i.e. all nodes whose data would be
// lost if the device were overwritten.
func (s Sys) Family(name string) []string {
	seen := map[string]bool{}
	var out []string
	var walk func(n string, depth int)
	walk = func(n string, depth int) {
		if seen[n] || depth > 16 || !safeName(n) {
			return
		}
		seen[n] = true
		out = append(out, n)
		for _, p := range s.Partitions(n) {
			walk(p, depth+1)
		}
		for _, h := range s.Holders(n) {
			walk(h, depth+1)
		}
	}
	walk(name, 0)
	return out
}

// CriticalPaths are the paths whose filesystems make a disk a system disk.
// Each is resolved to the mount that contains it, so "/boot" on the root
// filesystem resolves to the root mount.
var CriticalPaths = []string{
	"/", "/boot", "/boot/efi", "/efi", "/usr", "/usr/local/libexec", "/etc",
	"/var", "/var/lib", "/var/lib/myserver",
}

// IsCriticalMountPoint reports whether a mount point must never be
// unmounted, regardless of the device behind it.
func IsCriticalMountPoint(mp string) bool {
	for _, c := range CriticalPaths {
		if mp == c {
			return true
		}
	}
	return false
}

// Protection is the set of system devices.
type Protection struct {
	// OK is false when the device holding "/" could not be determined. All
	// destructive operations must then be refused.
	OK bool
	// Disks maps a protected physical disk to the reason ("/", "/boot",
	// "swap").
	Disks map[string]string
	// Nodes maps every node in the chain of a system filesystem
	// (partition, LVM volume, LUKS mapping) to the reason.
	Nodes map[string]string
}

// Resolver maps a device path such as /dev/mapper/vg-root to a kernel name.
type Resolver func(devPath string) (string, bool)

// MountDevice returns the kernel name of the device behind a mount.
func MountDevice(s Sys, m Mount, resolve Resolver) (string, bool) {
	if n, ok := s.NameByMajMin(m.MajMin); ok {
		return n, true
	}
	if strings.HasPrefix(m.Source, "/dev/") && resolve != nil {
		return resolve(m.Source)
	}
	return "", false
}

// Detect determines the system devices from the mount table and the swap
// list. It uses nothing supplied by a client.
func Detect(s Sys, mounts []Mount, swaps []string, resolve Resolver) Protection {
	p := Protection{Disks: map[string]string{}, Nodes: map[string]string{}}
	mark := func(name, reason string) bool {
		disks, chain := s.BaseDisks(name)
		for _, n := range chain {
			if _, ok := p.Nodes[n]; !ok {
				p.Nodes[n] = reason
			}
		}
		for _, d := range disks {
			if _, ok := p.Disks[d]; !ok {
				p.Disks[d] = reason
			}
		}
		return len(disks) > 0
	}
	unresolved := false
	for _, cp := range CriticalPaths {
		m, ok := ContainingMount(mounts, cp)
		if !ok {
			continue
		}
		name, ok := MountDevice(s, m, resolve)
		if !ok {
			// A system filesystem on a real block device that cannot be
			// identified (/boot, /boot/efi, /var ...): its disk would go
			// unprotected, so fail closed. Virtual filesystems (tmpfs,
			// network) have no disk to protect.
			if backedByBlockDevice(m) {
				unresolved = true
			}
			continue
		}
		marked := mark(name, m.MountPoint)
		if !marked {
			unresolved = true
		}
		if marked && m.MountPoint == "/" {
			p.OK = true
		}
	}
	for _, sw := range swaps {
		if !strings.HasPrefix(sw, "/dev/") {
			// A swap file: protect the filesystem that contains it.
			if m, ok := ContainingMount(mounts, sw); ok {
				if name, ok := MountDevice(s, m, resolve); ok {
					mark(name, "swap")
				}
			}
			continue
		}
		if resolve == nil {
			p.OK = false
			continue
		}
		name, ok := resolve(sw)
		if !ok || !mark(name, "swap") {
			// An active swap device that cannot be identified: fail closed.
			p.OK = false
		}
	}
	if unresolved {
		p.OK = false
	}
	return p
}

// backedByBlockDevice reports whether a mount sits on a real block device:
// its source is a device path or its device number has a non-zero major
// (major 0 is used by anonymous devices such as tmpfs, overlay and NFS).
func backedByBlockDevice(m Mount) bool {
	if strings.HasPrefix(m.Source, "/dev/") {
		return true
	}
	maj, _, ok := strings.Cut(m.MajMin, ":")
	if !ok {
		return false
	}
	n, err := strconv.ParseUint(maj, 10, 32)
	return err == nil && n != 0
}

// Protected reports whether a device is a system device: it is protected
// when any physical disk it lives on is a system disk, or when it is part
// of the chain of a system filesystem. The reason is the mount point (or
// "swap") that makes it so.
func (p Protection) Protected(s Sys, name string) (bool, string) {
	if !p.OK {
		return true, ""
	}
	if r, ok := p.Nodes[name]; ok {
		return true, r
	}
	disks, chain := s.BaseDisks(name)
	for _, n := range chain {
		if r, ok := p.Nodes[n]; ok {
			return true, r
		}
	}
	for _, d := range disks {
		if r, ok := p.Disks[d]; ok {
			return true, r
		}
	}
	// Anything stacked on top of the device (an LVM volume group spanning
	// it, a RAID array) that is itself protected also protects the device.
	for _, n := range s.Family(name) {
		if r, ok := p.Nodes[n]; ok {
			return true, r
		}
	}
	if len(disks) == 0 {
		// The device could not be traced to a physical disk.
		return true, ""
	}
	return false, ""
}
