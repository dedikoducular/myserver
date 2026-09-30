package system

import (
	"reflect"
	"testing"
)

func mountsOf(entries []mountEntry) []string {
	out := []string{}
	for _, e := range entries {
		out = append(out, e.mount+" "+e.fstype+" "+e.device)
	}
	return out
}

func findMount(entries []mountEntry, mount string) (mountEntry, bool) {
	for _, e := range entries {
		if e.mount == mount {
			return e, true
		}
	}
	return mountEntry{}, false
}

// captured inside an ubuntu:24.04 container; the overlay options are
// shortened.
const mountInfoContainer = `486 360 0:84 / / rw,relatime - overlay overlay rw,lowerdir=/var/lib/desktop-containerd/daemon/io.containerd.snapshotter.v1.overlayfs/snapshots/2415/fs,upperdir=/var/lib/desktop-containerd/snapshots/2416/fs,workdir=/var/lib/desktop-containerd/snapshots/2416/work
488 486 0:100 / /proc rw,nosuid,nodev,noexec,relatime - proc proc rw
489 486 0:101 / /dev rw,nosuid - tmpfs tmpfs rw,size=65536k,mode=755
490 489 0:102 / /dev/pts rw,nosuid,noexec,relatime - devpts devpts rw,gid=5,mode=620,ptmxmode=666
491 486 0:103 / /sys ro,nosuid,nodev,noexec,relatime - sysfs sysfs ro
492 491 0:23 / /sys/fs/cgroup ro,nosuid,nodev,noexec,relatime - cgroup2 cgroup rw
493 489 0:98 / /dev/mqueue rw,nosuid,nodev,noexec,relatime - mqueue mqueue rw
494 489 0:104 / /dev/shm rw,nosuid,nodev,noexec,relatime - tmpfs shm rw,size=65536k
495 486 8:64 /data/docker/containers/7aeb3f90ee0d/resolv.conf /etc/resolv.conf rw,relatime - ext4 /dev/sde rw
496 486 8:64 /data/docker/containers/7aeb3f90ee0d/hostname /etc/hostname rw,relatime - ext4 /dev/sde rw
497 486 8:64 /data/docker/containers/7aeb3f90ee0d/hosts /etc/hosts rw,relatime - ext4 /dev/sde rw
373 488 0:100 /bus /proc/bus ro,nosuid,nodev,noexec,relatime - proc proc rw
378 488 0:105 / /proc/acpi ro,relatime - tmpfs tmpfs ro,size=4k,nr_inodes=1
379 488 0:101 /null /proc/interrupts rw,nosuid - tmpfs tmpfs rw,size=65536k,mode=755
385 491 0:105 / /sys/firmware ro,relatime - tmpfs tmpfs ro,size=4k,nr_inodes=1
`

// The smoke test showed "Kök disk okunamadı" inside a container because the
// overlay root was skipped as a pseudo filesystem.
func TestParseMountInfoContainerRootIsReported(t *testing.T) {
	got := parseMountInfo([]byte(mountInfoContainer))
	root, ok := findMount(got, "/")
	if !ok {
		t.Fatalf("the filesystem mounted at / is missing: %v", mountsOf(got))
	}
	if root.fstype != "overlay" || root.device != "overlay" {
		t.Errorf("root = %+v", root)
	}
	for _, e := range got {
		if _, pseudo := pseudoFS[e.fstype]; pseudo && e.mount != "/" {
			t.Errorf("pseudo filesystem reported: %+v", e)
		}
	}
}

