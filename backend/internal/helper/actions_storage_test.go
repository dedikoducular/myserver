package helper

// Tests of the storage helper actions. Only argument validation is
// exercised: every call below carries at least one invalid argument, so
// no action ever gets as far as touching a device. mount, umount, mkfs,
// wipefs, sfdisk and smartctl are never run.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// storageHostile are values that must never be accepted as a device name,
// mount name, UUID, label, filesystem type or mode.
var storageHostile = []string{
	"-o", "--foo", "-", "--", "-rf", "--help", "-t", "/dev/sdb", "/", "../sdb", "..", ".", "a/b", `a\b`,
	"sdb 1", " sdb", "sdb ", "sdb\t1", "sdb\n", "\nsdb", "sdb\r", "sdb\x00", "sdb;reboot", "sdb|x",
	"sdb&", "$(id)", "`id`", "sdb$x", "sdb>x", "sdb*", "sdb?", "sdb'", `sdb"`, "sdb,ro", "sdb=1",
	"şdb", strings.Repeat("a", 5000),
}

const (
	storageGoodUUID   = "77777777-2222-3333-4444-555555555555"
	storageGoodSerial = "WD-WCC7K1234567"
	storageGoodSize   = "4000787030016"
)

// storageCall runs an action and fails the test unless it is refused
// without output and, for the actions that take the storage lock, before
// the lock is taken (the lock precedes every look at the system).
func storageCall(t *testing.T, action string, args ...string) error {
	t.Helper()
	a, ok := actions[action]
	if !ok {
		t.Fatalf("action %s is not registered", action)
	}
	_, statErr := os.Lstat(storageLockFile)
	lockExisted := statErr == nil
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out bytes.Buffer
	err := a(ctx, args, strings.NewReader(""), &out)
	if err == nil {
		t.Fatalf("%s %q was accepted", action, args)
	}
	if out.Len() != 0 {
		t.Errorf("%s %q wrote output although it failed: %q", action, args, out.String())
	}
	if !lockExisted {
		if _, e := os.Lstat(storageLockFile); e == nil {
			_ = os.Remove(storageLockFile)
			t.Errorf("%s %q took the storage lock: the arguments passed validation", action, args)
		}
	}
	return err
}

// storageRejected additionally requires a user-facing message that says
// the value is invalid.
func storageRejected(t *testing.T, action string, args ...string) {
	t.Helper()
	err := storageCall(t, action, args...)
	var ue *UserError
	if !errors.As(err, &ue) {
		t.Errorf("%s %q: error %v is not a user error", action, args, err)
		return
	}
	if !strings.Contains(ue.Message, "geçersiz") {
		t.Errorf("%s %q: refused with %q, not by validation", action, args, ue.Message)
	}
	for _, a := range args {
		if len(a) > 3 && strings.ContainsAny(a, "\n\x00;|$`") && strings.Contains(ue.Message, a) {
			t.Errorf("%s: hostile argument echoed in the message %q", action, ue.Message)
		}
	}
}

func TestStorageActionsAreRegistered(t *testing.T) {
	for _, n := range []string{"storage-smart", "storage-smart-test", "storage-mount", "storage-unmount",
		"storage-persist-add", "storage-persist-remove", "storage-format"} {
		if _, ok := actions[n]; !ok {
			t.Errorf("action %s missing", n)
		}
	}
}

func TestStorageActionsArgumentCount(t *testing.T) {
	want := map[string]int{
		"storage-smart": 3, "storage-smart-test": 3, "storage-mount": 3, "storage-unmount": 1,
		"storage-persist-add": 1, "storage-persist-remove": 1, "storage-format": 5,
	}
	for action, n := range want {
		for count := 0; count <= 7; count++ {
			if count == n {
				continue
			}
			args := make([]string, count)
			for i := range args {
				args[i] = "--invalid--"
			}
			storageCall(t, action, args...)
		}
		storageCall(t, action)
	}
}

func storageInvalidDevices(extra ...string) []string {
	return append(append([]string{"", "sd", "SDB", "hda", "loop0", "sr0", "zram0", "sdb1234", "mapper/vg-data", "nvme0n1p"}, extra...), storageHostile...)
}

func TestStorageSmartRejectsInvalidArguments(t *testing.T) {
	// Partitions and stacked devices are not valid SMART targets either.
	for _, dev := range storageInvalidDevices("sdb1", "nvme0n1p1", "md0", "dm-0") {
		storageRejected(t, "storage-smart", dev, "auto", "standby")
		storageRejected(t, "storage-smart-test", dev, "auto", "short")
	}
	for _, typ := range append([]string{"", "SAT", "scsi", "sat,12", "sat -T permissive", "usbjmicron", "auto "}, storageHostile...) {
		storageRejected(t, "storage-smart", "sdb", typ, "standby")
		storageRejected(t, "storage-smart-test", "sdb", typ, "short")
	}
	for _, mode := range append([]string{"", "never", "sleep", "idle", "Active", "standby,now", "active "}, storageHostile...) {
		storageRejected(t, "storage-smart", "sdb", "auto", mode)
	}
	for _, kind := range append([]string{"", "offline", "conveyance", "select,0-100", "Short", "long ", "short -C", "force"}, storageHostile...) {
		storageRejected(t, "storage-smart-test", "sdb", "auto", kind)
	}
}

