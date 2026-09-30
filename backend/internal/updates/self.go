package updates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"myserver/internal/auth"
	"myserver/internal/httpx"
	"myserver/internal/notify"
	"myserver/internal/privileged"
	"myserver/internal/updates/updatescheck"
)

const (
	maxReleaseBody = 1 << 20
	maxNotesLength = 20000
)

// Variables only so that the tests can use a local TLS server: the API
// address, the transport (nil: the default one) and the request timeout.
var (
	githubAPI                          = "https://api.github.com"
	releaseTransport http.RoundTripper = nil
	releaseTimeout                     = 20 * time.Second
)

// Release is one published MyServer version. Every field comes from a
// remote server and is treated as untrusted text.
type Release struct {
	Version     string `json:"version"`
	Notes       string `json:"notes"` // plain text; never rendered as HTML
	PublishedAt *int64 `json:"published_at"`
	URL         string `json:"url"`
	// Assets holds the release tarball per architecture ("amd64", "arm64").
	Assets map[string]Asset `json:"assets"`
}

// Asset is the release tarball for one architecture.
type Asset struct {
	URL string `json:"url"`
	// SHA256 is empty when the source does not publish a digest.
	SHA256 string `json:"sha256"`
}

// ReleaseSource is where new MyServer versions are announced.
type ReleaseSource interface {
	// ID identifies the source and its configuration.
	ID() string
	Latest(ctx context.Context) (Release, error)
}

// sourceError is a release-source failure with a user-facing explanation.
type sourceError struct {
	Code    string
	Message string
	Err     error
}

func (e *sourceError) Error() string {
	if e.Err != nil {
		return e.Code + ": " + e.Err.Error()
	}
	return e.Code
}

func releaseClient() *http.Client {
	return &http.Client{
		Timeout:   releaseTimeout,
		Transport: releaseTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return errors.New("redirect to a non-HTTPS address")
			}
			return nil
		},
	}
}

// fetchJSON performs a bounded HTTPS GET and decodes the JSON response.
func fetchJSON(ctx context.Context, rawURL string, headers map[string]string, v any) error {
	body, err := fetchBytes(ctx, rawURL, headers, maxReleaseBody)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return &sourceError{Code: "source_invalid", Message: "Güncelleme sunucusunun yanıtı anlaşılamadı.", Err: err}
	}
	return nil
}

// fetchBytes performs a bounded HTTPS GET.
func fetchBytes(ctx context.Context, rawURL string, headers map[string]string, limit int64) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return nil, &sourceError{Code: "insecure_source", Message: "Güncelleme kaynağı HTTPS adresi olmalıdır."}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, &sourceError{Code: "source_invalid", Message: "Güncelleme kaynağının adresi geçersiz.", Err: err}
	}
	req.Header.Set("User-Agent", "MyServer/"+installedVersion())
	req.Header.Set("Accept", "application/json")
	for k, val := range headers {
		req.Header.Set(k, val)
	}
	res, err := releaseClient().Do(req)
	if err != nil {
		return nil, &sourceError{Code: "source_unreachable", Message: "Güncelleme sunucusuna ulaşılamadı.", Err: err}
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusNotFound:
		return nil, &sourceError{Code: "no_release", Message: "Güncelleme kaynağında yayınlanmış bir sürüm bulunamadı."}
	case res.StatusCode == http.StatusTooManyRequests || res.StatusCode == http.StatusForbidden:
		return nil, &sourceError{Code: "source_rate_limited", Message: "Güncelleme sunucusu isteği reddetti (istek sınırı). Daha sonra yeniden deneyin."}
	case res.StatusCode != http.StatusOK:
		return nil, &sourceError{Code: "source_error", Message: "Güncelleme sunucusu beklenmeyen bir yanıt verdi (" + strconv.Itoa(res.StatusCode) + ")."}
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, &sourceError{Code: "source_unreachable", Message: "Güncelleme sunucusunun yanıtı okunamadı.", Err: err}
	}
	if int64(len(body)) > limit {
		return nil, &sourceError{Code: "source_too_large", Message: "Güncelleme sunucusunun yanıtı beklenenden büyük."}
	}
	return body, nil
}

