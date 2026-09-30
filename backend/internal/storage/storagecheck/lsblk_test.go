package storagecheck

import (
	"reflect"
	"testing"
)

// Fixtures written from knowledge of lsblk's JSON formats; they are not
// captured from a live system.

// util-linux 2.39 (Ubuntu 24.04): numbers and booleans are typed, the
// column is "mountpoints" and an unmounted device has [null].
const lsblkModern = `{
   "blockdevices": [
      {
         "name": "loop0", "kname": "loop0", "pkname": null, "path": "/dev/loop0", "maj:min": "7:0",
         "type": "loop", "size": 66547712, "model": null, "serial": null, "vendor": null, "tran": null,
         "rota": false, "rm": false, "hotplug": false, "ro": true, "fstype": "squashfs", "label": null,
         "uuid": null, "mountpoints": ["/snap/core22/1122"]
      },{
         "name": "sda", "kname": "sda", "pkname": null, "path": "/dev/sda", "maj:min": "8:0",
         "type": "disk", "size": 500107862016, "model": "Samsung SSD 860 EVO 500GB",
         "serial": "S3Z9NB0K123456A", "vendor": "ATA     ", "tran": "sata", "rota": false, "rm": false,
         "hotplug": false, "ro": false, "fstype": null, "label": null, "uuid": null,
         "mountpoints": [null],
         "children": [
            {
               "name": "sda1", "kname": "sda1", "pkname": "sda", "path": "/dev/sda1", "maj:min": "8:1",
               "type": "part", "size": 1127219200, "model": null, "serial": null, "vendor": null,
               "tran": null, "rota": false, "rm": false, "hotplug": false, "ro": false,
               "fstype": "vfat", "label": null, "uuid": "5A1B-C3D4", "mountpoints": ["/boot/efi"]
            },{
               "name": "sda2", "kname": "sda2", "pkname": "sda", "path": "/dev/sda2", "maj:min": "8:2",
               "type": "part", "size": 2147483648, "model": null, "serial": null, "vendor": null,
               "tran": null, "rota": false, "rm": false, "hotplug": false, "ro": false,
               "fstype": "ext4", "label": null, "uuid": "0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9",
               "mountpoints": ["/boot"]
            },{
               "name": "sda3", "kname": "sda3", "pkname": "sda", "path": "/dev/sda3", "maj:min": "8:3",
               "type": "part", "size": 496831823872, "model": null, "serial": null, "vendor": null,
               "tran": null, "rota": false, "rm": false, "hotplug": false, "ro": false,
               "fstype": "LVM2_member", "label": null, "uuid": "AbCdEf-1234-5678-9abc-defg-hijk-lmnopq",
               "mountpoints": [null],
               "children": [
                  {
                     "name": "ubuntu--vg-ubuntu--lv", "kname": "dm-0", "pkname": "sda3",
                     "path": "/dev/mapper/ubuntu--vg-ubuntu--lv", "maj:min": "252:0", "type": "lvm",
                     "size": 107374182400, "model": null, "serial": null, "vendor": null, "tran": null,
                     "rota": false, "rm": false, "hotplug": false, "ro": false, "fstype": "ext4",
                     "label": null, "uuid": "11111111-2222-3333-4444-555555555555",
                     "mountpoints": ["/", "/var/snap/firefox/common/host-hunspell"]
                  },{
                     "name": "ubuntu--vg-swap", "kname": "dm-1", "pkname": "sda3",
                     "path": "/dev/mapper/ubuntu--vg-swap", "maj:min": "252:1", "type": "lvm",
                     "size": 4294967296, "model": null, "serial": null, "vendor": null, "tran": null,
                     "rota": false, "rm": false, "hotplug": false, "ro": false, "fstype": "swap",
                     "label": null, "uuid": "66666666-2222-3333-4444-555555555555",
                     "mountpoints": ["[SWAP]"]
                  }
               ]
            }
         ]
      },{
         "name": "sdb", "kname": "sdb", "pkname": null, "path": "/dev/sdb", "maj:min": "8:16",
         "type": "disk", "size": 4000787030016, "model": "WDC WD40EFRX-68N32N0", "serial": "WD-WCC7K1234567",
         "vendor": "ATA     ", "tran": "sata", "rota": true, "rm": false, "hotplug": false, "ro": false,
         "fstype": null, "label": null, "uuid": null, "mountpoints": [null],
         "children": [
            {
               "name": "sdb1", "kname": "sdb1", "pkname": "sdb", "path": "/dev/sdb1", "maj:min": "8:17",
               "type": "part", "size": 4000785964544, "model": null, "serial": null, "vendor": null,
               "tran": null, "rota": true, "rm": false, "hotplug": false, "ro": false, "fstype": "ext4",
               "label": "Arsiv", "uuid": "77777777-2222-3333-4444-555555555555", "mountpoints": [null]
            }
         ]
      },{
         "name": "sdc", "kname": "sdc", "pkname": null, "path": "/dev/sdc", "maj:min": "8:32",
         "type": "disk", "size": 31914983424, "model": "Ultra Fit", "serial": "4C530001230101112233",
         "vendor": "SanDisk ", "tran": "usb", "rota": true, "rm": true, "hotplug": true, "ro": false,
         "fstype": null, "label": null, "uuid": null, "mountpoints": [null],
         "children": [
            {
               "name": "sdc1", "kname": "sdc1", "pkname": "sdc", "path": "/dev/sdc1", "maj:min": "8:33",
               "type": "part", "size": 31913934848, "model": null, "serial": null, "vendor": null,
               "tran": null, "rota": true, "rm": true, "hotplug": true, "ro": false, "fstype": "exfat",
               "label": "USB DISK", "uuid": "64A5-F009", "mountpoints": ["/media/usb"]
            }
         ]
      },{
         "name": "nvme0n1", "kname": "nvme0n1", "pkname": null, "path": "/dev/nvme0n1", "maj:min": "259:0",
         "type": "disk", "size": 1000204886016, "model": "Samsung SSD 970 EVO Plus 1TB",
         "serial": "S4EWNX0M123456B", "vendor": null, "tran": "nvme", "rota": false, "rm": false,
         "hotplug": false, "ro": false, "fstype": null, "label": null, "uuid": null, "mountpoints": [null]
      }
   ]
}`

