//go:build linux

package storagecheck

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// fakeSys builds a sysfs look-alike in a temporary directory, with the
// same layout and symbolic links the kernel exposes:
//
//	devices/<disk>/{size,ro,slaves/,holders/}
//	devices/<disk>/<part>/{partition,size,...}
//	class/block/<name> -> ../../devices/...
//	block/<disk>       -> ../devices/<disk>
//	dev/block/<maj:min> -> ../../devices/...
//	fs/btrfs/<uuid>/devices/<member>
//
// The device trees are fixtures written for the tests, not copies of a
// live system.
type fakeSys struct {
	t    *testing.T
	root string
	// dev maps /dev paths to kernel names, for the Resolver.
	dev map[string]string
}

func newFakeSys(t *testing.T) *fakeSys {
	t.Helper()
	f := &fakeSys{t: t, root: t.TempDir(), dev: map[string]string{}}
	for _, d := range []string{"devices", "class/block", "block", "dev/block", "fs/btrfs"} {
		f.mkdir(filepath.Join(f.root, d))
	}
	return f
}

func (f *fakeSys) sys() Sys { return Sys{Root: f.root} }

func (f *fakeSys) resolve(p string) (string, bool) {
	n, ok := f.dev[p]
	return n, ok
}

func (f *fakeSys) mkdir(p string) {
	f.t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeSys) write(p, content string) {
	f.t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeSys) link(target, name string) {
	f.t.Helper()
	if err := os.Symlink(target, name); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeSys) node(dir, rel, name, majmin string) {
	f.t.Helper()
	f.mkdir(filepath.Join(dir, "slaves"))
	f.mkdir(filepath.Join(dir, "holders"))
	f.write(filepath.Join(dir, "size"), "2048\n")
	f.write(filepath.Join(dir, "ro"), "0\n")
	f.write(filepath.Join(dir, "dev"), majmin+"\n")
	f.link("../../devices/"+rel, filepath.Join(f.root, "class/block", name))
	f.link("../../devices/"+rel, filepath.Join(f.root, "dev/block", majmin))
	f.dev["/dev/"+name] = name
}

// disk adds a whole disk (or a virtual device such as dm-0, md0).
func (f *fakeSys) disk(name, majmin string) {
	f.t.Helper()
	f.node(filepath.Join(f.root, "devices", name), name, name, majmin)
	f.link("../devices/"+name, filepath.Join(f.root, "block", name))
}

// part adds a partition of a disk.
func (f *fakeSys) part(disk, name, majmin string) {
	f.t.Helper()
	dir := filepath.Join(f.root, "devices", disk, name)
	f.node(dir, disk+"/"+name, name, majmin)
	f.write(filepath.Join(dir, "partition"), "1\n")
}

// stack records that upper (dm-0, md0) is built on lower.
func (f *fakeSys) stack(upper string, lowers ...string) {
	f.t.Helper()
	for _, l := range lowers {
		f.write(filepath.Join(f.root, "class/block", upper, "slaves", l), "")
		f.write(filepath.Join(f.root, "class/block", l, "holders", upper), "")
	}
}

func (f *fakeSys) btrfs(uuid string, members ...string) {
	f.t.Helper()
	dir := filepath.Join(f.root, "fs/btrfs", uuid, "devices")
	f.mkdir(dir)
	for _, m := range members {
		f.write(filepath.Join(dir, m), "")
	}
}

func (f *fakeSys) alias(path, name string) { f.dev[path] = name }

func mi(lines ...string) []Mount {
	return ParseMountInfo([]byte(strings.Join(lines, "\n") + "\n"))
}

