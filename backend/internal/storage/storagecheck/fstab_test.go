package storagecheck

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// A realistic Ubuntu Server fstab with awkward formatting: tabs, comments,
// blank lines, trailing spaces, a quoted UUID and an escaped mount point.
const fstabFixture = "# /etc/fstab: static file system information.\n" +
	"#\n" +
	"# <file system> <mount point>   <type>  <options>       <dump>  <pass>\n" +
	"# / was on /dev/ubuntu-vg/ubuntu-lv during curtin installation\n" +
	"/dev/disk/by-id/dm-uuid-LVM-abc / ext4 defaults 0 1\n" +
	"# /boot was on /dev/sda2 during curtin installation\n" +
	"/dev/disk/by-uuid/0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9 /boot ext4 defaults 0 1\n" +
	"UUID=5A1B-C3D4\t/boot/efi\tvfat\tumask=0077\t0\t1\n" +
	"\n" +
	"   # indented comment   \n" +
	"/swap.img\tnone\tswap\tsw\t0\t0\n" +
	"UUID=\"aaaaaaaa-0000-0000-0000-000000000001\"  /srv/my\\040data  xfs  defaults,nofail  0  2   \n" +
	"//nas/share /mnt/nas cifs credentials=/root/.smb,_netdev 0 0\n" +
	"#UUID=77777777-2222-3333-4444-555555555555 /mnt/old ext4 defaults 0 0\n"

const (
	uuidA = "77777777-2222-3333-4444-555555555555"
	uuidB = "64A5-F009"
)

func mustAdd(t *testing.T, content []byte, uuid, mp, fstype, opts string) []byte {
	t.Helper()
	out, err := FstabAdd(content, uuid, mp, fstype, opts)
	if err != nil {
		t.Fatalf("FstabAdd(%s, %s): %v", uuid, mp, err)
	}
	return out
}

func TestParseFstab(t *testing.T) {
	es := ParseFstab([]byte(fstabFixture))
	if len(es) != 6 {
		t.Fatalf("got %d entries, want 6: %+v", len(es), es)
	}
	if es[2].UUID() != "5A1B-C3D4" || es[2].MountPoint != "/boot/efi" || es[2].FSType != "vfat" || es[2].Options != "umask=0077" {
		t.Errorf("efi entry: %+v", es[2])
	}
	if es[4].UUID() != "aaaaaaaa-0000-0000-0000-000000000001" || es[4].MountPoint != "/srv/my data" {
		t.Errorf("quoted/escaped entry: %+v", es[4])
	}
	if es[0].UUID() != "" {
		t.Errorf("non-UUID spec returned a UUID: %+v", es[0])
	}
	for _, e := range es {
		if e.Managed {
			t.Errorf("entry wrongly marked managed: %+v", e)
		}
	}
}

func TestFstabAddAppendsAndPreservesEverything(t *testing.T) {
	out := mustAdd(t, []byte(fstabFixture), uuidA, "/mnt/arsiv", "ext4", "nosuid,nodev")
	if !bytes.HasPrefix(out, []byte(fstabFixture)) {
		t.Fatalf("existing content was changed:\n%s", out)
	}
	added := strings.TrimPrefix(string(out), fstabFixture)
	if strings.Count(added, "\n") != 1 || !strings.HasSuffix(added, "\n") {
		t.Fatalf("expected exactly one new line, got %q", added)
	}
	f := strings.Fields(added)
	if len(f) != 6 {
		t.Fatalf("new entry has %d fields: %q", len(f), added)
	}
	if f[0] != "UUID="+uuidA {
		t.Errorf("entry does not mount by UUID: %q", f[0])
	}
	if f[1] != "/mnt/arsiv" || f[2] != "ext4" || f[4] != "0" || f[5] != "0" {
		t.Errorf("unexpected entry: %q", added)
	}
	for _, o := range []string{"nofail", "nosuid", "nodev", ManagedOption} {
		if !HasOption(f[3], o) {
			t.Errorf("option %q missing in %q", o, f[3])
		}
	}
	if strings.Contains(added, "/dev/") {
		t.Errorf("entry refers to a device path: %q", added)
	}
	es := ParseFstab(out)
	last := es[len(es)-1]
	if !last.Managed || last.UUID() != uuidA {
		t.Errorf("new entry not recognised as managed: %+v", last)
	}
}

func TestFstabAddIsIdempotent(t *testing.T) {
	once := mustAdd(t, []byte(fstabFixture), uuidA, "/mnt/arsiv", "ext4", "nosuid,nodev")
	twice := mustAdd(t, once, uuidA, "/mnt/arsiv", "ext4", "nosuid,nodev")
	if !bytes.Equal(once, twice) {
		t.Errorf("second add changed the file:\n%s\n---\n%s", once, twice)
	}
	thrice := mustAdd(t, twice, strings.ToUpper(uuidA), "/mnt/arsiv", "ext4", "nosuid,nodev")
	if n := countEntries(thrice, uuidA); n != 1 {
		t.Errorf("UUID in another case produced %d entries", n)
	}
}

