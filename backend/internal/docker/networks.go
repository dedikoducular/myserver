package docker

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"

	"myserver/internal/auth"
	"myserver/internal/httpx"
)

// Subnet is one address range of a network.
type Subnet struct {
	Subnet  string `json:"subnet"`
	Gateway string `json:"gateway"`
}

// Network is one row of the network list.
type Network struct {
	ID        string            `json:"id"`
	ShortID   string            `json:"short_id"`
	Name      string            `json:"name"`
	Driver    string            `json:"driver"`
	Scope     string            `json:"scope"`
	Internal  bool              `json:"internal"`
	CreatedAt *int64            `json:"created_at"`
	Subnets   []Subnet          `json:"subnets"`
	Labels    map[string]string `json:"labels"`
	App       *string           `json:"app"`
	// Builtin networks (bridge, host, none, ...) cannot be removed.
	Builtin    bool  `json:"builtin"`
	InUse      bool  `json:"in_use"`
	Containers []Ref `json:"containers"`
}

// builtinNetwork reports networks created by Docker itself.
func builtinNetwork(name string) bool {
	switch name {
	case "bridge", "host", "none", "ingress", "docker_gwbridge":
		return true
	}
	return false
}

func (m *Module) handleNetworks(w http.ResponseWriter, r *http.Request) error {
	cli, err := m.client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), listTimeout)
	defer cancel()
	text := errText{fallback: "Ağ listesi alınamadı."}
	raw, err := cli.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return apiError(err, text)
	}
	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return apiError(err, text)
	}
	// The list call does not include attached containers, so they are
	// derived from the containers' own network settings.
	attached := map[string][]Ref{}
	for _, c := range containers {
		if c.NetworkSettings == nil {
			continue
		}
		for _, ep := range c.NetworkSettings.Networks {
			if ep == nil || ep.NetworkID == "" {
				continue
			}
			attached[ep.NetworkID] = append(attached[ep.NetworkID], Ref{
				ID: c.ID, Name: containerName(c.Names, c.ID), IP: ep.IPAddress,
			})
		}
	}
	out := make([]Network, 0, len(raw))
	for _, n := range raw {
		refs := attached[n.ID]
		if refs == nil {
			refs = []Ref{}
		}
		sort.Slice(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })
		item := Network{
			ID:         n.ID,
			ShortID:    shortID(n.ID),
			Name:       n.Name,
			Driver:     n.Driver,
			Scope:      n.Scope,
			Internal:   n.Internal,
			Subnets:    []Subnet{},
			Labels:     maskMap(n.Labels),
			Builtin:    builtinNetwork(n.Name),
			InUse:      len(refs) > 0,
			Containers: refs,
		}
		if !n.Created.IsZero() && n.Created.Year() > 1970 {
			u := n.Created.Unix()
			item.CreatedAt = &u
		}
		for _, c := range n.IPAM.Config {
			if c.Subnet != "" || c.Gateway != "" {
				item.Subnets = append(item.Subnets, Subnet{Subnet: c.Subnet, Gateway: c.Gateway})
			}
		}
		if slug := strings.TrimSpace(n.Labels[AppLabel]); slug != "" {
			item.App = &slug
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Builtin != out[j].Builtin {
			return !out[i].Builtin
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	httpx.OK(w, out)
	return nil
}

// handleNetworkRemove removes a user-defined network that no container is
// attached to.
func (m *Module) handleNetworkRemove(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	if !validNetworkID(id) {
		return httpx.BadRequest("Ağ kimliği geçersiz.")
	}
	cli, err := m.client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), actionTimeout)
	defer cancel()
	const inUse = "Ağa bağlı konteynerler var. Önce onları kaldırın veya ağdan ayırın."
	text := errText{notFound: "Ağ bulunamadı.", conflict: inUse, fallback: "Ağ kaldırılamadı."}
	insp, err := cli.NetworkInspect(ctx, id, network.InspectOptions{})
	if err != nil {
		return apiError(err, text)
	}
	if builtinNetwork(insp.Name) {
		return httpx.NewError(http.StatusForbidden, "network_builtin", "Docker'ın yerleşik ağları kaldırılamaz.")
	}
	if len(insp.Containers) > 0 {
		return httpx.Conflict(inUse)
	}
	// Stopped containers are not listed by inspect but still refer to it.
	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return apiError(err, text)
	}
	for _, c := range containers {
		if c.NetworkSettings == nil {
			continue
		}
		for _, ep := range c.NetworkSettings.Networks {
			if ep != nil && ep.NetworkID == insp.ID {
				return httpx.Conflict(inUse)
			}
		}
	}
	if err := cli.NetworkRemove(ctx, insp.ID); err != nil {
		m.deps.Audit.Log(ctx, auth.ActorFrom(r), "docker.network_remove", insp.Name, "başarısız", false)
		return apiError(err, text)
	}
	m.deps.Audit.Log(ctx, auth.ActorFrom(r), "docker.network_remove", insp.Name, "", true)
	httpx.OK(w, map[string]string{"id": insp.ID, "name": insp.Name})
	return nil
}
