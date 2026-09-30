package updates

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"myserver/internal/privileged"
)

const (
	jobRunning = "running"
	jobSuccess = "success"
	jobFailed  = "failed"

	kindApt        = "apt_upgrade"
	kindDockerPull = "docker_pull"

	maxJobLines   = 8000
	maxLineLength = 1000
	keepMemJobs   = 12
)

// JobMeta describes a job without its output.
type JobMeta struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	Detail     string `json:"detail"`
	Username   string `json:"username"`
	StartedAt  int64  `json:"started_at"`
	FinishedAt *int64 `json:"finished_at"`
	Status     string `json:"status"`
	Message    string `json:"message"`
}

// Job is a background operation whose output is kept and can be followed
// live by any number of listeners.
type Job struct {
	mu      sync.Mutex
	meta    JobMeta
	lines   []string
	partial []byte
	dropped bool
	subs    map[chan struct{}]struct{}
}

func newJob(kind, title, detail, username string) *Job {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return &Job{
		meta: JobMeta{ID: hex.EncodeToString(b), Kind: kind, Title: title, Detail: detail,
			Username: username, StartedAt: time.Now().Unix(), Status: jobRunning},
		subs: map[chan struct{}]struct{}{},
	}
}

// restoreJob rebuilds a running job from its stored description, after a
// panel restart.
func restoreJob(meta JobMeta) *Job {
	meta.Status, meta.FinishedAt, meta.Message = jobRunning, nil, ""
	return &Job{meta: meta, subs: map[chan struct{}]struct{}{}}
}

func (j *Job) Meta() JobMeta {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.meta
}

// Write implements io.Writer, splitting output into lines. Carriage returns
// (progress redraws) are treated as line ends.
func (j *Job) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, c := range p {
		if c == '\n' || c == '\r' {
			j.flushPartial()
			continue
		}
		if len(j.partial) < maxLineLength {
			j.partial = append(j.partial, c)
		}
	}
	j.wake()
	return len(p), nil
}

func (j *Job) flushPartial() {
	if len(j.partial) == 0 {
		return
	}
	line := cleanLine(string(j.partial))
	j.partial = j.partial[:0]
	if line == "" || strings.HasPrefix(line, privileged.UserMessagePrefix) {
		return
	}
	j.appendLine(line, false)
}

func (j *Job) appendLine(line string, force bool) {
	if len(j.lines) >= maxJobLines && !force {
		if !j.dropped {
			j.dropped = true
			j.lines = append(j.lines, "… çıktı çok uzun olduğu için devamı kaydedilmedi …")
		}
		return
	}
	j.lines = append(j.lines, line)
}

// Println adds a line written by the panel itself.
func (j *Job) Println(line string) {
	j.mu.Lock()
	j.flushPartial()
	if len(line) > maxLineLength {
		line = line[:maxLineLength]
	}
	j.appendLine(cleanLine(line), true)
	j.wake()
	j.mu.Unlock()
}

func (j *Job) wake() {
	for ch := range j.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Finish closes the job. A non-empty failure marks it failed.
func (j *Job) Finish(failure string) {
	j.mu.Lock()
	j.flushPartial()
	now := time.Now().Unix()
	j.meta.FinishedAt = &now
	if failure == "" {
		j.meta.Status = jobSuccess
		j.appendLine("İşlem başarıyla tamamlandı.", true)
	} else {
		j.meta.Status = jobFailed
		j.meta.Message = failure
		// One line in the output, whatever the message contains.
		j.appendLine("HATA: "+cleanLine(strings.Join(strings.Fields(failure), " ")), true)
	}
	j.wake()
	j.mu.Unlock()
}

// Snapshot returns the lines from index from on, and the current meta.
func (j *Job) Snapshot(from int) ([]string, JobMeta) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if from < 0 || from > len(j.lines) {
		from = 0
	}
	out := make([]string, len(j.lines)-from)
	copy(out, j.lines[from:])
	return out, j.meta
}

func (j *Job) Log() string {
	lines, _ := j.Snapshot(0)
	return strings.Join(lines, "\n")
}

func (j *Job) Subscribe() (<-chan struct{}, func()) {
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

// cleanLine removes terminal escape sequences and control characters.
func cleanLine(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "?")
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if r == 0x1b {
			// CSI sequence: ESC [ ... final byte in @-~
			if i < len(s) && s[i] == '[' {
				i++
				for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
					i++
				}
				if i < len(s) {
					i++
				}
			} else if i < len(s) {
				i++
			}
			continue
		}
		if r == '\t' {
			b.WriteByte(' ')
			continue
		}
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimRight(b.String(), " ")
}

// jobSet keeps recent jobs in memory.
type jobSet struct {
	mu    sync.Mutex
	jobs  map[string]*Job
	order []string
}

func (s *jobSet) add(j *Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jobs == nil {
		s.jobs = map[string]*Job{}
	}
	id := j.Meta().ID
	s.jobs[id] = j
	s.order = append(s.order, id)
	for len(s.order) > keepMemJobs {
		old := s.order[0]
		if o := s.jobs[old]; o != nil && o.Meta().Status == jobRunning {
			break
		}
		delete(s.jobs, old)
		s.order = s.order[1:]
	}
}

func (s *jobSet) get(id string) *Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.jobs[id]
}

// running returns the running job of a kind, if any.
func (s *jobSet) running(kind string) *Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if m := j.Meta(); m.Kind == kind && m.Status == jobRunning {
			return j
		}
	}
	return nil
}

// latest returns the most recently started job of a kind, if any.
func (s *jobSet) latest(kind string) *Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.order) - 1; i >= 0; i-- {
		if j := s.jobs[s.order[i]]; j != nil && j.Meta().Kind == kind {
			return j
		}
	}
	return nil
}
