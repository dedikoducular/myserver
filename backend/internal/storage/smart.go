package storage

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"sync"
	"time"

	"myserver/internal/notify"
	sc "myserver/internal/storage/storagecheck"
)

const smartCacheTTL = 10 * time.Minute

type smartCache struct {
	mu      sync.Mutex
	reports map[string]sc.SmartReport
	// sat remembers the disks that only answer through "-d sat" (USB
	// bridges), so the first attempt is not repeated every time.
	sat map[string]bool
	// busy serializes queries per disk.
	busy map[string]*sync.Mutex
}

func newSmartCache() *smartCache {
	return &smartCache{reports: map[string]sc.SmartReport{}, sat: map[string]bool{}, busy: map[string]*sync.Mutex{}}
}

func (c *smartCache) get(name string) (sc.SmartReport, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.reports[name]
	return r, ok
}

func (c *smartCache) all() []sc.SmartReport {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]sc.SmartReport, 0, len(c.reports))
	for _, r := range c.reports {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Device < out[j].Device })
	return out
}

func (c *smartCache) lock(name string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	l, ok := c.busy[name]
	if !ok {
		l = &sync.Mutex{}
		c.busy[name] = l
	}
	return l
}

// retain drops the reports of disks that are no longer attached.
func (c *smartCache) retain(names map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for n := range c.reports {
		if !names[n] {
			delete(c.reports, n)
			delete(c.sat, n)
			delete(c.busy, n)
		}
	}
}

func (m *Module) smartRun(ctx context.Context, name, devType, mode string) (sc.SmartReport, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	out, err := m.deps.Priv.Run(ctx, "storage-smart", name, devType, mode)
	if err != nil {
		return sc.SmartReport{}, err
	}
	var res sc.HelperSmartOutput
	if err := json.Unmarshal(out, &res); err != nil {
		return sc.SmartReport{}, err
	}
	if !res.Installed {
		return sc.SmartReport{
			Device: name, Status: sc.SmartNotInstalled,
			Attributes: []sc.SmartAttribute{}, Problems: []string{}, Messages: []string{},
		}, nil
	}
	return sc.ParseSmart(name, res.Output, res.ExitStatus, m.tempWarning()), nil
}

// smartCheck queries one disk and stores the result. mode is "standby"
// (never wakes a sleeping disk) or "active".
func (m *Module) smartCheck(ctx context.Context, d Device, mode string) (sc.SmartReport, error) {
	l := m.smart.lock(d.Name)
	l.Lock()
	defer l.Unlock()

	m.smart.mu.Lock()
	useSat := m.smart.sat[d.Name]
	prev, hasPrev := m.smart.reports[d.Name]
	m.smart.mu.Unlock()

	devType := "auto"
	if useSat {
		devType = "sat"
	}
	r, err := m.smartRun(ctx, d.Name, devType, mode)
	if err != nil {
		return sc.SmartReport{}, err
	}
	// Many USB bridges need SAT pass-through to be requested explicitly.
	if r.Status == sc.SmartUnsupported && !useSat && d.Transport != "nvme" {
		if r2, err2 := m.smartRun(ctx, d.Name, "sat", mode); err2 == nil &&
			r2.Status != sc.SmartUnsupported && r2.Status != sc.SmartUnknown {
			r = r2
			m.smart.mu.Lock()
			m.smart.sat[d.Name] = true
			m.smart.mu.Unlock()
		}
	}
	r.CheckedAt = time.Now().Unix()
	if r.Status == sc.SmartStandby && hasPrev && prev.Status != sc.SmartStandby &&
		(prev.Serial == "" || d.Serial == "" || prev.Serial == d.Serial) {
		// The disk sleeps: keep the last real measurement.
		return prev, nil
	}
	m.smart.mu.Lock()
	m.smart.reports[d.Name] = r
	m.smart.mu.Unlock()
	m.invalidate()
	m.smartNotify(ctx, d, r)
	if !hasPrev || prev.Status != r.Status {
		m.events.publish("smart")
	}
	return r, nil
}

func (m *Module) smartNotify(ctx context.Context, d Device, r sc.SmartReport) {
	if r.Status != sc.SmartFailed && r.Status != sc.SmartWarning {
		return
	}
	id := d.Serial
	if id == "" {
		id = d.Name
	}
	label := d.Name
	if d.Model != "" {
		label += " (" + d.Model + ")"
	}
	msg := label + ": "
	for i, p := range r.Problems {
		if i > 0 {
			msg += " "
		}
		msg += p
	}
	sev := notify.Warning
	if r.Status == sc.SmartFailed {
		sev = notify.Critical
		msg += " Verilerinizi en kısa sürede yedekleyin."
	}
	m.deps.Notify.PublishOnce(ctx, sev, source, "SMART uyarısı", msg,
		"storage.smart."+id+"."+r.Status, 24*time.Hour)
}

func (m *Module) smartCheckAll(ctx context.Context) {
	snap, err := m.snapshot(ctx, 30*time.Second)
	if err != nil {
		return
	}
	names := map[string]bool{}
	for _, d := range snap.disks() {
		names[d.Name] = true
	}
	m.smart.retain(names)
	for _, d := range snap.disks() {
		if ctx.Err() != nil {
			return
		}
		r, err := m.smartCheck(ctx, d, "standby")
		if err != nil {
			slog.Warn("SMART bilgisi okunamadı", "device", d.Name, "error", err.Error())
			// The helper itself is unavailable; do not retry per disk.
			return
		}
		if r.Status == sc.SmartNotInstalled {
			return
		}
	}
}

// smartLoop refreshes SMART data slowly in the background.
func (m *Module) smartLoop(ctx context.Context) {
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			m.smartCheckAll(ctx)
			timer.Reset(m.smartInterval())
		}
	}
}