func countEntries(content []byte, uuid string) int {
	n := 0
	for _, e := range ParseFstab(content) {
		if strings.EqualFold(e.UUID(), uuid) {
			n++
		}
	}
	return n
}

func TestFstabAddNeverDuplicates(t *testing.T) {
	c := mustAdd(t, []byte(fstabFixture), uuidA, "/mnt/arsiv", "ext4", "nosuid,nodev")
	c = mustAdd(t, c, uuidB, "/media/usb", "exfat", "nosuid,nodev,noexec")
	// The same disk is mounted somewhere else: the entry moves.
	c = mustAdd(t, c, uuidA, "/mnt/yeni", "ext4", "nosuid,nodev")
	if n := countEntries(c, uuidA); n != 1 {
		t.Fatalf("%d entries for the UUID:\n%s", n, c)
	}
	if n := countEntries(c, uuidB); n != 1 {
		t.Fatalf("the other managed entry was lost or duplicated (%d):\n%s", n, c)
	}
	// Another disk takes over a managed mount point: one entry per mount point.
	c = mustAdd(t, c, "aaaaaaaa-1111-2222-3333-444444444444", "/mnt/yeni", "xfs", "nosuid,nodev")
	points := map[string]int{}
	for _, e := range ParseFstab(c) {
		points[e.MountPoint]++
	}
	for mp, n := range points {
		if n != 1 {
			t.Errorf("mount point %s appears %d times:\n%s", mp, n, c)
		}
	}
	if !bytes.HasPrefix(c, []byte(fstabFixture)) {
		t.Errorf("unmanaged content changed:\n%s", c)
	}
}

func TestFstabAddRefusesForeignEntries(t *testing.T) {
	cases := []struct{ name, uuid, mp string }{
		{"same uuid, not managed", "5A1B-C3D4", "/mnt/efi"},
		{"same uuid in lower case", "5a1b-c3d4", "/mnt/efi"},
		{"quoted uuid", "aaaaaaaa-0000-0000-0000-000000000001", "/mnt/data"},
		{"same mount point", uuidA, "/mnt/nas"},
	}
	for _, c := range cases {
		out, err := FstabAdd([]byte(fstabFixture), c.uuid, c.mp, "ext4", "nosuid,nodev")
		if !errors.Is(err, ErrFstabConflict) {
			t.Errorf("%s: err = %v, out = %q; want ErrFstabConflict", c.name, err, out)
		}
		if out != nil {
			t.Errorf("%s: content returned together with an error", c.name)
		}
	}
	// A commented-out line is not an entry.
	if _, err := FstabAdd([]byte(fstabFixture), uuidA, "/mnt/old", "ext4", "nosuid,nodev"); err != nil {
		t.Errorf("a comment blocked the entry: %v", err)
	}
}

func TestFstabAddValidatesEveryField(t *testing.T) {
	cases := []struct{ name, uuid, mp, fstype, opts string }{
		{"empty uuid", "", "/mnt/a", "ext4", "nosuid,nodev"},
		{"uuid with space", "1234 ABCD", "/mnt/a", "ext4", "nosuid,nodev"},
		{"uuid with newline", "1234-ABCD\n/dev/sda1 / ext4 defaults 0 0", "/mnt/a", "ext4", "nosuid,nodev"},
		{"uuid option-like", "--foo", "/mnt/a", "ext4", "nosuid,nodev"},
		{"mount point outside base", uuidA, "/etc", "ext4", "nosuid,nodev"},
		{"mount point root", uuidA, "/", "ext4", "nosuid,nodev"},
		{"mount point traversal", uuidA, "/mnt/../etc", "ext4", "nosuid,nodev"},
		{"mount point nested", uuidA, "/mnt/a/b", "ext4", "nosuid,nodev"},
		{"mount point with space", uuidA, "/mnt/a b", "ext4", "nosuid,nodev"},
		{"mount point with newline", uuidA, "/mnt/a\n", "ext4", "nosuid,nodev"},
		{"fstype swap", uuidA, "/mnt/a", "swap", "nosuid,nodev"},
		{"fstype empty", uuidA, "/mnt/a", "", "nosuid,nodev"},
		{"fstype with space", uuidA, "/mnt/a", "ext4 defaults", "nosuid,nodev"},
		{"options empty", uuidA, "/mnt/a", "ext4", ""},
		{"options suid", uuidA, "/mnt/a", "ext4", "nosuid,nodev,suid"},
		{"options exec", uuidA, "/mnt/a", "ext4", "defaults"},
		{"options with space", uuidA, "/mnt/a", "ext4", "nosuid nodev"},
		{"options with newline", uuidA, "/mnt/a", "ext4", "nosuid\nnodev"},
		{"options trailing comma", uuidA, "/mnt/a", "ext4", "nosuid,"},
		{"options x-systemd", uuidA, "/mnt/a", "ext4", "nosuid,x-systemd.requires=evil.service"},
	}
	for _, c := range cases {
		out, err := FstabAdd([]byte(fstabFixture), c.uuid, c.mp, c.fstype, c.opts)
		if !errors.Is(err, ErrFstabInvalid) || out != nil {
			t.Errorf("%s: err = %v, out = %q; want ErrFstabInvalid", c.name, err, out)
		}
	}
}

