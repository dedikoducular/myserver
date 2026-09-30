package apps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"

	"myserver/internal/httpx"
)

var errDockerUnavailable = httpx.Unavailable("docker_unavailable", "Docker servisine ulaşılamıyor.")

// reporter receives the progress of an engine operation.
type reporter interface {
	Step(msg string)
	Log(msg string)
	emit(e Event)
}

type nopReporter struct{}

func (nopReporter) Step(string) {}
func (nopReporter) Log(string)  {}
func (nopReporter) emit(Event)  {}

// engine performs the Docker operations of the application system.
type engine struct {
	host string
	// dataDir is the panel's data directory; it is never mounted into an
	// application.
	dataDir string

	mu  sync.Mutex
	cli *client.Client
}

func (e *engine) client() (*client.Client, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cli != nil {
		return e.cli, nil
	}
	opts := []client.Opt{client.WithAPIVersionNegotiation()}
	if e.host != "" {
		opts = append(opts, client.WithHost(e.host))
	}
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, errDockerUnavailable.Wrap(err)
	}
	e.cli = cli
	return cli, nil
}

// ready returns the client after confirming the daemon answers.
func (e *engine) ready(ctx context.Context) (*client.Client, error) {
	cli, err := e.client()
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	if _, err := cli.Ping(pctx); err != nil {
		return nil, errDockerUnavailable.Wrap(err)
	}
	return cli, nil
}

