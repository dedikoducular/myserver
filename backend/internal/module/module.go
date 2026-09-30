// Package module defines the contract between the server core and feature
// modules. A module owns one package under internal/, registers its routes
// under /api/v1/<name> and may run background work; the core isolates
// modules from each other so one failing module cannot stop the panel.
package module

import (
	"context"
	"database/sql"

	"myserver/internal/audit"
	"myserver/internal/auth"
	"myserver/internal/config"
	"myserver/internal/httpx"
	"myserver/internal/logbuf"
	"myserver/internal/notify"
	"myserver/internal/privileged"
	"myserver/internal/settings"
)

// Deps are the shared services handed to every module constructor.
type Deps struct {
	Cfg      *config.Config
	DB       *sql.DB
	Settings *settings.Store
	Audit    *audit.Logger
	Notify   *notify.Center
	Logs     *logbuf.Buffer
	Priv     *privileged.Runner
	Auth     *auth.Service
}

// Module is a feature module.
type Module interface {
	// Name is the module's API path segment, e.g. "docker".
	Name() string
	// Register mounts routes. api is the /api/v1 router with session and
	// CSRF enforcement already applied; ws is the same prefix with
	// WebSocket (Origin-checked) session enforcement.
	Register(api, ws *httpx.Router)
}

// Starter is implemented by modules with background work. Start must return
// when ctx is cancelled.
type Starter interface {
	Start(ctx context.Context)
}

// HealthStatus is a health level, ordered by severity.
type HealthStatus string

const (
	Healthy      HealthStatus = "HEALTHY"
	WarningLevel HealthStatus = "WARNING"
	CriticalLvl  HealthStatus = "CRITICAL"
)

// HealthCheck is the result of one health probe.
type HealthCheck struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Status  HealthStatus `json:"status"`
	Message string       `json:"message"`
}

// HealthReporter is implemented by modules that contribute to the system
// health monitor. Health must be cheap or internally cached.
type HealthReporter interface {
	Health(ctx context.Context) []HealthCheck
}