// from memory: a typical Ubuntu Server 24.04 host with Docker, snaps, an
// EFI partition, a data disk whose mount point contains a space, and an
// NFS share.
const mountInfoHost = `24 31 0:22 / /sys rw,nosuid,nodev,noexec,relatime shared:7 - sysfs sysfs rw
25 31 0:23 / /proc rw,nosuid,nodev,noexec,relatime shared:12 - proc proc rw
26 31 0:5 / /dev rw,nosuid,relatime shared:2 - devtmpfs udev rw,size=3964356k,nr_inodes=991089,mode=755,inode64
27 26 0:24 / /dev/pts rw,nosuid,noexec,relatime shared:3 - devpts devpts rw,gid=5,mode=620,ptmxmode=000
28 31 0:25 / /run rw,nosuid,nodev,noexec,relatime shared:5 - tmpfs tmpfs rw,size=800164k,mode=755,inode64
31 1 8:2 / / rw,relatime shared:1 - ext4 /dev/sda2 rw
32 24 0:6 / /sys/kernel/security rw,nosuid,nodev,noexec,relatime shared:8 - securityfs securityfs rw
33 26 0:27 / /dev/shm rw,nosuid,nodev shared:4 - tmpfs tmpfs rw,inode64
35 24 0:29 / /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime shared:9 - cgroup2 cgroup2 rw,nsdelegate,memory_recursiveprot
38 25 0:32 / /proc/sys/fs/binfmt_misc rw,relatime shared:13 - autofs systemd-1 rw,fd=29,pgrp=1,timeout=0,minproto=5,maxproto=5,direct
90 31 7:0 / /snap/core22/1621 ro,nodev,relatime shared:47 - squashfs /dev/loop0 ro,errors=continue
91 31 7:1 / /snap/snapd/21759 ro,nodev,relatime shared:49 - squashfs /dev/loop1 ro,errors=continue
95 31 8:1 / /boot/efi rw,relatime shared:51 - vfat /dev/sda1 rw,fmask=0077,dmask=0077,codepage=437,iocharset=iso8859-1,shortname=mixed,errors=remount-ro
96 31 8:17 / /mnt/My\040Disk rw,relatime shared:53 - ext4 /dev/sdb1 rw
97 31 8:17 /media /srv/media rw,relatime shared:53 - ext4 /dev/sdb1 rw
98 31 0:45 / /mnt/nas rw,relatime shared:60 - nfs4 192.168.1.10:/export/share rw,vers=4.2,rsize=1048576,wsize=1048576
99 31 0:46 / /mnt/merged rw,relatime shared:61 - overlay overlay rw,lowerdir=/mnt/a,upperdir=/mnt/b,workdir=/mnt/w
120 31 0:50 / /var/lib/docker/overlay2/0a1b2c/merged rw,relatime shared:70 - overlay overlay rw,lowerdir=/var/lib/docker/overlay2/l/AAA,upperdir=/var/lib/docker/overlay2/0a1b2c/diff,workdir=/var/lib/docker/overlay2/0a1b2c/work
121 28 0:4 net:[4026532598] /run/docker/netns/1f2e3d rw shared:71 - nsfs nsfs rw
130 28 0:55 / /run/user/1000 rw,nosuid,nodev,relatime shared:80 - tmpfs tmpfs rw,size=800160k,nr_inodes=200040,mode=700,uid=1000,gid=1000,inode64
`

