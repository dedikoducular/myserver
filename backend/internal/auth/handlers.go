package auth

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"myserver/internal/config"
	"myserver/internal/httpx"
	"myserver/internal/settings"
)

// RegisterPublic mounts the endpoints reachable without a session.
func (s *Service) RegisterPublic(r *httpx.Router) {
	g := r.Group("/auth", OriginGuard)
	g.Get("/status", s.handleStatus)
	g.Post("/login", s.handleLogin)
	g.Get("/setup/checks", s.handleSetupChecks)
	g.Get("/setup/timezones", s.handleSetupTimezones)
	g.Post("/setup", s.handleSetup)
}

// RegisterPrivate mounts the endpoints that need a session; r must already
// apply Require.
func (s *Service) RegisterPrivate(r *httpx.Router) {
	g := r.Group("/auth")
	g.Post("/logout", s.handleLogout)
	g.Post("/logout-all", s.handleLogoutAll)
	g.Post("/password", s.handleChangePassword)
	g.Get("/sessions", s.handleSessions)

	a := g.Group("/users", RequireAdmin)
	a.Get("", s.handleUsersList)
	a.Post("", s.handleUsersCreate)
	a.Put("/{id}", s.handleUsersUpdate)
	a.Delete("/{id}", s.handleUsersDelete)
}

type statusResponse struct {
	SetupComplete bool   `json:"setup_complete"`
	Authenticated bool   `json:"authenticated"`
	User          *User  `json:"user"`
	CSRFToken     string `json:"csrf_token"`
	Version       string `json:"version"`
	Language      string `json:"language"`
}

func (s *Service) status(sess *Session) statusResponse {
	res := statusResponse{
		SetupComplete: s.settings.Bool(settings.KeySetupComplete),
		Version:       config.Version,
		Language:      s.settings.Get(settings.KeyLanguage),
	}
	if sess != nil {
		res.Authenticated = true
		u := sess.User
		res.User = &u
		res.CSRFToken = sess.CSRF
	}
	return res
}

func (s *Service) handleStatus(w http.ResponseWriter, r *http.Request) error {
	sess, err := s.sessionFromRequest(r)
	if err != nil {
		return httpx.Internal(err)
	}
	httpx.OK(w, s.status(sess))
	return nil
}

func (s *Service) handleLogin(w http.ResponseWriter, r *http.Request) error {
	if !s.settings.Bool(settings.KeySetupComplete) {
		return httpx.Conflict("Kurulum henüz tamamlanmadı.")
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	if req.Username == "" || req.Password == "" {
		return httpx.BadRequest("Kullanıcı adı ve parola gereklidir.")
	}
	sess, err := s.login(w, r, req.Username, req.Password)
	if err != nil {
		return err
	}
	httpx.OK(w, s.status(sess))
	return nil
}

func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) error {
	sess := From(r.Context())
	if _, err := s.db.ExecContext(r.Context(), `DELETE FROM sessions WHERE token_hash = ?`, sess.tokenHash); err != nil {
		return httpx.Internal(err)
	}
	s.clearCookie(w, r)
	s.audit.Log(r.Context(), sess.Actor(), "auth.logout", "", "oturum kapatıldı", true)
	httpx.OK(w, nil)
	return nil
}

func (s *Service) handleLogoutAll(w http.ResponseWriter, r *http.Request) error {
	sess := From(r.Context())
	if _, err := s.db.ExecContext(r.Context(),
		`DELETE FROM sessions WHERE user_id = ? AND token_hash <> ?`, sess.User.ID, sess.tokenHash); err != nil {
		return httpx.Internal(err)
	}
	s.audit.Log(r.Context(), sess.Actor(), "auth.logout_all", "", "diğer oturumlar kapatıldı", true)
	httpx.OK(w, nil)
	return nil
}