func TestStorageMountRejectsInvalidArguments(t *testing.T) {
	for _, dev := range storageInvalidDevices() {
		storageRejected(t, "storage-mount", dev, "data", "0")
	}
	for _, name := range append([]string{"", "_data", ".data", "my data", "data.", strings.Repeat("x", 49), "çöp"}, storageHostile...) {
		storageRejected(t, "storage-mount", "sdb1", name, "0")
	}
	for _, p := range append([]string{"", "2", "true", "false", "yes", "01", "1 ", "-1"}, storageHostile...) {
		storageRejected(t, "storage-mount", "sdb1", "data", p)
	}
}

func TestStorageUnmountAndPersistRejectInvalidArguments(t *testing.T) {
	for _, dev := range storageInvalidDevices("/mnt/data", "/media/usb") {
		storageRejected(t, "storage-unmount", dev)
		storageRejected(t, "storage-persist-add", dev)
	}
	for _, uuid := range append([]string{
		"", "abc", "-234-ABCD", "1234-ABCG", "1234 ABCD", "UUID=1234-ABCD", `"1234-ABCD"`,
		storageGoodUUID + "a", storageGoodUUID + "\n", storageGoodUUID + " /mnt/x ext4 defaults 0 0",
	}, storageHostile...) {
		storageRejected(t, "storage-persist-remove", uuid)
	}
}

func TestStorageFormatRejectsInvalidArguments(t *testing.T) {
	// Stacked devices can be mounted but never formatted.
	for _, dev := range storageInvalidDevices("md0", "dm-0", "md0p1") {
		storageRejected(t, "storage-format", dev, "ext4", "", storageGoodSize, storageGoodSerial)
	}
	for _, fs := range append([]string{"", "ntfs", "ext3", "ext2", "swap", "EXT4", "ext4 ", "ext4 -F", "minix", "mkfs.ext4"}, storageHostile...) {
		storageRejected(t, "storage-format", "sdb", fs, "", storageGoodSize, storageGoodSerial)
	}
	labels := append([]string{"my disk", "a.b", "-L", "-n", "etiket\n", "çöp", strings.Repeat("x", 49)}, storageHostile...)
	for _, fs := range []string{"ext4", "xfs", "btrfs", "exfat", "vfat"} {
		for _, label := range labels {
			storageRejected(t, "storage-format", "sdb", fs, label, storageGoodSize, storageGoodSerial)
		}
	}
	// Longer than the filesystem allows.
	for fs, label := range map[string]string{
		"ext4": strings.Repeat("a", 17), "xfs": strings.Repeat("a", 13), "exfat": strings.Repeat("a", 16),
		"vfat": strings.Repeat("a", 12),
	} {
		storageRejected(t, "storage-format", "sdb", fs, label, storageGoodSize, storageGoodSerial)
	}
	for _, size := range append([]string{
		"", "0", "-1", "-4000787030016", "1.5", "1e12", "4T", "0x1000", " 1024", "1024 ", "+", "१२३",
		"99999999999999999999999999", "NaN",
	}, storageHostile...) {
		storageRejected(t, "storage-format", "sdb", "ext4", "", size, storageGoodSerial)
	}
	for _, serial := range []string{"a\nb", "a\x00b", "a\tb", "a\x7fb", "a\rb", strings.Repeat("x", 129)} {
		storageRejected(t, "storage-format", "sdb", "ext4", "", storageGoodSize, serial)
	}
}

func TestStorageMountMessageNeverEchoesOutput(t *testing.T) {
	for _, out := range []string{
		"mount: /mnt/data: unknown filesystem type 'ntfs'.",
		"mount: /mnt/data: wrong fs type, bad option, bad superblock on /dev/sdb1",
		"mount: /mnt/data: /dev/sdb1 already mounted on /mnt/x.",
		"mount: /mnt/data: WARNING: source write-protected, mounted read-only.",
		"The disk contains an unclean file system (0, 0). Windows is hibernated",
		"something unexpected /etc/shadow",
		"",
	} {
		msg := storageMountMessage(out)
		if msg == "" || strings.Contains(msg, "/dev/") || strings.Contains(msg, "/etc/") || strings.Contains(msg, "mount:") {
			t.Errorf("storageMountMessage(%q) = %q", out, msg)
		}
	}
}

func TestStorageWriteAtomic(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/fstab.bak"
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := storageWriteAtomic(path, []byte("new content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "new content\n" {
		t.Errorf("content = %q, %v", b, err)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", fi.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temporary files left behind: %v", entries)
	}
	// A failure leaves neither the target nor a temporary file.
	if err := storageWriteAtomic(dir+"/missing/x", []byte("x"), 0o644); err == nil {
		t.Error("write into a missing directory succeeded")
	}
	sub := dir + "/sub"
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sub+"/target", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sub+"/target/keep", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := storageWriteAtomic(sub+"/target", []byte("x"), 0o644); err == nil {
		t.Error("a non-empty directory was replaced by a file")
	}
	entries, _ = os.ReadDir(sub)
	if len(entries) != 1 {
		t.Errorf("temporary file left after a failed rename: %v", entries)
	}
}