// dockerError converts a Docker failure into a user-facing error. The raw
// cause is kept for the log only.
func dockerError(err error, fallback string) error {
	if err == nil {
		return nil
	}
	var he *httpx.Error
	if errors.As(err, &he) {
		return err
	}
	var ie *InputError
	if errors.As(err, &ie) {
		return err
	}
	if client.IsErrConnectionFailed(err) {
		return errDockerUnavailable.Wrap(err)
	}
	if errors.Is(err, context.Canceled) {
		return httpx.NewError(http.StatusServiceUnavailable, "cancelled", "İşlem iptal edildi.").Wrap(err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return httpx.NewError(http.StatusGatewayTimeout, "docker_timeout", "Docker zamanında yanıt vermedi.").Wrap(err)
	}
	low := strings.ToLower(err.Error())
	msg := fallback
	switch {
	case strings.Contains(low, "port is already allocated"), strings.Contains(low, "address already in use"):
		msg = "Seçilen portlardan biri başka bir servis tarafından kullanılıyor."
	case strings.Contains(low, "no space left on device"):
		msg = "Diskte yeterli boş alan yok."
	case strings.Contains(low, "manifest unknown"), strings.Contains(low, "not found: manifest"),
		strings.Contains(low, "repository does not exist"), strings.Contains(low, "pull access denied"):
		msg = "Uygulama görüntüsü kayıt deposunda bulunamadı veya erişim reddedildi."
	case strings.Contains(low, "toomanyrequests"), strings.Contains(low, "rate limit"):
		msg = "Görüntü deposu indirme sınırına ulaşıldı. Bir süre sonra tekrar deneyin."
	case strings.Contains(low, "no such host"), strings.Contains(low, "i/o timeout"),
		strings.Contains(low, "tls handshake"), strings.Contains(low, "connection refused"),
		strings.Contains(low, "network is unreachable"), strings.Contains(low, "temporary failure in name resolution"):
		msg = "Görüntü deposuna ulaşılamıyor. Sunucunun internet bağlantısını kontrol edin."
	case strings.Contains(low, "no matching manifest"):
		msg = "Bu uygulamanın görüntüsü sunucunun işlemci mimarisini desteklemiyor."
	case strings.Contains(low, "error gathering device information"), strings.Contains(low, "no such device"):
		msg = "Uygulamanın istediği donanım aygıtı sunucuda bulunamadı."
	}
	return httpx.NewError(http.StatusBadGateway, "docker_error", msg).Wrap(err)
}

// userMessage extracts the Turkish message of an error for job results.
func userMessage(err error) string {
	var he *httpx.Error
	if errors.As(err, &he) {
		return he.Message
	}
	var ie *InputError
	if errors.As(err, &ie) {
		return ie.Message
	}
	return "Beklenmeyen bir hata oluştu."
}

func managedLabels(slug, service string) map[string]string {
	l := map[string]string{LabelApp: slug, LabelManaged: "true"}
	if service != "" {
		l[LabelService] = service
	}
	return l
}

func appFilter(slug string) filters.Args {
	f := filters.NewArgs(filters.Arg("label", LabelManaged+"=true"))
	if slug != "" {
		f.Add("label", LabelApp+"="+slug)
	}
	return f
}

func containerDisplayName(c container.Summary) string {
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	if len(c.ID) > 12 {
		return c.ID[:12]
	}
	return c.ID
}

// appContainers lists the containers of one application (all states), or of
// every managed application when slug is "".
func (e *engine) appContainers(ctx context.Context, cli *client.Client, slug string) ([]container.Summary, error) {
	list, err := cli.ContainerList(ctx, container.ListOptions{All: true, Filters: appFilter(slug)})
	if err != nil {
		return nil, dockerError(err, "Konteyner listesi alınamadı.")
	}
	return list, nil
}

// checkPorts reports which host ports of cfg are already taken by another
// container or by a service listening on the host. The address a port is
// bound on is taken into account: a port held on 0.0.0.0 conflicts with
// every address, a port held on a specific address only with that address
// and with 0.0.0.0. Ports held by the application's own running containers
// are not conflicts.
func (e *engine) checkPorts(ctx context.Context, cli *client.Client, cfg *Config) ([]PortConflict, error) {
	all, err := cli.ContainerList(ctx, container.ListOptions{All: false})
	if err != nil {
		return nil, dockerError(err, "Konteyner listesi alınamadı.")
	}
	type key struct {
		port  int
		proto string
	}
	type holder struct{ name, ip string }
	taken := map[key][]holder{}
	own := map[key]bool{}
	ownHostMode := false
	for _, c := range all {
		mine := c.Labels[LabelManaged] == "true" && c.Labels[LabelApp] == cfg.Slug
		if mine && c.HostConfig.NetworkMode == "host" {
			ownHostMode = true
		}
		for _, p := range c.Ports {
			if p.PublicPort == 0 {
				continue
			}
			k := key{int(p.PublicPort), strings.ToLower(p.Type)}
			if mine {
				own[k] = true
			} else {
				taken[k] = append(taken[k], holder{containerDisplayName(c), p.IP})
			}
		}
	}
	tcp, udp, _ := hostListening()
	conflicts := []PortConflict{}
	for _, s := range cfg.Services {
		for _, p := range s.Ports {
			ours := AddrAny
			if s.NetworkMode == "bridge" && p.HostIP != "" {
				ours = p.HostIP
			}
			k := key{p.Host, p.Protocol}
			usedBy := ""
			for _, h := range taken[k] {
				if BindsOverlap(ours, h.ip) {
					usedBy = h.name
					break
				}
			}
			if usedBy != "" {
				conflicts = append(conflicts, PortConflict{Port: p.Host, Protocol: p.Protocol, Label: p.Label, UsedBy: usedBy})
				continue
			}
			if own[k] || (ownHostMode && s.NetworkMode == "host") {
				continue
			}
			socks := tcp[p.Host]
			if p.Protocol == "udp" {
				socks = udp[p.Host]
			}
			for _, addr := range socks {
				// A specific IPv6 address never collides with an
				// IPv4 loopback binding.
				if addr == addrIPv6 && ours != AddrAny {
					continue
				}
				if BindsOverlap(ours, addr) {
					conflicts = append(conflicts, PortConflict{Port: p.Host, Protocol: p.Protocol, Label: p.Label})
					break
				}
			}
		}
	}
	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].Port < conflicts[j].Port })
	return conflicts, nil
}

func portConflictError(c []PortConflict) error {
	msgs := make([]string, 0, len(c))
	for _, x := range c {
		msgs = append(msgs, x.Message())
	}
	return httpx.NewError(http.StatusConflict, "port_in_use", strings.Join(msgs, " "))
}

// protectedDirs lists the directories that must not be mounted into an
// application: the data directory as configured and as it resolves through
// symbolic links.
func protectedDirs(dataDir string) []string {
	if dataDir == "" {
		return nil
	}
	out := []string{filepath.ToSlash(dataDir)}
	if real, err := filepath.EvalSymlinks(dataDir); err == nil && filepath.ToSlash(real) != out[0] {
		out = append(out, filepath.ToSlash(real))
	}
	return out
}

