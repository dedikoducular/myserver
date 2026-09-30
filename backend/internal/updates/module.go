// Package updates reports and applies updates for the three areas the panel
// manages: Ubuntu packages, Docker images and MyServer itself. Checks are
// cached and scheduled; nothing is ever installed without an explicit
// request from an administrator.
package updates

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/client"

	"myserver/internal/auth"
	"myserver/internal/config"
	"myserver/internal/httpx"
	"myserver/internal/module"
	"myserver/internal/settings"
)

// Settings keys owned by the module.
const (
	KeyInterval     = "updates.check_interval_hours"
	KeyAptAuto      = "updates.apt_check_enabled"
	KeyDockerAuto   = "updates.docker_check_enabled"
	KeySelfAuto     = "updates.self_check_enabled"
	KeySource       = "updates.source"
	KeyGitHubRepo   = "updates.github_repo"
	KeyManifestURL  = "updates.manifest_url"
	sourceNone      = "none"
	sourceGitHub    = "github"
	sourceURL       = "url"
	notifySource    = "updates"
	schedulerPeriod = 10 * time.Minute
	schedulerDelay  = 3 * time.Minute
)

type stateError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Module struct {
	deps  module.Deps
	store *store
	jobs  jobSet

	mu             sync.RWMutex
	apt            aptState
	aptChecking    bool
	docker         dockerState
	dockerChecking bool
	self           selfState
	selfChecking   bool

	dockerMu  sync.Mutex
	dockerCli *client.Client
}

var githubRepoRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})/[A-Za-z0-9._-]{1,100}$`)

func validateRepo(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	if !githubRepoRe.MatchString(v) || strings.Contains(v, "..") {
		return "", httpx.BadRequest("GitHub deposu 'sahip/depo' biçiminde olmalıdır.")
	}
	return v, nil
}

func validateManifestURL(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	u, err := url.Parse(v)
	if err != nil || len(v) > 500 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return "", httpx.BadRequest("Güncelleme adresi 'https://' ile başlayan geçerli bir adres olmalıdır.")
	}
	return u.String(), nil
}

func New(deps module.Deps, st *settings.API) (module.Module, error) {
	settings.RegisterDefault(KeyInterval, "24")
	settings.RegisterDefault(KeyAptAuto, "true")
	settings.RegisterDefault(KeyDockerAuto, "true")
	settings.RegisterDefault(KeySelfAuto, "true")
	settings.RegisterDefault(KeySource, sourceGitHub)
	settings.RegisterDefault(KeyGitHubRepo, "dedikoducular/myserver")
	settings.RegisterDefault(KeyManifestURL, "")
	if st != nil {
		st.Allow(KeyInterval, settings.IntRange(1, 720), nil)
		st.Allow(KeyAptAuto, settings.Bool, nil)
		st.Allow(KeyDockerAuto, settings.Bool, nil)
		st.Allow(KeySelfAuto, settings.Bool, nil)
		st.Allow(KeySource, settings.OneOf(sourceNone, sourceGitHub, sourceURL), nil)
		st.Allow(KeyGitHubRepo, validateRepo, nil)
		st.Allow(KeyManifestURL, validateManifestURL, nil)
	}

	m := &Module{deps: deps, store: &store{db: deps.DB}}
	if deps.DB != nil {
		m.store.loadState(areaApt, &m.apt)
		m.store.loadState(areaDocker, &m.docker)
		m.store.loadState(areaSelf, &m.self)
		m.store.closeInterrupted(m.reattachAptJob())
	}
	return m, nil
}

func (m *Module) Name() string { return "updates" }

func (m *Module) Register(api, _ *httpx.Router) {
	g := api.Group("/updates")
	g.Get("/summary", m.handleSummary)
	g.Get("/apt", m.handleApt)
	g.Get("/docker", m.handleDocker)
	g.Get("/self", m.handleSelf)

	a := api.Group("/updates", auth.RequireAdmin)
	a.Post("/apt/check", m.handleAptCheck)
	a.Post("/apt/upgrade", m.handleAptUpgrade)
	a.Post("/reboot", m.handleReboot)
	a.Post("/docker/check", m.handleDockerCheck)
	a.Post("/docker/pull", m.handleDockerPull)
	a.Post("/self/check", m.handleSelfCheck)
	a.Post("/self/apply", m.handleSelfApply)
	a.Get("/jobs", m.handleJobs)
	a.Get("/jobs/{id}", m.handleJob)
	a.Get("/jobs/{id}/stream", m.handleJobStream)
}

func (m *Module) interval() time.Duration {
	return time.Duration(m.deps.Settings.Int(KeyInterval, 24)) * time.Hour
}

// Start runs the check schedule until ctx is cancelled.
func (m *Module) Start(ctx context.Context) {
	timer := time.NewTimer(schedulerDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		m.runSchedule(ctx)
		timer.Reset(schedulerPeriod)
	}
}

func (m *Module) runSchedule(ctx context.Context) {
	due := func(checkedAt int64) bool {
		return checkedAt == 0 || time.Since(time.Unix(checkedAt, 0)) >= m.interval()
	}
	m.mu.RLock()
	aptAt, dockerAt := m.apt.CheckedAt, m.docker.CheckedAt
	selfAt := m.self.CheckedAt
	if m.self.SourceID != m.sourceID() {
		selfAt = 0
	}
	m.mu.RUnlock()

	s := m.deps.Settings
	if s.Bool(KeyAptAuto) && due(aptAt) && ctx.Err() == nil {
		_ = m.checkApt(ctx)
	}
	if s.Bool(KeyDockerAuto) && due(dockerAt) && ctx.Err() == nil {
		_ = m.checkDocker(ctx)
	}
	if s.Bool(KeySelfAuto) && s.Get(KeySource) != sourceNone && due(selfAt) && ctx.Err() == nil {
		_ = m.checkSelf(ctx)
	}
}

/* ---------- summary ---------- */

type summaryArea struct {
	Count     int         `json:"count"`
	CheckedAt *int64      `json:"checked_at"`
	Checking  bool        `json:"checking"`
	Error     *stateError `json:"error"`
}

type summaryView struct {
	Total int `json:"total"`
	Apt   struct {
		summaryArea
		SecurityCount  int  `json:"security_count"`
		KernelCount    int  `json:"kernel_count"`
		RebootRequired bool `json:"reboot_required"`
		Running        bool `json:"running"`
	} `json:"apt"`
	Docker struct {
		summaryArea
		Images    int `json:"images"`
		Unchecked int `json:"unchecked"`
	} `json:"docker"`
	Self struct {
		summaryArea
		Installed  string `json:"installed_version"`
		Latest     string `json:"latest_version"`
		Configured bool   `json:"configured"`
	} `json:"self"`
}

func timePtr(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}

func (m *Module) handleSummary(w http.ResponseWriter, _ *http.Request) error {
	var v summaryView
	apt := m.aptView(false)
	v.Apt.Count, v.Apt.CheckedAt, v.Apt.Checking, v.Apt.Error = apt.Count, apt.CheckedAt, apt.Checking, apt.Error
	v.Apt.SecurityCount, v.Apt.KernelCount = apt.SecurityCount, apt.KernelCount
	v.Apt.RebootRequired = apt.RebootRequired
	v.Apt.Running = m.jobs.running(kindApt) != nil

	d := m.dockerView()
	v.Docker.Count, v.Docker.CheckedAt, v.Docker.Checking, v.Docker.Error = d.Count, d.CheckedAt, d.Checking, d.Error
	v.Docker.Images, v.Docker.Unchecked = len(d.Images), d.Unchecked

	s := m.selfView()
	if s.UpdateAvailable {
		v.Self.Count = 1
	}
	v.Self.CheckedAt, v.Self.Checking, v.Self.Error = s.CheckedAt, s.Checking, s.Error
	v.Self.Installed, v.Self.Configured = s.Installed, s.Configured
	if s.Latest != nil {
		v.Self.Latest = s.Latest.Version
	}
	v.Total = v.Apt.Count + v.Docker.Count + v.Self.Count
	httpx.OK(w, v)
	return nil
}

/* ---------- jobs ---------- */

func (m *Module) handleJobs(w http.ResponseWriter, r *http.Request) error {
	list, err := m.store.listJobs(r.Context())
	if err != nil {
		return httpx.Internal(err)
	}
	// Running jobs are reported from memory, which is always current.
	for i := range list {
		if j := m.jobs.get(list[i].ID); j != nil {
			list[i] = j.Meta()
		}
	}
	httpx.OK(w, list)
	return nil
}

type jobDetail struct {
	JobMeta
	Lines []string `json:"lines"`
}

var jobIDRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

func (m *Module) loadJob(r *http.Request) (*Job, *jobDetail, error) {
	id := r.PathValue("id")
	if !jobIDRe.MatchString(id) {
		return nil, nil, httpx.BadRequest("İşlem kimliği geçersiz.")
	}
	if j := m.jobs.get(id); j != nil {
		return j, nil, nil
	}
	meta, log, err := m.store.getJob(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, httpx.NotFound("İşlem kaydı bulunamadı.")
	}
	if err != nil {
		return nil, nil, httpx.Internal(err)
	}
	d := &jobDetail{JobMeta: meta, Lines: []string{}}
	if log != "" {
		d.Lines = strings.Split(log, "\n")
	}
	return nil, d, nil
}

func (m *Module) handleJob(w http.ResponseWriter, r *http.Request) error {
	j, d, err := m.loadJob(r)
	if err != nil {
		return err
	}
	if j != nil {
		lines, meta := j.Snapshot(0)
		d = &jobDetail{JobMeta: meta, Lines: lines}
	}
	httpx.OK(w, d)
	return nil
}

type linesEvent struct {
	From  int      `json:"from"`
	Lines []string `json:"lines"`
}

// handleJobStream follows a job's output over SSE. Events: "start" (job
// meta; the client clears its buffer when from is 0), "lines" and "done".
func (m *Module) handleJobStream(w http.ResponseWriter, r *http.Request) error {
	j, d, err := m.loadJob(r)
	if err != nil {
		return err
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		return httpx.Internal(fmt.Errorf("response writer does not support streaming"))
	}
	from := 0
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			from = n
		}
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(event string, id int, v any) bool {
		b, err := json.Marshal(v)
		if err != nil {
			return true
		}
		if id >= 0 {
			_, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", id, event, b)
		} else {
			_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		}
		flusher.Flush()
		return err == nil
	}

	if j == nil { // finished job from history
		if from > len(d.Lines) {
			from = 0
		}
		send("start", -1, map[string]any{"job": d.JobMeta, "from": from})
		send("lines", len(d.Lines), linesEvent{From: from, Lines: d.Lines[from:]})
		send("done", -1, d.JobMeta)
		return nil
	}

	wakeup, cancel := j.Subscribe()
	defer cancel()
	// A position beyond the output (a stale Last-Event-ID) starts over; the
	// "start" event must name the position the lines really begin at.
	if all, _ := j.Snapshot(0); from > len(all) {
		from = 0
	}
	lines, meta := j.Snapshot(from)
	if !send("start", -1, map[string]any{"job": meta, "from": from}) {
		return nil
	}
	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	for {
		if len(lines) > 0 {
			if !send("lines", from+len(lines), linesEvent{From: from, Lines: lines}) {
				return nil
			}
			from += len(lines)
		}
		if meta.Status != jobRunning {
			send("done", -1, meta)
			return nil
		}
		select {
		case <-r.Context().Done():
			return nil
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return nil
			}
			flusher.Flush()
		case <-wakeup:
		}
		lines, meta = j.Snapshot(from)
	}
}

// finishJob closes a job and stores it in the history.
func (m *Module) finishJob(j *Job, failure string) JobMeta {
	j.Finish(failure)
	meta := j.Meta()
	m.store.saveJob(meta, j.Log())
	return meta
}

func installedVersion() string { return config.Version }