// util-linux 2.31 (Ubuntu 18.04): every value is a string, the column is
// "mountpoint".
const lsblkLegacy = `{
   "blockdevices": [
      {"name": "sda", "kname": "sda", "pkname": null, "path": "/dev/sda", "maj:min": "8:0", "type": "disk",
       "size": "500107862016", "model": "ST500DM002-1BD14", "serial": "Z3T12345", "vendor": "ATA     ",
       "tran": "sata", "rota": "1", "rm": "0", "hotplug": "0", "ro": "0", "fstype": null, "label": null,
       "uuid": null, "mountpoint": null,
         "children": [
            {"name": "sda1", "kname": "sda1", "pkname": "sda", "path": "/dev/sda1", "maj:min": "8:1",
             "type": "part", "size": "499999932416", "model": null, "serial": null, "vendor": null,
             "tran": null, "rota": "1", "rm": "0", "hotplug": "0", "ro": "0", "fstype": "ext4",
             "label": "root", "uuid": "0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9", "mountpoint": "/"},
            {"name": "sda2", "kname": "sda2", "pkname": "sda", "path": "/dev/sda2", "maj:min": "8:2",
             "type": "part", "size": "106885120", "model": null, "serial": null, "vendor": null,
             "tran": null, "rota": "1", "rm": "0", "hotplug": "0", "ro": "0", "fstype": "swap",
             "label": null, "uuid": "99999999-4e5f-6071-8293-a4b5c6d7e8f9", "mountpoint": "[SWAP]"}
         ]
      },
      {"name": "sdb", "kname": "sdb", "pkname": null, "path": "/dev/sdb", "maj:min": "8:16", "type": "disk",
       "size": "8004304896", "model": "DataTraveler 2.0", "serial": "001CC0EC34A1", "vendor": "Kingston",
       "tran": "usb", "rota": "1", "rm": "1", "hotplug": "1", "ro": "1", "fstype": "vfat",
       "label": "KINGSTON", "uuid": "1234-ABCD", "mountpoint": null}
   ]
}`

