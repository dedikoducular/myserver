// Package storage is the disks module: block devices, filesystems,
// mounting, formatting and SMART health.
//
// The panel process only reads (lsblk, /proc, /sys). Everything that
// changes the system goes through the root helper, which repeats all
// validation and the system-device determination on its own.
package storage

import (
	"context"
	"sync"
	"time"

	"myserver/internal/auth"
	"myserver/internal/httpx"
	"myserver/internal/module"
	"myserver/internal/settings"
	sc "myserver/internal/storage/storagecheck"
)

// Settings keys owned by the module.
const (
	KeySmartInterval   = "storage.smart_interval_minutes"
	KeyTempWarning     = "storage.temp_warning_celsius"
	KeyNotifyRemovable = "storage.notify_removable"
)

const source = "storage"

// Module implements module.Module, module.Starter and
// module.HealthReporter.
type Module struct {
	deps module.Deps
	sys  sc.Sys

	invMu sync.Mutex
	inv   *snapshot
	invAt time.Time

	smart  *smartCache
	tokens *tokenStore
	events *broker
}

// New constructs the module. It never fails because a tool is missing;
// such conditions are reported per request.
func New(deps module.Deps, st *settings.API) (module.Module, error) {
	settings.RegisterDefault(KeySmartInterval, "60")
	settings.RegisterDefault(KeyTempWarning, "55")
	settings.RegisterDefault(KeyNotifyRemovable, "true")
	st.Allow(KeySmartInterval, settings.IntRange(10, 1440), nil)
	st.Allow(KeyTempWarning, settings.IntRange(30, 90), nil)
	st.Allow(KeyNotifyRemovable, settings.Bool, nil)

	return &Module{
		deps:   deps,
		sys:    sc.DefaultSys,
		smart:  newSmartCache(),
		tokens: newTokenStore(),
		events: newBroker(),
	}, nil
}

func (m *Module) Name() string { return "storage" }

func (m *Module) Register(api, _ *httpx.Router) {
	g := api.Group("/storage")
	g.Get("/disks", m.handleDisks)
	g.Get("/events", m.handleEvents)
	g.Get("/smart/{device}", m.handleSmart)

	a := api.Group("/storage", auth.RequireAdmin)
	a.Post("/smart/{device}/refresh", m.handleSmartRefresh)
	a.Post("/smart/{device}/test", m.handleSmartTest)
	a.Post("/mount", m.handleMount)
	a.Post("/unmount", m.handleUnmount)
	a.Post("/persist", m.handlePersistAdd)
	a.Delete("/persist/{uuid}", m.handlePersistRemove)
	a.Post("/format/prepare", m.handleFormatPrepare)
	a.Post("/format", m.handleFormat)
	a.Post("/allow-root", m.handleAllowRoot)
}

// Start runs the hot-plug watcher and the SMART loop until ctx ends.
func (m *Module) Start(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		m.smartLoop(ctx)
	}()
	go func() {
		defer wg.Done()
		m.watchLoop(ctx)
	}()
	wg.Wait()
}

func (m *Module) tempWarning() int {
	n := m.deps.Settings.Int(KeyTempWarning, 55)
	if n < 30 || n > 90 {
		return 55
	}
	return n
}

func (m *Module) smartInterval() time.Duration {
	n := m.deps.Settings.Int(KeySmartInterval, 60)
	if n < 10 || n > 1440 {
		n = 60
	}
	return time.Duration(n) * time.Minute
}

// Health reports from cached data only.
func (m *Module) Health(ctx context.Context) []module.HealthCheck {
	var out []module.HealthCheck
	for _, r := range m.smart.all() {
		switch r.Status {
		case sc.SmartFailed:
			out = append(out, module.HealthCheck{
				ID: "storage.smart." + r.Device, Name: "Disk sağlığı: " + r.Device,
				Status: module.CriticalLvl, Message: "SMART testi başarısız; disk arızalanmak üzere olabilir. Verilerinizi yedekleyin.",
			})
		case sc.SmartWarning:
			msg := "SMART uyarısı var."
			if len(r.Problems) > 0 {
				msg = r.Problems[0]
			}
			out = append(out, module.HealthCheck{
				ID: "storage.smart." + r.Device, Name: "Disk sağlığı: " + r.Device,
				Status: module.WarningLevel, Message: msg,
			})
		}
	}
	if snap, err := m.snapshot(ctx, 60*time.Second); err == nil {
		for _, mp := range snap.unexpectedReadOnly() {
			out = append(out, module.HealthCheck{
				ID: "storage.readonly." + mp, Name: "Dosya sistemi: " + mp,
				Status:  module.WarningLevel,
				Message: "Dosya sistemi beklenmedik şekilde salt okunur bağlanmış. Diskte hata olabilir.",
			})
		}
	}
	if len(out) == 0 {
		out = append(out, module.HealthCheck{
			ID: "storage.disks", Name: "Diskler", Status: module.Healthy, Message: "Disklerde sorun saptanmadı.",
		})
	}
	return out
}
