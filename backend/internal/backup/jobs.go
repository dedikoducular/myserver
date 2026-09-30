package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"myserver/internal/audit"
)

// Job kinds.
const (
	jobBackup  = "backup"
	jobRestore = "restore"
	jobVerify  = "verify"
	jobImport  = "import"
)

const (
	maxJobLogs     = 200
	finishedJobTTL = 6 * time.Hour
)

// LogLine is one line of a job's log.
type LogLine struct {
	Time    int64  `json:"time"`
	Level   string `json:"level"` // info | warning | error
	Message string `json:"message"`
}

// Job is a long-running operation. It runs in the panel process and does
// not depend on any browser connection.
type Job struct {
	ID       string
	Kind     string
	Slug     string
	Name     string
	Trigger  string
	BackupID int64
	Actor    audit.Actor

	cancel context.CancelFunc
	mgr    *jobManager

	bytesDone atomic.Int64

	mu          sync.Mutex
	status      string
	step        string
	errMsg      string
	bytesTotal  int64
	cancellable bool
	cancelAsked bool
	startedAt   int64
	finishedAt  int64
	logs        []LogLine
	result      any
}

// JobView is the API representation of a job.
type JobView struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Trigger  string `json:"trigger"`
	BackupID int64  `json:"backup_id"`
	Status   string `json:"status"`
	Step     string `json:"step"`
	Error    string `json:"error"`
	// BytesDone counts the data processed so far; BytesTotal is an
	// estimate and 0 when unknown.
	BytesDone  int64 `json:"bytes_done"`
	BytesTotal int64 `json:"bytes_total"`
	// Cancellable is false once a restore started to overwrite data.
	Cancellable bool      `json:"cancellable"`
	CancelAsked bool      `json:"cancel_requested"`
	StartedAt   int64     `json:"started_at"`
	FinishedAt  int64     `json:"finished_at"`
	Logs        []LogLine `json:"logs"`
	Result      any       `json:"result"`
}

func (j *Job) view() JobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	logs := make([]LogLine, len(j.logs))
	copy(logs, j.logs)
	return JobView{
		ID: j.ID, Kind: j.Kind, Slug: j.Slug, Name: j.Name, Trigger: j.Trigger, BackupID: j.BackupID,
		Status: j.status, Step: j.step, Error: j.errMsg,
		BytesDone: j.bytesDone.Load(), BytesTotal: j.bytesTotal,
		Cancellable: j.cancellable && j.status == statusRunning, CancelAsked: j.cancelAsked,
		StartedAt: j.startedAt, FinishedAt: j.finishedAt, Logs: logs, Result: j.result,
	}
}

func (j *Job) appendLog(level, msg string) {
	j.mu.Lock()
	if len(j.logs) >= maxJobLogs {
		j.logs = j.logs[1:]
	}
	j.logs = append(j.logs, LogLine{Time: time.Now().Unix(), Level: level, Message: msg})
	j.mu.Unlock()
	j.mgr.notify()
}

// Step records the start of a phase.
func (j *Job) Step(msg string) {
	j.mu.Lock()
	j.step = msg
	j.mu.Unlock()
	j.appendLog("info", msg)
}

func (j *Job) Log(msg string)  { j.appendLog("info", msg) }
func (j *Job) Warn(msg string) { j.appendLog("warning", msg) }

// progress adds processed bytes. It is called for every block and therefore
// does not notify; streams pick the value up on their next tick.
func (j *Job) progress(n int64) { j.bytesDone.Add(n) }

func (j *Job) resetProgress(total int64) {
	j.bytesDone.Store(0)
	j.mu.Lock()
	j.bytesTotal = total
	j.mu.Unlock()
}

func (j *Job) setCancellable(v bool) {
	j.mu.Lock()
	j.cancellable = v
	j.mu.Unlock()
	j.mgr.notify()
}