// prepareBindDir makes sure a bind path resolves (through symbolic links) to
// a location inside the allowed roots and outside the protected directories,
// and creates the directory if needed.
func prepareBindDir(p string, roots, protected []string) error {
	if err := CheckBindPath(p, roots); err != nil {
		return err
	}
	if err := checkProtected(p, protected); err != nil {
		return err
	}
	realRoots := make([]string, 0, len(roots))
	for _, r := range roots {
		if rr, err := filepath.EvalSymlinks(r); err == nil {
			realRoots = append(realRoots, filepath.ToSlash(rr))
		}
	}
	existing := p
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return inputErr("%s klasörüne erişilemiyor.", p)
		}
		parent := path.Dir(existing)
		if parent == existing {
			break
		}
		existing = parent
	}
	real, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return inputErr("%s klasörüne erişilemiyor.", p)
	}
	resolved := path.Clean(filepath.ToSlash(real) + strings.TrimPrefix(p, existing))
	if err := CheckBindPath(resolved, realRoots); err != nil {
		return inputErr("%s izin verilen klasörlerin dışına işaret ediyor.", p)
	}
	if err := checkProtected(resolved, protected); err != nil {
		return inputErr("%s panelin kendi veri klasörüne işaret ediyor ve uygulamaya bağlanamaz.", p)
	}
	if st, err := os.Stat(p); err == nil {
		if !st.IsDir() {
			return inputErr("%s bir klasör değil.", p)
		}
		return nil
	}
	if err := os.MkdirAll(p, 0o755); err != nil {
		return inputErr("%s klasörü oluşturulamadı. Klasörün bulunduğu diskin bağlı ve yazılabilir olduğunu kontrol edin.", p)
	}
	return nil
}

type pullMessage struct {
	Status         string `json:"status"`
	ID             string `json:"id"`
	Error          string `json:"error"`
	ProgressDetail struct {
		Current int64 `json:"current"`
		Total   int64 `json:"total"`
	} `json:"progressDetail"`
}

// pull downloads an image, reporting aggregated layer progress.
func (e *engine) pull(ctx context.Context, cli *client.Client, ref string, rep reporter) error {
	rep.Log(ref + " indiriliyor…")
	rc, err := cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return dockerError(err, "Görüntü indirilemedi: "+ref)
	}
	defer rc.Close()
	type layer struct{ cur, total int64 }
	layers := map[string]*layer{}
	var lastEmit time.Time
	var lastCur int64
	dec := json.NewDecoder(rc)
	for {
		var m pullMessage
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return dockerError(err, "Görüntü indirilirken bağlantı kesildi: "+ref)
		}
		if m.Error != "" {
			return dockerError(errors.New(m.Error), "Görüntü indirilemedi: "+ref)
		}
		if m.ID == "" {
			continue
		}
		switch m.Status {
		case "Downloading":
			l := layers[m.ID]
			if l == nil {
				l = &layer{}
				layers[m.ID] = l
			}
			l.cur, l.total = m.ProgressDetail.Current, m.ProgressDetail.Total
		case "Download complete", "Pull complete":
			if l := layers[m.ID]; l != nil && l.total > 0 {
				l.cur = l.total
			}
		default:
			continue
		}
		var cur, total int64
		for _, l := range layers {
			cur += l.cur
			total += l.total
		}
		if total > 0 && cur != lastCur && time.Since(lastEmit) >= time.Second {
			lastEmit, lastCur = time.Now(), cur
			rep.emit(Event{Type: "progress", Image: ref, Message: ref, Current: cur, Total: total})
		}
	}
	var cur, total int64
	for _, l := range layers {
		cur += l.cur
		total += l.total
	}
	if total > 0 {
		rep.emit(Event{Type: "progress", Image: ref, Message: ref, Current: total, Total: total})
	}
	rep.Log(ref + " hazır.")
	return nil
}

func (e *engine) imageInfo(ctx context.Context, cli *client.Client, ref string) (ImageInfo, bool) {
	ins, err := cli.ImageInspect(ctx, ref)
	if err != nil {
		return ImageInfo{Image: ref}, false
	}
	info := ImageInfo{Image: ref, ID: ins.ID}
	if len(ins.RepoDigests) > 0 {
		info.Digest = ins.RepoDigests[0]
	}
	return info, true
}