func TestParseLsblkModern(t *testing.T) {
	devs, err := ParseLsblk([]byte(lsblkModern))
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 5 {
		t.Fatalf("got %d top-level devices, want 5", len(devs))
	}
	sda, root := FindDevice(devs, "sda")
	if sda == nil || root != sda {
		t.Fatal("sda not found as its own root")
	}
	if sda.Type != "disk" || sda.Size != 500107862016 || sda.Serial != "S3Z9NB0K123456A" ||
		sda.Model != "Samsung SSD 860 EVO 500GB" || sda.Vendor != "ATA" || sda.Transport != "sata" ||
		sda.Rotational || sda.Removable || sda.ReadOnly || sda.FSType != "" || sda.Path != "/dev/sda" ||
		sda.MajMin != "8:0" {
		t.Errorf("sda parsed wrongly: %+v", sda)
	}
	if len(sda.MountPoints) != 0 || sda.MountPoints == nil {
		t.Errorf("sda mountpoints = %#v, want empty non-nil", sda.MountPoints)
	}
	if len(sda.Children) != 3 {
		t.Fatalf("sda has %d children, want 3", len(sda.Children))
	}
	efi, root := FindDevice(devs, "sda1")
	if efi == nil || root.KName != "sda" || efi.FSType != "vfat" || efi.UUID != "5A1B-C3D4" ||
		!reflect.DeepEqual(efi.MountPoints, []string{"/boot/efi"}) || efi.PKName != "sda" || efi.Type != "part" {
		t.Errorf("sda1 parsed wrongly: %+v", efi)
	}
	lv, root := FindDevice(devs, "dm-0")
	if lv == nil || root == nil || root.KName != "sda" {
		t.Fatalf("dm-0 (nested two levels) not found under sda: %+v", lv)
	}
	if lv.Name != "ubuntu--vg-ubuntu--lv" || lv.Type != "lvm" || lv.Size != 107374182400 ||
		!reflect.DeepEqual(lv.MountPoints, []string{"/", "/var/snap/firefox/common/host-hunspell"}) {
		t.Errorf("dm-0 parsed wrongly: %+v", lv)
	}
	if sw, _ := FindDevice(devs, "dm-1"); sw == nil || !reflect.DeepEqual(sw.MountPoints, []string{"[SWAP]"}) {
		t.Errorf("dm-1 parsed wrongly: %+v", sw)
	}
	sdb, _ := FindDevice(devs, "sdb")
	if sdb == nil || !sdb.Rotational || sdb.Size != 4000787030016 || IsRemovable(sdb) {
		t.Errorf("sdb parsed wrongly: %+v", sdb)
	}
	if p, _ := FindDevice(devs, "sdb1"); p == nil || p.Label != "Arsiv" || len(p.MountPoints) != 0 {
		t.Errorf("sdb1 parsed wrongly: %+v", p)
	}
	usb, root := FindDevice(devs, "sdc1")
	if usb == nil || !usb.Removable || !usb.Hotplug || usb.Label != "USB DISK" || !IsRemovable(root) ||
		root.Transport != "usb" || root.Vendor != "SanDisk" {
		t.Errorf("sdc1 parsed wrongly: %+v root %+v", usb, root)
	}
	loop, _ := FindDevice(devs, "loop0")
	if loop == nil || !loop.ReadOnly || loop.Type != "loop" {
		t.Errorf("loop0 parsed wrongly: %+v", loop)
	}
	if n, _ := FindDevice(devs, "nvme0n1"); n == nil || n.Transport != "nvme" || n.Size != 1000204886016 || len(n.Children) != 0 {
		t.Errorf("nvme0n1 parsed wrongly: %+v", n)
	}
	if d, r := FindDevice(devs, "sdz"); d != nil || r != nil {
		t.Error("unknown device found")
	}
}