func (s *Service) handleSessions(w http.ResponseWriter, r *http.Request) error {
	sess := From(r.Context())
	rows, err := s.db.QueryContext(r.Context(), `SELECT token_hash, ip, user_agent, created_at, last_seen_at, expires_at
		FROM sessions WHERE user_id = ? AND expires_at > ? ORDER BY last_seen_at DESC`, sess.User.ID, time.Now().Unix())
	if err != nil {
		return httpx.Internal(err)
	}
	defer rows.Close()
	type item struct {
		IP         string `json:"ip"`
		UserAgent  string `json:"user_agent"`
		CreatedAt  int64  `json:"created_at"`
		LastSeenAt int64  `json:"last_seen_at"`
		ExpiresAt  int64  `json:"expires_at"`
		Current    bool   `json:"current"`
	}
	out := []item{}
	for rows.Next() {
		var it item
		var th string
		if err := rows.Scan(&th, &it.IP, &it.UserAgent, &it.CreatedAt, &it.LastSeenAt, &it.ExpiresAt); err != nil {
			return httpx.Internal(err)
		}
		it.Current = th == sess.tokenHash
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return httpx.Internal(err)
	}
	httpx.OK(w, out)
	return nil
}

func (s *Service) handleChangePassword(w http.ResponseWriter, r *http.Request) error {
	sess := From(r.Context())
	var req struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	key := "pw:" + sess.User.Username
	if d := s.byUser.Blocked(key); d > 0 {
		return blockedError(d)
	}
	var hash string
	if err := s.db.QueryRowContext(r.Context(), `SELECT password_hash FROM users WHERE id = ?`, sess.User.ID).Scan(&hash); err != nil {
		return httpx.Internal(err)
	}
	ok, err := VerifyPassword(hash, req.Current)
	if err != nil {
		return httpx.Internal(err)
	}
	if !ok {
		s.byUser.Fail(key)
		s.audit.Log(r.Context(), sess.Actor(), "auth.password_change", sess.User.Username, "mevcut parola hatalı", false)
		return httpx.BadRequest("Mevcut parola hatalı.")
	}
	s.byUser.Reset(key)
	if err := validatePassword(sess.User.Username, req.New); err != nil {
		return err
	}
	newHash, err := HashPassword(req.New)
	if err != nil {
		return httpx.Internal(err)
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return httpx.Internal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(r.Context(), `UPDATE users SET password_hash = ? WHERE id = ?`, newHash, sess.User.ID); err != nil {
		return httpx.Internal(err)
	}
	// A password change invalidates every other session of the user.
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM sessions WHERE user_id = ? AND token_hash <> ?`,
		sess.User.ID, sess.tokenHash); err != nil {
		return httpx.Internal(err)
	}
	if err := tx.Commit(); err != nil {
		return httpx.Internal(err)
	}
	s.audit.Log(r.Context(), sess.Actor(), "auth.password_change", sess.User.Username, "parola değiştirildi", true)
	httpx.OK(w, nil)
	return nil
}

func (s *Service) handleUsersList(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.db.QueryContext(r.Context(),
		`SELECT id, username, role, disabled, created_at, last_login_at FROM users ORDER BY id`)
	if err != nil {
		return httpx.Internal(err)
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &u.Disabled, &u.CreatedAt, &u.LastLoginAt); err != nil {
			return httpx.Internal(err)
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return httpx.Internal(err)
	}
	httpx.OK(w, out)
	return nil
}

func validRole(role string) bool { return role == RoleAdmin || role == RoleUser }

func (s *Service) handleUsersCreate(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	req.Username = strings.ToLower(strings.TrimSpace(req.Username))
	if err := validateUsername(req.Username); err != nil {
		return err
	}
	if err := validatePassword(req.Username, req.Password); err != nil {
		return err
	}
	if !validRole(req.Role) {
		return httpx.BadRequest("Geçersiz rol.")
	}
	hash, err := HashPassword(req.Password)
	if err != nil {
		return httpx.Internal(err)
	}
	now := time.Now().Unix()
	res, err := s.db.ExecContext(r.Context(),
		`INSERT INTO users (username, password_hash, role, created_at) VALUES (?, ?, ?, ?)`,
		req.Username, hash, req.Role, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return httpx.Conflict("Bu kullanıcı adı zaten kullanılıyor.")
		}
		return httpx.Internal(err)
	}
	id, _ := res.LastInsertId()
	s.audit.Log(r.Context(), ActorFrom(r), "users.create", req.Username, "rol: "+req.Role, true)
	httpx.JSON(w, http.StatusCreated, User{ID: id, Username: req.Username, Role: req.Role, CreatedAt: now})
	return nil
}

// activeAdmins counts enabled admins other than excludeID.
func (s *Service) activeAdmins(r *http.Request, excludeID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM users WHERE role = 'admin' AND disabled = 0 AND id <> ?`, excludeID).Scan(&n)
	return n, err
}

