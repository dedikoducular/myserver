// Package auth implements users, sessions, login protection, the first-run
// setup wizard and the HTTP middleware that guards every other module.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"myserver/internal/audit"
	"myserver/internal/config"
	"myserver/internal/httpx"
	"myserver/internal/privileged"
	"myserver/internal/settings"
)

const (
	cookieName = "myserver_session"
	csrfHeader = "X-CSRF-Token"

	RoleAdmin = "admin"
	RoleUser  = "user"
)

type User struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	Role        string `json:"role"`
	Disabled    bool   `json:"disabled"`
	CreatedAt   int64  `json:"created_at"`
	LastLoginAt *int64 `json:"last_login_at"`
}

// Session is the authenticated caller attached to a request context.
type Session struct {
	User      User
	CSRF      string
	IP        string
	tokenHash string
}

// Actor converts the session to an audit actor.
func (s *Session) Actor() audit.Actor {
	return audit.Actor{Username: s.User.Username, IP: s.IP}
}

type ctxKey struct{}

// From returns the session of an authenticated request, or nil.
func From(ctx context.Context) *Session {
	s, _ := ctx.Value(ctxKey{}).(*Session)
	return s
}

// ActorFrom returns the audit actor of a request.
func ActorFrom(r *http.Request) audit.Actor {
	if s := From(r.Context()); s != nil {
		return s.Actor()
	}
	return audit.Actor{Username: "-", IP: httpx.ClientIP(r)}
}

type Service struct {
	db       *sql.DB
	cfg      *config.Config
	settings *settings.Store
	audit    *audit.Logger
	priv     *privileged.Runner
	byIP     *Limiter
	byUser   *Limiter
	// adminMu serializes user changes, so two concurrent requests cannot
	// each see "another admin exists" and together remove the last one.
	adminMu sync.Mutex
	// dummyHash keeps login timing equal for unknown usernames.
	dummyHash string
}

func NewService(db *sql.DB, cfg *config.Config, st *settings.Store, al *audit.Logger, priv *privileged.Runner) (*Service, error) {
	dummy, err := HashPassword("myserver-timing-equalizer")
	if err != nil {
		return nil, err
	}
	return &Service{
		db: db, cfg: cfg, settings: st, audit: al, priv: priv,
		byIP:      NewLimiter(10, 15*time.Minute, 15*time.Minute),
		byUser:    NewLimiter(5, 15*time.Minute, 15*time.Minute),
		dummyHash: dummy,
	}, nil
}

// Start runs periodic cleanup until ctx is cancelled.
func (s *Service) Start(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.byIP.Sweep()
			s.byUser.Sweep()
			_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, time.Now().Unix())
		}
	}
}

var usernameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{2,31}$`)

func validateUsername(name string) error {
	if !usernameRe.MatchString(name) {
		return httpx.BadRequest("Kullanıcı adı 3-32 karakter olmalı, küçük harfle başlamalı ve yalnızca küçük harf, rakam, '-' veya '_' içermelidir.")
	}
	if name == "root" {
		return httpx.BadRequest("'root' kullanıcı adı panelde kullanılamaz.")
	}
	return nil
}

func validatePassword(username, password string) error {
	n := len([]rune(password))
	if n < 10 {
		return httpx.BadRequest("Parola en az 10 karakter olmalıdır.")
	}
	if len(password) > 256 {
		return httpx.BadRequest("Parola en fazla 256 bayt olabilir.")
	}
	if strings.EqualFold(password, username) {
		return httpx.BadRequest("Parola kullanıcı adıyla aynı olamaz.")
	}
	return nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

var errInvalidLogin = httpx.NewError(http.StatusUnauthorized, "invalid_credentials", "Kullanıcı adı veya parola hatalı.")

// login verifies credentials and, on success, creates a session and sets the
// cookie.
func (s *Service) login(w http.ResponseWriter, r *http.Request, username, password string) (*Session, error) {
	ip := httpx.ClientIP(r)
	username = strings.ToLower(strings.TrimSpace(username))
	userKey := "u:" + username
	if d := s.byIP.Blocked(ip); d > 0 {
		return nil, blockedError(d)
	}
	if d := s.byUser.Blocked(userKey); d > 0 {
		return nil, blockedError(d)
	}

	var u User
	var hash string
	err := s.db.QueryRowContext(r.Context(), `SELECT id, username, role, disabled, created_at, password_hash
		FROM users WHERE username = ?`, username).Scan(&u.ID, &u.Username, &u.Role, &u.Disabled, &u.CreatedAt, &hash)
	found := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.Internal(err)
	}
	if !found {
		hash = s.dummyHash
	}
	ok, verr := VerifyPassword(hash, password)
	if verr != nil {
		return nil, httpx.Internal(verr)
	}
	if !found || !ok || u.Disabled {
		s.byIP.Fail(ip)
		s.byUser.Fail(userKey)
		s.audit.Log(r.Context(), audit.Actor{Username: username, IP: ip}, "auth.login", "", "başarısız giriş denemesi", false)
		return nil, errInvalidLogin
	}
	s.byIP.Reset(ip)
	s.byUser.Reset(userKey)

	sess, err := s.createSession(w, r, u)
	if err != nil {
		return nil, httpx.Internal(err)
	}
	s.audit.Log(r.Context(), sess.Actor(), "auth.login", "", "oturum açıldı", true)
	return sess, nil
}

func blockedError(d time.Duration) error {
	mins := int(d.Minutes()) + 1
	return httpx.TooManyRequests("Çok fazla başarısız giriş denemesi. Lütfen " + itoa(mins) + " dakika sonra tekrar deneyin.")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func (s *Service) createSession(w http.ResponseWriter, r *http.Request, u User) (*Session, error) {
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	csrf, err := randomToken()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	hours := s.settings.Int(settings.KeySessionHours, 12)
	if hours < 1 || hours > 24*30 {
		hours = 12
	}
	expires := now.Add(time.Duration(hours) * time.Hour)
	ip := httpx.ClientIP(r)
	ua := r.UserAgent()
	if len(ua) > 255 {
		ua = ua[:255]
	}
	th := hashToken(token)
	if _, err := s.db.ExecContext(r.Context(), `INSERT INTO sessions
		(token_hash, user_id, csrf_token, ip, user_agent, created_at, last_seen_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		th, u.ID, csrf, ip, ua, now.Unix(), now.Unix(), expires.Unix()); err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(r.Context(), `UPDATE users SET last_login_at = ? WHERE id = ?`, now.Unix(), u.ID); err != nil {
		return nil, err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure || r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})
	return &Session{User: u, CSRF: csrf, IP: ip, tokenHash: th}, nil
}

