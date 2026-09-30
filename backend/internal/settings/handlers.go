package settings

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"myserver/internal/audit"
	"myserver/internal/httpx"
	"myserver/internal/privileged"
)

// Validator checks and normalizes a value before it is stored.
type Validator func(value string) (string, error)

// Applier performs the system side effect of a setting (for example
// changing the hostname) before the value is stored.
type Applier func(ctx context.Context, value string) error

type keySpec struct {
	validate Validator
	apply    Applier
}

// API exposes settings over HTTP. Only registered keys can be written, so
// the endpoint can never be used to set internal state such as
// setup.complete.
type API struct {
	store *Store
	audit *audit.Logger
	actor func(*http.Request) audit.Actor
	keys  map[string]keySpec
}

func NewAPI(store *Store, al *audit.Logger, priv *privileged.Runner, actor func(*http.Request) audit.Actor) *API {
	a := &API{store: store, audit: al, actor: actor, keys: map[string]keySpec{}}

	a.Allow(KeyHostname, func(v string) (string, error) {
		v = strings.ToLower(strings.TrimSpace(v))
		if !ValidHostname(v) {
			return "", httpx.BadRequest("Sunucu adı geçersiz.")
		}
		return v, nil
	}, func(ctx context.Context, v string) error {
		if _, err := priv.Run(ctx, "hostname-set", v); err != nil {
			return httpx.NewError(http.StatusBadGateway, "hostname_failed",
				privileged.UserMessage(err, "Sunucu adı değiştirilemedi.")).Wrap(err)
		}
		return nil
	})
	a.Allow(KeyTimezone, func(v string) (string, error) {
		v = strings.TrimSpace(v)
		if !ValidTimezone(v) {
			return "", httpx.BadRequest("Saat dilimi geçersiz.")
		}
		return v, nil
	}, func(ctx context.Context, v string) error {
		if _, err := priv.Run(ctx, "timezone-set", v); err != nil {
			return httpx.NewError(http.StatusBadGateway, "timezone_failed",
				privileged.UserMessage(err, "Saat dilimi değiştirilemedi.")).Wrap(err)
		}
		return nil
	})
	a.Allow(KeyLanguage, OneOf("tr"), nil)
	a.Allow(KeyTerminalEnabled, Bool, nil)
	a.Allow(KeyTerminalTimeout, IntRange(1, 240), nil)
	a.Allow(KeySessionHours, IntRange(1, 720), nil)
	a.Allow(KeyAllowedRoots, validateRoots, nil)
	return a
}

// Allow makes a key writable through the API. Modules call it for the keys
// they own.
func (a *API) Allow(key string, v Validator, apply Applier) {
	a.keys[key] = keySpec{validate: v, apply: apply}
}

// Bool validates "true"/"false".
func Bool(v string) (string, error) {
	b, err := strconv.ParseBool(v)
	if err != nil {
		return "", httpx.BadRequest("Değer 'true' veya 'false' olmalıdır.")
	}
	return strconv.FormatBool(b), nil
}

// IntRange validates an integer within [min, max].
func IntRange(min, max int) Validator {
	return func(v string) (string, error) {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < min || n > max {
			return "", httpx.BadRequest("Değer " + strconv.Itoa(min) + " ile " + strconv.Itoa(max) + " arasında olmalıdır.")
		}
		return strconv.Itoa(n), nil
	}
}

// OneOf validates membership in a fixed set.
func OneOf(options ...string) Validator {
	return func(v string) (string, error) {
		for _, o := range options {
			if v == o {
				return v, nil
			}
		}
		return "", httpx.BadRequest("Geçersiz değer.")
	}
}

// forbiddenRoots may never be exposed through the file manager: not the
// directory itself and nothing beneath it. The panel holds CAP_DAC_OVERRIDE,
// so a root such as /etc/ssh or /root/.ssh would hand out the system.
var forbiddenRoots = []string{"/etc", "/proc", "/sys", "/dev", "/boot", "/root", "/run", "/var", "/usr", "/bin", "/sbin", "/lib", "/lib64"}

// protectedDirs are further directories that must stay closed, registered at
// start (the panel's data directory, which holds the password database).
var protectedDirs []string

// ProtectDir closes dir and everything beneath it to the file manager. Call
// it before the server starts.
func ProtectDir(dir string) {
	protectedDirs = append(protectedDirs, filepath.ToSlash(filepath.Clean(dir)))
}