// virtualMounts are present on every system.
var virtualMounts = []string{
	"24 30 0:22 / /sys rw,nosuid,nodev,noexec,relatime shared:7 - sysfs sysfs rw",
	"25 30 0:23 / /proc rw,nosuid,nodev,noexec,relatime shared:13 - proc proc rw",
	"26 30 0:5 / /dev rw,nosuid,relatime shared:2 - devtmpfs udev rw,size=8118164k",
	"28 30 0:25 / /run rw,nosuid,nodev,noexec,relatime shared:5 - tmpfs tmpfs rw,size=1630000k",
}

func expectProtected(t *testing.T, s Sys, p Protection, names ...string) {
	t.Helper()
	for _, n := range names {
		if ok, _ := p.Protected(s, n); !ok {
			t.Errorf("%s is NOT protected but belongs to the system", n)
		}
	}
}

func expectFree(t *testing.T, s Sys, p Protection, names ...string) {
	t.Helper()
	for _, n := range names {
		if ok, reason := p.Protected(s, n); ok {
			t.Errorf("%s is protected (%q) but is a separate data device", n, reason)
		}
	}
}

// plainLayout: one system disk with EFI, /boot, / and swap partitions plus
// an unused partition; a data disk; an unpartitioned NVMe; a USB stick.
func plainLayout(t *testing.T) (*fakeSys, []Mount, []string) {
	f := newFakeSys(t)
	f.disk("sda", "8:0")
	f.part("sda", "sda1", "8:1")
	f.part("sda", "sda2", "8:2")
	f.part("sda", "sda3", "8:3")
	f.part("sda", "sda4", "8:4")
	f.part("sda", "sda5", "8:5")
	f.disk("sdb", "8:16")
	f.part("sdb", "sdb1", "8:17")
	f.part("sdb", "sdb2", "8:18")
	f.disk("sdc", "8:32")
	f.part("sdc", "sdc1", "8:33")
	f.disk("nvme0n1", "259:0")
	mounts := mi(append(virtualMounts,
		"30 1 8:3 / / rw,relatime shared:1 - ext4 /dev/sda3 rw",
		"95 30 8:2 / /boot rw,relatime shared:50 - ext4 /dev/sda2 rw",
		"97 95 8:1 / /boot/efi rw,relatime shared:52 - vfat /dev/sda1 rw",
		"120 30 8:17 / /mnt/data rw,nosuid,nodev,relatime shared:60 - ext4 /dev/sdb1 rw",
		"121 30 8:33 / /media/usb rw,nosuid,nodev,noexec shared:61 - exfat /dev/sdc1 rw",
	)...)
	return f, mounts, []string{"/dev/sda4"}
}

func TestDetectPlainLayout(t *testing.T) {
	f, mounts, swaps := plainLayout(t)
	s := f.sys()
	p := Detect(s, mounts, swaps, f.resolve)
	if !p.OK {
		t.Fatal("root device not determined")
	}
	expectProtected(t, s, p, "sda", "sda1", "sda2", "sda3", "sda4", "sda5")
	expectFree(t, s, p, "sdb", "sdb1", "sdb2", "sdc", "sdc1", "nvme0n1")

	reasons := map[string]string{"sda3": "/", "sda2": "/boot", "sda1": "/boot/efi", "sda4": "swap", "sda": "/"}
	for n, want := range reasons {
		if _, got := p.Protected(s, n); got != want {
			t.Errorf("reason for %s = %q, want %q", n, got, want)
		}
	}
	if len(p.Disks) != 1 {
		t.Errorf("protected disks = %v, want only sda", p.Disks)
	}
}

