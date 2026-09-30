package docker

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"

	"myserver/internal/auth"
	"myserver/internal/httpx"
)

// PortMapping is one published or exposed container port.
type PortMapping struct {
	HostIP        string `json:"host_ip"`
	HostPort      int    `json:"host_port"` // 0 when the port is not published
	ContainerPort int    `json:"container_port"`
	Protocol      string `json:"protocol"`
}

// Address is a container's IP address on one network.
type Address struct {
	Network string `json:"network"`
	IP      string `json:"ip"`
}

// Container is one row of the container list.
type Container struct {
	ID            string            `json:"id"`
	ShortID       string            `json:"short_id"`
	Name          string            `json:"name"`
	Image         string            `json:"image"`
	ImageID       string            `json:"image_id"`
	State         string            `json:"state"`
	Status        string            `json:"status"`
	ExitCode      *int              `json:"exit_code"`
	CreatedAt     int64             `json:"created_at"`
	StartedAt     *int64            `json:"started_at"`
	UptimeSeconds *int64            `json:"uptime_seconds"`
	Addresses     []Address         `json:"addresses"`
	Ports         []PortMapping     `json:"ports"`
	Labels        map[string]string `json:"labels"`
	App           *string           `json:"app"`
}

func containerName(names []string, id string) string {
	for _, n := range names {
		n = strings.TrimPrefix(n, "/")
		// Names containing "/" are legacy link aliases.
		if n != "" && !strings.Contains(n, "/") {
			return n
		}
	}
	if len(names) > 0 {
		return strings.TrimPrefix(names[0], "/")
	}
	return shortID(id)
}

func isActive(state string) bool {
	return state == "running" || state == "paused"
}

func summaryToContainer(c container.Summary) Container {
	out := Container{
		ID:        c.ID,
		ShortID:   shortID(c.ID),
		Name:      containerName(c.Names, c.ID),
		Image:     c.Image,
		ImageID:   c.ImageID,
		State:     string(c.State),
		Status:    c.Status,
		CreatedAt: c.Created,
		Addresses: []Address{},
		Ports:     []PortMapping{},
		Labels:    maskMap(c.Labels),
	}
	if code, ok := exitCodeFromStatus(c.Status); ok && out.State == "exited" {
		out.ExitCode = &code
	}
	if slug := strings.TrimSpace(c.Labels[AppLabel]); slug != "" {
		out.App = &slug
	}
	if c.NetworkSettings != nil {
		for name, ep := range c.NetworkSettings.Networks {
			if ep == nil {
				continue
			}
			if ep.IPAddress != "" {
				out.Addresses = append(out.Addresses, Address{Network: name, IP: ep.IPAddress})
			}
			if ep.GlobalIPv6Address != "" {
				out.Addresses = append(out.Addresses, Address{Network: name, IP: ep.GlobalIPv6Address})
			}
		}
		sort.Slice(out.Addresses, func(i, j int) bool {
			if out.Addresses[i].Network != out.Addresses[j].Network {
				return out.Addresses[i].Network < out.Addresses[j].Network
			}
			return out.Addresses[i].IP < out.Addresses[j].IP
		})
	}
	for _, p := range c.Ports {
		out.Ports = append(out.Ports, PortMapping{
			HostIP:        p.IP,
			HostPort:      int(p.PublicPort),
			ContainerPort: int(p.PrivatePort),
			Protocol:      p.Type,
		})
	}
	sortPorts(out.Ports)
	return out
}

func sortPorts(p []PortMapping) {
	sort.Slice(p, func(i, j int) bool {
		a, b := p[i], p[j]
		if a.ContainerPort != b.ContainerPort {
			return a.ContainerPort < b.ContainerPort
		}
		if a.Protocol != b.Protocol {
			return a.Protocol < b.Protocol
		}
		if a.HostPort != b.HostPort {
			return a.HostPort < b.HostPort
		}
		return a.HostIP < b.HostIP
	})
}

// startCache remembers container start times, which the list API does not
// return, so the list does not inspect every container on every request.
type startCache struct {
	mu    sync.Mutex
	items map[string]startEntry
}

type startEntry struct {
	startedAt *int64
	fetched   time.Time
}

const startCacheTTL = 20 * time.Second

func newStartCache() *startCache {
	return &startCache{items: map[string]startEntry{}}
}

func (c *startCache) get(id string) (*int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[id]
	if !ok || time.Since(e.fetched) > startCacheTTL {
		return nil, false
	}
	return e.startedAt, true
}

func (c *startCache) put(id string, startedAt *int64) {
	c.mu.Lock()
	c.items[id] = startEntry{startedAt: startedAt, fetched: time.Now()}
	c.mu.Unlock()
}

func (c *startCache) drop(id string) {
	c.mu.Lock()
	delete(c.items, id)
	c.mu.Unlock()
}

