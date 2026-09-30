package storage

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"

	"myserver/internal/settings"
	sc "myserver/internal/storage/storagecheck"
)

// Usage is the space of a mounted filesystem, in bytes.
type Usage struct {
	Total   uint64  `json:"total"`
	Used    uint64  `json:"used"`
	Free    uint64  `json:"free"`
	Percent float64 `json:"percent"`
}

// MountPoint is one place a filesystem is mounted.
type MountPoint struct {
	Path     string `json:"path"`
	ReadOnly bool   `json:"read_only"`
	// Browsable is true when the path is inside the file manager's
	// allowed roots.
	Browsable bool `json:"browsable"`
}

// SmartSummary is the part of a SMART report shown on the disk list.
type SmartSummary struct {
	Status      string   `json:"status"`
	Temperature *int     `json:"temperature"`
	Problems    []string `json:"problems"`
	CheckedAt   int64    `json:"checked_at"`
}

// Device is a disk, a partition or a volume stacked on them.
type Device struct {
	Name        string       `json:"name"`
	Path        string       `json:"path"`
	Type        string       `json:"type"`
	Size        int64        `json:"size"`
	Model       string       `json:"model"`
	Serial      string       `json:"serial"`
	Vendor      string       `json:"vendor"`
	Transport   string       `json:"transport"`
	Rotational  bool         `json:"rotational"`
	Removable   bool         `json:"removable"`
	Hotplug     bool         `json:"hotplug"`
	ReadOnly    bool         `json:"read_only"`
	FSType      string       `json:"fstype"`
	Label       string       `json:"label"`
	UUID        string       `json:"uuid"`
	MountPoints []MountPoint `json:"mountpoints"`
	Usage       *Usage       `json:"usage"`
	Swap        bool         `json:"swap"`
	// InUse: carries an active LVM/RAID/LUKS volume or swap.
	InUse bool `json:"in_use"`
	// System devices are protected; SystemReason is the mount point (or
	// "swap") that makes the device a system device.
	System       bool   `json:"system"`
	SystemReason string `json:"system_reason"`
	// Manageable: the name is one the panel can act on.
	Manageable bool `json:"manageable"`
	Mountable  bool `json:"mountable"`
	// Persistent: a panel-written fstab entry exists for the UUID.
	Persistent    bool          `json:"persistent"`
	SuggestedName string        `json:"suggested_name"`
	Smart         *SmartSummary `json:"smart"`
	Children      []Device      `json:"children"`

	hidden bool
}

// PersistentMount is a panel-written /etc/fstab entry.
type PersistentMount struct {
	UUID       string `json:"uuid"`
	MountPoint string `json:"mountpoint"`
	FSType     string `json:"fstype"`
	// Present is false when the disk is not attached right now.
	Present bool   `json:"present"`
	Device  string `json:"device"`
}

type snapshot struct {
	devices    []Device
	detection  bool
	persistent []PersistentMount
	mounts     []sc.Mount
	fstab      []sc.FstabEntry
	at         time.Time
}

var errLsblk = errors.New("lsblk failed")

func runLsblk(ctx context.Context) ([]*sc.BlockDevice, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var lastErr error = errLsblk
	for _, legacy := range []bool{false, true} {
		cmd := exec.CommandContext(ctx, sc.LsblkBinary, sc.LsblkArgs(legacy)...)
		cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "LANG=C"}
		cmd.WaitDelay = 2 * time.Second
		out, err := cmd.Output()
		if err != nil {
			lastErr = err
			continue
		}
		return sc.ParseLsblk(out)
	}
	return nil, lastErr
}

// snapshot returns the device inventory, rebuilt when older than maxAge.
func (m *Module) snapshot(ctx context.Context, maxAge time.Duration) (*snapshot, error) {
	m.invMu.Lock()
	defer m.invMu.Unlock()
	if m.inv != nil && time.Since(m.invAt) < maxAge {
		return m.inv, nil
	}
	snap, err := m.build(ctx)
	if err != nil {
		return nil, err
	}
	m.inv, m.invAt = snap, time.Now()
	return snap, nil
}