// pullAll pulls the images of cfg. With force false, images already present
// locally are not pulled again.
func (e *engine) pullAll(ctx context.Context, cli *client.Client, cfg *Config, force bool, rep reporter) (map[string]ImageInfo, error) {
	done := map[string]bool{}
	for _, s := range cfg.Services {
		if done[s.Image] {
			continue
		}
		done[s.Image] = true
		if !force {
			if _, ok := e.imageInfo(ctx, cli, s.Image); ok {
				continue
			}
		}
		if err := e.pull(ctx, cli, s.Image, rep); err != nil {
			return nil, err
		}
	}
	out := map[string]ImageInfo{}
	for _, s := range cfg.Services {
		info, ok := e.imageInfo(ctx, cli, s.Image)
		if !ok {
			return nil, httpx.NewError(http.StatusBadGateway, "docker_error", "Görüntü indirildi ancak bulunamadı: "+s.Image)
		}
		out[s.Name] = info
	}
	return out, nil
}

// created records what one operation created, for cleanup on failure.
type created struct {
	containers []string
	network    string
	volumes    []string
}

func (e *engine) ensureNetwork(ctx context.Context, cli *client.Client, cfg *Config, made *created) error {
	if cfg.Network == "" {
		return nil
	}
	ins, err := cli.NetworkInspect(ctx, cfg.Network, network.InspectOptions{})
	if err == nil {
		if ins.Labels[LabelManaged] != "true" || ins.Labels[LabelApp] != cfg.Slug {
			return httpx.Conflict("\"" + cfg.Network + "\" adında, bu panelin oluşturmadığı bir Docker ağı zaten var.")
		}
		return nil
	}
	if !cerrdefs.IsNotFound(err) {
		return dockerError(err, "Docker ağı sorgulanamadı.")
	}
	if _, err := cli.NetworkCreate(ctx, cfg.Network, network.CreateOptions{
		Driver: "bridge", Labels: managedLabels(cfg.Slug, ""),
	}); err != nil {
		return dockerError(err, "Uygulama ağı oluşturulamadı.")
	}
	made.network = cfg.Network
	return nil
}

func (e *engine) ensureVolumes(ctx context.Context, cli *client.Client, cfg *Config, roots []string, made *created) error {
	seen := map[string]bool{}
	for _, s := range cfg.Services {
		for _, v := range s.Volumes {
			switch v.Type {
			case VolumeNamed:
				if seen[v.Source] {
					continue
				}
				seen[v.Source] = true
				if _, err := cli.VolumeInspect(ctx, v.Source); err == nil {
					continue // existing data is reused, never recreated
				} else if !cerrdefs.IsNotFound(err) {
					return dockerError(err, "Veri birimi sorgulanamadı.")
				}
				if _, err := cli.VolumeCreate(ctx, volume.CreateOptions{
					Name: v.Source, Labels: managedLabels(cfg.Slug, ""),
				}); err != nil {
					return dockerError(err, "Veri birimi oluşturulamadı: "+v.Source)
				}
				made.volumes = append(made.volumes, v.Source)
			case VolumeBind:
				if err := prepareBindDir(v.Source, roots, protectedDirs(e.dataDir)); err != nil {
					return err
				}
			case VolumeSystem:
				if _, err := os.Lstat(v.Source); err != nil {
					return inputErr("Uygulamanın ihtiyaç duyduğu %s yolu sunucuda bulunamadı.", v.Source)
				}
			}
		}
	}
	return nil
}

