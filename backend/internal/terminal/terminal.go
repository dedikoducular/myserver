// Package terminal is the web terminal: an interactive login shell of a
// system user chosen by the administrator, carried over a WebSocket.
//
// A web terminal is remote code execution by design, so the module is
// mostly access control: admin role only, a switch in Settings that also
// ends live sessions, session limits, idle and lifetime limits, periodic
// re-validation of the panel session, and an audit trail. Terminal input
// and output are never logged.
package terminal

import (
	"context"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"myserver/internal/auth"
	"myserver/internal/httpx"
	"myserver/internal/module"
	"myserver/internal/settings"
	"myserver/internal/terminal/termcheck"
)

const (
	maxSessionsPerUser = 4
	maxSessionsTotal   = 8

	maxSessionLength = 12 * time.Hour
	idleWarningLead  = 60 * time.Second
	defaultIdleMins  = 15

	settingsCheckEvery = 2 * time.Second
	authCheckEvery     = 30 * time.Second
	helperCacheFor     = 30 * time.Second
)

// passwdPath is the local account database. It is a variable only so tests
// can point it at a fixture.
var passwdPath = "/etc/passwd"

// clock is the time source of a session's supervisor (idle time, the
// one-second tick and the lifetime limit). Tests replace it; socket
// deadlines and process timeouts always use real time.
type clock struct {
	now    func() time.Time
	ticker func(d time.Duration) (c <-chan time.Time, stop func())
	timer  func(d time.Duration) (c <-chan time.Time, stop func())
}

func realClock() clock {
	return clock{
		now: time.Now,
		ticker: func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		},
		timer: func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTimer(d)
			return t.C, func() { t.Stop() }
		},
	}
}

type Module struct {
	deps module.Deps
	clk  clock

	// base is cancelled on shutdown and ends every session.
	base   context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	sessions map[int64]int // panel user id -> open terminals
	total    int

	helperMu      sync.Mutex
	helperChecked time.Time
	helperOK      bool
}

func New(deps module.Deps, st *settings.API) (module.Module, error) {
	base, cancel := context.WithCancel(context.Background())
	m := &Module{deps: deps, clk: realClock(), base: base, cancel: cancel, sessions: map[int64]int{}}
	settings.RegisterDefault(settings.KeyTerminalUser, "")
	st.Allow(settings.KeyTerminalUser, validateUserSetting, nil)
	return m, nil
}

func (m *Module) Name() string { return "terminal" }

func (m *Module) Register(api, ws *httpx.Router) {
	g := api.Group("/terminal")
	g.Get("/status", m.handleStatus)
	a := api.Group("/terminal", auth.RequireAdmin)
	a.Get("/users", m.handleUsers)
	ws.Raw(http.MethodGet, "/terminal/ws", http.HandlerFunc(m.handleSocket))
}

// Start waits for shutdown, then ends every open terminal and waits for the
// shells to be reaped.
func (m *Module) Start(ctx context.Context) {
	<-ctx.Done()
	m.cancel()
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
	}
}

// validateUserSetting accepts an empty value (terminal unavailable until a
// user is chosen) or the name of an eligible system user.
func validateUserSetting(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	if _, err := lookupUser(v); err != nil {
		return "", err
	}
	return v, nil
}

// lookupUser checks a system account against the local account database.
// The helper repeats the check with root's view before starting a shell.
func lookupUser(name string) (termcheck.Entry, error) {
	if !termcheck.ValidUsername(name) {
		return termcheck.Entry{}, httpx.BadRequest(termcheck.Message(termcheck.BadName))
	}
	data, err := os.ReadFile(passwdPath)
	if err != nil {
		return termcheck.Entry{}, httpx.Unavailable("passwd_unreadable",
			"Sistem kullanıcıları okunamadı.").Wrap(err)
	}
	e, reason := termcheck.Lookup(string(data), name)
	if reason != termcheck.OK {
		return termcheck.Entry{}, httpx.BadRequest(termcheck.Message(reason))
	}
	if fi, err := os.Stat(e.Shell); err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return termcheck.Entry{}, httpx.BadRequest("Bu kullanıcının kabuğu çalıştırılabilir değil.")
	}
	return e, nil
}

func userMessage(err error) string {
	if e, ok := err.(*httpx.Error); ok {
		return e.Message
	}
	return "Sistem kullanıcısı doğrulanamadı."
}