// retain forgets containers that no longer exist.
func (c *startCache) retain(ids map[string]bool) {
	c.mu.Lock()
	for id := range c.items {
		if !ids[id] {
			delete(c.items, id)
		}
	}
	c.mu.Unlock()
}

const inspectConcurrency = 6

// fillStartTimes sets StartedAt/UptimeSeconds of active containers.
func (m *Module) fillStartTimes(ctx context.Context, cli *client.Client, list []Container) {
	sem := make(chan struct{}, inspectConcurrency)
	var wg sync.WaitGroup
	present := make(map[string]bool, len(list))
	for i := range list {
		c := &list[i]
		present[c.ID] = true
		if !isActive(c.State) {
			continue
		}
		if at, ok := m.started.get(c.ID); ok {
			c.StartedAt = at
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			insp, err := cli.ContainerInspect(ctx, c.ID)
			if err != nil || insp.ContainerJSONBase == nil || insp.State == nil {
				return
			}
			at := parseDockerTime(insp.State.StartedAt)
			m.started.put(c.ID, at)
			c.StartedAt = at
		}()
	}
	wg.Wait()
	m.started.retain(present)
	now := time.Now().Unix()
	for i := range list {
		c := &list[i]
		if c.StartedAt != nil && isActive(c.State) {
			up := now - *c.StartedAt
			if up < 0 {
				up = 0
			}
			c.UptimeSeconds = &up
		}
	}
}

func stateRank(state string) int {
	switch state {
	case "running":
		return 0
	case "restarting":
		return 1
	case "paused":
		return 2
	}
	return 3
}

func (m *Module) listContainers(ctx context.Context) ([]Container, error) {
	cli, err := m.client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	raw, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, apiError(err, errText{fallback: "Konteyner listesi alınamadı."})
	}
	out := make([]Container, 0, len(raw))
	for _, c := range raw {
		out = append(out, summaryToContainer(c))
	}
	m.fillStartTimes(ctx, cli, out)
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := stateRank(out[i].State), stateRank(out[j].State)
		if ri != rj {
			return ri < rj
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

func (m *Module) handleContainers(w http.ResponseWriter, r *http.Request) error {
	list, err := m.listContainers(r.Context())
	if err != nil {
		return err
	}
	httpx.OK(w, list)
	return nil
}

/* ---------- inspect ---------- */

// Mount is one mount of a container.
type Mount struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	ReadOnly    bool   `json:"read_only"`
}

// NetworkAttachment is a container's endpoint on one network.
type NetworkAttachment struct {
	Network    string   `json:"network"`
	NetworkID  string   `json:"network_id"`
	IPAddress  string   `json:"ip_address"`
	IPv6       string   `json:"ipv6_address"`
	Gateway    string   `json:"gateway"`
	MacAddress string   `json:"mac_address"`
	Aliases    []string `json:"aliases"`
}

