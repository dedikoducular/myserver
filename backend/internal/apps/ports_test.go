package apps

import (
	"context"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Fixtures in the format of the kernel (Linux 6.8, little endian).
const procTCP = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 20511 1 0000000000000000 100 0 0 10 0
   1: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 31337 1 0000000000000000 100 0 0 10 0
   2: 3500007F:0035 00000000:0000 0A 00000000:00000000 00:00000000 00000000   101        0 18001 1 0000000000000000 100 0 0 10 5
   3: 0501A8C0:01BB 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 18002 1 0000000000000000 100 0 0 10 0
   4: 0501A8C0:D431 0A01A8C0:0016 01 00000000:00000000 02:000A1B2C 00000000  1000        0 40001 2 0000000000000000 20 4 30 10 -1
   5: 0100007F:C350 0100007F:2328 06 00000000:00000000 03:00000A00 00000000     0        0 0 3 0000000000000000
   6: 00000000:2710 00000000:0000 02 00000000:00000000 00:00000000 00000000     0        0 40002 1 0000000000000000 100 0 0 10 0
`

const procTCP6 = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:0016 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 17001 1 0000000000000000 100 0 0 10 0
   1: 00000000000000000000000001000000:0CEA 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000   999        0 17002 1 0000000000000000 100 0 0 10 0
   2: 0000000000000000FFFF00000100007F:2382 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 17003 1 0000000000000000 100 0 0 10 0
   3: 000080FE00000000FF005450B6F2FEFF:1F40 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 17004 1 0000000000000000 100 0 0 10 0
   4: 0000000000000000FFFF00000501A8C0:C001 0000000000000000FFFF00000A01A8C0:01BB 01 00000000:00000000 00:00000000 00000000     0        0 17005 1 0000000000000000 20 4 30 10 -1
`

const procUDP = `   sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
 1201: 3500007F:0035 00000000:0000 07 00000000:00000000 00:00000000 00000000   101        0 18010 2 0000000000000000 0
 2217: 00000000:0044 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 18011 2 0000000000000000 0
 9001: 0501A8C0:14E9 0101A8C0:0035 01 00000000:00000000 00:00000000 00000000     0        0 18012 2 0000000000000000 0
`

const procUDP6 = `   sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
 5353: 00000000000000000000000000000000:14E9 00000000000000000000000000000000:0000 07 00000000:00000000 00:00000000 00000000   108        0 19001 2 0000000000000000 0
  546: 000080FE00000000FF005450B6F2FEFF:0222 00000000000000000000000000000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 19002 2 0000000000000000 0
`

func socketsString(m map[int][]string) string {
	ports := make([]int, 0, len(m))
	for p := range m {
		ports = append(ports, p)
	}
	sort.Ints(ports)
	var out []string
	for _, p := range ports {
		out = append(out, strconv.Itoa(p)+"="+strings.Join(m[p], "+"))
	}
	return strings.Join(out, " ")
}

func TestListeningSockets(t *testing.T) {
	cases := []struct {
		name, text string
		udp        bool
		want       string
	}{
		// Established (01), closing (06) and SYN_SENT (02) sockets are
		// not listeners.
		{"tcp", procTCP, false, "53=127.0.0.53 80=0.0.0.0 443=192.168.1.5 8080=127.0.0.1"},
		{"tcp6", procTCP6, false, "22=0.0.0.0 3306=ipv6 8000=ipv6 9090=127.0.0.1"},
		// Every bound UDP socket occupies its port, connected or not.
		{"udp", procUDP, true, "53=127.0.0.53 68=0.0.0.0 5353=192.168.1.5"},
		{"udp6", procUDP6, true, "546=ipv6 5353=0.0.0.0"},
		{"empty", "", false, ""},
		{"header only", strings.SplitAfter(procTCP, "\n")[0], false, ""},
		{"garbage", "header\nnot a socket line\n 1: zz:zz zz 0A\n 2: 0100007F:GGGG x 0A\n 3: 0100007F 0 0A\n 4: 00:0050 0 0A\n\n", false, ""},
		{"port zero", "header\n 0: 00000000:0000 00000000:0000 0A\n", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := socketsString(ListeningSockets(c.text, c.udp)); got != c.want {
				t.Errorf("got  %q\nwant %q", got, c.want)
			}
		})
	}
	ports := ListeningPorts(procTCP, false)
	if len(ports) != 4 || !ports[80] || ports[54321] || ports[50000] || ports[10000] {
		t.Errorf("ListeningPorts: %v", ports)
	}
}