func buildContainer(cfg *Config, s *ServiceConfig, rep reporter) (*container.Config, *container.HostConfig, *network.NetworkingConfig, error) {
	labels := managedLabels(cfg.Slug, s.Name)
	cc := &container.Config{
		Image:  s.Image,
		Labels: labels,
		User:   s.User,
	}
	if len(s.Command) > 0 {
		cc.Cmd = append([]string{}, s.Command...)
	}
	for _, e := range s.Env {
		cc.Env = append(cc.Env, e.Name+"="+e.Value)
	}
	if h := s.Healthcheck; h != nil {
		hc := &container.HealthConfig{Test: append([]string{}, h.Test...), Retries: h.Retries}
		hc.Interval, _ = time.ParseDuration(h.Interval)
		hc.Timeout, _ = time.ParseDuration(h.Timeout)
		hc.StartPeriod, _ = time.ParseDuration(h.StartPeriod)
		cc.Healthcheck = hc
	}

	hc := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyMode(s.Restart)},
		Privileged:    s.Privileged,
		ShmSize:       s.ShmSize,
	}
	if len(s.CapAdd) > 0 {
		hc.CapAdd = append([]string{}, s.CapAdd...)
	}
	for _, d := range s.Devices {
		if _, err := os.Stat(d.Host); err != nil {
			if d.Optional {
				rep.Log(d.Host + " aygıtı sunucuda yok, atlandı.")
				continue
			}
			return nil, nil, nil, inputErr("Uygulamanın ihtiyaç duyduğu %s aygıtı sunucuda bulunamadı.", d.Host)
		}
		hc.Devices = append(hc.Devices, container.DeviceMapping{
			PathOnHost: d.Host, PathInContainer: d.Container, CgroupPermissions: d.Permissions,
		})
	}
	if len(s.Tmpfs) > 0 {
		hc.Tmpfs = map[string]string{}
		for _, t := range s.Tmpfs {
			opt := ""
			if t.Size != "" {
				n, err := ParseSize(t.Size)
				if err != nil {
					return nil, nil, nil, inputErr("Geçici bellek alanı boyutu geçersiz.")
				}
				opt = "size=" + strconv.FormatInt(n, 10)
			}
			hc.Tmpfs[t.Target] = opt
		}
	}
	for _, v := range s.Volumes {
		mt := mount.Mount{Source: v.Source, Target: v.Target, ReadOnly: v.ReadOnly}
		if v.Type == VolumeNamed {
			mt.Type = mount.TypeVolume
		} else {
			mt.Type = mount.TypeBind
		}
		hc.Mounts = append(hc.Mounts, mt)
	}

	var nc *network.NetworkingConfig
	switch s.NetworkMode {
	case "host":
		hc.NetworkMode = "host"
	case "none":
		hc.NetworkMode = "none"
	default:
		hc.NetworkMode = container.NetworkMode(cfg.Network)
		nc = &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{
			cfg.Network: {Aliases: []string{s.Name}},
		}}
		if len(s.Ports) > 0 {
			cc.ExposedPorts = nat.PortSet{}
			hc.PortBindings = nat.PortMap{}
			for _, p := range s.Ports {
				np, err := nat.NewPort(p.Protocol, strconv.Itoa(p.Container))
				if err != nil {
					return nil, nil, nil, inputErr("Port tanımı geçersiz: %d/%s", p.Container, p.Protocol)
				}
				cc.ExposedPorts[np] = struct{}{}
				hostIP := p.HostIP
				if hostIP == "" {
					hostIP = hostIPFor(cfg.BindAddress)
				}
				hc.PortBindings[np] = append(hc.PortBindings[np], nat.PortBinding{HostIP: hostIP, HostPort: strconv.Itoa(p.Host)})
			}
		}
	}
	return cc, hc, nc, nil
}

func orderedServices(cfg *Config) ([]*ServiceConfig, error) {
	specs := make([]ServiceSpec, 0, len(cfg.Services))
	byName := map[string]*ServiceConfig{}
	for i := range cfg.Services {
		s := &cfg.Services[i]
		specs = append(specs, ServiceSpec{Name: s.Name, DependsOn: s.DependsOn})
		byName[s.Name] = s
	}
	order, err := StartOrder(specs)
	if err != nil {
		return nil, inputErr("Servis bağımlılıkları geçersiz.")
	}
	out := make([]*ServiceConfig, 0, len(order))
	for _, n := range order {
		out = append(out, byName[n])
	}
	return out, nil
}

const (
	dependencyTimeout = 4 * time.Minute
	settleTime        = 4 * time.Second
	stopTimeoutSecs   = 30
)