// ContainerDetail is the sanitised result of inspecting a container. Secret
// values (environment, labels, command-line options) are masked.
type ContainerDetail struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	Image         string              `json:"image"`
	ImageID       string              `json:"image_id"`
	CreatedAt     *int64              `json:"created_at"`
	State         string              `json:"state"`
	Running       bool                `json:"running"`
	Paused        bool                `json:"paused"`
	Restarting    bool                `json:"restarting"`
	OOMKilled     bool                `json:"oom_killed"`
	ExitCode      int                 `json:"exit_code"`
	StartedAt     *int64              `json:"started_at"`
	FinishedAt    *int64              `json:"finished_at"`
	Health        *string             `json:"health"`
	RestartCount  int                 `json:"restart_count"`
	RestartPolicy string              `json:"restart_policy"`
	Platform      string              `json:"platform"`
	Driver        string              `json:"driver"`
	Hostname      string              `json:"hostname"`
	User          string              `json:"user"`
	WorkingDir    string              `json:"working_dir"`
	Entrypoint    []string            `json:"entrypoint"`
	Command       []string            `json:"command"`
	TTY           bool                `json:"tty"`
	Privileged    bool                `json:"privileged"`
	NetworkMode   string              `json:"network_mode"`
	MemoryLimit   int64               `json:"memory_limit"` // bytes, 0 = unlimited
	NanoCPUs      int64               `json:"nano_cpus"`    // 0 = unlimited
	Env           []EnvVar            `json:"env"`
	Labels        map[string]string   `json:"labels"`
	App           *string             `json:"app"`
	Mounts        []Mount             `json:"mounts"`
	Networks      []NetworkAttachment `json:"networks"`
	Ports         []PortMapping       `json:"ports"`
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func toDetail(insp container.InspectResponse) ContainerDetail {
	d := ContainerDetail{
		Entrypoint: []string{},
		Command:    []string{},
		Env:        []EnvVar{},
		Labels:     map[string]string{},
		Mounts:     []Mount{},
		Networks:   []NetworkAttachment{},
		Ports:      []PortMapping{},
	}
	if b := insp.ContainerJSONBase; b != nil {
		d.ID = b.ID
		d.Name = strings.TrimPrefix(b.Name, "/")
		d.ImageID = b.Image
		d.CreatedAt = parseDockerTime(b.Created)
		d.RestartCount = b.RestartCount
		d.Platform = b.Platform
		d.Driver = b.Driver
		if s := b.State; s != nil {
			d.State = string(s.Status)
			d.Running = s.Running
			d.Paused = s.Paused
			d.Restarting = s.Restarting
			d.OOMKilled = s.OOMKilled
			d.ExitCode = s.ExitCode
			d.StartedAt = parseDockerTime(s.StartedAt)
			d.FinishedAt = parseDockerTime(s.FinishedAt)
			if s.Health != nil {
				h := string(s.Health.Status)
				d.Health = &h
			}
		}
		if hc := b.HostConfig; hc != nil {
			d.RestartPolicy = string(hc.RestartPolicy.Name)
			d.Privileged = hc.Privileged
			d.NetworkMode = string(hc.NetworkMode)
			d.MemoryLimit = hc.Memory
			d.NanoCPUs = hc.NanoCPUs
		}
	}
	if c := insp.Config; c != nil {
		d.Image = c.Image
		d.Hostname = c.Hostname
		d.User = c.User
		d.WorkingDir = c.WorkingDir
		d.TTY = c.Tty
		d.Entrypoint = maskArgs(orEmpty(c.Entrypoint))
		d.Command = maskArgs(orEmpty(c.Cmd))
		d.Env = maskEnv(c.Env)
		sort.Slice(d.Env, func(i, j int) bool { return d.Env[i].Name < d.Env[j].Name })
		d.Labels = maskMap(c.Labels)
		if slug := strings.TrimSpace(c.Labels[AppLabel]); slug != "" {
			d.App = &slug
		}
	}
	for _, mp := range insp.Mounts {
		d.Mounts = append(d.Mounts, Mount{
			Type:        string(mp.Type),
			Name:        mp.Name,
			Source:      mp.Source,
			Destination: mp.Destination,
			ReadOnly:    !mp.RW,
		})
	}
	sort.Slice(d.Mounts, func(i, j int) bool { return d.Mounts[i].Destination < d.Mounts[j].Destination })
	if ns := insp.NetworkSettings; ns != nil {
		for name, ep := range ns.Networks {
			if ep == nil {
				continue
			}
			d.Networks = append(d.Networks, NetworkAttachment{
				Network:    name,
				NetworkID:  ep.NetworkID,
				IPAddress:  ep.IPAddress,
				IPv6:       ep.GlobalIPv6Address,
				Gateway:    ep.Gateway,
				MacAddress: ep.MacAddress,
				Aliases:    orEmpty(ep.Aliases),
			})
		}
		sort.Slice(d.Networks, func(i, j int) bool { return d.Networks[i].Network < d.Networks[j].Network })
		for port, bindings := range ns.Ports {
			num, proto, _ := strings.Cut(string(port), "/")
			cp, err := strconv.Atoi(num)
			if err != nil {
				continue
			}
			if len(bindings) == 0 {
				d.Ports = append(d.Ports, PortMapping{ContainerPort: cp, Protocol: proto})
				continue
			}
			for _, b := range bindings {
				hp, _ := strconv.Atoi(b.HostPort)
				d.Ports = append(d.Ports, PortMapping{HostIP: b.HostIP, HostPort: hp, ContainerPort: cp, Protocol: proto})
			}
		}
		sortPorts(d.Ports)
	}
	return d
}

var containerNotFound = "Konteyner bulunamadı."

func pathContainerID(r *http.Request) (string, error) {
	id := r.PathValue("id")
	if !validContainerID(id) {
		return "", httpx.BadRequest("Konteyner kimliği geçersiz.")
	}
	return id, nil
}

func (m *Module) handleInspect(w http.ResponseWriter, r *http.Request) error {
	id, err := pathContainerID(r)
	if err != nil {
		return err
	}
	cli, err := m.client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), listTimeout)
	defer cancel()
	insp, err := cli.ContainerInspect(ctx, id)
	if err != nil {
		return apiError(err, errText{notFound: containerNotFound, fallback: "Konteyner bilgileri alınamadı."})
	}
	httpx.OK(w, toDetail(insp))
	return nil
}

/* ---------- actions ---------- */