// /boot, /boot/efi and swap each live on another disk than "/".
func TestDetectSystemSpreadOverDisks(t *testing.T) {
	f := newFakeSys(t)
	for i, d := range []string{"sda", "sdb", "sdc", "sdd", "sde"} {
		f.disk(d, "8:"+itoa(i*16))
		f.part(d, d+"1", "8:"+itoa(i*16+1))
		f.part(d, d+"2", "8:"+itoa(i*16+2))
	}
	mounts := mi(append(virtualMounts,
		"30 1 8:1 / / rw,relatime - ext4 /dev/sda1 rw",
		"95 30 8:17 / /boot rw,relatime - ext4 /dev/sdb1 rw",
		"97 95 8:33 / /boot/efi rw,relatime - vfat /dev/sdc1 rw",
		"98 30 8:66 / /mnt/data rw,relatime - ext4 /dev/sde2 rw",
	)...)
	s := f.sys()
	p := Detect(s, mounts, []string{"/dev/sdd2"}, f.resolve)
	if !p.OK {
		t.Fatal("root device not determined")
	}
	expectProtected(t, s, p, "sda", "sda1", "sda2", "sdb", "sdb1", "sdb2", "sdc", "sdc1", "sdc2", "sdd", "sdd1", "sdd2")
	expectFree(t, s, p, "sde", "sde1", "sde2")
}

// Separate /var and /usr filesystems are system filesystems too.
func TestDetectSeparateVarAndUsr(t *testing.T) {
	f := newFakeSys(t)
	for i, d := range []string{"sda", "sdb", "sdc", "sdd"} {
		f.disk(d, "8:"+itoa(i*16))
		f.part(d, d+"1", "8:"+itoa(i*16+1))
	}
	mounts := mi(
		"30 1 8:1 / / rw,relatime - ext4 /dev/sda1 rw",
		"95 30 8:17 / /var rw,relatime - ext4 /dev/sdb1 rw",
		"96 30 8:33 / /usr ro,relatime - ext4 /dev/sdc1 ro",
		"97 30 8:49 / /srv rw,relatime - ext4 /dev/sdd1 rw",
	)
	s := f.sys()
	p := Detect(s, mounts, nil, f.resolve)
	if !p.OK {
		t.Fatal("root device not determined")
	}
	expectProtected(t, s, p, "sda", "sda1", "sdb", "sdb1", "sdc", "sdc1")
	expectFree(t, s, p, "sdd", "sdd1")
}

// Ubuntu Server default: / on LVM. The volume group spans two disks and a
// second volume group holds data only.
func TestDetectLVM(t *testing.T) {
	f := newFakeSys(t)
	f.disk("sda", "8:0")
	f.part("sda", "sda1", "8:1")
	f.part("sda", "sda2", "8:2")
	f.part("sda", "sda3", "8:3")
	f.disk("sdb", "8:16")
	f.part("sdb", "sdb1", "8:17")
	f.disk("sdc", "8:32")
	f.part("sdc", "sdc1", "8:33")
	f.disk("sdd", "8:48")
	f.disk("dm-0", "252:0") // ubuntu--vg-ubuntu--lv, spans sda3 + sdb1
	f.disk("dm-1", "252:1") // ubuntu--vg-swap
	f.disk("dm-2", "252:2") // ubuntu--vg-spare: same VG, not mounted
	f.disk("dm-3", "252:3") // data--vg-data on sdc1
	f.stack("dm-0", "sda3", "sdb1")
	f.stack("dm-1", "sda3")
	f.stack("dm-2", "sdb1")
	f.stack("dm-3", "sdc1")
	f.alias("/dev/mapper/ubuntu--vg-ubuntu--lv", "dm-0")
	f.alias("/dev/mapper/ubuntu--vg-swap", "dm-1")
	mounts := mi(append(virtualMounts,
		"30 1 252:0 / / rw,relatime shared:1 - ext4 /dev/mapper/ubuntu--vg-ubuntu--lv rw",
		"95 30 8:2 / /boot rw,relatime shared:50 - ext4 /dev/sda2 rw",
		"97 95 8:1 / /boot/efi rw,relatime shared:52 - vfat /dev/sda1 rw",
		"120 30 252:3 / /mnt/data rw,relatime shared:60 - ext4 /dev/mapper/data--vg-data rw",
	)...)
	s := f.sys()
	p := Detect(s, mounts, []string{"/dev/dm-1"}, f.resolve)
	if !p.OK {
		t.Fatal("root device not determined")
	}
	expectProtected(t, s, p, "dm-0", "dm-1", "dm-2", "sda", "sda1", "sda2", "sda3", "sdb", "sdb1")
	expectFree(t, s, p, "sdc", "sdc1", "dm-3", "sdd")
}