func TestParseMountInfoHost(t *testing.T) {
	got := mountsOf(parseMountInfo([]byte(mountInfoHost)))
	want := []string{
		"/ ext4 /dev/sda2",
		"/boot/efi vfat /dev/sda1",
		"/mnt/My Disk ext4 /dev/sdb1",
		"/mnt/nas nfs4 192.168.1.10:/export/share",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestParseMountInfoOtherOverlayStillSkipped(t *testing.T) {
	in := "31 1 8:2 / / rw,relatime shared:1 - ext4 /dev/sda2 rw\n" +
		"99 31 0:46 / /mnt/merged rw,relatime shared:61 - overlay overlay rw,lowerdir=/mnt/a\n" +
		"100 31 0:47 / /home/u/merged rw,relatime - overlay overlay rw,lowerdir=/mnt/a\n"
	got := mountsOf(parseMountInfo([]byte(in)))
	if want := []string{"/ ext4 /dev/sda2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestParseMountInfoRootOfAnyType(t *testing.T) {
	cases := map[string]struct {
		in   string
		want []string
	}{
		// from memory: Ubuntu on ZFS.
		"zfs": {
			"30 1 0:26 / / rw,relatime shared:1 - zfs rpool/ROOT/ubuntu_abc123 rw,xattr,posixacl\n" +
				"40 30 0:40 / /home rw,relatime shared:20 - zfs rpool/USERDATA/home_abc123 rw,xattr,posixacl\n" +
				"41 30 0:41 / /boot rw,relatime shared:21 - zfs bpool/BOOT/ubuntu_abc123 rw,nodev,xattr,posixacl\n",
			[]string{"/ zfs rpool/ROOT/ubuntu_abc123", "/boot zfs bpool/BOOT/ubuntu_abc123", "/home zfs rpool/USERDATA/home_abc123"},
		},
		// from memory: btrfs subvolumes of one device collapse to one entry.
		"btrfs subvolumes": {
			"40 30 0:31 /@home /home rw,relatime shared:20 - btrfs /dev/nvme0n1p2 rw,ssd,subvolid=257,subvol=/@home\n" +
				"30 1 0:30 /@ / rw,relatime shared:1 - btrfs /dev/nvme0n1p2 rw,ssd,subvolid=256,subvol=/@\n" +
				"41 30 0:32 /@snapshots /.snapshots rw,relatime shared:21 - btrfs /dev/nvme0n1p2 rw,ssd,subvolid=258,subvol=/@snapshots\n",
			[]string{"/ btrfs /dev/nvme0n1p2"},
		},
		"live system on squashfs": {
			"30 1 7:0 / / ro,relatime - squashfs /dev/loop0 ro\n",
			[]string{"/ squashfs /dev/loop0"},
		},
		"tmpfs root": {
			"30 1 0:2 / / rw - tmpfs tmpfs rw\n" + "31 30 0:22 / /sys rw - sysfs sysfs rw\n",
			[]string{"/ tmpfs tmpfs"},
		},
		// The initramfs root is covered by the real root mounted later.
		"rootfs covered by the real root": {
			"1 1 0:2 / / rw - rootfs rootfs rw\n" + "30 1 8:2 / / rw,relatime - ext4 /dev/sda2 rw\n",
			[]string{"/ ext4 /dev/sda2"},
		},
	}
	for name, c := range cases {
		// Map iteration must not influence the result.
		for i := 0; i < 50; i++ {
			if got := mountsOf(parseMountInfo([]byte(c.in))); !reflect.DeepEqual(got, c.want) {
				t.Errorf("%s: got %q, want %q", name, got, c.want)
				break
			}
		}
	}
}

func TestParseMountInfoDuplicateDevices(t *testing.T) {
	// The same device bound to several places is one disk, reported at its
	// shortest mount point, wherever that line stands in the file.
	in := "50 31 8:17 /sub /srv/very/long/path rw - ext4 /dev/sdb1 rw\n" +
		"51 31 8:17 / /data rw - ext4 /dev/sdb1 rw\n" +
		"52 31 8:17 /x /data/x rw - ext4 /dev/sdb1 rw\n" +
		// No /dev/ device name: grouped by device number.
		"60 31 0:60 / /mnt/share/deep rw - cifs //nas/share rw\n" +
		"61 31 0:60 / /mnt/share rw - cifs //nas/share rw\n"
	got := mountsOf(parseMountInfo([]byte(in)))
	want := []string{"/data ext4 /dev/sdb1", "/mnt/share cifs //nas/share"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestParseMountInfoOvermountIsDeterministic(t *testing.T) {
	// Two different devices on one mount point: the later one is visible.
	in := "50 31 8:17 / /data rw - ext4 /dev/sdb1 rw\n" +
		"51 31 8:33 / /data rw - xfs /dev/sdc1 rw\n"
	for i := 0; i < 100; i++ {
		got := mountsOf(parseMountInfo([]byte(in)))
		if want := []string{"/data xfs /dev/sdc1"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d: got %q, want %q", i, got, want)
		}
	}
}

func TestParseMountInfoOptionalFieldsAndGarbage(t *testing.T) {
	in := "garbage line\n" +
		"\n" +
		"1 2 3 - ext4\n" +
		// several optional fields before the separator
		"51 31 8:17 / /data rw,relatime shared:5 master:2 propagate_from:3 - ext4 /dev/sdb1 rw\n" +
		// no optional field at all
		"52 31 8:33 / /backup rw,relatime - xfs /dev/sdc1 rw\n"
	got := mountsOf(parseMountInfo([]byte(in)))
	want := []string{"/backup xfs /dev/sdc1", "/data ext4 /dev/sdb1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestParseMountInfoPrefixIsWholeComponent(t *testing.T) {
	// /running and /devices are not under /run and /dev.
	in := "51 31 8:17 / /running rw - ext4 /dev/sdb1 rw\n" +
		"52 31 8:33 / /devices rw - ext4 /dev/sdc1 rw\n" +
		"53 31 8:49 / /run/media/disk rw - ext4 /dev/sdd1 rw\n"
	got := mountsOf(parseMountInfo([]byte(in)))
	want := []string{"/devices ext4 /dev/sdc1", "/running ext4 /dev/sdb1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestUnescapeMount(t *testing.T) {
	cases := map[string]string{
		`/mnt/plain`:          "/mnt/plain",
		`/mnt/My\040Disk`:     "/mnt/My Disk",
		`/mnt/a\040b\040c`:    "/mnt/a b c",
		`/mnt/tab\011here`:    "/mnt/tab\there",
		`/mnt/new\012line`:    "/mnt/new\nline",
		`/mnt/back\134slash`:  `/mnt/back\slash`,
		`/mnt/end\040`:        "/mnt/end ",
		`\040`:                " ",
		`/mnt/truncated\04`:   `/mnt/truncated\04`,
		`/mnt/notoctal\09x`:   `/mnt/notoctal\09x`,
		`/mnt/trailing\`:      `/mnt/trailing\`,
		`/mnt/Yedek\040Diski`: "/mnt/Yedek Diski",
		"/mnt/çıktı":          "/mnt/çıktı",
	}
	for in, want := range cases {
		if got := unescapeMount(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestRootDisk(t *testing.T) {
	disks := []Disk{{Mount: "/boot"}, {Mount: "/", FSType: "overlay", Percent: 12}, {Mount: "/data"}}
	r := rootDisk(disks)
	if r == nil || r.FSType != "overlay" {
		t.Fatalf("got %+v", r)
	}
	r.Percent = 99
	if disks[1].Percent != 12 {
		t.Error("rootDisk must return a copy")
	}
	if rootDisk([]Disk{{Mount: "/data"}}) != nil || rootDisk(nil) != nil {
		t.Error("no root expected")
	}
}