// resolve inspects a container to obtain its full ID and name, so audit
// records and crash detection refer to the same object whatever identifier
// the caller used.
func (m *Module) resolve(ctx context.Context, cli *client.Client, id string) (fullID, name string, err error) {
	insp, err := cli.ContainerInspect(ctx, id)
	if err != nil {
		return "", "", apiError(err, errText{notFound: containerNotFound, fallback: "Konteyner bilgileri alınamadı."})
	}
	if insp.ContainerJSONBase == nil {
		return "", "", httpx.NotFound(containerNotFound)
	}
	return insp.ID, strings.TrimPrefix(insp.Name, "/"), nil
}

type actionSpec struct {
	audit    string
	stops    bool // the action ends the container's main process
	text     errText
	doAction func(ctx context.Context, cli *client.Client, id string) error
}

var actionSpecs = map[string]actionSpec{
	"start": {
		audit: "docker.container_start",
		text:  errText{notFound: containerNotFound, fallback: "Konteyner başlatılamadı."},
		doAction: func(ctx context.Context, cli *client.Client, id string) error {
			return cli.ContainerStart(ctx, id, container.StartOptions{})
		},
	},
	"stop": {
		audit: "docker.container_stop",
		stops: true,
		text:  errText{notFound: containerNotFound, fallback: "Konteyner durdurulamadı."},
		doAction: func(ctx context.Context, cli *client.Client, id string) error {
			return cli.ContainerStop(ctx, id, container.StopOptions{})
		},
	},
	"restart": {
		audit: "docker.container_restart",
		stops: true,
		text:  errText{notFound: containerNotFound, fallback: "Konteyner yeniden başlatılamadı."},
		doAction: func(ctx context.Context, cli *client.Client, id string) error {
			return cli.ContainerRestart(ctx, id, container.StopOptions{})
		},
	},
	"kill": {
		audit: "docker.container_kill",
		stops: true,
		text: errText{
			notFound: containerNotFound,
			conflict: "Konteyner çalışmıyor; zorla durdurulacak bir süreç yok.",
			fallback: "Konteyner zorla durdurulamadı.",
		},
		doAction: func(ctx context.Context, cli *client.Client, id string) error {
			return cli.ContainerKill(ctx, id, "SIGKILL")
		},
	},
}

func (m *Module) containerAction(verb string) httpx.HandlerFunc {
	spec := actionSpecs[verb]
	return func(w http.ResponseWriter, r *http.Request) error {
		id, err := pathContainerID(r)
		if err != nil {
			return err
		}
		cli, err := m.client()
		if err != nil {
			return err
		}
		// The operation must finish even if the browser goes away, so the
		// audit record reflects what really happened.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), actionTimeout)
		defer cancel()
		fullID, name, err := m.resolve(ctx, cli, id)
		if err != nil {
			return err
		}
		if spec.stops {
			m.expected.mark(fullID)
		}
		if err := spec.doAction(ctx, cli, fullID); err != nil {
			if spec.stops {
				m.expected.unmark(fullID)
			}
			m.deps.Audit.Log(ctx, auth.ActorFrom(r), spec.audit, name, "başarısız", false)
			return apiError(err, spec.text)
		}
		m.started.drop(fullID)
		m.deps.Audit.Log(ctx, auth.ActorFrom(r), spec.audit, name, "", true)
		httpx.OK(w, map[string]string{"id": fullID, "name": name})
		return nil
	}
}

func (m *Module) handleContainerRemove(w http.ResponseWriter, r *http.Request) error {
	id, err := pathContainerID(r)
	if err != nil {
		return err
	}
	// Both options must be stated explicitly; removing volumes destroys data.
	var req struct {
		Force         *bool `json:"force"`
		RemoveVolumes *bool `json:"remove_volumes"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	if req.Force == nil || req.RemoveVolumes == nil {
		return httpx.BadRequest("'force' ve 'remove_volumes' seçenekleri açıkça belirtilmelidir.")
	}
	cli, err := m.client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), actionTimeout)
	defer cancel()
	fullID, name, err := m.resolve(ctx, cli, id)
	if err != nil {
		return err
	}
	detail := "force=" + strconv.FormatBool(*req.Force) + " remove_volumes=" + strconv.FormatBool(*req.RemoveVolumes)
	m.expected.mark(fullID)
	err = cli.ContainerRemove(ctx, fullID, container.RemoveOptions{
		Force:         *req.Force,
		RemoveVolumes: *req.RemoveVolumes,
	})
	if err != nil {
		m.expected.unmark(fullID)
		m.deps.Audit.Log(ctx, auth.ActorFrom(r), "docker.container_remove", name, detail+" başarısız", false)
		return apiError(err, errText{
			notFound: containerNotFound,
			conflict: "Konteyner çalışıyor. Önce durdurun veya zorla kaldırmayı seçin.",
			fallback: "Konteyner kaldırılamadı.",
		})
	}
	m.started.drop(fullID)
	m.deps.Audit.Log(ctx, auth.ActorFrom(r), "docker.container_remove", name, detail, true)
	httpx.OK(w, map[string]string{"id": fullID, "name": name})
	return nil
}