// cleanText keeps printable text only and caps the length.
func cleanText(s string, max int) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var b strings.Builder
	for _, r := range s {
		if r == '\n' || r == '\t' || (r >= 0x20 && r != 0x7f && r != utf8.RuneError) {
			b.WriteRune(r)
		}
		if b.Len() >= max {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

func httpsOrEmpty(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || len(raw) > 500 {
		return ""
	}
	return u.String()
}

func normalizeRelease(version, notes, published, link string, assets map[string]Asset) (Release, error) {
	version = strings.TrimSpace(version)
	if !updatescheck.ValidReleaseVersion(version) {
		return Release{}, &sourceError{Code: "version_invalid", Message: "Güncelleme kaynağının bildirdiği sürüm numarası geçersiz."}
	}
	r := Release{
		Version: strings.TrimPrefix(version, "v"),
		Notes:   cleanText(notes, maxNotesLength),
		URL:     httpsOrEmpty(link),
		Assets:  map[string]Asset{},
	}
	for arch, a := range assets {
		if !updatescheck.SupportedArch(arch) || !updatescheck.ValidUpdateURL(a.URL) {
			continue
		}
		a.SHA256 = strings.ToLower(strings.TrimSpace(a.SHA256))
		if !updatescheck.ValidSHA256(a.SHA256) {
			a.SHA256 = ""
		}
		r.Assets[arch] = a
	}
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(published)); err == nil {
		u := t.Unix()
		r.PublishedAt = &u
	}
	return r, nil
}

// githubSource reads the latest release of a GitHub repository.
type githubSource struct{ repo string }

func (g githubSource) ID() string { return sourceGitHub + ":" + g.repo }

func (g githubSource) Latest(ctx context.Context) (Release, error) {
	var body struct {
		TagName     string `json:"tag_name"`
		Body        string `json:"body"`
		PublishedAt string `json:"published_at"`
		HTMLURL     string `json:"html_url"`
		Assets      []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Digest string `json:"digest"` // "sha256:<hex>", on newer releases
		} `json:"assets"`
	}
	err := fetchJSON(ctx, githubAPI+"/repos/"+g.repo+"/releases/latest",
		map[string]string{"Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28"}, &body)
	if err != nil {
		return Release{}, err
	}
	assets := map[string]Asset{}
	sumsURL := ""
	for i, a := range body.Assets {
		if i >= 200 {
			break
		}
		if a.Name == "SHA256SUMS" {
			sumsURL = a.URL
		}
		for _, arch := range []string{"amd64", "arm64"} {
			if a.Name == updatescheck.ReleaseTarball(arch) {
				sum, _ := strings.CutPrefix(a.Digest, "sha256:")
				assets[arch] = Asset{URL: a.URL, SHA256: sum}
			}
		}
	}
	// Releases without per-asset digests: take them from the published
	// SHA256SUMS file. update.sh downloads and checks that file again.
	if len(assets) > 0 && httpsOrEmpty(sumsURL) != "" {
		if text, err := fetchBytes(ctx, sumsURL, nil, 64<<10); err == nil {
			sums := parseSums(string(text))
			for arch, a := range assets {
				if !updatescheck.ValidSHA256(a.SHA256) {
					a.SHA256 = sums[updatescheck.ReleaseTarball(arch)]
					assets[arch] = a
				}
			}
		}
	}
	return normalizeRelease(body.TagName, body.Body, body.PublishedAt, body.HTMLURL, assets)
}

// parseSums reads "digest  filename" lines of a SHA256SUMS file.
func parseSums(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || len(out) >= 64 {
			continue
		}
		sum := strings.ToLower(f[0])
		if updatescheck.ValidSHA256(sum) {
			out[strings.TrimPrefix(f[1], "*")] = sum
		}
	}
	return out
}

// manifestSource reads a JSON manifest from the owner's own server:
//
//	{"version":"1.2.0","notes":"...","published_at":"2026-01-02T15:04:05Z",
//	 "url":"https://... (release page, optional)",
//	 "assets":{"amd64":{"url":"https://.../myserver-linux-amd64.tar.gz","sha256":"<hex>"},
//	           "arm64":{"url":"https://.../myserver-linux-arm64.tar.gz","sha256":"<hex>"}}}
//
// SHA256SUMS must be published in the same directory as each tarball.
type manifestSource struct{ url string }

func (s manifestSource) ID() string { return sourceURL + ":" + s.url }