func TestBindsOverlap(t *testing.T) {
	cases := []struct {
		ours, theirs string
		want         bool
	}{
		{"0.0.0.0", "0.0.0.0", true},
		{"0.0.0.0", "127.0.0.1", true},
		{"0.0.0.0", "192.168.1.5", true},
		{"0.0.0.0", "::", true},
		{"0.0.0.0", "", true},
		{"0.0.0.0", "ipv6", true},
		{"127.0.0.1", "0.0.0.0", true},
		{"127.0.0.1", "::", true},
		{"127.0.0.1", "", true},
		{"127.0.0.1", "127.0.0.1", true},
		{"127.0.0.1", "192.168.1.5", false},
		{"127.0.0.1", "127.0.0.53", false},
		{"127.0.0.1", "ipv6", false},
		{"", "192.168.1.5", true},
		{"192.168.1.5", "192.168.1.6", false},
	}
	for _, c := range cases {
		if got := BindsOverlap(c.ours, c.theirs); got != c.want {
			t.Errorf("BindsOverlap(%q, %q) = %v, want %v", c.ours, c.theirs, got, c.want)
		}
	}
}

func TestPortConflictMessage(t *testing.T) {
	c := PortConflict{Port: 8096, Protocol: "tcp", Label: "Web arayüzü", UsedBy: "jellyfin-eski"}
	for _, p := range []string{"8096/tcp", "Web arayüzü", `"jellyfin-eski"`, "konteyneri tarafından kullanılıyor"} {
		if !strings.Contains(c.Message(), p) {
			t.Errorf("%q does not contain %q", c.Message(), p)
		}
	}
	c = PortConflict{Port: 53, Protocol: "udp"}
	for _, p := range []string{"53/udp", "sunucuda çalışan başka bir servis"} {
		if !strings.Contains(c.Message(), p) {
			t.Errorf("%q does not contain %q", c.Message(), p)
		}
	}
}

/* ---------- conflicts as seen by the engine ---------- */

func portsConfig(slug, bind string, ports ...PortBinding) *Config {
	cfg := &Config{Slug: slug, Name: slug, BindAddress: bind, Services: []ServiceConfig{{
		Name: "app", NetworkMode: "bridge", Ports: ports,
	}}}
	NormalizeConfig(cfg)
	return cfg
}

func checkPorts(t *testing.T, f *fakeDocker, cfg *Config) []PortConflict {
	t.Helper()
	e := &engine{host: f.host()}
	cli, err := e.ready(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cli.Close() })
	list, err := e.checkPorts(context.Background(), cli, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestCheckPortsAgainstContainers(t *testing.T) {
	f := newFakeDocker(t)
	f.addContainer("nginx", "nginx", "running", nil,
		fakePort{IP: "0.0.0.0", PrivatePort: 80, PublicPort: 28080, Type: "tcp"},
		fakePort{IP: "::", PrivatePort: 80, PublicPort: 28080, Type: "tcp"},
		fakePort{PrivatePort: 9000, Type: "tcp"}) // exposed, not published
	f.addContainer("yerel", "x", "running", nil, fakePort{IP: "127.0.0.1", PrivatePort: 80, PublicPort: 28081, Type: "tcp"})
	f.addContainer("lan", "x", "running", nil, fakePort{IP: "192.168.1.5", PrivatePort: 80, PublicPort: 28082, Type: "tcp"})
	f.addContainer("dns", "x", "running", nil, fakePort{IP: "0.0.0.0", PrivatePort: 53, PublicPort: 28053, Type: "udp"})
	f.addContainer("durmus", "x", "exited", nil, fakePort{IP: "0.0.0.0", PrivatePort: 80, PublicPort: 28090, Type: "tcp"})
	f.addContainer("myserver-tek", "x", "running", managed("tek", "app"),
		fakePort{IP: "0.0.0.0", PrivatePort: 80, PublicPort: 28095, Type: "tcp"})

	all := func(port int, proto string) PortBinding {
		return PortBinding{Host: port, Container: 80, Protocol: proto, Label: "Web arayüzü"}
	}
	cases := []struct {
		name string
		cfg  *Config
		want string
	}{
		{"wildcard holder, all", portsConfig("yeni", BindAll, all(28080, "tcp")), "28080/tcp nginx"},
		{"wildcard holder, loopback", portsConfig("yeni", BindLoopback, all(28080, "tcp")), "28080/tcp nginx"},
		{"loopback holder, all", portsConfig("yeni", BindAll, all(28081, "tcp")), "28081/tcp yerel"},
		{"loopback holder, loopback", portsConfig("yeni", BindLoopback, all(28081, "tcp")), "28081/tcp yerel"},
		{"specific holder, all", portsConfig("yeni", BindAll, all(28082, "tcp")), "28082/tcp lan"},
		{"specific holder, loopback", portsConfig("yeni", BindLoopback, all(28082, "tcp")), ""},
		{"other protocol", portsConfig("yeni", BindAll, all(28053, "tcp")), ""},
		{"udp", portsConfig("yeni", BindAll, all(28053, "udp")), "28053/udp dns"},
		{"container port is not a host port", portsConfig("yeni", BindAll, all(9000, "tcp")), ""},
		{"stopped container", portsConfig("yeni", BindAll, all(28090, "tcp")), ""},
		{"own container", portsConfig("tek", BindAll, all(28095, "tcp")), ""},
		{"container of another application", portsConfig("yeni", BindAll, all(28095, "tcp")), "28095/tcp myserver-tek"},
		{"several, ordered by port", portsConfig("yeni", BindAll, all(28082, "tcp"), all(28080, "tcp"), all(28070, "tcp")),
			"28080/tcp nginx,28082/tcp lan"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, x := range checkPorts(t, f, c.cfg) {
				got = append(got, strconv.Itoa(x.Port)+"/"+x.Protocol+" "+x.UsedBy)
				if x.Label != "Web arayüzü" {
					t.Errorf("label %q", x.Label)
				}
			}
			if strings.Join(got, ",") != c.want {
				t.Errorf("conflicts %v, want %q", got, c.want)
			}
		})
	}
	for _, op := range f.changes() {
		t.Errorf("a port check changed something: %s", op)
	}
}