// LVM on LUKS on an NVMe partition.
func TestDetectLUKS(t *testing.T) {
	f := newFakeSys(t)
	f.disk("nvme0n1", "259:0")
	f.part("nvme0n1", "nvme0n1p1", "259:1")
	f.part("nvme0n1", "nvme0n1p2", "259:2")
	f.part("nvme0n1", "nvme0n1p3", "259:3")
	f.disk("nvme1n1", "259:4")
	f.part("nvme1n1", "nvme1n1p1", "259:5")
	f.disk("sda", "8:0")
	f.part("sda", "sda1", "8:1")
	f.disk("dm-0", "252:0") // dm_crypt-0 on nvme0n1p3
	f.disk("dm-1", "252:1") // vg-root on dm-0
	f.disk("dm-2", "252:2") // vg-swap on dm-0
	f.disk("dm-3", "252:3") // encrypted data on sda1
	f.stack("dm-0", "nvme0n1p3")
	f.stack("dm-1", "dm-0")
	f.stack("dm-2", "dm-0")
	f.stack("dm-3", "sda1")
	f.alias("/dev/mapper/vg-root", "dm-1")
	f.alias("/dev/mapper/vg-swap", "dm-2")
	mounts := mi(
		"30 1 252:1 / / rw,relatime shared:1 - ext4 /dev/mapper/vg-root rw",
		"95 30 259:2 / /boot rw,relatime shared:50 - ext4 /dev/nvme0n1p2 rw",
		"97 95 259:1 / /boot/efi rw,relatime shared:52 - vfat /dev/nvme0n1p1 rw",
		"98 30 252:3 / /mnt/kasa rw,relatime shared:53 - ext4 /dev/mapper/kasa rw",
	)
	s := f.sys()
	p := Detect(s, mounts, []string{"/dev/mapper/vg-swap"}, f.resolve)
	if !p.OK {
		t.Fatal("root device not determined")
	}
	expectProtected(t, s, p, "dm-0", "dm-1", "dm-2", "nvme0n1", "nvme0n1p1", "nvme0n1p2", "nvme0n1p3")
	expectFree(t, s, p, "nvme1n1", "nvme1n1p1", "sda", "sda1", "dm-3")
}

// Software RAID1 for / over two disks, EFI on each; a second array holds
// data.
func TestDetectRAID(t *testing.T) {
	f := newFakeSys(t)
	for i, d := range []string{"sda", "sdb", "sdc", "sdd"} {
		f.disk(d, "8:"+itoa(i*16))
		f.part(d, d+"1", "8:"+itoa(i*16+1))
		f.part(d, d+"2", "8:"+itoa(i*16+2))
	}
	f.disk("md0", "9:0")
	f.disk("md1", "9:1")
	f.stack("md0", "sda2", "sdb2")
	f.stack("md1", "sdc1", "sdd1")
	mounts := mi(
		"30 1 9:0 / / rw,relatime shared:1 - ext4 /dev/md0 rw",
		"97 30 8:1 / /boot/efi rw,relatime shared:52 - vfat /dev/sda1 rw",
		"98 30 9:1 / /mnt/depo rw,relatime shared:53 - xfs /dev/md1 rw",
	)
	s := f.sys()
	p := Detect(s, mounts, nil, f.resolve)
	if !p.OK {
		t.Fatal("root device not determined")
	}
	expectProtected(t, s, p, "md0", "sda", "sda1", "sda2", "sdb", "sdb1", "sdb2")
	expectFree(t, s, p, "md1", "sdc", "sdc1", "sdc2", "sdd", "sdd1", "sdd2")
}