func (s *Service) handleUsersUpdate(w http.ResponseWriter, r *http.Request) error {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return httpx.BadRequest("Geçersiz kullanıcı.")
	}
	var req struct {
		Role     *string `json:"role"`
		Disabled *bool   `json:"disabled"`
		Password *string `json:"password"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	var u User
	err = s.db.QueryRowContext(r.Context(), `SELECT id, username, role, disabled, created_at, last_login_at
		FROM users WHERE id = ?`, id).Scan(&u.ID, &u.Username, &u.Role, &u.Disabled, &u.CreatedAt, &u.LastLoginAt)
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.NotFound("Kullanıcı bulunamadı.")
	}
	if err != nil {
		return httpx.Internal(err)
	}
	newRole, newDisabled := u.Role, u.Disabled
	if req.Role != nil {
		if !validRole(*req.Role) {
			return httpx.BadRequest("Geçersiz rol.")
		}
		newRole = *req.Role
	}
	if req.Disabled != nil {
		newDisabled = *req.Disabled
	}
	losesAdmin := u.Role == RoleAdmin && !u.Disabled && (newRole != RoleAdmin || newDisabled)
	if losesAdmin {
		n, err := s.activeAdmins(r, u.ID)
		if err != nil {
			return httpx.Internal(err)
		}
		if n == 0 {
			return httpx.Conflict("Son yönetici hesabı devre dışı bırakılamaz veya yetkisi düşürülemez.")
		}
	}
	var newHash string
	if req.Password != nil {
		if err := validatePassword(u.Username, *req.Password); err != nil {
			return err
		}
		if newHash, err = HashPassword(*req.Password); err != nil {
			return httpx.Internal(err)
		}
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return httpx.Internal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(r.Context(), `UPDATE users SET role = ?, disabled = ? WHERE id = ?`,
		newRole, newDisabled, id); err != nil {
		return httpx.Internal(err)
	}
	if newHash != "" {
		if _, err := tx.ExecContext(r.Context(), `UPDATE users SET password_hash = ? WHERE id = ?`, newHash, id); err != nil {
			return httpx.Internal(err)
		}
	}
	// Any privilege, state or password change ends the user's sessions,
	// except the caller's own session when editing themselves.
	if newHash != "" || newDisabled || newRole != u.Role {
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM sessions WHERE user_id = ? AND token_hash <> ?`,
			id, From(r.Context()).tokenHash); err != nil {
			return httpx.Internal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return httpx.Internal(err)
	}
	detail := "rol: " + newRole + ", devre dışı: " + strconv.FormatBool(newDisabled)
	if newHash != "" {
		detail += ", parola sıfırlandı"
	}
	s.audit.Log(r.Context(), ActorFrom(r), "users.update", u.Username, detail, true)
	u.Role, u.Disabled = newRole, newDisabled
	httpx.OK(w, u)
	return nil
}

func (s *Service) handleUsersDelete(w http.ResponseWriter, r *http.Request) error {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return httpx.BadRequest("Geçersiz kullanıcı.")
	}
	sess := From(r.Context())
	if id == sess.User.ID {
		return httpx.Conflict("Kendi hesabınızı silemezsiniz.")
	}
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	var username, role string
	var disabled bool
	err = s.db.QueryRowContext(r.Context(), `SELECT username, role, disabled FROM users WHERE id = ?`, id).
		Scan(&username, &role, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.NotFound("Kullanıcı bulunamadı.")
	}
	if err != nil {
		return httpx.Internal(err)
	}
	if role == RoleAdmin && !disabled {
		n, err := s.activeAdmins(r, id)
		if err != nil {
			return httpx.Internal(err)
		}
		if n == 0 {
			return httpx.Conflict("Son yönetici hesabı silinemez.")
		}
	}
	if _, err := s.db.ExecContext(r.Context(), `DELETE FROM users WHERE id = ?`, id); err != nil {
		return httpx.Internal(err)
	}
	s.audit.Log(r.Context(), sess.Actor(), "users.delete", username, "", true)
	httpx.OK(w, nil)
	return nil
}