func (m *Module) invalidate() {
	m.invMu.Lock()
	m.inv = nil
	m.invMu.Unlock()
}

func insideRoots(path string, roots []string) bool {
	for _, r := range roots {
		r = strings.TrimRight(r, "/")
		if r == "" {
			continue
		}
		if path == r || strings.HasPrefix(path, r+"/") {
			return true
		}
	}
	return false
}

func hiddenName(name string) bool {
	return strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "zram") ||
		strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "fd")
}

func (m *Module) build(ctx context.Context) (*snapshot, error) {
	devs, err := runLsblk(ctx)
	if err != nil {
		return nil, err
	}
	prot, mounts, swaps, err := sc.LoadState(m.sys, "/proc/self/mountinfo")
	if err != nil {
		// Without a mount table nothing can be declared safe.
		prot = sc.Protection{}
	}
	var fstab []sc.FstabEntry
	if b, err := os.ReadFile(sc.FstabPath); err == nil {
		fstab = sc.ParseFstab(b)
	}
	managed := map[string]sc.FstabEntry{}
	for _, e := range fstab {
		if e.Managed && e.UUID() != "" {
			managed[strings.ToLower(e.UUID())] = e
		}
	}
	roots := m.deps.Settings.Strings(settings.KeyAllowedRoots)
	byPoint := map[string]sc.Mount{}
	for _, mt := range mounts {
		byPoint[mt.MountPoint] = mt
	}
	uuidDevice := map[string]string{}

	var convert func(b *sc.BlockDevice, root *sc.BlockDevice, top bool) Device
	convert = func(b *sc.BlockDevice, root *sc.BlockDevice, top bool) Device {
		d := Device{
			Name: b.KName, Path: b.Path, Type: b.Type, Size: b.Size,
			Model: b.Model, Serial: b.Serial, Vendor: b.Vendor, Transport: b.Transport,
			Rotational: b.Rotational, Removable: b.Removable, Hotplug: b.Hotplug, ReadOnly: b.ReadOnly,
			FSType: b.FSType, Label: b.Label, UUID: b.UUID,
			MountPoints: []MountPoint{}, Children: []Device{},
		}
		if d.Path == "" {
			d.Path = "/dev/" + d.Name
		}
		if !top {
			// Partitions inherit what lsblk reports only for the disk.
			d.Transport = root.Transport
			d.Removable = d.Removable || root.Removable
			d.Hotplug = d.Hotplug || root.Hotplug
		}
		d.hidden = b.Type == "loop" || hiddenName(b.KName) || b.FSType == "squashfs"
		for _, mp := range b.MountPoints {
			if mp == "[SWAP]" {
				d.Swap = true
				continue
			}
			if strings.HasPrefix(mp, "/snap/") || strings.HasPrefix(mp, "/var/lib/snapd/") {
				continue
			}
			info := MountPoint{Path: mp, Browsable: insideRoots(mp, roots)}
			if mt, ok := byPoint[mp]; ok {
				info.ReadOnly = mt.ReadOnly()
			}
			d.MountPoints = append(d.MountPoints, info)
		}
		if len(d.MountPoints) > 0 {
			if total, free, avail, err := sc.Usage(d.MountPoints[0].Path); err == nil && total > 0 {
				used := total - free
				u := &Usage{Total: total, Used: used, Free: avail}
				if used+avail > 0 {
					u.Percent = float64(used) * 100 / float64(used+avail)
				}
				d.Usage = u
			}
		}
		d.Manageable = sc.ValidMountDevice(d.Name)
		d.System, d.SystemReason = prot.Protected(m.sys, d.Name)
		if sc.SwapOn(m.sys, swaps, d.Name) {
			d.Swap = true
		}
		d.InUse = d.Swap || len(m.sys.Holders(d.Name)) > 0
		d.Mountable = d.Manageable && !d.System && !d.InUse && sc.MountableFS(d.FSType) && len(d.MountPoints) == 0
		if d.UUID != "" {
			uuidDevice[strings.ToLower(d.UUID)] = d.Name
			_, d.Persistent = managed[strings.ToLower(d.UUID)]
		}
		d.SuggestedName = sc.DeriveMountName(d.Label, d.UUID, d.Name)
		if top && b.Type == "disk" {
			if r, ok := m.smart.get(d.Name); ok {
				d.Smart = &SmartSummary{Status: r.Status, Temperature: r.Temperature, Problems: r.Problems, CheckedAt: r.CheckedAt}
			}
		}
		for _, c := range b.Children {
			d.Children = append(d.Children, convert(c, root, false))
		}
		return d
	}

	snap := &snapshot{detection: prot.OK, mounts: mounts, fstab: fstab, at: time.Now(), persistent: []PersistentMount{}}
	for _, b := range devs {
		snap.devices = append(snap.devices, convert(b, b, true))
	}
	for uuid, e := range managed {
		pm := PersistentMount{UUID: e.UUID(), MountPoint: e.MountPoint, FSType: e.FSType}
		if name, ok := uuidDevice[uuid]; ok {
			pm.Present, pm.Device = true, name
		}
		snap.persistent = append(snap.persistent, pm)
	}
	return snap, nil
}