// helperAvailable reports whether the root helper can be invoked; the
// answer is cached because it costs a sudo invocation.
func (m *Module) helperAvailable(ctx context.Context) bool {
	m.helperMu.Lock()
	defer m.helperMu.Unlock()
	if !m.helperChecked.IsZero() && time.Since(m.helperChecked) < helperCacheFor {
		return m.helperOK
	}
	m.helperOK = m.deps.Priv.Available(ctx) == nil
	m.helperChecked = time.Now()
	return m.helperOK
}

func (m *Module) idleTimeout() time.Duration {
	n := m.deps.Settings.Int(settings.KeyTerminalTimeout, defaultIdleMins)
	if n < 1 || n > 240 {
		n = defaultIdleMins
	}
	return time.Duration(n) * time.Minute
}

type statusResponse struct {
	// Supported is false when the panel runs on a platform without PTYs.
	Supported bool `json:"supported"`
	Enabled   bool `json:"enabled"`
	// Allowed tells the caller whether their role may open a terminal.
	Allowed bool `json:"allowed"`
	// The remaining fields are only filled in for administrators.
	User               string  `json:"user"`
	UserValid          bool    `json:"user_valid"`
	UserError          *string `json:"user_error"`
	HelperAvailable    *bool   `json:"helper_available"`
	ActiveSessions     int     `json:"active_sessions"`
	OwnSessions        int     `json:"own_sessions"`
	MaxSessions        int     `json:"max_sessions"`
	MaxSessionsPerUser int     `json:"max_sessions_per_user"`
	IdleTimeoutMinutes int     `json:"idle_timeout_minutes"`
	MaxSessionSeconds  int     `json:"max_session_seconds"`
}

func (m *Module) handleStatus(w http.ResponseWriter, r *http.Request) error {
	sess := auth.From(r.Context())
	if sess == nil {
		return httpx.Unauthorized()
	}
	res := statusResponse{
		Supported:          runtime.GOOS == "linux",
		Enabled:            m.deps.Settings.Bool(settings.KeyTerminalEnabled),
		Allowed:            sess.User.Role == auth.RoleAdmin,
		MaxSessions:        maxSessionsTotal,
		MaxSessionsPerUser: maxSessionsPerUser,
		IdleTimeoutMinutes: int(m.idleTimeout() / time.Minute),
		MaxSessionSeconds:  int(maxSessionLength / time.Second),
	}
	if !res.Allowed {
		httpx.OK(w, res)
		return nil
	}
	res.User = strings.TrimSpace(m.deps.Settings.Get(settings.KeyTerminalUser))
	if res.User != "" {
		if _, err := lookupUser(res.User); err != nil {
			msg := userMessage(err)
			res.UserError = &msg
		} else {
			res.UserValid = true
		}
	}
	if res.Supported {
		ok := m.helperAvailable(r.Context())
		res.HelperAvailable = &ok
	}
	m.mu.Lock()
	res.ActiveSessions = m.total
	res.OwnSessions = m.sessions[sess.User.ID]
	m.mu.Unlock()
	httpx.OK(w, res)
	return nil
}

type systemUser struct {
	Username string `json:"username"`
	UID      uint32 `json:"uid"`
	FullName string `json:"full_name"`
	Home     string `json:"home"`
	Shell    string `json:"shell"`
}

func (m *Module) handleUsers(w http.ResponseWriter, _ *http.Request) error {
	data, err := os.ReadFile(passwdPath)
	if err != nil {
		return httpx.Unavailable("passwd_unreadable", "Sistem kullanıcıları okunamadı.").Wrap(err)
	}
	out := []systemUser{}
	for _, e := range termcheck.Eligible(string(data)) {
		if fi, err := os.Stat(e.Shell); err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
			continue
		}
		out = append(out, systemUser{
			Username: e.Name, UID: e.UID, FullName: e.FullName(), Home: e.Home, Shell: e.Shell,
		})
	}
	httpx.OK(w, out)
	return nil
}

// acquire reserves a session slot for a panel user.
func (m *Module) acquire(userID int64) (ok bool, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.total >= maxSessionsTotal {
		return false, "Aynı anda açılabilecek terminal sayısına ulaşıldı. Açık terminallerden birini kapatın."
	}
	if m.sessions[userID] >= maxSessionsPerUser {
		return false, "Hesabınızla açılabilecek terminal sayısına ulaştınız. Açık terminallerden birini kapatın."
	}
	m.sessions[userID]++
	m.total++
	return true, ""
}

func (m *Module) release(userID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions[userID] > 0 {
		m.sessions[userID]--
		m.total--
	}
	if m.sessions[userID] == 0 {
		delete(m.sessions, userID)
	}
}
