package storagecheck

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// LsblkBinary is the absolute path of lsblk.
const LsblkBinary = "/usr/bin/lsblk"

const lsblkColumns = "NAME,KNAME,PKNAME,PATH,MAJ:MIN,TYPE,SIZE,MODEL,SERIAL,VENDOR,TRAN,ROTA,RM,HOTPLUG,RO,FSTYPE,LABEL,UUID,"

// LsblkArgs returns the lsblk arguments. Older util-linux versions only
// know the MOUNTPOINT column; pass legacy=true to retry with it.
func LsblkArgs(legacy bool) []string {
	col := "MOUNTPOINTS"
	if legacy {
		col = "MOUNTPOINT"
	}
	return []string{"--json", "--bytes", "--output", lsblkColumns + col}
}

// BlockDevice is one node of the lsblk tree.
type BlockDevice struct {
	Name        string
	KName       string
	PKName      string
	Path        string
	MajMin      string
	Type        string
	Size        int64
	Model       string
	Serial      string
	Vendor      string
	Transport   string
	Rotational  bool
	Removable   bool
	Hotplug     bool
	ReadOnly    bool
	FSType      string
	Label       string
	UUID        string
	MountPoints []string
	Children    []*BlockDevice
}

// ParseLsblk parses `lsblk --json` output. It tolerates the differences
// between util-linux versions: numbers and booleans encoded as strings,
// and "mountpoint" (string) versus "mountpoints" (array).
func ParseLsblk(data []byte) ([]*BlockDevice, error) {
	var doc struct {
		BlockDevices []map[string]json.RawMessage `json:"blockdevices"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	if doc.BlockDevices == nil {
		return nil, errors.New("lsblk: blockdevices missing")
	}
	out := make([]*BlockDevice, 0, len(doc.BlockDevices))
	for _, raw := range doc.BlockDevices {
		out = append(out, parseNode(raw, 0))
	}
	return out, nil
}

func parseNode(raw map[string]json.RawMessage, depth int) *BlockDevice {
	d := &BlockDevice{
		Name:       rawString(raw["name"]),
		KName:      rawString(raw["kname"]),
		PKName:     rawString(raw["pkname"]),
		Path:       rawString(raw["path"]),
		MajMin:     rawString(raw["maj:min"]),
		Type:       strings.ToLower(rawString(raw["type"])),
		Size:       rawInt(raw["size"]),
		Model:      rawString(raw["model"]),
		Serial:     rawString(raw["serial"]),
		Vendor:     rawString(raw["vendor"]),
		Transport:  strings.ToLower(rawString(raw["tran"])),
		Rotational: rawBool(raw["rota"]),
		Removable:  rawBool(raw["rm"]),
		Hotplug:    rawBool(raw["hotplug"]),
		ReadOnly:   rawBool(raw["ro"]),
		FSType:     rawString(raw["fstype"]),
		Label:      rawString(raw["label"]),
		UUID:       rawString(raw["uuid"]),
	}
	if d.KName == "" {
		d.KName = d.Name
	}
	d.MountPoints = []string{}
	if mp, ok := raw["mountpoints"]; ok {
		var list []json.RawMessage
		if json.Unmarshal(mp, &list) == nil {
			for _, item := range list {
				if s := rawString(item); s != "" {
					d.MountPoints = append(d.MountPoints, s)
				}
			}
		} else if s := rawString(mp); s != "" {
			d.MountPoints = append(d.MountPoints, s)
		}
	}
	if s := rawString(raw["mountpoint"]); s != "" && len(d.MountPoints) == 0 {
		d.MountPoints = append(d.MountPoints, s)
	}
	if ch, ok := raw["children"]; ok && depth < 8 {
		var list []map[string]json.RawMessage
		if json.Unmarshal(ch, &list) == nil {
			for _, c := range list {
				d.Children = append(d.Children, parseNode(c, depth+1))
			}
		}
	}
	return d
}

func rawString(r json.RawMessage) string {
	if len(r) == 0 || string(r) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(r, &s) == nil {
		return strings.TrimSpace(s)
	}
	// Numbers and booleans are returned in their literal form.
	return strings.TrimSpace(string(r))
}

func rawInt(r json.RawMessage) int64 {
	s := rawString(r)
	if s == "" {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		if f, ferr := strconv.ParseFloat(s, 64); ferr == nil {
			return int64(f)
		}
		return 0
	}
	return n
}

func rawBool(r json.RawMessage) bool {
	switch strings.ToLower(rawString(r)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// FindDevice returns the node with the given kernel name and the top-level
// disk it belongs to (the node itself for a whole disk).
func FindDevice(devs []*BlockDevice, kname string) (dev, root *BlockDevice) {
	var walk func(n, top *BlockDevice) *BlockDevice
	walk = func(n, top *BlockDevice) *BlockDevice {
		if n.KName == kname {
			return n
		}
		for _, c := range n.Children {
			if f := walk(c, top); f != nil {
				return f
			}
		}
		return nil
	}
	for _, d := range devs {
		if f := walk(d, d); f != nil {
			return f, d
		}
	}
	return nil, nil
}

// IsRemovable reports whether a disk is removable media for the purpose of
// mount location and options.
func IsRemovable(root *BlockDevice) bool {
	return root != nil && (root.Removable || root.Hotplug || root.Transport == "usb")
}