func (s manifestSource) Latest(ctx context.Context) (Release, error) {
	var body struct {
		Version     string           `json:"version"`
		Notes       string           `json:"notes"`
		PublishedAt string           `json:"published_at"`
		URL         string           `json:"url"`
		Assets      map[string]Asset `json:"assets"`
	}
	if err := fetchJSON(ctx, s.url, nil, &body); err != nil {
		return Release{}, err
	}
	return normalizeRelease(body.Version, body.Notes, body.PublishedAt, body.URL, body.Assets)
}

// source returns the configured release source, or nil when none is
// configured (the default) or the configuration is incomplete.
func (m *Module) source() ReleaseSource {
	s := m.deps.Settings
	switch s.Get(KeySource) {
	case sourceGitHub:
		if repo := s.Get(KeyGitHubRepo); githubRepoRe.MatchString(repo) {
			return githubSource{repo: repo}
		}
	case sourceURL:
		if u := httpsOrEmpty(s.Get(KeyManifestURL)); u != "" {
			return manifestSource{url: u}
		}
	}
	return nil
}

func (m *Module) sourceID() string {
	if src := m.source(); src != nil {
		return src.ID()
	}
	return ""
}

/* ---------- semantic versions ---------- */

type semver struct {
	nums [3]int
	pre  []string
}

