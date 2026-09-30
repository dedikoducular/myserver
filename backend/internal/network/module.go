// Package network is the read-only network information module: interfaces,
// gateways, DNS, the network manager in use, netplan configuration and
// listening ports. It changes nothing on the system.
package network

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"time"

	"myserver/internal/auth"
	"myserver/internal/httpx"
	"myserver/internal/module"
	"myserver/internal/network/netcheck"
	"myserver/internal/privileged"
	"myserver/internal/settings"
)

// Overview is the response of GET /network/overview.
type Overview struct {
	Hostname       string              `json:"hostname"`
	Interfaces     []Interface         `json:"interfaces"`
	Gateways       []netcheck.Gateway  `json:"gateways"`
	DNS            DNS                 `json:"dns"`
	Manager        Manager             `json:"manager"`
	PrivateSubnets []Subnet            `json:"private_subnets"`
	Listening      []netcheck.Listener `json:"listening"`
	CollectedAt    int64               `json:"collected_at"`
}

type Module struct {
	deps module.Deps

	mu       sync.Mutex
	cached   *Overview
	cachedAt time.Time
}

const cacheTTL = 3 * time.Second

func New(deps module.Deps, _ *settings.API) (module.Module, error) {
	return &Module{deps: deps}, nil
}

func (m *Module) Name() string { return "network" }

func (m *Module) Register(api, _ *httpx.Router) {
	g := api.Group("/network")
	g.Get("/overview", m.handleOverview)
	a := api.Group("/network", auth.RequireAdmin)
	a.Get("/netplan", m.handleNetplan)
}

// Collect gathers the overview from the kernel, /proc, /sys and
// configuration files.
func Collect() (*Overview, error) {
	gateways := collectGateways()
	ifs, err := collectInterfaces(gateways)
	if err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	return &Overview{
		Hostname:       host,
		Interfaces:     ifs,
		Gateways:       gateways,
		DNS:            collectDNS(),
		Manager:        detectManager(),
		PrivateSubnets: subnetsOf(ifs),
		Listening:      collectListeners(),
		CollectedAt:    time.Now().Unix(),
	}, nil
}

func (m *Module) overview() (*Overview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cached != nil && time.Since(m.cachedAt) < cacheTTL {
		return m.cached, nil
	}
	o, err := Collect()
	if err != nil {
		return nil, err
	}
	m.cached, m.cachedAt = o, time.Now()
	return o, nil
}

func (m *Module) handleOverview(w http.ResponseWriter, _ *http.Request) error {
	o, err := m.overview()
	if err != nil {
		return httpx.NewError(http.StatusInternalServerError, "network_read_failed",
			"Ağ arayüzleri okunamadı.").Wrap(err)
	}
	httpx.OK(w, o)
	return nil
}

// NetplanView is the response of GET /network/netplan.
type NetplanView struct {
	// Present is false on systems that do not use netplan.
	Present bool                   `json:"present"`
	Files   []netcheck.NetplanFile `json:"files"`
}

func (m *Module) handleNetplan(w http.ResponseWriter, r *http.Request) error {
	view := NetplanView{Files: []netcheck.NetplanFile{}}
	if fi, err := os.Stat("/etc/netplan"); err != nil || !fi.IsDir() {
		httpx.OK(w, view)
		return nil
	}
	view.Present = true
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	out, err := m.deps.Priv.Run(ctx, "network-netplan-read")
	if err != nil {
		return httpx.NewError(http.StatusBadGateway, "netplan_read_failed",
			privileged.UserMessage(err, "Netplan yapılandırması okunamadı.")).Wrap(err)
	}
	var res netcheck.NetplanResult
	if err := json.Unmarshal(out, &res); err != nil {
		return httpx.NewError(http.StatusBadGateway, "netplan_read_failed",
			"Netplan yapılandırması okunamadı.").Wrap(err)
	}
	if res.Files != nil {
		view.Files = res.Files
	}
	httpx.OK(w, view)
	return nil
}
