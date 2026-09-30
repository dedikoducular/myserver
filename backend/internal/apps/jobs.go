package apps

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"time"

	"myserver/internal/audit"
)

// Job kinds.
const (
	JobInstall  = "install"
	JobUpdate   = "update"
	JobSettings = "settings"
	JobRestore  = "restore"
)

// Job states.
const (
	JobRunning = "running"
	JobSuccess = "success"
	JobFailed  = "failed"
)

// Event is one progress record of a job.
type Event struct {
	Seq  int   `json:"seq"`
	Time int64 `json:"time"`
	// Type is "step" (a new phase), "progress" (image download), "log"
	// (detail line), "done" or "error".
	Type    string `json:"type"`
	Message string `json:"message"`
	Service string `json:"service,omitempty"`
	Image   string `json:"image,omitempty"`
	// Download progress in bytes; Total is 0 while unknown.
	Current int64 `json:"current,omitempty"`
	Total   int64 `json:"total,omitempty"`
}

const maxJobEvents = 2000

// Job is a long-running operation on one application. It runs in the panel
// process, independent of any browser connection.
type Job struct {
	ID    string
	Slug  string
	Name  string
	Kind  string
	Actor audit.Actor

	mu         sync.Mutex
	status     string
	errMsg     string
	startedAt  int64
	finishedAt int64
	events     []Event
	subs       map[chan struct{}]struct{}
}

// JobView is the API representation of a job.
type JobView struct {
	ID         string `json:"id"`
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Status     string `json:"status"`
	Error      string `json:"error"`
	StartedAt  int64  `json:"started_at"`
	FinishedAt int64  `json:"finished_at"`
	LastStep   string `json:"last_step"`
}

func (j *Job) view() JobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	v := JobView{
		ID: j.ID, Slug: j.Slug, Name: j.Name, Kind: j.Kind, Status: j.status, Error: j.errMsg,
		StartedAt: j.startedAt, FinishedAt: j.finishedAt,
	}
	for i := len(j.events) - 1; i >= 0; i-- {
		if j.events[i].Type == "step" {
			v.LastStep = j.events[i].Message
			break
		}
	}
	return v
}

func (j *Job) emit(e Event) {
	j.mu.Lock()
	if len(j.events) < maxJobEvents || e.Type == "done" || e.Type == "error" {
		e.Seq = len(j.events) + 1
		e.Time = time.Now().Unix()
		j.events = append(j.events, e)
	}
	for ch := range j.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	j.mu.Unlock()
}

// Step records the start of a phase.
func (j *Job) Step(msg string) { j.emit(Event{Type: "step", Message: msg}) }

// Log records a detail line.
func (j *Job) Log(msg string) { j.emit(Event{Type: "log", Message: msg}) }

func (j *Job) finish(errMsg string) {
	j.mu.Lock()
	j.finishedAt = time.Now().Unix()
	if errMsg == "" {
		j.status = JobSuccess
	} else {
		j.status = JobFailed
		j.errMsg = errMsg
	}
	j.mu.Unlock()
	if errMsg == "" {
		j.emit(Event{Type: "done", Message: "İşlem tamamlandı."})
	} else {
		j.emit(Event{Type: "error", Message: errMsg})
	}
}

// since returns the events after seq and whether the job has finished.
func (j *Job) since(seq int) ([]Event, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if seq < 0 {
		seq = 0
	}
	var out []Event
	if seq < len(j.events) {
		out = append(out, j.events[seq:]...)
	}
	return out, j.status != JobRunning
}

func (j *Job) subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	j.mu.Lock()
	j.subs[ch] = struct{}{}
	j.mu.Unlock()
	return ch, func() {
		j.mu.Lock()
		delete(j.subs, ch)
		j.mu.Unlock()
	}
}

type jobManager struct {
	mu   sync.Mutex
	jobs map[string]*Job
}

func newJobManager() *jobManager { return &jobManager{jobs: map[string]*Job{}} }

const finishedJobTTL = 2 * time.Hour

func (jm *jobManager) create(slug, name, kind string, actor audit.Actor) (*Job, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	j := &Job{
		ID: hex.EncodeToString(b), Slug: slug, Name: name, Kind: kind, Actor: actor,
		status: JobRunning, startedAt: time.Now().Unix(), subs: map[chan struct{}]struct{}{},
	}
	jm.mu.Lock()
	cutoff := time.Now().Add(-finishedJobTTL).Unix()
	for id, old := range jm.jobs {
		old.mu.Lock()
		expired := old.status != JobRunning && old.finishedAt < cutoff
		old.mu.Unlock()
		if expired {
			delete(jm.jobs, id)
		}
	}
	jm.jobs[j.ID] = j
	jm.mu.Unlock()
	return j, nil
}

func (jm *jobManager) get(id string) *Job {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return jm.jobs[id]
}

// list returns job views, newest first.
func (jm *jobManager) list() []JobView {
	jm.mu.Lock()
	jobs := make([]*Job, 0, len(jm.jobs))
	for _, j := range jm.jobs {
		jobs = append(jobs, j)
	}
	jm.mu.Unlock()
	out := make([]JobView, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, j.view())
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].StartedAt != out[b].StartedAt {
			return out[a].StartedAt > out[b].StartedAt
		}
		return out[a].ID > out[b].ID
	})
	return out
}

// running returns the running job of a slug, or nil.
func (jm *jobManager) running(slug string) *Job {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for _, j := range jm.jobs {
		j.mu.Lock()
		hit := j.Slug == slug && j.status == JobRunning
		j.mu.Unlock()
		if hit {
			return j
		}
	}
	return nil
}