func TestFstabWithoutTrailingNewline(t *testing.T) {
	in := "# comment\nUUID=5A1B-C3D4 /boot/efi vfat umask=0077 0 1"
	out := mustAdd(t, []byte(in), uuidA, "/mnt/arsiv", "ext4", "nosuid,nodev")
	lines := strings.Split(string(out), "\n")
	if len(lines) != 4 || lines[3] != "" {
		t.Fatalf("unexpected result %q", out)
	}
	if lines[0] != "# comment" || lines[1] != "UUID=5A1B-C3D4 /boot/efi vfat umask=0077 0 1" {
		t.Errorf("existing lines changed or merged with the new entry: %q", out)
	}
	if !strings.HasPrefix(lines[2], "UUID="+uuidA+" ") {
		t.Errorf("new entry is not on its own line: %q", out)
	}
	back, removed := FstabRemove(out, uuidA)
	if !removed {
		t.Fatal("entry not removed")
	}
	if string(back) != in+"\n" {
		t.Errorf("after remove: %q, want the original lines", back)
	}
}

func TestFstabEmptyFile(t *testing.T) {
	out := mustAdd(t, nil, uuidA, "/mnt/arsiv", "ext4", "nosuid,nodev")
	if strings.HasPrefix(string(out), "\n") || strings.Count(string(out), "\n") != 1 {
		t.Errorf("adding to an empty file produced %q", out)
	}
	back, removed := FstabRemove(out, uuidA)
	if !removed || len(back) != 0 {
		t.Errorf("removing the only entry: %q, %v; want an empty file", back, removed)
	}
}

func TestFstabPreservesBlankLinesAndCRLF(t *testing.T) {
	in := "# a\r\n\n\n/dev/sda1 / ext4 defaults 0 1   \n\t\n\n\n"
	out := mustAdd(t, []byte(in), uuidA, "/mnt/arsiv", "ext4", "nosuid,nodev")
	if !bytes.HasPrefix(out, []byte(in)) {
		t.Errorf("content changed:\n%q\n%q", in, out)
	}
	back, removed := FstabRemove(out, uuidA)
	if !removed || string(back) != in {
		t.Errorf("add+remove is not the identity:\n%q\n%q", in, back)
	}
}

func TestFstabRemove(t *testing.T) {
	c := mustAdd(t, []byte(fstabFixture), uuidA, "/mnt/arsiv", "ext4", "nosuid,nodev")
	c = mustAdd(t, c, uuidB, "/media/usb", "exfat", "nosuid,nodev,noexec")
	withB := mustAdd(t, []byte(fstabFixture), uuidB, "/media/usb", "exfat", "nosuid,nodev,noexec")

	out, removed := FstabRemove(c, strings.ToLower(uuidA))
	if !removed {
		t.Fatal("managed entry not removed")
	}
	if !bytes.Equal(out, withB) {
		t.Errorf("remove did not leave exactly the other lines:\n%s", out)
	}
	again, removed := FstabRemove(out, uuidA)
	if removed || !bytes.Equal(again, out) {
		t.Errorf("second remove: removed=%v, content changed=%v", removed, !bytes.Equal(again, out))
	}
	out, removed = FstabRemove(out, uuidB)
	if !removed || string(out) != fstabFixture {
		t.Errorf("after removing everything the file differs from the original:\n%q", out)
	}
}

func TestFstabRemoveNeverTouchesForeignEntries(t *testing.T) {
	for _, uuid := range []string{"5A1B-C3D4", "aaaaaaaa-0000-0000-0000-000000000001", uuidA, "", "0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9"} {
		out, removed := FstabRemove([]byte(fstabFixture), uuid)
		if removed || string(out) != fstabFixture {
			t.Errorf("FstabRemove(%q) removed=%v or changed an unmanaged file", uuid, removed)
		}
	}
	// "x-myserver" must be a whole option, not a substring.
	in := "UUID=" + uuidA + " /mnt/a ext4 defaults,x-myserver-like 0 0\n" +
		"UUID=" + uuidA + " /mnt/b ext4 defaults 0 0 # x-myserver\n"
	out, removed := FstabRemove([]byte(in), uuidA)
	if removed || string(out) != in {
		t.Errorf("look-alike entries were removed: %q", out)
	}
}

func TestFstabRemovesDuplicatedManagedEntries(t *testing.T) {
	line := "UUID=" + uuidA + " /mnt/a ext4 nosuid,nodev,nofail," + ManagedOption + " 0 0\n"
	in := "# top\n" + line + "/dev/sda1 / ext4 defaults 0 1\n" + line
	out, removed := FstabRemove([]byte(in), uuidA)
	if !removed || string(out) != "# top\n/dev/sda1 / ext4 defaults 0 1\n" {
		t.Errorf("got %q", out)
	}
	added := mustAdd(t, []byte(in), uuidA, "/mnt/a", "ext4", "nosuid,nodev")
	if n := countEntries(added, uuidA); n != 1 {
		t.Errorf("add left %d entries", n)
	}
}
