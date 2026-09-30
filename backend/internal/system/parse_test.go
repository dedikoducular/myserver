package system

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

// Fixtures marked "captured" were read with cat inside an ubuntu:24.04
// container (Docker Desktop, kernel 6.x). Fixtures marked "from memory"
// follow the documented format of the file.

// captured (the long "intr" line is shortened)
const procStatCaptured = `cpu  9433 0 7300 1283989 2885 0 1313 0 0 0
cpu0 545 0 540 79943 207 0 598 0 0 0
cpu1 456 0 413 80435 167 0 229 0 0 0
intr 2860869 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 1 1167 1 1 10 0 0
ctxt 5042793
btime 1790725018
processes 4627
procs_running 3
procs_blocked 1
softirq 631174 0 31511 39 138470 2972 0 146011 183491 0 128680
`

func TestParseCPUStatCaptured(t *testing.T) {
	got, ok := parseCPUStat([]byte(procStatCaptured))
	if !ok {
		t.Fatal("captured /proc/stat was not parsed")
	}
	// user+nice+system+idle+iowait+irq+softirq+steal; guest is not added twice.
	if want := uint64(9433 + 7300 + 1283989 + 2885 + 1313); got.total != want {
		t.Errorf("total = %d, want %d", got.total, want)
	}
	if want := uint64(1283989 + 2885); got.idle != want {
		t.Errorf("idle = %d, want %d (idle + iowait)", got.idle, want)
	}
}

func TestParseCPUStatGuestNotCountedTwice(t *testing.T) {
	// from memory: a KVM host, guest time is already part of user.
	got, ok := parseCPUStat([]byte("cpu  100 0 50 800 50 0 0 0 40 0\n"))
	if !ok || got.total != 1000 || got.idle != 850 {
		t.Fatalf("got %+v ok=%v, want total 1000 idle 850", got, ok)
	}
}

func TestParseCPUStatRejectsBadInput(t *testing.T) {
	for name, in := range map[string]string{
		"empty":         "",
		"no aggregate":  "cpu0 545 0 540 79943 207 0 598 0 0 0\n",
		"short":         "cpu 1 2 3\n",
		"not a number":  "cpu  9433 x 7300 1283989 2885 0 1313 0 0 0\n",
		"negative":      "cpu  9433 -1 7300 1283989 2885 0 1313 0 0 0\n",
		"other content": "MemTotal: 5 kB\n",
	} {
		if got, ok := parseCPUStat([]byte(in)); ok {
			t.Errorf("%s: parsed as %+v, want failure", name, got)
		}
	}
}