// semverRe is the Semantic Versioning 2.0 grammar with an optional "v".
var semverRe = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)` +
	`(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

func parseSemver(v string) (semver, bool) {
	var out semver
	v = strings.TrimSpace(v)
	if len(v) > 100 || !semverRe.MatchString(v) {
		return out, false
	}
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	core, pre, hasPre := strings.Cut(v, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		if p == "" || len(p) > 9 {
			return out, false
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out.nums[i] = n
	}
	if hasPre {
		if pre == "" {
			return out, false
		}
		out.pre = strings.Split(pre, ".")
	}
	return out, true
}

// compareSemver orders two versions by the Semantic Versioning 2.0 rules.
func compareSemver(a, b semver) int {
	for i := range a.nums {
		if a.nums[i] != b.nums[i] {
			if a.nums[i] < b.nums[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return 0
	case len(a.pre) == 0:
		return 1
	case len(b.pre) == 0:
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		x, y := a.pre[i], b.pre[i]
		if x == y {
			continue
		}
		xn, xerr := strconv.Atoi(x)
		yn, yerr := strconv.Atoi(y)
		switch {
		case xerr == nil && yerr == nil:
			if xn < yn {
				return -1
			}
			return 1
		case xerr == nil:
			return -1
		case yerr == nil:
			return 1
		case x < y:
			return -1
		default:
			return 1
		}
	}
	switch {
	case len(a.pre) < len(b.pre):
		return -1
	case len(a.pre) > len(b.pre):
		return 1
	}
	return 0
}

// isNewer reports whether candidate is a later version than installed.
func isNewer(candidate, installed string) bool {
	c, ok1 := parseSemver(candidate)
	i, ok2 := parseSemver(installed)
	return ok1 && ok2 && compareSemver(c, i) > 0
}

/* ---------- state and handlers ---------- */

type selfState struct {
	SourceID  string      `json:"source_id"`
	Latest    *Release    `json:"latest"`
	CheckedAt int64       `json:"checked_at"`
	Error     *stateError `json:"error"`
}

type selfView struct {
	Installed       string      `json:"installed_version"`
	Source          string      `json:"source"`
	SourceLabel     string      `json:"source_label"`
	Configured      bool        `json:"configured"`
	Latest          *Release    `json:"latest"`
	UpdateAvailable bool        `json:"update_available"`
	CheckedAt       *int64      `json:"checked_at"`
	Error           *stateError `json:"error"`
	Checking        bool        `json:"checking"`
	AutoCheck       bool        `json:"auto_check"`
	// Arch is this server's architecture; Asset the tarball for it.
	Arch  string `json:"arch"`
	Asset *Asset `json:"asset"`
	// Installable: the update can be started from the panel. When false
	// although an update exists, InstallBlocker explains why.
	Installable    bool        `json:"installable"`
	InstallBlocker string      `json:"install_blocker"`
	LastUpdate     *lastUpdate `json:"last_update"`
}

func (m *Module) selfView() selfView {
	src := m.source()
	v := selfView{
		Installed: installedVersion(),
		Source:    m.deps.Settings.Get(KeySource),
		AutoCheck: m.deps.Settings.Bool(KeySelfAuto),
		Arch:      runtime.GOARCH,
	}
	v.LastUpdate = m.readLastUpdate()
	if src == nil {
		// Nothing is configured, so nothing has been checked.
		if v.Source != sourceNone {
			v.Error = &stateError{Code: "source_incomplete", Message: "Güncelleme kaynağı seçilmiş ancak adresi girilmemiş."}
		}
		return v
	}
	v.Configured = true
	switch s := src.(type) {
	case githubSource:
		v.SourceLabel = "github.com/" + s.repo
	case manifestSource:
		if u, err := url.Parse(s.url); err == nil {
			v.SourceLabel = u.Host
		}
	}
	m.mu.RLock()
	st, checking := m.self, m.selfChecking
	m.mu.RUnlock()
	v.Checking = checking
	if st.SourceID != src.ID() {
		return v // cached result belongs to another source
	}
	v.CheckedAt = timePtr(st.CheckedAt)
	v.Error = st.Error
	v.Latest = st.Latest
	v.UpdateAvailable = st.Latest != nil && st.Error == nil && isNewer(st.Latest.Version, v.Installed)
	if v.UpdateAvailable {
		asset, ok := st.Latest.Assets[v.Arch]
		switch {
		case !updatescheck.SupportedArch(v.Arch):
			v.InstallBlocker = "Bu sunucunun işlemci mimarisi (" + v.Arch + ") için MyServer sürümü yayınlanmıyor; güncelleme panelden yüklenemez."
		case !ok || !updatescheck.ValidUpdateURL(asset.URL):
			v.InstallBlocker = "Güncelleme kaynağı bu sunucunun işlemci mimarisi (" + v.Arch + ") için kurulum arşivi (" +
				updatescheck.ReleaseTarball(v.Arch) + ") yayınlamıyor; güncelleme panelden yüklenemez."
		case !updatescheck.ValidSHA256(asset.SHA256):
			v.Asset = &asset
			v.InstallBlocker = "Güncelleme kaynağı kurulum arşivinin SHA-256 özetini bildirmiyor. Doğrulanamayan bir sürüm panelden yüklenemez."
		default:
			v.Asset = &asset
			v.Installable = true
		}
	}
	return v
}

func (m *Module) handleSelf(w http.ResponseWriter, _ *http.Request) error {
	httpx.OK(w, m.selfView())
	return nil
}

func (m *Module) checkSelf(ctx context.Context) error {
	src := m.source()
	if src == nil {
		return httpx.NewError(http.StatusConflict, "source_not_configured",
			"Güncelleme kaynağı yapılandırılmamış. Ayarlar bölümünden bir kaynak tanımlayın.")
	}
	m.mu.Lock()
	if m.selfChecking {
		m.mu.Unlock()
		return httpx.Conflict("Sürüm denetimi zaten sürüyor.")
	}
	m.selfChecking = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.selfChecking = false
		m.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	rel, err := src.Latest(ctx)

	next := selfState{SourceID: src.ID(), CheckedAt: time.Now().Unix()}
	var failure *httpx.Error
	if err != nil {
		var se *sourceError
		if !errors.As(err, &se) {
			se = &sourceError{Code: "source_error", Message: "Sürüm bilgisi alınamadı.", Err: err}
		}
		slog.Warn("sürüm denetimi başarısız", "source", src.ID(), "error", err.Error())
		next.Error = &stateError{Code: se.Code, Message: se.Message}
		failure = httpx.NewError(http.StatusBadGateway, se.Code, se.Message).Wrap(err)
	} else {
		next.Latest = &rel
	}
	m.mu.Lock()
	m.self = next
	m.mu.Unlock()
	m.store.saveState(areaSelf, next.CheckedAt, next)
	if failure != nil {
		return failure
	}
	if isNewer(rel.Version, installedVersion()) {
		m.deps.Notify.PublishOnce(ctx, notify.Info, notifySource, "Güncelleme mevcut",
			fmt.Sprintf("MyServer %s sürümü yayınlandı (kurulu sürüm: %s).", rel.Version, installedVersion()),
			"updates.self."+rel.Version, 30*24*time.Hour)
	}
	return nil
}

func (m *Module) handleSelfCheck(w http.ResponseWriter, r *http.Request) error {
	err := m.checkSelf(r.Context())
	var he *httpx.Error
	if errors.As(err, &he) && he.Status == http.StatusConflict {
		return err
	}
	m.deps.Audit.Log(r.Context(), auth.ActorFrom(r), "updates.self_check", m.sourceID(), "", err == nil)
	if err != nil {
		return err
	}
	httpx.OK(w, m.selfView())
	return nil
}

func (m *Module) handleSelfApply(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Version string `json:"version"`
		Confirm bool   `json:"confirm"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	if !req.Confirm {
		return httpx.BadRequest("Güncelleme için açık onay gerekiyor.")
	}
	v := m.selfView()
	if !v.Configured {
		return httpx.NewError(http.StatusConflict, "source_not_configured", "Güncelleme kaynağı yapılandırılmamış.")
	}
	if v.Latest == nil || !v.UpdateAvailable {
		return httpx.Conflict("Yüklenecek yeni bir MyServer sürümü bulunmuyor.")
	}
	if !v.Installable || v.Asset == nil {
		return httpx.NewError(http.StatusConflict, "not_installable", v.InstallBlocker)
	}
	// Only the version found by the last check can be installed.
	if strings.TrimPrefix(strings.TrimSpace(req.Version), "v") != v.Latest.Version ||
		!updatescheck.ValidReleaseVersion(v.Latest.Version) {
		return httpx.BadRequest("İstenen sürüm, son denetimde bulunan sürümle eşleşmiyor. Yeniden denetleyin.")
	}
	if m.jobs.running(kindApt) != nil || unitActive(r.Context(), updatescheck.AptUnit) {
		return httpx.Conflict("Paket güncellemesi sürerken MyServer güncellenemez.")
	}
	if unitActive(r.Context(), updatescheck.SelfUnit) {
		return httpx.Conflict("Bir MyServer güncellemesi zaten çalışıyor.")
	}
	args := []string{v.Latest.Version, v.Asset.SHA256, v.Asset.URL}
	actor := auth.ActorFrom(r)
	target := installedVersion() + " -> " + v.Latest.Version
	if _, err := m.deps.Priv.Run(r.Context(), "updates-self", args...); err != nil {
		msg := privileged.UserMessage(err, "MyServer güncellemesi başlatılamadı.")
		m.deps.Audit.Log(r.Context(), actor, "updates.self_update", target, msg, false)
		m.deps.Notify.Publish(r.Context(), notify.Error, notifySource, "Güncelleme başarısız oldu", msg)
		return httpx.NewError(http.StatusBadGateway, "self_update_failed", msg).Wrap(err)
	}
	m.deps.Audit.Log(r.Context(), actor, "updates.self_update", target, "başlatıldı", true)
	httpx.JSON(w, http.StatusAccepted, map[string]any{"started": true, "version": v.Latest.Version})
	return nil
}

/* ---------- result of the last self-update ---------- */

// lastUpdate is what scripts/update.sh recorded in update.status.
type lastUpdate struct {
	State       string `json:"state"` // running | success | failed | rolled_back
	FromVersion string `json:"from_version"`
	ToVersion   string `json:"to_version"`
	Time        *int64 `json:"time"`
	Message     string `json:"message"`
}

func (m *Module) readLastUpdate() *lastUpdate {
	path := filepath.Join(m.deps.Cfg.DataDir, "update.status")
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	text, ok := readSmallFile(path, 16<<10)
	if !ok {
		return nil
	}
	kv := updatescheck.ParseKV(text)
	switch kv["state"] {
	case "running", "success", "failed", "rolled_back":
	default:
		return nil
	}
	version := func(v string) string {
		if !updatescheck.ValidReleaseVersion(v) {
			return ""
		}
		return strings.TrimPrefix(v, "v")
	}
	u := &lastUpdate{
		State:       kv["state"],
		FromVersion: version(kv["from_version"]),
		ToVersion:   version(kv["to_version"]),
		Message:     cleanText(kv["message"], 500),
	}
	if u.ToVersion == "" {
		u.ToVersion = version(kv["target"])
	}
	if t, err := strconv.ParseInt(kv["time"], 10, 64); err == nil && t > 0 {
		u.Time = &t
	}
	return u
}