// waitReady waits until a container runs and, when it has a health check,
// reports healthy. A container without a health check is given a short
// moment to show that it does not exit immediately.
func (e *engine) waitReady(ctx context.Context, cli *client.Client, id, name string, mustBeHealthy bool) error {
	deadline := time.Now().Add(dependencyTimeout)
	settled := time.Now().Add(settleTime)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		ins, err := cli.ContainerInspect(ctx, id)
		if err != nil {
			return dockerError(err, "Konteyner durumu okunamadı.")
		}
		st := ins.State
		if st != nil {
			if !st.Running && !st.Restarting && (st.Status == "exited" || st.Status == "dead") {
				return httpx.NewError(http.StatusBadGateway, "start_failed",
					fmt.Sprintf("\"%s\" başlatıldıktan hemen sonra durdu (çıkış kodu %d). Ayrıntılar için uygulama loglarına bakın.", name, st.ExitCode))
			}
			if st.Running {
				switch {
				case st.Health != nil && st.Health.Status == container.Healthy:
					return nil
				case st.Health != nil && st.Health.Status == container.Unhealthy && mustBeHealthy:
					return httpx.NewError(http.StatusBadGateway, "start_failed",
						"\""+name+"\" sağlık denetimini geçemedi. Ayrıntılar için uygulama loglarına bakın.")
				case st.Health == nil || st.Health.Status == container.NoHealthcheck || !mustBeHealthy:
					if time.Now().After(settled) {
						return nil
					}
				}
			}
		}
		if time.Now().After(deadline) {
			return httpx.NewError(http.StatusGatewayTimeout, "start_timeout",
				"\""+name+"\" beklenen sürede hazır olmadı. Ayrıntılar için uygulama loglarına bakın.")
		}
		select {
		case <-ctx.Done():
			return dockerError(ctx.Err(), "İşlem iptal edildi.")
		case <-tick.C:
		}
	}
}

// tailLogs copies the last lines of a container's output into the job log,
// so that a failed start explains itself.
func (e *engine) tailLogs(cli *client.Client, id, service string, mask func(string) string, rep reporter) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rc, err := cli.ContainerLogs(ctx, id, container.LogsOptions{ShowStdout: true, ShowStderr: true, Tail: "15"})
	if err != nil {
		return
	}
	defer rc.Close()
	_ = demuxLines(rc, func(_ string, line string) bool {
		rep.emit(Event{Type: "log", Service: service, Message: mask(line)})
		return true
	})
}

// up creates and starts the containers of cfg in dependency order.
func (e *engine) up(ctx context.Context, cli *client.Client, cfg *Config, roots []string, rep reporter, made *created) error {
	services, err := orderedServices(cfg)
	if err != nil {
		return err
	}
	// A name held by a container that is not ours must not be touched.
	for _, s := range services {
		ins, err := cli.ContainerInspect(ctx, s.ContainerName)
		if err == nil {
			l := map[string]string{}
			if ins.Config != nil {
				l = ins.Config.Labels
			}
			if l[LabelManaged] != "true" || l[LabelApp] != cfg.Slug {
				return httpx.Conflict("\"" + s.ContainerName + "\" adında, bu panelin oluşturmadığı bir konteyner zaten var. Önce onu kaldırın veya yeniden adlandırın.")
			}
			return httpx.Conflict("\"" + s.ContainerName + "\" konteyneri zaten var.")
		} else if !cerrdefs.IsNotFound(err) {
			return dockerError(err, "Konteyner sorgulanamadı.")
		}
	}

	rep.Step("Ağ ve veri birimleri hazırlanıyor")
	if err := e.ensureNetwork(ctx, cli, cfg, made); err != nil {
		return err
	}
	if err := e.ensureVolumes(ctx, cli, cfg, roots, made); err != nil {
		return err
	}

	needed := map[string]bool{}
	for _, s := range services {
		for _, d := range s.DependsOn {
			needed[d] = true
		}
	}
	ids := map[string]string{}
	for _, s := range services {
		label := cfg.Name
		if len(services) > 1 {
			label = s.Name
		}
		rep.Step("\"" + label + "\" oluşturuluyor ve başlatılıyor")
		cc, hc, nc, err := buildContainer(cfg, s, rep)
		if err != nil {
			return err
		}
		res, err := cli.ContainerCreate(ctx, cc, hc, nc, nil, s.ContainerName)
		if err != nil {
			return dockerError(err, "\""+label+"\" konteyneri oluşturulamadı.")
		}
		made.containers = append(made.containers, res.ID)
		ids[s.Name] = res.ID
		if err := cli.ContainerStart(ctx, res.ID, container.StartOptions{}); err != nil {
			return dockerError(err, "\""+label+"\" başlatılamadı.")
		}
		if err := e.waitReady(ctx, cli, res.ID, label, needed[s.Name]); err != nil {
			e.tailLogs(cli, res.ID, s.Name, secretMasker(cfg), rep)
			return err
		}
	}
	return nil
}

