package system

import (
	"bufio"
	"bytes"
	"sort"
	"strconv"
	"strings"
)

// Parsers take the raw text of /proc files so they can be tested anywhere.

type cpuTimes struct {
	total uint64
	idle  uint64
}

// parseCPUStat reads the aggregate "cpu" line of /proc/stat.
func parseCPUStat(data []byte) (cpuTimes, bool) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 5 || f[0] != "cpu" {
			continue
		}
		var t cpuTimes
		// user nice system idle iowait irq softirq steal; guest times are
		// already included in user and nice.
		for i := 1; i < len(f) && i <= 8; i++ {
			v, err := strconv.ParseUint(f[i], 10, 64)
			if err != nil {
				return cpuTimes{}, false
			}
			t.total += v
			if i == 4 || i == 5 {
				t.idle += v
			}
		}
		return t, true
	}
	return cpuTimes{}, false
}

// cpuPercent is the busy share between two readings.
func cpuPercent(prev, cur cpuTimes) (float64, bool) {
	if cur.total <= prev.total || cur.idle < prev.idle {
		return 0, false
	}
	total := float64(cur.total - prev.total)
	idle := float64(cur.idle - prev.idle)
	if idle > total {
		idle = total
	}
	return (total - idle) / total * 100, true
}

func parseLoadAvg(data []byte) ([3]float64, bool) {
	var out [3]float64
	f := strings.Fields(string(data))
	if len(f) < 3 {
		return out, false
	}
	for i := 0; i < 3; i++ {
		v, err := strconv.ParseFloat(f[i], 64)
		if err != nil {
			return out, false
		}
		out[i] = v
	}
	return out, true
}

type cpuInfo struct {
	model   string
	cores   int
	threads int
}

// parseCPUInfo extracts the model name, physical core count and thread
// count from /proc/cpuinfo.
func parseCPUInfo(data []byte) cpuInfo {
	var info cpuInfo
	var fallbackModel, boardModel string
	coreIDs := map[string]struct{}{}
	physical, core := "", ""
	haveCore := false
	flush := func() {
		if haveCore {
			coreIDs[physical+"/"+core] = struct{}{}
		}
		physical, core, haveCore = "", "", false
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		i := strings.IndexByte(line, ':')
		if i < 0 {
			continue
		}
		key := strings.TrimSpace(line[:i])
		val := strings.TrimSpace(line[i+1:])
		switch key {
		case "processor":
			info.threads++
		case "model name":
			if info.model == "" {
				info.model = val
			}
		case "Model":
			// ARM boards: the board name. It is more telling than the
			// "Hardware" line, which names the SoC family and comes first.
			if boardModel == "" {
				boardModel = val
			}
		case "Hardware", "cpu model":
			if fallbackModel == "" {
				fallbackModel = val
			}
		case "physical id":
			physical = val
		case "core id":
			core = val
			haveCore = true
		}
	}
	flush()
	if info.model == "" {
		info.model = boardModel
	}
	if info.model == "" {
		info.model = fallbackModel
	}
	info.model = strings.Join(strings.Fields(info.model), " ")
	info.cores = len(coreIDs)
	if info.cores == 0 || info.cores > info.threads {
		info.cores = info.threads
	}
	return info
}

func parseMemInfo(data []byte) (Memory, bool) {
	vals := map[string]uint64{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		v, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		vals[strings.TrimSuffix(f[0], ":")] = v * 1024
	}
	total, ok := vals["MemTotal"]
	if !ok || total == 0 {
		return Memory{}, false
	}
	avail, ok := vals["MemAvailable"]
	if !ok {
		avail = vals["MemFree"] + vals["Buffers"] + vals["Cached"]
	}
	if avail > total {
		avail = total
	}
	// Used follows htop and free(1): everything the kernel can drop at once
	// (buffers, page cache, reclaimable slab) counts as free, while shared
	// memory (tmpfs) stays used. Available is the kernel's own, more
	// conservative estimate and drives the health check.
	cache := vals["Cached"] + vals["SReclaimable"]
	if sh := vals["Shmem"]; sh <= cache {
		cache -= sh
	}
	free := vals["MemFree"] + vals["Buffers"] + cache
	if free > total {
		free = total
	}
	m := Memory{Total: total, Available: avail, Used: total - free, SwapTotal: vals["SwapTotal"]}
	m.Percent = float64(m.Used) / float64(total) * 100
	if free := vals["SwapFree"]; free <= m.SwapTotal {
		m.SwapUsed = m.SwapTotal - free
	}
	return m, true
}

type mountEntry struct {
	devID  string
	device string
	mount  string
	fstype string
	seq    int  // line order in mountinfo; a later mount covers an earlier one
	whole  bool // the root of the filesystem is mounted, not a subdirectory
}

// betterMount reports whether e represents a device better than old: the
// mount of the whole filesystem rather than a bind mount of a directory in
// it, then the shortest mount point.
func betterMount(e, old mountEntry) bool {
	if e.whole != old.whole {
		return e.whole
	}
	if len(e.mount) != len(old.mount) {
		return len(e.mount) < len(old.mount)
	}
	return e.mount == old.mount && e.seq > old.seq
}

