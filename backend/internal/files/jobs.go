package files

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"myserver/internal/audit"
	"myserver/internal/auth"
	"myserver/internal/httpx"
)

const (
	jobRunning   = "running"
	jobDone      = "done"
	jobFailed    = "failed"
	jobCancelled = "cancelled"

	maxRunningJobs = 4
	maxKeptJobs    = 50
	jobRetention   = 10 * time.Minute
)

// Job is one long-running file operation.
type Job struct {
	id      string
	kind    string
	title   string
	actor   audit.Actor
	started int64
	cancel  context.CancelFunc

	itemsTotal atomic.Int64
	itemsDone  atomic.Int64
	bytesTotal atomic.Int64
	bytesDone  atomic.Int64
	skipped    atomic.Int64

	mu       sync.Mutex
	status   string
	phase    string
	current  string
	message  string
	result   any
	finished int64
}

// JobView is the JSON form of a job.
type JobView struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	Owner      string `json:"owner"`
	Status     string `json:"status"`
	Phase      string `json:"phase"`
	ItemsTotal int64  `json:"items_total"`
	ItemsDone  int64  `json:"items_done"`
	BytesTotal int64  `json:"bytes_total"`
	BytesDone  int64  `json:"bytes_done"`
	Skipped    int64  `json:"skipped"`
	Current    string `json:"current"`
	Message    string `json:"message"`
	Result     any    `json:"result"`
	StartedAt  int64  `json:"started_at"`
	FinishedAt *int64 `json:"finished_at"`
}

func (j *Job) view() JobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	v := JobView{
		ID: j.id, Kind: j.kind, Title: j.title, Owner: j.actor.Username,
		Status: j.status, Phase: j.phase, Current: j.current, Message: j.message, Result: j.result,
		ItemsTotal: j.itemsTotal.Load(), ItemsDone: j.itemsDone.Load(),
		BytesTotal: j.bytesTotal.Load(), BytesDone: j.bytesDone.Load(),
		Skipped: j.skipped.Load(), StartedAt: j.started,
	}
	if j.finished != 0 {
		f := j.finished
		v.FinishedAt = &f
	}
	return v
}

func (j *Job) setPhase(phase string) {
	j.mu.Lock()
	j.phase = phase
	j.mu.Unlock()
}

func (j *Job) setCurrent(p string) {
	j.mu.Lock()
	j.current = p
	j.mu.Unlock()
}

// opError attaches the affected path to a failure inside a job.
type opError struct {
	path string
	msg  string // optional fixed Turkish message
	err  error
}

func (e *opError) Error() string {
	if e.err != nil {
		return e.path + ": " + e.err.Error()
	}
	return e.path + ": " + e.msg
}

func (e *opError) Unwrap() error { return e.err }

func opErr(p string, err error) error {
	var oe *opError
	if err == nil || errors.As(err, &oe) || errors.Is(err, context.Canceled) {
		return err
	}
	return &opError{path: p, err: err}
}

func opMsg(p, msg string) error { return &opError{path: p, msg: msg} }

func jobMessage(err error) string {
	var oe *opError
	if errors.As(err, &oe) {
		msg := oe.msg
		if msg == "" {
			_, _, msg = fsMessage(oe.err, "İşlem tamamlanamadı.")
		}
		if oe.path != "" {
			return msg + " (" + oe.path + ")"
		}
		return msg
	}
	var he *httpx.Error
	if errors.As(err, &he) {
		return he.Message
	}
	_, _, msg := fsMessage(err, "İşlem tamamlanamadı.")
	return msg
}

type jobManager struct {
	audit *audit.Logger
	mu    sync.Mutex
	jobs  map[string]*Job
}

func newJobManager(al *audit.Logger) *jobManager {
	return &jobManager{audit: al, jobs: map[string]*Job{}}
}

type jobSpec struct {
	kind   string
	title  string
	actor  audit.Actor
	action string // audit action; empty for read-only jobs
	target string // audit target
	// run does the work. cleanup always runs afterwards.
	run     func(ctx context.Context, j *Job) (any, error)
	cleanup func()
}