// A service listening on the host itself (not a container) is found through
// /proc/net of the machine the test runs on.
func TestCheckPortsAgainstHostListeners(t *testing.T) {
	if _, _, ok := hostListening(); !ok {
		t.Skip("/proc/net is not readable here")
	}
	f := newFakeDocker(t)
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	uport := pc.LocalAddr().(*net.UDPAddr).Port

	for _, bind := range []string{BindAll, BindLoopback} {
		got := checkPorts(t, f, portsConfig("yeni", bind,
			PortBinding{Host: port, Container: 80, Protocol: "tcp", Label: "Web"},
			PortBinding{Host: uport, Container: 53, Protocol: "udp", Label: "DNS"}))
		if len(got) != 2 {
			t.Fatalf("%s: conflicts %+v, want tcp %d and udp %d", bind, got, port, uport)
		}
		for _, c := range got {
			if c.UsedBy != "" || !strings.Contains(c.Message(), "sunucuda çalışan başka bir servis") {
				t.Errorf("%s: %+v", bind, c)
			}
		}
	}
	// The same numbers on the other protocol are free.
	if port != uport {
		got := checkPorts(t, f, portsConfig("yeni", BindAll,
			PortBinding{Host: port, Container: 80, Protocol: "udp"},
			PortBinding{Host: uport, Container: 53, Protocol: "tcp"}))
		if len(got) != 0 {
			t.Errorf("conflicts across protocols: %+v", got)
		}
	}
}

func TestInstallRefusedOnPortConflict(t *testing.T) {
	captureLogs(t)
	f := newFakeDocker(t)
	f.addContainer("eski-nginx", "nginx", "running", nil,
		fakePort{IP: "0.0.0.0", PrivatePort: 80, PublicPort: 18080, Type: "tcp"})
	e := newTestEnv(t, f.host(), map[string]string{"tek.yaml": manifestTek})
	w := e.do("POST", "/apps/catalog/tek/install", e.admin, `{"env":{"ADMIN_PASSWORD":"x"}}`)
	wantError(t, w, http.StatusConflict, "port_in_use", "18080/tcp", "Web arayüzü", `"eski-nginx"`)
	if ops := f.changes(); len(ops) != 0 {
		t.Errorf("a refused installation changed Docker: %v", ops)
	}
	if len(e.mod.jobs.list()) != 0 {
		t.Error("a refused installation created a job")
	}
	// Two fields of one request on the same port.
	multi := strings.Replace(manifestTek, "    web_ui: { scheme: http, path: / }",
		"    web_ui: { scheme: http, path: / }\n  - host: 18443\n    container: 443\n    key: tls\n    label: Güvenli arayüz", 1)
	e2 := newTestEnv(t, f.host(), map[string]string{"tek.yaml": multi})
	w = e2.do("POST", "/apps/catalog/tek/install", e2.admin, `{"env":{"ADMIN_PASSWORD":"x"},"ports":{"web":19000,"tls":19000}}`)
	wantError(t, w, http.StatusBadRequest, "invalid_input", "19000/tcp", "Web arayüzü", "Güvenli arayüz")
}