func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, strings.TrimSuffix(dir, "/")+"/")
}

// RootForbidden reports whether a cleaned absolute path may not be used as
// an allowed root.
func RootForbidden(path string) bool {
	if path == "/" {
		return true
	}
	for _, f := range forbiddenRoots {
		if within(path, f) {
			return true
		}
	}
	for _, d := range protectedDirs {
		// Neither inside the protected directory nor a parent of it.
		if within(path, d) || within(d, path) {
			return true
		}
	}
	return false
}

// ErrRootIsLink reports an allowed root that is, or passes through, a
// symbolic link.
var ErrRootIsLink = errors.New("settings: allowed root resolves through a symbolic link")

// CheckRootReal verifies that an allowed root is a real directory path and
// not a symbolic link (or a path through one). Without this, a root such as
// /data pointing at /etc would expose /etc. The file manager calls it on
// every access, because a link can appear after the setting was saved. A
// root that does not exist yet passes; there is nothing to open.
func CheckRootReal(root string) error {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if filepath.Clean(real) != filepath.Clean(root) {
		return ErrRootIsLink
	}
	return nil
}

func validateRoots(v string) (string, error) {
	var roots []string
	if err := json.Unmarshal([]byte(v), &roots); err != nil {
		return "", httpx.BadRequest("Dizin listesi geçersiz.")
	}
	if len(roots) > 32 {
		return "", httpx.BadRequest("En fazla 32 dizin tanımlanabilir.")
	}
	seen := map[string]bool{}
	out := []string{}
	for _, r := range roots {
		if !strings.HasPrefix(r, "/") || strings.ContainsRune(r, 0) {
			return "", httpx.BadRequest("Dizin yolu '/' ile başlamalıdır: " + r)
		}
		c := filepath.ToSlash(filepath.Clean(r))
		if RootForbidden(c) {
			return "", httpx.BadRequest("Bu dizin güvenlik nedeniyle açılamaz: " + c)
		}
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return "", httpx.BadRequest("Bu yol bir dizin değil: " + c)
		}
		if err := CheckRootReal(filepath.FromSlash(c)); err != nil {
			return "", httpx.BadRequest("Bu yol sembolik bağlantı içerdiği için eklenemez. Bağlantının gösterdiği gerçek dizini ekleyin: " + c)
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

// Register mounts the settings routes on the authenticated router.
func (a *API) Register(r *httpx.Router, requireAdmin httpx.Middleware) {
	g := r.Group("/settings", requireAdmin)
	g.Get("", a.handleGet)
	g.Put("", a.handlePut)
	g.Get("/timezones", func(w http.ResponseWriter, _ *http.Request) error {
		httpx.OK(w, Timezones())
		return nil
	})
}

func (a *API) handleGet(w http.ResponseWriter, _ *http.Request) error {
	all := a.store.All()
	out := map[string]string{}
	for k := range a.keys {
		out[k] = all[k]
	}
	// Report the live system values rather than what was last stored.
	if h, err := os.Hostname(); err == nil {
		out[KeyHostname] = h
	}
	out[KeyTimezone] = CurrentTimezone()
	httpx.OK(w, out)
	return nil
}

func (a *API) handlePut(w http.ResponseWriter, r *http.Request) error {
	var req map[string]string
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	if len(req) == 0 {
		return httpx.BadRequest("Değiştirilecek ayar belirtilmedi.")
	}
	// Validate everything before changing anything.
	clean := make(map[string]string, len(req))
	for k, v := range req {
		spec, ok := a.keys[k]
		if !ok {
			return httpx.BadRequest("Bilinmeyen ayar: " + k)
		}
		nv, err := spec.validate(v)
		if err != nil {
			return err
		}
		clean[k] = nv
	}
	actor := a.actor(r)
	for k, v := range clean {
		if v == a.store.Get(k) && a.keys[k].apply == nil {
			continue
		}
		if apply := a.keys[k].apply; apply != nil {
			if err := apply(r.Context(), v); err != nil {
				a.audit.Log(r.Context(), actor, "settings.update", k, "başarısız", false)
				return err
			}
		}
		if err := a.store.Set(r.Context(), k, v); err != nil {
			return httpx.Internal(err)
		}
		a.audit.Log(r.Context(), actor, "settings.update", k, v, true)
	}
	return a.handleGet(w, r)
}