func TestParseCPUStatOldKernelFourColumns(t *testing.T) {
	got, ok := parseCPUStat([]byte("cpu 10 20 30 40\n"))
	if !ok || got.total != 100 || got.idle != 40 {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
}

func TestCPUPercent(t *testing.T) {
	cases := []struct {
		name      string
		prev, cur cpuTimes
		want      float64
		ok        bool
	}{
		{"half busy", cpuTimes{1000, 800}, cpuTimes{2000, 1300}, 50, true},
		{"fully idle", cpuTimes{1000, 800}, cpuTimes{2000, 1800}, 0, true},
		{"fully busy", cpuTimes{1000, 800}, cpuTimes{2000, 800}, 100, true},
		{"zero delta", cpuTimes{1000, 800}, cpuTimes{1000, 800}, 0, false},
		{"total backwards", cpuTimes{2000, 800}, cpuTimes{1000, 900}, 0, false},
		{"idle backwards", cpuTimes{1000, 800}, cpuTimes{2000, 700}, 0, false},
		{"wrapped counter", cpuTimes{math.MaxUint64 - 5, math.MaxUint64 - 500}, cpuTimes{100, 50}, 0, false},
		{"idle delta above total", cpuTimes{1000, 100}, cpuTimes{1100, 900}, 0, true},
	}
	for _, c := range cases {
		got, ok := cpuPercent(c.prev, c.cur)
		if ok != c.ok {
			t.Errorf("%s: ok = %v, want %v", c.name, ok, c.ok)
			continue
		}
		if math.IsNaN(got) || math.IsInf(got, 0) || got < 0 || got > 100 {
			t.Errorf("%s: percentage %v is outside 0..100", c.name, got)
		}
		if ok && math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// captured
const memInfoCaptured = `MemTotal:        8001612 kB
MemFree:         5832900 kB
MemAvailable:    7196012 kB
Buffers:          240484 kB
Cached:          1261932 kB
SwapCached:            0 kB
Active:           601008 kB
Inactive:        1200876 kB
Shmem:             26888 kB
SwapTotal:       2097152 kB
SwapFree:        2097152 kB
Dirty:               204 kB
VmallocTotal:   34359738367 kB
HugePages_Total:       0
HugePages_Free:        0
Hugepagesize:       2048 kB
DirectMap1G:    12582912 kB
`

func TestParseMemInfoCaptured(t *testing.T) {
	m, ok := parseMemInfo([]byte(memInfoCaptured))
	if !ok {
		t.Fatal("not parsed")
	}
	const kb = 1024
	// Used as htop computes it: total - free - buffers - (cached + reclaimable slab - shmem).
	const used = 8001612 - 5832900 - 240484 - (1261932 - 26888)
	if m.Total != 8001612*kb || m.Available != 7196012*kb || m.Used != used*kb {
		t.Errorf("unexpected sizes: %+v", m)
	}
	if want := float64(used) / 8001612 * 100; math.Abs(m.Percent-want) > 1e-9 {
		t.Errorf("percent = %v, want %v", m.Percent, want)
	}
	if m.SwapTotal != 2097152*kb || m.SwapUsed != 0 {
		t.Errorf("swap: %+v", m)
	}
}

func TestParseMemInfoVariants(t *testing.T) {
	// Kernel older than 3.14: no MemAvailable.
	m, ok := parseMemInfo([]byte("MemTotal: 1000 kB\nMemFree: 100 kB\nBuffers: 50 kB\nCached: 150 kB\nSwapTotal: 400 kB\nSwapFree: 100 kB\n"))
	if !ok || m.Available != 300*1024 || m.Used != 700*1024 || m.Percent != 70 {
		t.Errorf("fallback: %+v ok=%v", m, ok)
	}
	if m.SwapUsed != 300*1024 {
		t.Errorf("swap used = %d", m.SwapUsed)
	}
	// Available above total is clamped; free memory above total must not
	// underflow Used.
	m, ok = parseMemInfo([]byte("MemTotal: 1000 kB\nMemFree: 2000 kB\nMemAvailable: 2000 kB\n"))
	if !ok || m.Available != 1000*1024 || m.Used != 0 || m.Percent != 0 {
		t.Errorf("clamp: %+v ok=%v", m, ok)
	}
	// Shared memory (tmpfs) is part of the page cache but stays used.
	m, _ = parseMemInfo([]byte("MemTotal: 1000 kB\nMemFree: 500 kB\nBuffers: 0 kB\nCached: 300 kB\nShmem: 100 kB\nSReclaimable: 50 kB\nMemAvailable: 700 kB\n"))
	if m.Used != 250*1024 {
		t.Errorf("shmem: used = %d, want %d", m.Used, 250*1024)
	}
	// SwapFree above SwapTotal must not underflow.
	m, _ = parseMemInfo([]byte("MemTotal: 1000 kB\nMemAvailable: 500 kB\nSwapTotal: 10 kB\nSwapFree: 20 kB\n"))
	if m.SwapUsed != 0 {
		t.Errorf("swap underflow: %d", m.SwapUsed)
	}
	for name, in := range map[string]string{
		"empty": "", "no total": "MemFree: 5 kB\n", "zero total": "MemTotal: 0 kB\n", "garbage": "MemTotal: many kB\n",
	} {
		if m, ok := parseMemInfo([]byte(in)); ok {
			t.Errorf("%s: parsed as %+v", name, m)
		}
	}
}

func TestParseLoadAvg(t *testing.T) {
	got, ok := parseLoadAvg([]byte("0.10 0.08 0.07 1/430 9\n")) // captured
	if !ok || got != [3]float64{0.10, 0.08, 0.07} {
		t.Errorf("got %v ok=%v", got, ok)
	}
	got, ok = parseLoadAvg([]byte("128.50 64.25 12.00 9/2000 123456\n"))
	if !ok || got != [3]float64{128.5, 64.25, 12} {
		t.Errorf("got %v ok=%v", got, ok)
	}
	for _, in := range []string{"", "0.10 0.08", "a b c 1/2 3"} {
		if _, ok := parseLoadAvg([]byte(in)); ok {
			t.Errorf("%q parsed", in)
		}
	}
}

func TestParseUptime(t *testing.T) {
	v, ok := parseUptime([]byte("816.93 12839.94\n")) // captured
	if !ok || v != 816.93 {
		t.Errorf("got %v ok=%v", v, ok)
	}
	for _, in := range []string{"", "\n", "abc 1", "-5.0 1"} {
		if v, ok := parseUptime([]byte(in)); ok {
			t.Errorf("%q parsed as %v", in, v)
		}
	}
}

// captured, first two processors of sixteen; the flags line is shortened.
const cpuInfoCaptured = `processor	: 0
vendor_id	: GenuineIntel
cpu family	: 6
model		: 154
model name	: 12th Gen Intel(R) Core(TM) i5-12500H
stepping	: 3
microcode	: 0xffffffff
cpu MHz		: 3110.401
cache size	: 18432 KB
physical id	: 0
siblings	: 16
core id		: 0
cpu cores	: 8
apicid		: 0
initial apicid	: 0
fpu		: yes
fpu_exception	: yes
cpuid level	: 28
wp		: yes
flags		: fpu vme de pse tsc msr pae mce cx8 apic sep mtrr pge mca cmov pat
bugs		: spectre_v1 spectre_v2 spec_store_bypass swapgs retbleed eibrs_pbrsb rfds bhi its
bogomips	: 6220.80
clflush size	: 64
cache_alignment	: 64
address sizes	: 39 bits physical, 48 bits virtual
power management:

processor	: 1
vendor_id	: GenuineIntel
cpu family	: 6
model		: 154
model name	: 12th Gen Intel(R) Core(TM) i5-12500H
stepping	: 3
microcode	: 0xffffffff
cpu MHz		: 3110.401
cache size	: 18432 KB
physical id	: 0
siblings	: 16
core id		: 0
cpu cores	: 8
apicid		: 1
initial apicid	: 1
fpu		: yes
flags		: fpu vme de pse tsc msr pae mce cx8 apic sep mtrr pge mca cmov pat
bogomips	: 6220.80
address sizes	: 39 bits physical, 48 bits virtual
power management:

`

func TestParseCPUInfoCaptured(t *testing.T) {
	got := parseCPUInfo([]byte(cpuInfoCaptured))
	want := cpuInfo{model: "12th Gen Intel(R) Core(TM) i5-12500H", cores: 1, threads: 2}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func cpuBlock(proc, physical, core string, model string) string {
	var b strings.Builder
	b.WriteString("processor\t: " + proc + "\n")
	if model != "" {
		b.WriteString("model name\t: " + model + "\n")
	}
	if physical != "" {
		b.WriteString("physical id\t: " + physical + "\n")
	}
	if core != "" {
		b.WriteString("core id\t\t: " + core + "\n")
	}
	b.WriteString("bogomips\t: 4800.00\n\n")
	return b.String()
}

func TestParseCPUInfoTwoSockets(t *testing.T) {
	// from memory: 2 sockets x 2 cores x 2 threads. Core ids repeat on the
	// second socket and must not be merged with those of the first.
	var in string
	n := 0
	for _, socket := range []string{"0", "1"} {
		for _, core := range []string{"0", "1"} {
			for i := 0; i < 2; i++ {
				in += cpuBlock(string(rune('0'+n)), socket, core, "Intel(R) Xeon(R)   CPU E5-2620 v4 @ 2.10GHz")
				n++
			}
		}
	}
	got := parseCPUInfo([]byte(in))
	want := cpuInfo{model: "Intel(R) Xeon(R) CPU E5-2620 v4 @ 2.10GHz", cores: 4, threads: 8}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseCPUInfoNoTrailingBlankLine(t *testing.T) {
	in := strings.TrimRight(cpuBlock("0", "0", "0", "X")+cpuBlock("1", "0", "1", "X"), "\n")
	got := parseCPUInfo([]byte(in))
	if got.cores != 2 || got.threads != 2 {
		t.Errorf("got %+v", got)
	}
}

// from memory: Raspberry Pi 4 on a 64-bit kernel. No "model name", no
// "physical id", no "core id".
const cpuInfoARM = `processor	: 0
BogoMIPS	: 108.00
Features	: fp asimd evtstrm crc32 cpuid
CPU implementer	: 0x41
CPU architecture: 8
CPU variant	: 0x0
CPU part	: 0xd08
CPU revision	: 3

processor	: 1
BogoMIPS	: 108.00
Features	: fp asimd evtstrm crc32 cpuid
CPU implementer	: 0x41
CPU architecture: 8
CPU variant	: 0x0
CPU part	: 0xd08
CPU revision	: 3

processor	: 2
BogoMIPS	: 108.00
Features	: fp asimd evtstrm crc32 cpuid
CPU implementer	: 0x41
CPU architecture: 8
CPU variant	: 0x0
CPU part	: 0xd08
CPU revision	: 3

processor	: 3
BogoMIPS	: 108.00
Features	: fp asimd evtstrm crc32 cpuid
CPU implementer	: 0x41
CPU architecture: 8
CPU variant	: 0x0
CPU part	: 0xd08
CPU revision	: 3

Hardware	: BCM2835
Revision	: c03114
Serial		: 10000000abcdef01
Model		: Raspberry Pi 4 Model B Rev 1.4
`

func TestParseCPUInfoARM(t *testing.T) {
	got := parseCPUInfo([]byte(cpuInfoARM))
	if got.threads != 4 || got.cores != 4 {
		t.Errorf("threads=%d cores=%d, want 4 and 4", got.threads, got.cores)
	}
	// The kernel reports "BCM2835" as Hardware on every Raspberry Pi; the
	// Model line is the accurate one.
	if got.model != "Raspberry Pi 4 Model B Rev 1.4" {
		t.Errorf("model = %q", got.model)
	}
}

func TestParseCPUInfoARMWithoutModelLine(t *testing.T) {
	in := "processor\t: 0\nBogoMIPS\t: 38.40\n\nHardware\t: Allwinner sun8i Family\nRevision\t: 0000\n"
	got := parseCPUInfo([]byte(in))
	want := cpuInfo{model: "Allwinner sun8i Family", cores: 1, threads: 1}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseCPUInfoEmpty(t *testing.T) {
	if got := parseCPUInfo(nil); got != (cpuInfo{}) {
		t.Errorf("got %+v", got)
	}
}

// captured
const netDevCaptured = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo:       0       0    0    0    0     0          0         0        0       0    0    0    0     0       0          0
  eth0:     152       2    0    0    0     0          0         0       42       1    0    0    0     0       0          0
`

func TestParseNetDevCaptured(t *testing.T) {
	got := parseNetDev([]byte(netDevCaptured))
	want := map[string]netCounters{"lo": {0, 0}, "eth0": {152, 42}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseNetDevNoSpaceAfterColon(t *testing.T) {
	// from memory: large counters touch the colon on older kernels.
	in := "enp3s0:98765432109 1 0 0 0 0 0 0 12345678901 1 0 0 0 0 0 0\n" +
		"broken: 1 2 3\n" +
		"bad: x 1 0 0 0 0 0 0 1 1 0 0 0 0 0 0\n"
	got := parseNetDev([]byte(in))
	want := map[string]netCounters{"enp3s0": {98765432109, 12345678901}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func rateOf(n Network, name string) (Interface, bool) {
	for _, i := range n.Interfaces {
		if i.Name == name {
			return i, true
		}
	}
	return Interface{}, false
}

func TestNetRates(t *testing.T) {
	prev := map[string]netCounters{
		"eth0":    {1000, 5000},
		"eth1":    {900000, 900000}, // will reset
		"gone0":   {77, 77},         // will disappear
		"docker0": {0, 0},
		"lo":      {10, 10},
	}
	cur := map[string]netCounters{
		"eth0":    {3000, 9000},
		"eth1":    {100, 200},
		"new0":    {123456789, 987654321}, // appeared with large counters
		"docker0": {4000, 4000},
		"lo":      {2010, 2010},
	}
	n := netRates(prev, cur, 2, virtualByName)

	names := []string{}
	for _, i := range n.Interfaces {
		names = append(names, i.Name)
	}
	if want := []string{"docker0", "eth0", "eth1", "lo", "new0"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("interfaces = %v, want %v (sorted, without the removed one)", names, want)
	}
	if i, _ := rateOf(n, "eth0"); i.RxRate != 1000 || i.TxRate != 2000 || i.RxBytes != 3000 || i.Virtual {
		t.Errorf("eth0: %+v", i)
	}
	if i, _ := rateOf(n, "eth1"); i.RxRate != 0 || i.TxRate != 0 {
		t.Errorf("counter reset must give a zero rate, got %+v", i)
	}
	if i, _ := rateOf(n, "new0"); i.RxRate != 0 || i.TxRate != 0 {
		t.Errorf("a new interface has no rate on its first reading, got %+v", i)
	}
	if i, _ := rateOf(n, "docker0"); i.RxRate != 2000 || !i.Virtual {
		t.Errorf("docker0: %+v", i)
	}
	if n.RxRate != 1000 || n.TxRate != 2000 {
		t.Errorf("totals rx=%v tx=%v, want only eth0 (1000, 2000)", n.RxRate, n.TxRate)
	}
}

func TestNetRatesDegenerate(t *testing.T) {
	cur := map[string]netCounters{"eth0": {3000, 9000}}
	for name, n := range map[string]Network{
		"no previous sample": netRates(nil, cur, 2, virtualByName),
		"zero interval":      netRates(map[string]netCounters{"eth0": {1, 1}}, cur, 0, virtualByName),
		"negative interval":  netRates(map[string]netCounters{"eth0": {1, 1}}, cur, -3, virtualByName),
	} {
		if n.RxRate != 0 || n.TxRate != 0 || len(n.Interfaces) != 1 {
			t.Errorf("%s: %+v", name, n)
		}
		for _, i := range n.Interfaces {
			if math.IsNaN(i.RxRate) || math.IsInf(i.RxRate, 0) || i.RxRate != 0 {
				t.Errorf("%s: rate %v", name, i.RxRate)
			}
		}
	}
	if n := netRates(nil, nil, 2, virtualByName); n.Interfaces == nil || len(n.Interfaces) != 0 {
		t.Errorf("empty input must give an empty, non-nil list: %+v", n)
	}
}

func TestCounterDelta(t *testing.T) {
	if counterDelta(5, 9) != 4 || counterDelta(9, 5) != 0 || counterDelta(7, 7) != 0 {
		t.Error("counterDelta")
	}
}

// captured
const osReleaseCaptured = `PRETTY_NAME="Ubuntu 24.04.5 LTS"
NAME="Ubuntu"
VERSION_ID="24.04"
VERSION="24.04.5 LTS (Noble Numbat)"
VERSION_CODENAME=noble
ID=ubuntu
ID_LIKE=debian
HOME_URL="https://www.ubuntu.com/"
SUPPORT_URL="https://help.ubuntu.com/"
BUG_REPORT_URL="https://bugs.launchpad.net/ubuntu/"
PRIVACY_POLICY_URL="https://www.ubuntu.com/legal/terms-and-policies/privacy-policy"
UBUNTU_CODENAME=noble
LOGO=ubuntu-logo
`

func TestParseOSRelease(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"captured":            {osReleaseCaptured, "Ubuntu 24.04.5 LTS"},
		"name before pretty":  {"NAME=\"Debian GNU/Linux\"\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\n", "Debian GNU/Linux 12 (bookworm)"},
		"unquoted":            {"NAME=Fedora\nPRETTY_NAME=Fedora\n", "Fedora"},
		"single quotes":       {"PRETTY_NAME='Arch Linux'\n", "Arch Linux"},
		"only name":           {"ID=alpine\nNAME=\"Alpine Linux\"\n", "Alpine Linux"},
		"comment and blanks":  {"# comment\n\n  PRETTY_NAME=\"Ubuntu 24.04 LTS\"  \n", "Ubuntu 24.04 LTS"},
		"value with equals":   {"PRETTY_NAME=\"A=B\"\n", "A=B"},
		"crlf":                {"PRETTY_NAME=\"Ubuntu\"\r\n", "Ubuntu"},
		"empty":               {"", ""},
		"similar key ignored": {"MY_PRETTY_NAME=\"x\"\nXNAME=y\n", ""},
	}
	for name, c := range cases {
		if got := parseOSRelease([]byte(c.in)); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}

// captured
const routeCaptured = "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT                                                       \n" +
	"eth0\t00000000\t010011AC\t0003\t0\t0\t0\t00000000\t0\t0\t0                                                                               \n" +
	"eth0\t000011AC\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0                                                                               \n"

const routeHeader = "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n"

func TestParseDefaultRoute(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"captured": {routeCaptured, "eth0"},
		// from memory: wired and wireless, NetworkManager metrics.
		"lowest metric wins": {routeHeader +
			"wlp2s0\t00000000\t0101A8C0\t0003\t0\t0\t600\t00000000\t0\t0\t0\n" +
			"enp3s0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n" +
			"enp3s0\t0001A8C0\t00000000\t0001\t0\t0\t100\t00FFFFFF\t0\t0\t0\n", "enp3s0"},
		"no default route": {routeHeader +
			"enp3s0\t0001A8C0\t00000000\t0001\t0\t0\t100\t00FFFFFF\t0\t0\t0\n", ""},
		"route that is not up": {routeHeader +
			"enp3s0\t00000000\t0101A8C0\t0002\t0\t0\t100\t00000000\t0\t0\t0\n", ""},
		"zero destination with a mask is not a default route": {routeHeader +
			"enp3s0\t00000000\t00000000\t0001\t0\t0\t100\t000000FF\t0\t0\t0\n", ""},
		"header only": {routeHeader, ""},
		"empty":       {"", ""},
	}
	for name, c := range cases {
		if got := parseDefaultRoute([]byte(c.in)); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}

func TestParseFailedUnits(t *testing.T) {
	// from memory: with and without --plain, and the summary of the legend.
	in := "● nginx.service loaded failed failed A high performance web server\n" +
		"  systemd-fsck@dev-disk-by\\x2duuid-1234.service loaded failed failed File System Check\n" +
		"* user@1000.service loaded failed failed User Manager for UID 1000\n" +
		"\n" +
		"3 loaded units listed.\n"
	got := parseFailedUnits([]byte(in))
	want := []string{"nginx.service", `systemd-fsck@dev-disk-by\x2duuid-1234.service`, "user@1000.service"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := parseFailedUnits(nil); got == nil || len(got) != 0 {
		t.Errorf("empty output: %v", got)
	}
}

func TestParseMilliCelsius(t *testing.T) {
	for in, want := range map[string]float64{"45000\n": 45, "38850": 38.85, " 99999 ": 99.999} {
		if got, ok := parseMilliCelsius([]byte(in)); !ok || math.Abs(got-want) > 1e-9 {
			t.Errorf("%q: got %v ok=%v", in, got, ok)
		}
	}
	// Values no working sensor reports.
	for _, in := range []string{"", "abc", "0", "-273150", "150000", "255000", "NaN", "Inf"} {
		if got, ok := parseMilliCelsius([]byte(in)); ok {
			t.Errorf("%q accepted as %v", in, got)
		}
	}
}

func TestVirtualByName(t *testing.T) {
	for _, n := range []string{"lo", "docker0", "veth1a2b3c", "br-0123456789ab", "virbr0", "tun0", "tap0", "wg0", "tailscale0"} {
		if !virtualByName(n) {
			t.Errorf("%s should be virtual", n)
		}
	}
	for _, n := range []string{"eth0", "enp3s0", "eno1", "wlp2s0", "wlan0", "bond0", "end0", "ens18"} {
		if virtualByName(n) {
			t.Errorf("%s should not be virtual", n)
		}
	}
}