func TestParseLsblkLegacyStrings(t *testing.T) {
	devs, err := ParseLsblk([]byte(lsblkLegacy))
	if err != nil {
		t.Fatal(err)
	}
	sda, _ := FindDevice(devs, "sda")
	if sda == nil || sda.Size != 500107862016 || !sda.Rotational || sda.Removable || sda.Hotplug || sda.ReadOnly {
		t.Fatalf("sda parsed wrongly: %+v", sda)
	}
	if len(sda.MountPoints) != 0 {
		t.Errorf("null mountpoint produced %q", sda.MountPoints)
	}
	r, root := FindDevice(devs, "sda1")
	if r == nil || root != sda || !reflect.DeepEqual(r.MountPoints, []string{"/"}) || r.Size != 499999932416 {
		t.Errorf("sda1 parsed wrongly: %+v", r)
	}
	if s, _ := FindDevice(devs, "sda2"); s == nil || !reflect.DeepEqual(s.MountPoints, []string{"[SWAP]"}) {
		t.Errorf("sda2 parsed wrongly: %+v", s)
	}
	usb, _ := FindDevice(devs, "sdb")
	if usb == nil || !usb.Removable || !usb.Hotplug || !usb.ReadOnly || usb.Size != 8004304896 || !IsRemovable(usb) {
		t.Errorf("sdb parsed wrongly: %+v", usb)
	}
}

func TestParseLsblkFieldEncodings(t *testing.T) {
	cases := []struct {
		name, json string
		check      func(d *BlockDevice) bool
	}{
		{"size number", `{"name":"sdb","size":1024}`, func(d *BlockDevice) bool { return d.Size == 1024 }},
		{"size string", `{"name":"sdb","size":"1024"}`, func(d *BlockDevice) bool { return d.Size == 1024 }},
		{"size null", `{"name":"sdb","size":null}`, func(d *BlockDevice) bool { return d.Size == 0 }},
		{"size missing", `{"name":"sdb"}`, func(d *BlockDevice) bool { return d.Size == 0 }},
		{"size human is not guessed", `{"name":"sdb","size":"3.6T"}`, func(d *BlockDevice) bool { return d.Size == 0 }},
		{"size large", `{"name":"sdb","size":18000207937536}`, func(d *BlockDevice) bool { return d.Size == 18000207937536 }},
		{"bool true", `{"name":"sdb","rm":true,"ro":true,"rota":true,"hotplug":true}`,
			func(d *BlockDevice) bool { return d.Removable && d.ReadOnly && d.Rotational && d.Hotplug }},
		{"bool false", `{"name":"sdb","rm":false,"ro":false,"rota":false,"hotplug":false}`,
			func(d *BlockDevice) bool { return !d.Removable && !d.ReadOnly && !d.Rotational && !d.Hotplug }},
		{"bool string 1", `{"name":"sdb","rm":"1","ro":"1","rota":"1","hotplug":"1"}`,
			func(d *BlockDevice) bool { return d.Removable && d.ReadOnly && d.Rotational && d.Hotplug }},
		{"bool string 0", `{"name":"sdb","rm":"0","ro":"0","rota":"0","hotplug":"0"}`,
			func(d *BlockDevice) bool { return !d.Removable && !d.ReadOnly && !d.Rotational && !d.Hotplug }},
		{"bool number", `{"name":"sdb","rm":1,"ro":0}`, func(d *BlockDevice) bool { return d.Removable && !d.ReadOnly }},
		{"bool null", `{"name":"sdb","rm":null,"ro":null}`, func(d *BlockDevice) bool { return !d.Removable && !d.ReadOnly }},
		{"kname falls back to name", `{"name":"sdb"}`, func(d *BlockDevice) bool { return d.KName == "sdb" }},
		{"kname differs from name", `{"name":"vg-data","kname":"dm-3"}`, func(d *BlockDevice) bool { return d.KName == "dm-3" && d.Name == "vg-data" }},
		{"type lowercased", `{"name":"sdb","type":"DISK","tran":"USB"}`, func(d *BlockDevice) bool { return d.Type == "disk" && d.Transport == "usb" }},
		{"strings trimmed", `{"name":"sdb","model":"  WDC WD40  ","serial":" X1 "}`, func(d *BlockDevice) bool { return d.Model == "WDC WD40" && d.Serial == "X1" }},
		{"mountpoints array", `{"name":"sdb","mountpoints":["/a","/b"]}`, func(d *BlockDevice) bool { return reflect.DeepEqual(d.MountPoints, []string{"/a", "/b"}) }},
		{"mountpoints nulls", `{"name":"sdb","mountpoints":[null,"/b",null]}`, func(d *BlockDevice) bool { return reflect.DeepEqual(d.MountPoints, []string{"/b"}) }},
		{"mountpoints null", `{"name":"sdb","mountpoints":null}`, func(d *BlockDevice) bool { return len(d.MountPoints) == 0 && d.MountPoints != nil }},
		{"mountpoints empty", `{"name":"sdb","mountpoints":[]}`, func(d *BlockDevice) bool { return len(d.MountPoints) == 0 && d.MountPoints != nil }},
		{"mountpoints as string", `{"name":"sdb","mountpoints":"/a"}`, func(d *BlockDevice) bool { return reflect.DeepEqual(d.MountPoints, []string{"/a"}) }},
		{"mountpoint string", `{"name":"sdb","mountpoint":"/a"}`, func(d *BlockDevice) bool { return reflect.DeepEqual(d.MountPoints, []string{"/a"}) }},
		{"mountpoint null", `{"name":"sdb","mountpoint":null}`, func(d *BlockDevice) bool { return len(d.MountPoints) == 0 }},
		{"both columns do not duplicate", `{"name":"sdb","mountpoint":"/a","mountpoints":["/a","/b"]}`,
			func(d *BlockDevice) bool { return reflect.DeepEqual(d.MountPoints, []string{"/a", "/b"}) }},
		{"mountpoint with space", `{"name":"sdb","mountpoints":["/mnt/my disk"]}`, func(d *BlockDevice) bool { return reflect.DeepEqual(d.MountPoints, []string{"/mnt/my disk"}) }},
		{"children null", `{"name":"sdb","children":null}`, func(d *BlockDevice) bool { return len(d.Children) == 0 }},
		{"children empty", `{"name":"sdb","children":[]}`, func(d *BlockDevice) bool { return len(d.Children) == 0 }},
		{"unknown fields ignored", `{"name":"sdb","zoned":"none","wwn":"0x5000"}`, func(d *BlockDevice) bool { return d.Name == "sdb" }},
	}
	for _, c := range cases {
		devs, err := ParseLsblk([]byte(`{"blockdevices":[` + c.json + `]}`))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if len(devs) != 1 || !c.check(devs[0]) {
			t.Errorf("%s: parsed wrongly: %+v", c.name, devs[0])
		}
	}
}