var pseudoFS = map[string]struct{}{
	"proc": {}, "sysfs": {}, "tmpfs": {}, "devtmpfs": {}, "devpts": {}, "overlay": {},
	"squashfs": {}, "cgroup": {}, "cgroup2": {}, "securityfs": {}, "debugfs": {},
	"tracefs": {}, "configfs": {}, "fusectl": {}, "pstore": {}, "bpf": {}, "mqueue": {},
	"hugetlbfs": {}, "autofs": {}, "binfmt_misc": {}, "ramfs": {}, "efivarfs": {},
	"nsfs": {}, "rpc_pipefs": {}, "selinuxfs": {}, "nfsd": {}, "sunrpc": {},
	"rootfs": {}, "aufs": {}, "iso9660": {}, "udf": {}, "fuse.lxcfs": {},
	"fuse.gvfsd-fuse": {}, "fuse.portal": {}, "fuse.snapfuse": {}, "zram": {},
}

var skippedMountPrefixes = []string{"/proc", "/sys", "/dev", "/run", "/snap", "/var/lib/docker", "/var/lib/containers", "/var/lib/kubelet", "/var/snap"}

func underPrefix(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// unescapeMount decodes the octal escapes (\040 for a space) used in
// mountinfo paths.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// parseMountInfo returns the real filesystems of /proc/self/mountinfo, one
// entry per device (see betterMount), sorted by mount point. The filesystem mounted at "/" is always reported, whatever its type
// (overlay in a container, a btrfs subvolume, ZFS).
func parseMountInfo(data []byte) []mountEntry {
	byDev := map[string]mountEntry{}
	seq := 0
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		sep := strings.Index(line, " - ")
		if sep < 0 {
			continue
		}
		left := strings.Fields(line[:sep])
		right := strings.Fields(line[sep+3:])
		if len(left) < 5 || len(right) < 2 {
			continue
		}
		e := mountEntry{
			devID:  left[2],
			mount:  unescapeMount(left[4]),
			fstype: right[0],
			device: unescapeMount(right[1]),
			seq:    seq,
			whole:  left[3] == "/",
		}
		seq++
		if e.mount != "/" {
			if _, skip := pseudoFS[e.fstype]; skip {
				continue
			}
			skip := false
			for _, p := range skippedMountPrefixes {
				if underPrefix(e.mount, p) {
					skip = true
					break
				}
			}
			if skip {
				continue
			}
		}
		key := e.devID
		if strings.HasPrefix(e.device, "/dev/") {
			// btrfs subvolumes share a device but not a device number.
			key = e.device
		}
		if old, ok := byDev[key]; ok && !betterMount(e, old) {
			continue
		}
		byDev[key] = e
	}
	// Two devices mounted on the same point: the later mount is the visible
	// one.
	byMount := map[string]mountEntry{}
	for _, e := range byDev {
		if old, ok := byMount[e.mount]; ok && old.seq > e.seq {
			continue
		}
		byMount[e.mount] = e
	}
	out := make([]mountEntry, 0, len(byMount))
	for _, e := range byMount {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].mount < out[j].mount })
	return out
}

type netCounters struct {
	rx uint64
	tx uint64
}

func parseNetDev(data []byte) map[string]netCounters {
	out := map[string]netCounters{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		i := strings.IndexByte(line, ':')
		if i < 0 {
			continue
		}
		name := strings.TrimSpace(line[:i])
		f := strings.Fields(line[i+1:])
		if name == "" || len(f) < 9 {
			continue
		}
		rx, err1 := strconv.ParseUint(f[0], 10, 64)
		tx, err2 := strconv.ParseUint(f[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out[name] = netCounters{rx: rx, tx: tx}
	}
	return out
}

var virtualPrefixes = []string{"docker", "veth", "br-", "virbr", "tun", "tap", "cni", "flannel", "cali", "kube", "lxc", "lxd", "vnet", "zt", "wg", "tailscale", "dummy", "ifb", "vxlan", "podman"}

// virtualByName reports whether an interface name belongs to loopback or a
// well-known virtual interface family.
func virtualByName(name string) bool {
	if name == "lo" {
		return true
	}
	for _, p := range virtualPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// parseDefaultRoute returns the interface of the IPv4 default route with
// the lowest metric in /proc/net/route, or "".
func parseDefaultRoute(data []byte) string {
	best := ""
	bestMetric := uint64(0)
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 8 || f[1] != "00000000" || f[7] != "00000000" {
			continue
		}
		flags, err := strconv.ParseUint(f[3], 16, 32)
		if err != nil || flags&0x1 == 0 { // RTF_UP
			continue
		}
		metric, err := strconv.ParseUint(f[6], 10, 64)
		if err != nil {
			continue
		}
		if best == "" || metric < bestMetric {
			best, bestMetric = f[0], metric
		}
	}
	return best
}

func parseOSRelease(data []byte) string {
	name := ""
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch k {
		case "PRETTY_NAME":
			return v
		case "NAME":
			name = v
		}
	}
	return name
}

func parseUptime(data []byte) (float64, bool) {
	f := strings.Fields(string(data))
	if len(f) < 1 {
		return 0, false
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}

// parseFailedUnits returns the unit names printed by
// `systemctl --failed --no-legend --plain`.
func parseFailedUnits(out []byte) []string {
	units := []string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		name := f[0]
		if name == "●" || name == "*" {
			if len(f) < 2 {
				continue
			}
			name = f[1]
		}
		if strings.Contains(name, ".") {
			units = append(units, name)
		}
	}
	return units
}

// parseMilliCelsius converts a hwmon/thermal reading to degrees, rejecting
// values no real sensor reports.
func parseMilliCelsius(data []byte) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
	if err != nil {
		return 0, false
	}
	c := v / 1000
	if !(c > 0 && c < 150) { // also rejects NaN
		return 0, false
	}
	return c, true
}
