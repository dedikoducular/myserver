package docker

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"

	"myserver/internal/auth"
	"myserver/internal/httpx"
)

// Ref names a container that uses a volume or network.
type Ref struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// IP is set for network attachments only.
	IP string `json:"ip,omitempty"`
}

// Volume is one row of the volume list.
type Volume struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	Mountpoint string            `json:"mountpoint"`
	Scope      string            `json:"scope"`
	CreatedAt  *int64            `json:"created_at"`
	Labels     map[string]string `json:"labels"`
	App        *string           `json:"app"`
	// Anonymous volumes have a generated 64-character name.
	Anonymous  bool  `json:"anonymous"`
	InUse      bool  `json:"in_use"`
	Containers []Ref `json:"containers"`
}

// volumeUsers maps a volume name to the containers (running or stopped)
// that mount it.
func volumeUsers(list []container.Summary) map[string][]Ref {
	users := map[string][]Ref{}
	for _, c := range list {
		seen := map[string]bool{}
		for _, mp := range c.Mounts {
			if string(mp.Type) != "volume" || mp.Name == "" || seen[mp.Name] {
				continue
			}
			seen[mp.Name] = true
			users[mp.Name] = append(users[mp.Name], Ref{ID: c.ID, Name: containerName(c.Names, c.ID)})
		}
	}
	return users
}

func (m *Module) listVolumes(ctx context.Context, cli *client.Client) ([]Volume, error) {
	text := errText{fallback: "Birim listesi alınamadı."}
	resp, err := cli.VolumeList(ctx, volume.ListOptions{})
	if err != nil {
		return nil, apiError(err, text)
	}
	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, apiError(err, text)
	}
	users := volumeUsers(containers)
	out := make([]Volume, 0, len(resp.Volumes))
	for _, v := range resp.Volumes {
		if v == nil {
			continue
		}
		refs := users[v.Name]
		if refs == nil {
			refs = []Ref{}
		}
		sort.Slice(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })
		item := Volume{
			Name:       v.Name,
			Driver:     v.Driver,
			Mountpoint: v.Mountpoint,
			Scope:      v.Scope,
			CreatedAt:  parseDockerTime(v.CreatedAt),
			Labels:     maskMap(v.Labels),
			Anonymous:  hex64Re.MatchString(v.Name),
			InUse:      len(refs) > 0,
			Containers: refs,
		}
		if slug := strings.TrimSpace(v.Labels[AppLabel]); slug != "" {
			item.App = &slug
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Anonymous != out[j].Anonymous {
			return !out[i].Anonymous
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

func (m *Module) handleVolumes(w http.ResponseWriter, r *http.Request) error {
	cli, err := m.client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), listTimeout)
	defer cancel()
	list, err := m.listVolumes(ctx, cli)
	if err != nil {
		return err
	}
	httpx.OK(w, list)
	return nil
}

// handleVolumesUnused lists exactly what a prune would remove, so the user
// can confirm it.
func (m *Module) handleVolumesUnused(w http.ResponseWriter, r *http.Request) error {
	cli, err := m.client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), listTimeout)
	defer cancel()
	list, err := m.listVolumes(ctx, cli)
	if err != nil {
		return err
	}
	out := []Volume{}
	for _, v := range list {
		if !v.InUse {
			out = append(out, v)
		}
	}
	httpx.OK(w, out)
	return nil
}

const volumeInUseMessage = "Birim bir konteyner tarafından kullanılıyor. Önce o konteyneri kaldırın."

// removeVolume deletes one volume after verifying that no container, running
// or stopped, refers to it. It never forces.
func (m *Module) removeVolume(ctx context.Context, cli *client.Client, name string) error {
	text := errText{notFound: "Birim bulunamadı.", conflict: volumeInUseMessage, fallback: "Birim kaldırılamadı."}
	if _, err := cli.VolumeInspect(ctx, name); err != nil {
		return apiError(err, text)
	}
	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return apiError(err, text)
	}
	if len(volumeUsers(containers)[name]) > 0 {
		return httpx.Conflict(volumeInUseMessage)
	}
	if err := cli.VolumeRemove(ctx, name, false); err != nil {
		return apiError(err, text)
	}
	return nil
}

func (m *Module) handleVolumeRemove(w http.ResponseWriter, r *http.Request) error {
	name := r.PathValue("name")
	if !validVolumeName(name) {
		return httpx.BadRequest("Birim adı geçersiz.")
	}
	cli, err := m.client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), actionTimeout)
	defer cancel()
	if err := m.removeVolume(ctx, cli, name); err != nil {
		m.deps.Audit.Log(ctx, auth.ActorFrom(r), "docker.volume_remove", name, "başarısız", false)
		return err
	}
	m.deps.Audit.Log(ctx, auth.ActorFrom(r), "docker.volume_remove", name, "", true)
	httpx.OK(w, map[string]string{"name": name})
	return nil
}

const pruneMaxVolumes = 200

// PruneFailure is a volume that could not be removed during a prune.
type PruneFailure struct {
	Name    string `json:"name"`
	Message string `json:"message"`
}

// handleVolumesPrune removes only the volumes the user was shown and
// confirmed, by name. Each one is re-checked; a volume that came into use in
// the meantime is skipped. The Engine's own prune call is deliberately not
// used, because it would decide by itself what to delete.
func (m *Module) handleVolumesPrune(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Names   []string `json:"names"`
		Confirm bool     `json:"confirm"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	if !req.Confirm {
		return httpx.BadRequest("Birimlerin silinmesi açıkça onaylanmalıdır.")
	}
	if len(req.Names) == 0 {
		return httpx.BadRequest("Silinecek birim belirtilmedi.")
	}
	if len(req.Names) > pruneMaxVolumes {
		return httpx.BadRequest("Tek seferde en fazla " + strconv.Itoa(pruneMaxVolumes) + " birim silinebilir.")
	}
	seen := map[string]bool{}
	names := make([]string, 0, len(req.Names))
	for _, n := range req.Names {
		if !validVolumeName(n) {
			return httpx.BadRequest("Birim adı geçersiz.")
		}
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	cli, err := m.client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), actionTimeout)
	defer cancel()
	if _, err := cli.Ping(ctx); err != nil {
		return apiError(err, errText{fallback: unavailableMessage})
	}
	removed := []string{}
	failed := []PruneFailure{}
	for _, n := range names {
		if err := m.removeVolume(ctx, cli, n); err != nil {
			failed = append(failed, PruneFailure{Name: n, Message: userMessage(err, "Birim kaldırılamadı.")})
			m.deps.Audit.Log(ctx, auth.ActorFrom(r), "docker.volume_prune", n, "başarısız", false)
			continue
		}
		removed = append(removed, n)
		m.deps.Audit.Log(ctx, auth.ActorFrom(r), "docker.volume_prune", n, "", true)
	}
	httpx.OK(w, map[string]any{"removed": removed, "failed": failed})
	return nil
}