func TestParseLsblkRejectsGarbage(t *testing.T) {
	for _, in := range []string{
		"", "   ", "lsblk: unknown column: MOUNTPOINTS", "{", `{"blockdevices":`, `[]`, `{}`,
		`{"blockdevices":null}`, `{"blockdevices":"x"}`, `{"blockdevices":[1,2]}`, `{"other":[]}`,
	} {
		if devs, err := ParseLsblk([]byte(in)); err == nil {
			t.Errorf("ParseLsblk(%q) = %v, want an error", in, devs)
		}
	}
	devs, err := ParseLsblk([]byte(`{"blockdevices":[]}`))
	if err != nil || len(devs) != 0 {
		t.Errorf("empty list: %v, %v", devs, err)
	}
}

func TestParseLsblkNestingIsBounded(t *testing.T) {
	// 40 levels deep: the parser must neither crash nor recurse forever.
	in := ""
	for i := 0; i < 40; i++ {
		in += `{"name":"n","kname":"k` + itoa(i) + `","children":[`
	}
	in += `{"name":"leaf","kname":"leaf"}`
	for i := 0; i < 40; i++ {
		in += `]}`
	}
	devs, err := ParseLsblk([]byte(`{"blockdevices":[` + in + `]}`))
	if err != nil || len(devs) != 1 {
		t.Fatalf("%v %v", devs, err)
	}
	if d, _ := FindDevice(devs, "k3"); d == nil {
		t.Error("a device three levels deep was lost")
	}
	depth := 0
	for n := devs[0]; len(n.Children) > 0; n = n.Children[0] {
		depth++
	}
	if depth > 16 {
		t.Errorf("parsed %d levels, expected a bound", depth)
	}
}

func TestLsblkArgs(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		a := LsblkArgs(legacy)
		if len(a) != 4 || a[0] != "--json" || a[1] != "--bytes" || a[2] != "--output" {
			t.Fatalf("unexpected args %q", a)
		}
		wantSuffix := ",MOUNTPOINTS"
		if legacy {
			wantSuffix = ",MOUNTPOINT"
		}
		if got := a[3]; len(got) < len(wantSuffix) || got[len(got)-len(wantSuffix):] != wantSuffix {
			t.Errorf("columns %q do not end with %q", got, wantSuffix)
		}
	}
}