// btrfs uses an anonymous device number (major 0) and names only one
// member as the source of a multi-device filesystem.
func TestDetectBtrfsMultiDevice(t *testing.T) {
	f := newFakeSys(t)
	for i, d := range []string{"sda", "sdb", "sdc", "sdd"} {
		f.disk(d, "8:"+itoa(i*16))
		f.part(d, d+"1", "8:"+itoa(i*16+1))
		f.part(d, d+"2", "8:"+itoa(i*16+2))
	}
	f.btrfs("11111111-2222-3333-4444-555555555555", "sda2", "sdb1")
	f.btrfs("99999999-2222-3333-4444-555555555555", "sdc1", "sdd1")
	mounts := mi(append(virtualMounts,
		"30 1 0:26 /@ / rw,relatime shared:1 - btrfs /dev/sda2 rw,subvol=/@",
		"31 30 0:26 /@home /home rw,relatime shared:2 - btrfs /dev/sda2 rw,subvol=/@home",
		"97 30 8:1 / /boot/efi rw,relatime shared:52 - vfat /dev/sda1 rw",
		"98 30 0:40 / /mnt/depo rw,relatime shared:53 - btrfs /dev/sdc1 rw",
	)...)
	s := f.sys()
	p := Detect(s, mounts, nil, f.resolve)
	if !p.OK {
		t.Fatal("root device not determined")
	}
	expectProtected(t, s, p, "sda", "sda1", "sda2", "sdb", "sdb1", "sdb2")
	expectFree(t, s, p, "sdc", "sdc1", "sdd", "sdd1")
}

// A swap file protects the filesystem that holds it.
func TestDetectSwapFile(t *testing.T) {
	f, mounts, _ := plainLayout(t)
	s := f.sys()
	p := Detect(s, mounts, []string{"/swap.img", "/mnt/data/swapfile"}, f.resolve)
	if !p.OK {
		t.Fatal("root device not determined")
	}
	expectProtected(t, s, p, "sda", "sda3", "sdb", "sdb1", "sdb2")
	expectFree(t, s, p, "sdc", "sdc1", "nvme0n1")
	if _, r := p.Protected(s, "sdb1"); r != "swap" {
		t.Errorf("reason = %q, want swap", r)
	}
}