// cleanup removes what a failed operation created. Volumes are only removed
// when this very operation created them (they hold nothing but what the
// failed start wrote); volumes that existed before are never touched.
func (e *engine) cleanup(cli *client.Client, made *created, rep reporter) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if len(made.containers) == 0 && made.network == "" && len(made.volumes) == 0 {
		return
	}
	rep.Step("Yarım kalan kurulum temizleniyor")
	for i := len(made.containers) - 1; i >= 0; i-- {
		if err := cli.ContainerRemove(ctx, made.containers[i], container.RemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
			slog.Warn("konteyner temizlenemedi", "id", made.containers[i], "error", err.Error())
		}
	}
	if made.network != "" {
		if err := cli.NetworkRemove(ctx, made.network); err != nil && !cerrdefs.IsNotFound(err) {
			slog.Warn("ağ temizlenemedi", "network", made.network, "error", err.Error())
		}
	}
	for _, v := range made.volumes {
		if err := cli.VolumeRemove(ctx, v, false); err != nil && !cerrdefs.IsNotFound(err) {
			slog.Warn("veri birimi temizlenemedi", "volume", v, "error", err.Error())
		}
	}
	*made = created{}
}

// removeContainers stops and removes every container of an application.
func (e *engine) removeContainers(ctx context.Context, cli *client.Client, slug string) error {
	list, err := e.appContainers(ctx, cli, slug)
	if err != nil {
		return err
	}
	timeout := stopTimeoutSecs
	for _, c := range list {
		if c.State == container.StateRunning || c.State == container.StateRestarting || c.State == container.StatePaused {
			if err := cli.ContainerStop(ctx, c.ID, container.StopOptions{Timeout: &timeout}); err != nil && !cerrdefs.IsNotFound(err) {
				slog.Warn("konteyner durdurulamadı", "name", containerDisplayName(c), "error", err.Error())
			}
		}
		if err := cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
			return dockerError(err, "\""+containerDisplayName(c)+"\" konteyneri kaldırılamadı.")
		}
	}
	return nil
}

func (e *engine) removeNetwork(ctx context.Context, cli *client.Client, slug string) {
	name := NetworkName(slug)
	ins, err := cli.NetworkInspect(ctx, name, network.InspectOptions{})
	if err != nil || ins.Labels[LabelManaged] != "true" || ins.Labels[LabelApp] != slug {
		return
	}
	if err := cli.NetworkRemove(ctx, name); err != nil && !cerrdefs.IsNotFound(err) {
		slog.Warn("uygulama ağı kaldırılamadı", "network", name, "error", err.Error())
	}
}

// removeVolumes deletes the named volumes of cfg that carry this
// application's labels. Bind paths and system mounts are never deleted.
func (e *engine) removeVolumes(ctx context.Context, cli *client.Client, cfg *Config) (removed []string, failed []string) {
	seen := map[string]bool{}
	for _, s := range cfg.Services {
		for _, v := range s.Volumes {
			if v.Type != VolumeNamed || seen[v.Source] {
				continue
			}
			seen[v.Source] = true
			ins, err := cli.VolumeInspect(ctx, v.Source)
			if err != nil {
				continue
			}
			if ins.Labels[LabelManaged] != "true" || ins.Labels[LabelApp] != cfg.Slug {
				failed = append(failed, v.Source)
				continue
			}
			if err := cli.VolumeRemove(ctx, v.Source, false); err != nil {
				slog.Warn("veri birimi silinemedi", "volume", v.Source, "error", err.Error())
				failed = append(failed, v.Source)
				continue
			}
			removed = append(removed, v.Source)
		}
	}
	return removed, failed
}