func (s *Service) clearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.CookieSecure || r.TLS != nil, SameSite: http.SameSiteStrictMode,
	})
}

// sessionFromRequest loads the session named by the cookie, or nil.
func (s *Service) sessionFromRequest(r *http.Request) (*Session, error) {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return nil, nil
	}
	th := hashToken(c.Value)
	now := time.Now().Unix()
	sess := &Session{IP: httpx.ClientIP(r), tokenHash: th}
	var lastSeen int64
	err = s.db.QueryRowContext(r.Context(), `SELECT u.id, u.username, u.role, u.disabled, u.created_at, u.last_login_at,
			s.csrf_token, s.last_seen_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ?`, th, now).Scan(
		&sess.User.ID, &sess.User.Username, &sess.User.Role, &sess.User.Disabled,
		&sess.User.CreatedAt, &sess.User.LastLoginAt, &sess.CSRF, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if sess.User.Disabled {
		return nil, nil
	}
	if now-lastSeen > 60 {
		_, _ = s.db.ExecContext(r.Context(), `UPDATE sessions SET last_seen_at = ? WHERE token_hash = ?`, now, th)
	}
	return sess, nil
}

// SessionActive reports whether sess is still valid: not expired, not
// logged out, and its user still enabled with the same role. Long-lived
// connections (terminal, exec) call it periodically, because they outlive
// the request that authenticated them.
func (s *Service) SessionActive(ctx context.Context, sess *Session) bool {
	if sess == nil {
		return false
	}
	var role string
	var disabled bool
	err := s.db.QueryRowContext(ctx, `SELECT u.role, u.disabled
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ?`, sess.tokenHash, time.Now().Unix()).Scan(&role, &disabled)
	if err != nil {
		return false
	}
	return !disabled && role == sess.User.Role
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// OriginGuard rejects state-changing requests whose Origin header names a
// different host. It protects the unauthenticated endpoints (login, setup)
// that cannot carry a CSRF token yet.
func OriginGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isSafeMethod(r.Method) && r.Header.Get("Origin") != "" && !httpx.SameOrigin(r) {
			httpx.Fail(w, r, httpx.Forbidden())
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Require authenticates the request and enforces the CSRF token on
// state-changing methods.
func (s *Service) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, err := s.sessionFromRequest(r)
		if err != nil {
			httpx.Fail(w, r, httpx.Internal(err))
			return
		}
		if sess == nil {
			httpx.Fail(w, r, httpx.Unauthorized())
			return
		}
		if !isSafeMethod(r.Method) {
			got := r.Header.Get(csrfHeader)
			if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(sess.CSRF)) != 1 {
				httpx.Fail(w, r, httpx.NewError(http.StatusForbidden, "csrf_invalid",
					"Güvenlik doğrulaması başarısız oldu. Sayfayı yenileyip tekrar deneyin."))
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, sess)))
	})
}

// RequireAdmin must be placed after Require.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := From(r.Context())
		if sess == nil || sess.User.Role != RoleAdmin {
			httpx.Fail(w, r, httpx.Forbidden())
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireWebSocket is Require for WebSocket upgrades: browsers cannot send
// custom headers on a handshake, so the Origin check replaces the CSRF token.
func (s *Service) RequireWebSocket(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !httpx.SameOrigin(r) {
			httpx.Fail(w, r, httpx.Forbidden())
			return
		}
		sess, err := s.sessionFromRequest(r)
		if err != nil {
			httpx.Fail(w, r, httpx.Internal(err))
			return
		}
		if sess == nil {
			httpx.Fail(w, r, httpx.Unauthorized())
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, sess)))
	})
}