func allNames(f *fakeSys) []string {
	var out []string
	for _, n := range f.dev {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func TestDetectFailsClosed(t *testing.T) {
	cases := []struct {
		name   string
		mounts func(base []Mount) []Mount
		swaps  []string
		noRes  bool
	}{
		{"empty mount table", func([]Mount) []Mount { return nil }, nil, false},
		{"only virtual filesystems", func([]Mount) []Mount { return mi(virtualMounts...) }, nil, false},
		{"root on overlay", func([]Mount) []Mount {
			return mi("30 1 0:50 / / rw,relatime - overlay overlay rw,lowerdir=/l,upperdir=/u,workdir=/w",
				"120 30 8:17 / /mnt/data rw - ext4 /dev/sdb1 rw")
		}, nil, false},
		{"root on nfs", func([]Mount) []Mount {
			return mi("30 1 0:51 / / rw,relatime - nfs4 10.0.0.2:/export/root rw")
		}, nil, false},
		{"root on zfs", func([]Mount) []Mount {
			return mi("30 1 0:52 / / rw,relatime - zfs rpool/ROOT/ubuntu rw")
		}, nil, false},
		{"root device unknown to sysfs", func([]Mount) []Mount {
			return mi("30 1 8:99 / / rw,relatime - ext4 /dev/sdq3 rw",
				"95 30 8:2 / /boot rw,relatime - ext4 /dev/sda2 rw")
		}, nil, false},
		{"root entry missing, others present", func([]Mount) []Mount {
			return mi("95 30 8:2 / /boot rw,relatime - ext4 /dev/sda2 rw",
				"97 95 8:1 / /boot/efi rw,relatime - vfat /dev/sda1 rw")
		}, nil, false},
		{"swap device cannot be identified", func(b []Mount) []Mount { return b }, []string{"/dev/mapper/unknown-swap"}, false},
		{"swap device, no resolver", func(b []Mount) []Mount { return b }, []string{"/dev/sda4"}, true},
		{"boot device cannot be identified", func([]Mount) []Mount {
			return mi("30 1 8:3 / / rw,relatime - ext4 /dev/sda3 rw",
				"95 30 8:98 / /boot rw,relatime - ext4 /dev/sdq2 rw")
		}, nil, false},
		{"efi device cannot be identified", func([]Mount) []Mount {
			return mi("30 1 8:3 / / rw,relatime - ext4 /dev/sda3 rw",
				"97 30 8:97 / /boot/efi rw,relatime - vfat /dev/sdq1 rw")
		}, nil, false},
		{"var on an unidentified btrfs", func([]Mount) []Mount {
			return mi("30 1 8:3 / / rw,relatime - ext4 /dev/sda3 rw",
				"97 30 0:60 / /var rw,relatime - btrfs /dev/sdq1 rw")
		}, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, base, _ := plainLayout(t)
			s := f.sys()
			resolve := Resolver(f.resolve)
			if c.noRes {
				resolve = nil
			}
			p := Detect(s, c.mounts(base), c.swaps, resolve)
			if p.OK {
				t.Fatalf("detection reported success")
			}
			for _, n := range append(allNames(f), "sdz", "md5", "dm-9") {
				if ok, _ := p.Protected(s, n); !ok {
					t.Errorf("%s is not protected although detection failed", n)
				}
			}
		})
	}
}

// The zero value (mount table unreadable) protects everything.
func TestZeroProtectionProtectsEverything(t *testing.T) {
	f, _, _ := plainLayout(t)
	var p Protection
	for _, n := range allNames(f) {
		if ok, _ := p.Protected(f.sys(), n); !ok {
			t.Errorf("%s not protected by the zero Protection", n)
		}
	}
}

// Virtual filesystems on critical paths have no disk and must not switch
// detection off.
func TestDetectIgnoresVirtualCriticalMounts(t *testing.T) {
	f, base, swaps := plainLayout(t)
	mounts := append(base, mi(
		"130 30 0:70 / /var/lib rw,relatime - tmpfs tmpfs rw",
		"131 30 0:71 / /usr/local/libexec rw,relatime - nfs4 10.0.0.2:/x rw",
	)...)
	s := f.sys()
	p := Detect(s, mounts, swaps, f.resolve)
	if !p.OK {
		t.Fatal("a tmpfs/nfs mount disabled detection")
	}
	expectProtected(t, s, p, "sda", "sda3")
	expectFree(t, s, p, "sdb", "sdb1")
}

// A mount namespace may show the root filesystem a second time (bind
// mounts); a data disk mounted over a system path becomes a system disk.
func TestDetectDataDiskMountedOnSystemPath(t *testing.T) {
	f, base, swaps := plainLayout(t)
	mounts := append(base, mi("140 30 8:33 / /var/lib/myserver rw,relatime - ext4 /dev/sdc1 rw")...)
	s := f.sys()
	p := Detect(s, mounts, swaps, f.resolve)
	expectProtected(t, s, p, "sda", "sdc", "sdc1")
	expectFree(t, s, p, "sdb", "sdb1", "nvme0n1")
}

// A device whose chain ends nowhere cannot be declared safe.
func TestProtectedUntraceableDevice(t *testing.T) {
	f, mounts, swaps := plainLayout(t)
	s := f.sys()
	p := Detect(s, mounts, swaps, f.resolve)
	for _, n := range []string{"", ".", "..", "a/b", "../sda", "sd\x00a"} {
		if ok, _ := p.Protected(s, n); !ok {
			t.Errorf("Protected(%q) = false", n)
		}
	}
}

func TestSysQueries(t *testing.T) {
	f, _, _ := plainLayout(t)
	f.disk("dm-0", "252:0")
	f.stack("dm-0", "sdb2")
	f.write(filepath.Join(f.root, "devices/sdb/size"), "7814037168\n")
	f.write(filepath.Join(f.root, "devices/sdc/ro"), "1\n")
	s := f.sys()

	if !s.Exists("sda") || !s.Exists("sda1") || s.Exists("sdz") || s.Exists("") || s.Exists("../class") || s.Exists("sda/sda1") {
		t.Error("Exists is wrong")
	}
	if s.IsPartition("sda") || !s.IsPartition("sda1") || s.IsPartition("dm-0") || s.IsPartition("nope") {
		t.Error("IsPartition is wrong")
	}
	if p, ok := s.Parent("sda3"); !ok || p != "sda" {
		t.Errorf("Parent(sda3) = %q, %v", p, ok)
	}
	if _, ok := s.Parent("sda"); ok {
		t.Error("a whole disk has a parent")
	}
	parts := s.Partitions("sda")
	sort.Strings(parts)
	if strings.Join(parts, ",") != "sda1,sda2,sda3,sda4,sda5" {
		t.Errorf("Partitions(sda) = %v", parts)
	}
	if len(s.Partitions("nvme0n1")) != 0 || len(s.Partitions("../x")) != 0 {
		t.Error("unexpected partitions")
	}
	if n, ok := s.NameByMajMin("8:17"); !ok || n != "sdb1" {
		t.Errorf("NameByMajMin(8:17) = %q, %v", n, ok)
	}
	for _, mm := range []string{"0:26", "8:200", "", "8", "a:b", "8:1/../8:2", "-1:2", "8:17:1"} {
		if n, ok := s.NameByMajMin(mm); ok {
			t.Errorf("NameByMajMin(%q) = %q", mm, n)
		}
	}
	if s.SizeBytes("sdb") != 7814037168*512 || s.SizeBytes("sdz") != 0 {
		t.Errorf("SizeBytes = %d", s.SizeBytes("sdb"))
	}
	if !s.ReadOnly("sdc") || s.ReadOnly("sdb") {
		t.Error("ReadOnly is wrong")
	}
	if h := s.Holders("sdb2"); len(h) != 1 || h[0] != "dm-0" {
		t.Errorf("Holders(sdb2) = %v", h)
	}
	if sl := s.Slaves("dm-0"); len(sl) != 1 || sl[0] != "sdb2" {
		t.Errorf("Slaves(dm-0) = %v", sl)
	}
	fam := s.Family("sdb")
	sort.Strings(fam)
	if strings.Join(fam, ",") != "dm-0,sdb,sdb1,sdb2" {
		t.Errorf("Family(sdb) = %v", fam)
	}
	disks, chain := s.BaseDisks("dm-0")
	if len(disks) != 1 || disks[0] != "sdb" || strings.Join(chain, ",") != "dm-0,sdb2,sdb" {
		t.Errorf("BaseDisks(dm-0) = %v, %v", disks, chain)
	}
	names := strings.Join(s.DiskNames(), ",")
	if names != "dm-0,nvme0n1,sda,sdb,sdc" {
		t.Errorf("DiskNames = %s", names)
	}
}

// Stacking loops (which a broken or hostile sysfs could show) terminate.
func TestSysWalksTerminateOnLoops(t *testing.T) {
	f := newFakeSys(t)
	f.disk("dm-0", "252:0")
	f.disk("dm-1", "252:1")
	f.stack("dm-0", "dm-1")
	f.stack("dm-1", "dm-0")
	s := f.sys()
	disks, chain := s.BaseDisks("dm-0")
	if len(chain) != 2 {
		t.Errorf("chain = %v", chain)
	}
	if len(s.Family("dm-0")) != 2 {
		t.Errorf("family = %v", s.Family("dm-0"))
	}
	// Nothing in the loop reaches a physical disk: never declare it safe.
	p := Protection{OK: true, Disks: map[string]string{"sda": "/"}, Nodes: map[string]string{"sda": "/"}}
	if ok, _ := p.Protected(s, "dm-0"); !ok {
		t.Errorf("a device without a physical disk is unprotected (disks %v)", disks)
	}
}

func TestMountDevice(t *testing.T) {
	f, _, _ := plainLayout(t)
	f.alias("/dev/disk/by-uuid/1234-ABCD", "sdb2")
	s := f.sys()
	cases := []struct {
		m    Mount
		want string
		ok   bool
	}{
		{Mount{MajMin: "8:17", Source: "/dev/sdb1"}, "sdb1", true},
		// The device number wins over the (spoofable) source text.
		{Mount{MajMin: "8:17", Source: "/dev/sda1"}, "sdb1", true},
		{Mount{MajMin: "0:26", Source: "/dev/sda3"}, "sda3", true},
		{Mount{MajMin: "0:26", Source: "/dev/disk/by-uuid/1234-ABCD"}, "sdb2", true},
		{Mount{MajMin: "0:26", Source: "tmpfs"}, "", false},
		{Mount{MajMin: "0:26", Source: "sda3"}, "", false},
		{Mount{MajMin: "0:26", Source: "/dev/unknown"}, "", false},
	}
	for _, c := range cases {
		got, ok := MountDevice(s, c.m, f.resolve)
		if got != c.want || ok != c.ok {
			t.Errorf("MountDevice(%+v) = %q, %v; want %q, %v", c.m, got, ok, c.want, c.ok)
		}
	}
	if _, ok := MountDevice(s, Mount{MajMin: "0:26", Source: "/dev/sda3"}, nil); ok {
		t.Error("resolved without a resolver")
	}
}

// ResolvePath and VerifyNode only accept real block device nodes.
func TestResolvePathRejectsNonDevices(t *testing.T) {
	f, _, _ := plainLayout(t)
	s := f.sys()
	dir := t.TempDir()
	file := filepath.Join(dir, "sdb")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{file, dir, filepath.Join(dir, "missing"), "", "/dev/null", "/proc/self/mountinfo"} {
		if n, ok := s.ResolvePath(p); ok {
			t.Errorf("ResolvePath(%q) = %q", p, n)
		}
	}
	for _, n := range []string{"", "..", "../etc/passwd", "sdz", "null", "a/b"} {
		if p, ok := s.VerifyNode(n); ok {
			t.Errorf("VerifyNode(%q) = %q", n, p)
		}
	}
	// Known to the fake sysfs under a device number no real sdb has, so
	// whatever /dev/sdb is on the machine running the test, it is not that
	// device.
	f2 := newFakeSys(t)
	f2.disk("sdb", "251:77")
	if p, ok := f2.sys().VerifyNode("sdb"); ok {
		t.Errorf("VerifyNode(sdb) = %q although /dev/sdb does not match the fake sysfs", p)
	}
}

func TestLoadStateFailsWithoutMountTable(t *testing.T) {
	f, _, _ := plainLayout(t)
	p, mounts, _, err := LoadState(f.sys(), filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatal("no error for a missing mount table")
	}
	if p.OK || len(mounts) != 0 {
		t.Error("state returned together with an error")
	}
	if ok, _ := p.Protected(f.sys(), "sdb"); !ok {
		t.Error("device not protected after a failed load")
	}
}

func TestLockIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	unlock, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(path); err == nil {
		t.Error("second lock succeeded while the first is held")
	}
	unlock()
	unlock2, err := Lock(path)
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	unlock2()
	// A symbolic link in place of the lock file is refused.
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(link); err == nil {
		t.Error("lock followed a symbolic link")
	}
}
