package docker

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

const (
	statsInterval     = 3 * time.Second
	statsFirstDelay   = 1 * time.Second // second sample comes quickly so CPU % appears soon
	statsDownInterval = 10 * time.Second
	statsConcurrency  = 4
	statsCallTimeout  = 5 * time.Second
)

// ContainerStats is the live resource usage of one running container.
type ContainerStats struct {
	ID string `json:"id"`
	// CPUPercent is null until two samples exist.
	CPUPercent    *float64 `json:"cpu_percent"`
	MemoryUsage   uint64   `json:"memory_usage"`
	MemoryLimit   uint64   `json:"memory_limit"`
	MemoryPercent float64  `json:"memory_percent"`
	Pids          uint64   `json:"pids"`
}

// StatsSnapshot is the payload of the "stats" SSE event.
type StatsSnapshot struct {
	At        int64            `json:"at"`
	Available bool             `json:"available"`
	Items     []ContainerStats `json:"items"`
}

// cpuPercent computes CPU usage the way `docker stats` does on Linux: the
// container's share of the host CPU time consumed between two samples,
// scaled by the number of online CPUs.
func cpuPercent(prevTotal, prevSystem, curTotal, curSystem uint64, onlineCPUs float64) float64 {
	cpuDelta := float64(curTotal) - float64(prevTotal)
	systemDelta := float64(curSystem) - float64(prevSystem)
	if systemDelta > 0 && cpuDelta > 0 {
		return cpuDelta / systemDelta * onlineCPUs * 100
	}
	return 0
}

// memoryUsed mirrors `docker stats`: usage without the inactive page cache.
func memoryUsed(mem container.MemoryStats) uint64 {
	// cgroup v1
	if v, ok := mem.Stats["total_inactive_file"]; ok && v < mem.Usage {
		return mem.Usage - v
	}
	// cgroup v2
	if v := mem.Stats["inactive_file"]; v < mem.Usage {
		return mem.Usage - v
	}
	return mem.Usage
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

type cpuSample struct {
	total  uint64
	system uint64
}

// statsHub runs a single collector for all subscribers. The collector only
// exists while at least one client is subscribed.
type statsHub struct {
	m *Module

	mu     sync.Mutex
	subs   map[chan StatsSnapshot]struct{}
	cancel context.CancelFunc
	last   *StatsSnapshot
}

func newStatsHub(m *Module) *statsHub {
	return &statsHub{m: m, subs: map[chan StatsSnapshot]struct{}{}}
}

func (h *statsHub) subscribe() (<-chan StatsSnapshot, func()) {
	ch := make(chan StatsSnapshot, 2)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	if h.last != nil {
		ch <- *h.last
	}
	if h.cancel == nil {
		ctx, cancel := context.WithCancel(h.m.ctx)
		h.cancel = cancel
		go h.run(ctx)
	}
	h.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			if len(h.subs) == 0 && h.cancel != nil {
				h.cancel()
				h.cancel = nil
				h.last = nil
			}
			h.mu.Unlock()
		})
	}
}

func (h *statsHub) broadcast(ctx context.Context, snap StatsSnapshot) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ctx.Err() != nil {
		return // a stopped collector must not overwrite its successor's data
	}
	h.last = &snap
	for ch := range h.subs {
		select {
		case ch <- snap:
		default: // slow subscriber: skip this sample
		}
	}
}

func (h *statsHub) run(ctx context.Context) {
	defer func() {
		if v := recover(); v != nil {
			slog.Error("docker istatistik toplayıcısı çöktü", "panic", v)
			h.mu.Lock()
			h.cancel = nil
			h.mu.Unlock()
		}
	}()
	prev := map[string]cpuSample{}
	first := true
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		snap := h.collect(ctx, prev)
		if ctx.Err() != nil {
			return
		}
		h.broadcast(ctx, snap)
		wait := statsInterval
		switch {
		case !snap.Available:
			wait = statsDownInterval
			first = true
		case first:
			wait = statsFirstDelay
			first = false
		}
		timer.Reset(wait)
	}
}

