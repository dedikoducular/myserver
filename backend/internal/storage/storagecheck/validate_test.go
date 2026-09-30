package storagecheck

import (
	"strings"
	"testing"
)

// hostile is a set of values no validator may ever accept.
var hostile = []string{
	"-o", "--foo", "-", "--", "-rf", "/dev/sdb", "/", "../sdb", "..", ".", "a/b", `a\b`,
	"sdb 1", " sdb", "sdb ", "sdb\t1", "sdb\n", "\nsdb", "sdb\r", "sdb\x00", "sdb\x00x",
	"sdb;reboot", "sdb|x", "sdb&", "$(id)", "`id`", "sdb$x", "sdb>x", "sdb<x", "sdb*", "sdb?",
	"sdb'", `sdb"`, "sdb(1)", "sdb{1}", "sdb!", "sdb#", "sdb=1", "sdb,1", "sdb:1", "sdb%1",
	"şdb", "sdb ", "sdb‮",
}

func TestValidDevice(t *testing.T) {
	valid := []string{
		"sda", "sdb", "sdz", "sdaa", "sdb1", "sdb15", "sda128", "vda", "vdb2", "xvda", "xvdf1",
		"nvme0n1", "nvme0n1p1", "nvme1n2p15", "nvme10n1", "mmcblk0", "mmcblk0p1", "mmcblk1p12",
	}
	for _, v := range valid {
		if !ValidDevice(v) {
			t.Errorf("ValidDevice(%q) = false, want true", v)
		}
		if !ValidMountDevice(v) {
			t.Errorf("ValidMountDevice(%q) = false, want true", v)
		}
	}
	invalid := append([]string{
		"", "sd", "SDA", "Sda", "sda1234", "sdabcde", "hda", "loop0", "sr0", "fd0", "ram0", "zram0",
		"md0", "dm-0", "dm-3", "md127p1", "nvme0", "nvme0n", "nvme0n1p", "nvme0n1pp1", "nvmen1",
		"mmcblk", "mmcblk0p", "mmcblk0boot0", "sda-1", "sda_1", "sda.1", "mapper/vg-root",
		"sda1 ", "sda1\n", "1sda", strings.Repeat("a", 300),
	}, hostile...)
	for _, v := range invalid {
		if ValidDevice(v) {
			t.Errorf("ValidDevice(%q) = true, want false", v)
		}
	}
}

func TestValidDiskName(t *testing.T) {
	for _, v := range []string{"sda", "sdab", "vdc", "xvdb", "nvme0n1", "nvme12n3", "mmcblk0"} {
		if !ValidDiskName(v) {
			t.Errorf("ValidDiskName(%q) = false, want true", v)
		}
	}
	for _, v := range append([]string{"", "sda1", "nvme0n1p1", "mmcblk0p1", "md0", "dm-0", "loop0", "sr0"}, hostile...) {
		if ValidDiskName(v) {
			t.Errorf("ValidDiskName(%q) = true, want false", v)
		}
	}
}

func TestValidMountDevice(t *testing.T) {
	for _, v := range []string{"md0", "md127", "md0p1", "dm-0", "dm-12", "sdb1"} {
		if !ValidMountDevice(v) {
			t.Errorf("ValidMountDevice(%q) = false, want true", v)
		}
	}
	for _, v := range append([]string{"", "md", "mdp1", "dm-", "dm0", "dm--1", "dm-1234567", "md12345", "loop0", "mapper/x", "zd0"}, hostile...) {
		if ValidMountDevice(v) {
			t.Errorf("ValidMountDevice(%q) = true, want false", v)
		}
	}
}

func TestValidMountName(t *testing.T) {
	for _, v := range []string{"a", "data", "Data_1", "yedek-disk", "0", "x" + strings.Repeat("y", 47)} {
		if !ValidMountName(v) {
			t.Errorf("ValidMountName(%q) = false, want true", v)
		}
	}
	for _, v := range append([]string{"", "_data", "-data", ".hidden", "data.", "my data", "x" + strings.Repeat("y", 48), "veri\n", "çöp"}, hostile...) {
		if ValidMountName(v) {
			t.Errorf("ValidMountName(%q) = true, want false", v)
		}
	}
}