// start launches a job. cleanup is called even when the job is refused.
func (jm *jobManager) start(spec jobSpec) (*Job, error) {
	jm.mu.Lock()
	jm.pruneLocked()
	running := 0
	for _, j := range jm.jobs {
		j.mu.Lock()
		if j.status == jobRunning {
			running++
		}
		j.mu.Unlock()
	}
	if running >= maxRunningJobs {
		jm.mu.Unlock()
		if spec.cleanup != nil {
			spec.cleanup()
		}
		return nil, httpx.TooManyRequests("Aynı anda en fazla 4 dosya işlemi çalışabilir. Lütfen diğerlerinin bitmesini bekleyin.")
	}
	idb := make([]byte, 8)
	if _, err := rand.Read(idb); err != nil {
		jm.mu.Unlock()
		if spec.cleanup != nil {
			spec.cleanup()
		}
		return nil, httpx.Internal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := &Job{
		id: hex.EncodeToString(idb), kind: spec.kind, title: spec.title, actor: spec.actor,
		started: time.Now().Unix(), cancel: cancel, status: jobRunning, phase: "preparing",
	}
	jm.jobs[j.id] = j
	jm.mu.Unlock()

	go func() {
		var result any
		var err error
		func() {
			defer func() {
				if v := recover(); v != nil {
					slog.Error("dosya işlemi çöktü", "kind", spec.kind, "panic", fmt.Sprint(v))
					err = errors.New("panic")
				}
			}()
			result, err = spec.run(ctx, j)
		}()
		if spec.cleanup != nil {
			spec.cleanup()
		}
		cancelled := ctx.Err() != nil
		cancel()

		j.mu.Lock()
		j.finished = time.Now().Unix()
		j.current = ""
		j.result = result
		switch {
		case err != nil && (cancelled || errors.Is(err, context.Canceled)):
			j.status, j.message = jobCancelled, "İşlem iptal edildi."
		case err != nil:
			j.status, j.message = jobFailed, jobMessage(err)
			slog.Warn("dosya işlemi başarısız", "kind", spec.kind, "error", err.Error())
		default:
			j.status = jobDone
		}
		status, message := j.status, j.message
		j.mu.Unlock()

		if spec.action != "" && jm.audit != nil {
			detail := fmt.Sprintf("%d öğe", j.itemsDone.Load())
			if status != jobDone {
				detail = message
			}
			jm.audit.Log(context.Background(), spec.actor, spec.action, spec.target, truncate(detail, 300), status == jobDone)
		}
	}()
	return j, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

func (jm *jobManager) pruneLocked() {
	now := time.Now().Unix()
	var finished []*Job
	for id, j := range jm.jobs {
		j.mu.Lock()
		fin := j.finished
		j.mu.Unlock()
		if fin == 0 {
			continue
		}
		if now-fin > int64(jobRetention/time.Second) {
			delete(jm.jobs, id)
			continue
		}
		finished = append(finished, j)
	}
	if len(finished) > maxKeptJobs {
		sort.Slice(finished, func(a, b int) bool { return finished[a].finished < finished[b].finished })
		for _, j := range finished[:len(finished)-maxKeptJobs] {
			delete(jm.jobs, j.id)
		}
	}
}

func (jm *jobManager) get(id string) *Job {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return jm.jobs[id]
}

// views returns the jobs visible to a user: admins see all, others their own.
func (jm *jobManager) views(username string, admin bool) []JobView {
	jm.mu.Lock()
	jm.pruneLocked()
	list := make([]*Job, 0, len(jm.jobs))
	for _, j := range jm.jobs {
		if admin || j.actor.Username == username {
			list = append(list, j)
		}
	}
	jm.mu.Unlock()
	out := make([]JobView, 0, len(list))
	for _, j := range list {
		out = append(out, j.view())
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].StartedAt != out[b].StartedAt {
			return out[a].StartedAt > out[b].StartedAt
		}
		return out[a].ID < out[b].ID
	})
	return out
}

func (jm *jobManager) cancelAll() {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for _, j := range jm.jobs {
		j.cancel()
	}
}

func sessionUser(r *http.Request) (string, bool) {
	s := auth.From(r.Context())
	if s == nil {
		return "", false
	}
	return s.User.Username, s.User.Role == auth.RoleAdmin
}

func (m *Module) handleJobs(w http.ResponseWriter, r *http.Request) error {
	user, admin := sessionUser(r)
	httpx.OK(w, m.jobs.views(user, admin))
	return nil
}

func (m *Module) handleJobCancel(w http.ResponseWriter, r *http.Request) error {
	user, admin := sessionUser(r)
	j := m.jobs.get(r.PathValue("id"))
	if j == nil || (!admin && j.actor.Username != user) {
		return httpx.NotFound("İşlem bulunamadı.")
	}
	j.cancel()
	httpx.OK(w, j.view())
	return nil
}

// handleJobStream pushes the list of visible jobs over SSE whenever it
// changes. The list is small and only produced while a client listens.
func (m *Module) handleJobStream(w http.ResponseWriter, r *http.Request) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return httpx.Internal(errors.New("response writer does not support streaming"))
	}
	user, admin := sessionUser(r)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	var last []byte
	send := func() bool {
		b, err := json.Marshal(m.jobs.views(user, admin))
		if err != nil {
			return true
		}
		if bytes.Equal(b, last) {
			return true
		}
		last = b
		if _, err := fmt.Fprintf(w, "event: jobs\ndata: %s\n\n", b); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if !send() {
		return nil
	}
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return nil
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return nil
			}
			flusher.Flush()
		case <-tick.C:
			if !send() {
				return nil
			}
		}
	}
}