// collect takes one sample of every running container with bounded
// concurrency. prev holds the previous CPU counters and is updated in place.
func (h *statsHub) collect(ctx context.Context, prev map[string]cpuSample) StatsSnapshot {
	snap := StatsSnapshot{At: time.Now().Unix(), Items: []ContainerStats{}}
	cli, err := h.m.client()
	if err != nil {
		return snap
	}
	lctx, cancel := context.WithTimeout(ctx, listTimeout)
	running, err := cli.ContainerList(lctx, container.ListOptions{
		Filters: filters.NewArgs(filters.Arg("status", "running")),
	})
	cancel()
	if err != nil {
		if !isUnavailable(err) && ctx.Err() == nil {
			slog.Warn("docker istatistikleri için konteyner listesi alınamadı", "error", err.Error())
		}
		clear(prev)
		return snap
	}
	snap.Available = true

	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, statsConcurrency)
		cur = make(map[string]cpuSample, len(running))
	)
	for _, c := range running {
		id := c.ID
		before, hasBefore := prev[id]
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			st, ok := readStats(ctx, cli, id)
			if !ok {
				return
			}
			item := ContainerStats{
				ID:          id,
				MemoryUsage: memoryUsed(st.MemoryStats),
				MemoryLimit: st.MemoryStats.Limit,
				Pids:        st.PidsStats.Current,
			}
			if item.MemoryLimit > 0 {
				item.MemoryPercent = round1(float64(item.MemoryUsage) / float64(item.MemoryLimit) * 100)
			}
			online := float64(st.CPUStats.OnlineCPUs)
			if online == 0 {
				online = float64(len(st.CPUStats.CPUUsage.PercpuUsage))
			}
			now := cpuSample{total: st.CPUStats.CPUUsage.TotalUsage, system: st.CPUStats.SystemUsage}
			switch {
			case st.PreCPUStats.SystemUsage > 0:
				// The daemon supplied its own previous sample.
				v := round1(cpuPercent(st.PreCPUStats.CPUUsage.TotalUsage, st.PreCPUStats.SystemUsage, now.total, now.system, online))
				item.CPUPercent = &v
			case hasBefore && now.total >= before.total:
				v := round1(cpuPercent(before.total, before.system, now.total, now.system, online))
				item.CPUPercent = &v
			}
			mu.Lock()
			cur[id] = now
			snap.Items = append(snap.Items, item)
			mu.Unlock()
		}()
	}
	wg.Wait()

	clear(prev)
	for id, s := range cur {
		prev[id] = s
	}
	sort.Slice(snap.Items, func(i, j int) bool { return snap.Items[i].ID < snap.Items[j].ID })
	return snap
}

func readStats(ctx context.Context, cli *client.Client, id string) (container.StatsResponse, bool) {
	var st container.StatsResponse
	cctx, cancel := context.WithTimeout(ctx, statsCallTimeout)
	defer cancel()
	resp, err := cli.ContainerStatsOneShot(cctx, id)
	if err != nil {
		return st, false // the container may just have stopped
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&st); err != nil {
		return st, false
	}
	return st, true
}

// handleStatsStream is the live feed of the container list:
//
//	event "stats"   → StatsSnapshot, every few seconds
//	event "changed" → changeEvent, when a Docker object changes
func (m *Module) handleStatsStream(w http.ResponseWriter, r *http.Request) error {
	sse, err := startSSE(w)
	if err != nil || sse == nil {
		return err
	}
	stats, unsubStats := m.stats.subscribe()
	defer unsubStats()
	changes, unsubChanges := m.changes.subscribe()
	defer unsubChanges()

	keepalive := time.NewTicker(sseKeepalive)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return nil
		case <-m.ctx.Done():
			return nil
		case <-keepalive.C:
			if sse.comment("ping") != nil {
				return nil
			}
		case snap := <-stats:
			if sse.event("stats", snap) != nil {
				return nil
			}
		case ev := <-changes:
			if sse.event("changed", ev) != nil {
				return nil
			}
		}
	}
}