func TestManagedMountPoint(t *testing.T) {
	for _, v := range []string{"/mnt/data", "/media/usb-1", "/mnt/0"} {
		if !ManagedMountPoint(v) {
			t.Errorf("ManagedMountPoint(%q) = false, want true", v)
		}
	}
	for _, v := range []string{
		"", "/", "/mnt", "/mnt/", "/media", "/mnt/a/b", "/mnt/../etc", "/mnt/..", "/mnt/.", "/mnt//data",
		"/mnt/data/", "mnt/data", "/mntx/data", "/home/data", "/mnt/-o", "/mnt/da ta", "/mnt/data\n",
		"/mnt/data\x00", "/etc", "/boot", "/media/../mnt/x",
	} {
		if ManagedMountPoint(v) {
			t.Errorf("ManagedMountPoint(%q) = true, want false", v)
		}
	}
}

func TestValidUUID(t *testing.T) {
	for _, v := range []string{
		"1234-ABCD", "abcd-1234", "0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9",
		"0A1B2C3D-4E5F-6071-8293-A4B5C6D7E8F9", "5E2A3B1C4D5E6F70", "2024-05-01-12-30-00-00",
	} {
		if !ValidUUID(v) {
			t.Errorf("ValidUUID(%q) = false, want true", v)
		}
	}
	for _, v := range append([]string{
		"", "abc", "-234-ABCD", "--foo", "-o", "1234-ABCG", "1234 ABCD", "1234-ABCD\n", "1234/ABCD",
		"0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9a", `"1234-ABCD"`, "UUID=1234-ABCD", "../1234",
		"1234-abcd;id", "1234_abcd",
	}, hostile...) {
		if ValidUUID(v) {
			t.Errorf("ValidUUID(%q) = true, want false", v)
		}
	}
}

func TestMountableFS(t *testing.T) {
	for _, v := range []string{"ext4", "ext3", "ext2", "xfs", "btrfs", "vfat", "exfat", "ntfs", "ntfs3"} {
		if !MountableFS(v) {
			t.Errorf("MountableFS(%q) = false, want true", v)
		}
	}
	for _, v := range append([]string{
		"", "EXT4", "ext4 ", "swap", "LVM2_member", "crypto_LUKS", "linux_raid_member", "zfs_member",
		"squashfs", "iso9660", "nfs", "cifs", "fuse", "fuseblk", "tmpfs", "proc", "overlay", "auto",
		"ext4,xfs", "noext4", "ext4\n",
	}, hostile...) {
		if MountableFS(v) {
			t.Errorf("MountableFS(%q) = true, want false", v)
		}
	}
}

func TestLookupFormatFS(t *testing.T) {
	for _, v := range []string{"ext4", "xfs", "btrfs", "exfat", "vfat"} {
		fs, ok := LookupFormatFS(v)
		if !ok || fs.Type != v {
			t.Errorf("LookupFormatFS(%q) = %+v, %v", v, fs, ok)
			continue
		}
		if !strings.HasPrefix(fs.Binary, "/usr/sbin/mkfs.") || strings.ContainsAny(fs.Binary, " \t") {
			t.Errorf("%s: binary %q is not an absolute mkfs path", v, fs.Binary)
		}
	}
	for _, v := range append([]string{"", "ntfs", "ext3", "EXT4", "swap", "ext4 ", "ext4\n", "ext4 -F"}, hostile...) {
		if _, ok := LookupFormatFS(v); ok {
			t.Errorf("LookupFormatFS(%q) accepted", v)
		}
	}
}

func TestValidLabel(t *testing.T) {
	for _, f := range FormatFilesystems {
		for _, v := range []string{"", "a", "DATA", "veri_1", "a-b", strings.Repeat("x", f.MaxLabel)} {
			if !ValidLabel(f, v) {
				t.Errorf("%s: ValidLabel(%q) = false, want true", f.Type, v)
			}
		}
		for _, v := range append([]string{
			strings.Repeat("x", f.MaxLabel+1), "my disk", "a.b", "a/b", "etiket\n", "çöp", "-L", "-n", "-a",
		}, hostile...) {
			if ValidLabel(f, v) {
				t.Errorf("%s: ValidLabel(%q) = true, want false", f.Type, v)
			}
		}
	}
}