// visible returns the devices to show. Loop devices and snap/squashfs
// images are hidden unless all is set.
func (s *snapshot) visible(all bool) []Device {
	var filter func(in []Device) []Device
	filter = func(in []Device) []Device {
		out := make([]Device, 0, len(in))
		for _, d := range in {
			if d.hidden && !all {
				continue
			}
			d.Children = filter(d.Children)
			out = append(out, d)
		}
		return out
	}
	return filter(s.devices)
}

// find returns a device and the top-level disk it belongs to.
func (s *snapshot) find(name string) (dev, root *Device) {
	var walk func(d *Device) *Device
	walk = func(d *Device) *Device {
		if d.Name == name {
			return d
		}
		for i := range d.Children {
			if f := walk(&d.Children[i]); f != nil {
				return f
			}
		}
		return nil
	}
	for i := range s.devices {
		if f := walk(&s.devices[i]); f != nil {
			return f, &s.devices[i]
		}
	}
	return nil, nil
}

// disks returns the whole physical disks SMART can be read from.
func (s *snapshot) disks() []Device {
	var out []Device
	for _, d := range s.devices {
		if d.Type == "disk" && !d.hidden && sc.ValidDiskName(d.Name) {
			out = append(out, d)
		}
	}
	return out
}

var writableFS = map[string]bool{
	"ext4": true, "ext3": true, "ext2": true, "xfs": true, "btrfs": true,
	"vfat": true, "exfat": true, "ntfs": true, "ntfs3": true, "fuseblk": true,
}

// unexpectedReadOnly lists mount points of writable filesystems that are
// mounted read-only although neither fstab nor the device asks for it.
// This is what the kernel does after a filesystem error.
func (s *snapshot) unexpectedReadOnly() []string {
	wanted := map[string]bool{}
	for _, e := range s.fstab {
		if sc.HasOption(e.Options, "ro") {
			wanted[e.MountPoint] = true
		}
	}
	seen := map[string]bool{}
	var out []string
	var walk func(d Device)
	walk = func(d Device) {
		if !d.hidden && !d.ReadOnly && d.Type != "rom" {
			for _, mp := range d.MountPoints {
				if !mp.ReadOnly || wanted[mp.Path] || seen[mp.Path] {
					continue
				}
				fstype := d.FSType
				for _, mt := range s.mounts {
					if mt.MountPoint == mp.Path {
						fstype = mt.FSType
					}
				}
				if writableFS[fstype] {
					seen[mp.Path] = true
					out = append(out, mp.Path)
				}
			}
		}
		for _, c := range d.Children {
			walk(c)
		}
	}
	for _, d := range s.devices {
		walk(d)
	}
	return out
}
