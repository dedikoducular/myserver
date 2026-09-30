package storagecheck

import (
	"reflect"
	"testing"
)

// Written from knowledge of the proc(5) formats; not captured from a live
// system.
const mountInfoFixture = `24 30 0:22 / /sys rw,nosuid,nodev,noexec,relatime shared:7 - sysfs sysfs rw
25 30 0:23 / /proc rw,nosuid,nodev,noexec,relatime shared:13 - proc proc rw
30 1 8:3 / / rw,relatime shared:1 - ext4 /dev/sda3 rw,errors=remount-ro
95 30 8:2 / /boot rw,relatime shared:50 - ext4 /dev/sda2 rw
97 95 8:1 / /boot/efi rw,relatime shared:52 - vfat /dev/sda1 rw,fmask=0077,dmask=0077
120 30 8:17 / /mnt/my\040disk ro,nosuid,nodev,relatime shared:60 master:3 propagate_from:4 - ext4 /dev/sdb1 ro
121 30 0:45 /@home /home rw,relatime - btrfs /dev/sdc1 rw,subvol=/@home
122 30 0:50 / /run/user/1000 rw,nosuid,nodev - tmpfs tmpfs rw,size=1024k
123 30 8:33 / /media/usb rw,nosuid,nodev,noexec - fuseblk /dev/sdd1 rw,user_id=0
broken line
124 30 8:49
`

func TestParseMountInfo(t *testing.T) {
	ms := ParseMountInfo([]byte(mountInfoFixture))
	if len(ms) != 9 {
		t.Fatalf("got %d mounts, want 9", len(ms))
	}
	want := Mount{MajMin: "8:3", Root: "/", MountPoint: "/", Options: "rw,relatime", FSType: "ext4", Source: "/dev/sda3"}
	if ms[2] != want {
		t.Errorf("root = %+v, want %+v", ms[2], want)
	}
	// Several optional fields before the separator, and an escaped space.
	d := ms[5]
	if d.MountPoint != "/mnt/my disk" || d.FSType != "ext4" || d.Source != "/dev/sdb1" || !d.ReadOnly() {
		t.Errorf("data mount = %+v", d)
	}
	if ms[2].ReadOnly() {
		t.Error("rw mount reported read-only")
	}
	// No optional field at all.
	if b := ms[6]; b.Root != "/@home" || b.FSType != "btrfs" || b.Source != "/dev/sdc1" || b.MajMin != "0:45" {
		t.Errorf("btrfs mount = %+v", b)
	}
	if len(ParseMountInfo(nil)) != 0 {
		t.Error("empty input produced mounts")
	}
}

func TestHasOption(t *testing.T) {
	if !HasOption("rw,ro,x", "ro") || HasOption("rw,errors=remount-ro", "ro") || HasOption("", "ro") || HasOption("rox", "ro") {
		t.Error("HasOption matches substrings or misses options")
	}
}

func TestUnescape(t *testing.T) {
	cases := map[string]string{
		`/mnt/a`:          "/mnt/a",
		`/mnt/a\040b`:     "/mnt/a b",
		`/mnt/a\011b`:     "/mnt/a\tb",
		`/mnt/a\134b`:     `/mnt/a\b`,
		`/mnt/a\040`:      "/mnt/a ",
		`\040`:            " ",
		`/mnt/a\04`:       `/mnt/a\04`,
		`/mnt/a\`:         `/mnt/a\`,
		`/mnt/a\xyz`:      `/mnt/a\xyz`,
		`/mnt/a\040\040b`: "/mnt/a  b",
	}
	for in, want := range cases {
		if got := unescape(in); got != want {
			t.Errorf("unescape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseSwaps(t *testing.T) {
	in := "Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n" +
		"/dev/sda4                               partition\t8388604\t\t0\t\t-2\n" +
		"/dev/dm-1                               partition\t4194300\t\t1024\t\t-3\n" +
		"/swap.img                               file\t\t4194300\t\t0\t\t-4\n" +
		"/var/my\\040swap                        file\t\t1024\t\t0\t\t-5\n" +
		"/old.img\\040(deleted)                  file\t\t1024\t\t0\t\t-6\n" +
		"\n"
	got := ParseSwaps([]byte(in))
	want := []string{"/dev/sda4", "/dev/dm-1", "/swap.img", "/var/my swap", "/old.img"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseSwaps = %q, want %q", got, want)
	}
	if len(ParseSwaps(nil)) != 0 || len(ParseSwaps([]byte("Filename Type Size Used Priority\n"))) != 0 {
		t.Error("no swap expected")
	}
}

func TestContainingMount(t *testing.T) {
	ms := ParseMountInfo([]byte(mountInfoFixture))
	cases := map[string]string{
		"/":                  "/",
		"/etc":               "/",
		"/boot":              "/boot",
		"/boot/grub":         "/boot",
		"/boot/efi":          "/boot/efi",
		"/boot/efi/EFI":      "/boot/efi",
		"/bootx":             "/",
		"/home/ali/file":     "/home",
		"/mnt/my disk/x":     "/mnt/my disk",
		"/mnt/my diskette":   "/",
		"/var/lib/myserver":  "/",
		"/usr/local/libexec": "/",
	}
	for path, want := range cases {
		m, ok := ContainingMount(ms, path)
		if !ok || m.MountPoint != want {
			t.Errorf("ContainingMount(%q) = %q, %v; want %q", path, m.MountPoint, ok, want)
		}
	}
	if _, ok := ContainingMount(nil, "/"); ok {
		t.Error("found a mount in an empty table")
	}
	// A later mount on the same mount point hides the earlier one.
	over := []Mount{
		{MajMin: "8:3", MountPoint: "/", Source: "/dev/sda3"},
		{MajMin: "8:17", MountPoint: "/data", Source: "/dev/sdb1"},
		{MajMin: "8:33", MountPoint: "/data", Source: "/dev/sdc1"},
	}
	if m, _ := ContainingMount(over, "/data/x"); m.Source != "/dev/sdc1" {
		t.Errorf("shadowed mount returned: %+v", m)
	}
}

func TestMergeMounts(t *testing.T) {
	a := []Mount{{MajMin: "8:3", MountPoint: "/"}, {MajMin: "8:2", MountPoint: "/boot"}}
	b := []Mount{{MajMin: "8:3", MountPoint: "/"}, {MajMin: "8:17", MountPoint: "/mnt/data"}}
	got := MergeMounts(a, b)
	if len(got) != 3 || got[0].MountPoint != "/" || got[2].MountPoint != "/mnt/data" {
		t.Errorf("MergeMounts = %+v", got)
	}
}

func TestIsCriticalMountPoint(t *testing.T) {
	for _, p := range []string{"/", "/boot", "/boot/efi", "/efi", "/usr", "/etc", "/var", "/var/lib", "/var/lib/myserver"} {
		if !IsCriticalMountPoint(p) {
			t.Errorf("%s must be critical", p)
		}
	}
	for _, p := range []string{"/mnt/data", "/media/usb", "/home", "", "/boot/", "/srv"} {
		if IsCriticalMountPoint(p) {
			t.Errorf("%s must not be critical", p)
		}
	}
}