func TestMkfsArgsEndWithSeparatorAndDevice(t *testing.T) {
	for _, f := range FormatFilesystems {
		for _, label := range []string{"", "data"} {
			args := MkfsArgs(f, label, "/dev/sdb1")
			n := len(args)
			if n < 2 || args[n-2] != "--" || args[n-1] != "/dev/sdb1" {
				t.Fatalf("%s: args %q do not end with -- /dev/sdb1", f.Type, args)
			}
			sep := 0
			for _, a := range args {
				if a == "--" {
					sep++
				}
				if a == "" {
					t.Errorf("%s: empty argument in %q", f.Type, args)
				}
			}
			if sep != 1 {
				t.Errorf("%s: %d separators in %q", f.Type, sep, args)
			}
			joined := strings.Join(args, " ")
			if label == "" && (strings.Contains(joined, "-L") || strings.Contains(joined, "-n")) {
				t.Errorf("%s: label option without label: %q", f.Type, args)
			}
			if label != "" {
				opt := "-L"
				want := label
				if f.Type == "vfat" {
					opt, want = "-n", strings.ToUpper(label)
				}
				found := false
				for i := 0; i+1 < n-2; i++ {
					if args[i] == opt && args[i+1] == want {
						found = true
					}
				}
				if !found {
					t.Errorf("%s: label not passed as %s %s in %q", f.Type, opt, want, args)
				}
			}
		}
	}
}

func TestDeriveMountNameIsAlwaysValidOrDevice(t *testing.T) {
	cases := []struct{ label, uuid, dev, want string }{
		{"Data", "1234-ABCD", "sdb1", "Data"},
		{"My Disk", "1234-ABCD", "sdb1", "My-Disk"},
		{"  yedek.2024 ", "", "sdb1", "yedek-2024"},
		{"", "1234-ABCD", "sdb1", "1234-ABCD"},
		{"", "", "sdb1", "sdb1"},
		{"../../etc", "", "sdb1", "etc"},
		{"--force", "", "sdb1", "force"},
		{"çöp", "", "sdb1", "p"},
		{"///", "1234-ABCD", "sdb1", "1234-ABCD"},
		{"$(reboot)", "", "sdb1", "reboot"},
		{strings.Repeat("a", 80), "", "sdb1", strings.Repeat("a", 48)},
		{"-", "-bad-uuid", "sdb1", "sdb1"},
	}
	for _, c := range cases {
		got := DeriveMountName(c.label, c.uuid, c.dev)
		if got != c.want {
			t.Errorf("DeriveMountName(%q,%q,%q) = %q, want %q", c.label, c.uuid, c.dev, got, c.want)
		}
		if !ValidMountName(got) {
			t.Errorf("DeriveMountName(%q,%q,%q) = %q is not a valid mount name", c.label, c.uuid, c.dev, got)
		}
	}
}

func TestPartitionName(t *testing.T) {
	cases := []struct {
		disk string
		n    int
		want string
	}{
		{"sdb", 1, "sdb1"}, {"vda", 2, "vda2"}, {"nvme0n1", 1, "nvme0n1p1"}, {"mmcblk0", 12, "mmcblk0p12"},
	}
	for _, c := range cases {
		got := PartitionName(c.disk, c.n)
		if got != c.want {
			t.Errorf("PartitionName(%q,%d) = %q, want %q", c.disk, c.n, got, c.want)
		}
		if !ValidDevice(got) {
			t.Errorf("PartitionName result %q is not a valid device", got)
		}
	}
}

func TestValidSerial(t *testing.T) {
	for _, v := range []string{"", "WD-WCC4E1234567", "S3Z9NB0K123456A", "0123 4567 89AB", strings.Repeat("x", 128)} {
		if !ValidSerial(v) {
			t.Errorf("ValidSerial(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"a\nb", "a\x00b", "a\tb", "a\x7fb", "a\rb", strings.Repeat("x", 129)} {
		if ValidSerial(v) {
			t.Errorf("ValidSerial(%q) = true, want false", v)
		}
	}
}

func TestMountOptionsAreSafe(t *testing.T) {
	for _, removable := range []bool{false, true} {
		o := MountOptions(removable)
		if !HasOption(o, "nosuid") || !HasOption(o, "nodev") {
			t.Errorf("MountOptions(%v) = %q lacks nosuid/nodev", removable, o)
		}
	}
	if !HasOption(MountOptions(true), "noexec") {
		t.Error("removable media must be mounted noexec")
	}
	if MountBase(true) != "/media" || MountBase(false) != "/mnt" {
		t.Error("unexpected mount bases")
	}
}