// recreate replaces the containers of an application with containers built
// from cfg. The old containers are kept (stopped and renamed) until the new
// ones run, and are put back if the new ones fail.
func (e *engine) recreate(ctx context.Context, cli *client.Client, cfg *Config, roots []string, tag string, rep reporter) error {
	old, err := e.appContainers(ctx, cli, cfg.Slug)
	if err != nil {
		return err
	}
	type kept struct {
		id, name, backup string
		running          bool
	}
	var parked []kept
	timeout := stopTimeoutSecs

	restore := func() {
		bctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		rep.Step("Önceki sürüm geri yükleniyor")
		for _, k := range parked {
			if err := cli.ContainerRename(bctx, k.id, k.name); err != nil {
				slog.Warn("konteyner adı geri alınamadı", "name", k.name, "error", err.Error())
			}
		}
		for i := len(parked) - 1; i >= 0; i-- {
			k := parked[i]
			if !k.running {
				continue
			}
			if err := cli.ContainerStart(bctx, k.id, container.StartOptions{}); err != nil {
				slog.Warn("önceki konteyner başlatılamadı", "name", k.name, "error", err.Error())
			}
		}
	}

	if len(old) > 0 {
		rep.Step("Çalışan konteynerler durduruluyor")
	}
	for _, c := range old {
		name := containerDisplayName(c)
		k := kept{id: c.ID, name: name, backup: name + "-onceki-" + tag,
			running: c.State == container.StateRunning || c.State == container.StateRestarting}
		if c.State != container.StateExited && c.State != container.StateCreated && c.State != container.StateDead {
			if err := cli.ContainerStop(ctx, c.ID, container.StopOptions{Timeout: &timeout}); err != nil {
				restore()
				return dockerError(err, "\""+name+"\" durdurulamadı.")
			}
		}
		if err := cli.ContainerRename(ctx, c.ID, k.backup); err != nil {
			parked = append(parked, kept{id: c.ID, name: name, running: k.running})
			restore()
			return dockerError(err, "\""+name+"\" yeniden adlandırılamadı.")
		}
		parked = append(parked, k)
	}

	made := &created{}
	if err := e.up(ctx, cli, cfg, roots, rep, made); err != nil {
		// What was created for the new configuration is dropped with it.
		// The network and the volumes of the previous configuration
		// existed before and are therefore not in the list.
		e.cleanup(cli, made, rep)
		restore()
		return err
	}
	for _, k := range parked {
		if err := cli.ContainerRemove(ctx, k.id, container.RemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
			slog.Warn("eski konteyner kaldırılamadı", "name", k.backup, "error", err.Error())
		}
	}
	return nil
}

// lifecycle applies start/stop/restart to the containers of an application
// in dependency order (reverse order for stop).
func (e *engine) lifecycle(ctx context.Context, cli *client.Client, cfg *Config, action string) error {
	services, err := orderedServices(cfg)
	if err != nil {
		return err
	}
	list, err := e.appContainers(ctx, cli, cfg.Slug)
	if err != nil {
		return err
	}
	byService := map[string]container.Summary{}
	for _, c := range list {
		byService[c.Labels[LabelService]] = c
	}
	if len(byService) == 0 {
		return httpx.NewError(http.StatusConflict, "container_missing",
			"Uygulamanın konteynerleri bulunamadı. Ayarlar penceresinden kaydederek yeniden oluşturabilirsiniz.")
	}
	timeout := stopTimeoutSecs
	stop := func() error {
		for i := len(services) - 1; i >= 0; i-- {
			c, ok := byService[services[i].Name]
			if !ok {
				continue
			}
			if err := cli.ContainerStop(ctx, c.ID, container.StopOptions{Timeout: &timeout}); err != nil && !cerrdefs.IsNotFound(err) {
				return dockerError(err, "\""+containerDisplayName(c)+"\" durdurulamadı.")
			}
		}
		return nil
	}
	start := func() error {
		for _, s := range services {
			c, ok := byService[s.Name]
			if !ok {
				return httpx.NewError(http.StatusConflict, "container_missing",
					"\""+s.Name+"\" servisinin konteyneri bulunamadı. Ayarlar penceresinden kaydederek yeniden oluşturabilirsiniz.")
			}
			if err := cli.ContainerStart(ctx, c.ID, container.StartOptions{}); err != nil {
				return dockerError(err, "\""+containerDisplayName(c)+"\" başlatılamadı.")
			}
		}
		return nil
	}
	switch action {
	case "stop":
		return stop()
	case "start":
		return start()
	case "restart":
		if err := stop(); err != nil {
			return err
		}
		return start()
	}
	return httpx.BadRequest("Bilinmeyen işlem.")
}
