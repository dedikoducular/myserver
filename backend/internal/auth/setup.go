package auth

import (
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"myserver/internal/audit"
	"myserver/internal/httpx"
	"myserver/internal/settings"
)

// setupMu serializes setup so two concurrent requests cannot both create
// the first admin.
var setupMu sync.Mutex

var errSetupDone = httpx.NewError(http.StatusForbidden, "setup_complete",
	"Kurulum zaten tamamlandı. Bu işlem tekrar kullanılamaz.")

// Check is one wizard readiness check.
type Check struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

type setupChecks struct {
	Hostname string `json:"hostname"`
	Timezone string `json:"timezone"`
	Storage  Check  `json:"storage"`
	Docker   Check  `json:"docker"`
	Helper   Check  `json:"helper"`
}

func (s *Service) handleSetupChecks(w http.ResponseWriter, r *http.Request) error {
	if s.settings.Bool(settings.KeySetupComplete) {
		return errSetupDone
	}
	host, _ := os.Hostname()
	res := setupChecks{
		Hostname: host,
		Timezone: settings.CurrentTimezone(),
		Storage:  checkStorage(s.cfg.DataDir),
		Docker:   checkDocker(r.Context(), s.cfg.DockerHost),
	}
	if err := s.priv.Available(r.Context()); err != nil {
		res.Helper = Check{OK: false, Message: "Yetkili yardımcı servis kullanılamıyor. Sistem ayarları panelden değiştirilemez."}
	} else {
		res.Helper = Check{OK: true, Message: "Yetkili yardımcı servis hazır."}
	}
	httpx.OK(w, res)
	return nil
}

func (s *Service) handleSetupTimezones(w http.ResponseWriter, r *http.Request) error {
	if s.settings.Bool(settings.KeySetupComplete) {
		return errSetupDone
	}
	httpx.OK(w, settings.Timezones())
	return nil
}

func (s *Service) handleSetup(w http.ResponseWriter, r *http.Request) error {
	// Refuse before reading the body: once setup is done this endpoint must
	// not validate input or hash passwords for unauthenticated callers. The
	// flag is checked again under setupMu below for concurrent requests.
	if s.settings.Bool(settings.KeySetupComplete) {
		return errSetupDone
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Hostname string `json:"hostname"`
		Timezone string `json:"timezone"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	req.Username = strings.ToLower(strings.TrimSpace(req.Username))
	req.Hostname = strings.ToLower(strings.TrimSpace(req.Hostname))
	req.Timezone = strings.TrimSpace(req.Timezone)
	if err := validateUsername(req.Username); err != nil {
		return err
	}
	if err := validatePassword(req.Username, req.Password); err != nil {
		return err
	}
	if !settings.ValidHostname(req.Hostname) {
		return httpx.BadRequest("Sunucu adı geçersiz. Yalnızca harf, rakam ve '-' kullanın (en fazla 63 karakter).")
	}
	if !settings.ValidTimezone(req.Timezone) {
		return httpx.BadRequest("Saat dilimi geçersiz.")
	}
	hash, err := HashPassword(req.Password)
	if err != nil {
		return httpx.Internal(err)
	}

	setupMu.Lock()
	defer setupMu.Unlock()
	if s.settings.Bool(settings.KeySetupComplete) {
		return errSetupDone
	}
	var n int
	if err := s.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return httpx.Internal(err)
	}
	if n > 0 {
		// Users exist but the flag is missing: repair the flag, never
		// allow a second "first admin".
		_ = s.settings.Set(r.Context(), settings.KeySetupComplete, "true")
		return errSetupDone
	}
	now := time.Now().Unix()
	res, err := s.db.ExecContext(r.Context(),
		`INSERT INTO users (username, password_hash, role, created_at) VALUES (?, ?, 'admin', ?)`,
		req.Username, hash, now)
	if err != nil {
		return httpx.Internal(err)
	}
	id, _ := res.LastInsertId()
	if err := s.settings.Set(r.Context(), settings.KeySetupComplete, "true"); err != nil {
		return httpx.Internal(err)
	}

	actor := audit.Actor{Username: req.Username, IP: httpx.ClientIP(r)}
	s.audit.Log(r.Context(), actor, "setup.complete", req.Username, "ilk yönetici hesabı oluşturuldu", true)

	// System changes are best effort: the account must exist even if the
	// helper is unavailable, and the user is told exactly what failed.
	warnings := []string{}
	current, _ := os.Hostname()
	if req.Hostname != current {
		if _, err := s.priv.Run(r.Context(), "hostname-set", req.Hostname); err != nil {
			warnings = append(warnings, "Sunucu adı değiştirilemedi. Ayarlar > Genel bölümünden tekrar deneyebilirsiniz.")
			s.audit.Log(r.Context(), actor, "system.hostname", req.Hostname, "başarısız", false)
		} else {
			s.audit.Log(r.Context(), actor, "system.hostname", req.Hostname, "", true)
		}
	}
	if req.Timezone != settings.CurrentTimezone() {
		if _, err := s.priv.Run(r.Context(), "timezone-set", req.Timezone); err != nil {
			warnings = append(warnings, "Saat dilimi değiştirilemedi. Ayarlar > Genel bölümünden tekrar deneyebilirsiniz.")
			s.audit.Log(r.Context(), actor, "system.timezone", req.Timezone, "başarısız", false)
		} else {
			s.audit.Log(r.Context(), actor, "system.timezone", req.Timezone, "", true)
		}
	}
	_ = s.settings.Set(r.Context(), settings.KeyHostname, req.Hostname)
	_ = s.settings.Set(r.Context(), settings.KeyTimezone, req.Timezone)

	sess, err := s.createSession(w, r, User{ID: id, Username: req.Username, Role: RoleAdmin, CreatedAt: now})
	if err != nil {
		return httpx.Internal(err)
	}
	httpx.JSON(w, http.StatusCreated, struct {
		statusResponse
		Warnings []string `json:"warnings"`
	}{s.status(sess), warnings})
	return nil
}