func (j *Job) setSlug(slug, name string) {
	j.mu.Lock()
	j.Slug, j.Name = slug, name
	j.mu.Unlock()
}

func (j *Job) setResult(v any) {
	j.mu.Lock()
	j.result = v
	j.mu.Unlock()
}

func (j *Job) setBackupID(id int64) {
	j.mu.Lock()
	j.BackupID = id
	j.mu.Unlock()
}

// requestCancel cancels the job when that is still safe.
func (j *Job) requestCancel() bool {
	j.mu.Lock()
	ok := j.status == statusRunning && j.cancellable
	if ok {
		j.cancelAsked = true
	}
	j.mu.Unlock()
	if ok {
		j.cancel()
		j.mgr.notify()
	}
	return ok
}

func (j *Job) finish(status, errMsg string) {
	j.mu.Lock()
	j.status = status
	j.errMsg = errMsg
	j.finishedAt = time.Now().Unix()
	j.cancellable = false
	j.mu.Unlock()
	switch status {
	case statusSuccess:
		j.appendLog("info", "İşlem tamamlandı.")
	case statusCancelled:
		j.appendLog("warning", "İşlem iptal edildi.")
	default:
		j.appendLog("error", errMsg)
	}
}

type jobManager struct {
	mu    sync.Mutex
	jobs  map[string]*Job
	locks map[string]string // lock key -> job id
	subs  map[chan struct{}]struct{}
}

func newJobManager() *jobManager {
	return &jobManager{jobs: map[string]*Job{}, locks: map[string]string{}, subs: map[chan struct{}]struct{}{}}
}

func (jm *jobManager) notify() {
	jm.mu.Lock()
	for ch := range jm.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	jm.mu.Unlock()
}

func (jm *jobManager) subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	jm.mu.Lock()
	jm.subs[ch] = struct{}{}
	jm.mu.Unlock()
	return ch, func() {
		jm.mu.Lock()
		delete(jm.subs, ch)
		jm.mu.Unlock()
	}
}

// create registers a job and takes the lock named key. It returns nil when
// another job holds the lock.
func (jm *jobManager) create(parent context.Context, key string, j *Job) (context.Context, func(), bool) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return nil, nil, false
	}
	jm.mu.Lock()
	if _, busy := jm.locks[key]; busy {
		jm.mu.Unlock()
		return nil, nil, false
	}
	cutoff := time.Now().Add(-finishedJobTTL).Unix()
	for id, old := range jm.jobs {
		old.mu.Lock()
		expired := old.status != statusRunning && old.finishedAt < cutoff
		old.mu.Unlock()
		if expired {
			delete(jm.jobs, id)
		}
	}
	ctx, cancel := context.WithCancel(parent)
	j.ID = hex.EncodeToString(b)
	j.status = statusRunning
	j.startedAt = time.Now().Unix()
	j.cancel = cancel
	j.mgr = jm
	jm.jobs[j.ID] = j
	jm.locks[key] = j.ID
	jm.mu.Unlock()
	jm.notify()
	release := func() {
		cancel()
		jm.mu.Lock()
		if jm.locks[key] == j.ID {
			delete(jm.locks, key)
		}
		jm.mu.Unlock()
		jm.notify()
	}
	return ctx, release, true
}

func (jm *jobManager) get(id string) *Job {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return jm.jobs[id]
}

// busy returns the id of the job holding the lock, or "".
func (jm *jobManager) busy(key string) string {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return jm.locks[key]
}

// list returns job views, running jobs first, then newest first.
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
		ra, rb := out[a].Status == statusRunning, out[b].Status == statusRunning
		if ra != rb {
			return ra
		}
		if out[a].StartedAt != out[b].StartedAt {
			return out[a].StartedAt > out[b].StartedAt
		}
		return out[a].ID > out[b].ID
	})
	return out
}

func (jm *jobManager) anyRunning() bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return len(jm.locks) > 0
}
