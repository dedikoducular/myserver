package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"

	"myserver/internal/audit"
	"myserver/internal/auth"
	"myserver/internal/httpx"
)

// Image is one row of the image list.
type Image struct {
	ID         string   `json:"id"`
	ShortID    string   `json:"short_id"`
	Tags       []string `json:"tags"`
	Digests    []string `json:"digests"`
	Size       int64    `json:"size"`
	CreatedAt  int64    `json:"created_at"`
	Dangling   bool     `json:"dangling"`
	InUse      bool     `json:"in_use"`
	Containers []string `json:"containers"` // names of containers using the image
}

func realTags(tags []string) []string {
	out := []string{}
	for _, t := range tags {
		if t != "" && t != "<none>:<none>" {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

func realDigests(digests []string) []string {
	out := []string{}
	for _, d := range digests {
		if d != "" && d != "<none>@<none>" {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

func (m *Module) handleImages(w http.ResponseWriter, r *http.Request) error {
	cli, err := m.client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), listTimeout)
	defer cancel()
	text := errText{fallback: "İmaj listesi alınamadı."}
	raw, err := cli.ImageList(ctx, image.ListOptions{})
	if err != nil {
		return apiError(err, text)
	}
	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return apiError(err, text)
	}
	users := map[string][]string{}
	for _, c := range containers {
		users[c.ImageID] = append(users[c.ImageID], containerName(c.Names, c.ID))
	}
	out := make([]Image, 0, len(raw))
	for _, im := range raw {
		tags := realTags(im.RepoTags)
		used := users[im.ID]
		if used == nil {
			used = []string{}
		}
		sort.Strings(used)
		out = append(out, Image{
			ID:         im.ID,
			ShortID:    shortID(im.ID),
			Tags:       tags,
			Digests:    realDigests(im.RepoDigests),
			Size:       im.Size,
			CreatedAt:  im.Created,
			Dangling:   len(tags) == 0,
			InUse:      len(used) > 0,
			Containers: used,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Dangling != out[j].Dangling {
			return !out[i].Dangling
		}
		a, b := out[i].ShortID, out[j].ShortID
		if len(out[i].Tags) > 0 {
			a = out[i].Tags[0]
		}
		if len(out[j].Tags) > 0 {
			b = out[j].Tags[0]
		}
		return a < b
	})
	httpx.OK(w, out)
	return nil
}

func (m *Module) handleImageRemove(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	if !validImageID(id) {
		return httpx.BadRequest("İmaj kimliği geçersiz.")
	}
	force := false
	if v := r.URL.Query().Get("force"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return httpx.BadRequest("'force' değeri geçersiz.")
		}
		force = b
	}
	cli, err := m.client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), actionTimeout)
	defer cancel()
	detail := "force=" + strconv.FormatBool(force)
	res, err := cli.ImageRemove(ctx, id, image.RemoveOptions{Force: force, PruneChildren: true})
	if err != nil {
		m.deps.Audit.Log(ctx, auth.ActorFrom(r), "docker.image_remove", shortID(id), detail+" başarısız", false)
		return apiError(err, errText{
			notFound: "İmaj bulunamadı.",
			conflict: "İmaj bir konteyner tarafından kullanılıyor veya birden fazla etikete sahip.",
			fallback: "İmaj kaldırılamadı.",
		})
	}
	m.deps.Audit.Log(ctx, auth.ActorFrom(r), "docker.image_remove", shortID(id), detail, true)
	deleted, untagged := 0, 0
	for _, d := range res {
		if d.Deleted != "" {
			deleted++
		}
		if d.Untagged != "" {
			untagged++
		}
	}
	httpx.OK(w, map[string]int{"deleted": deleted, "untagged": untagged})
	return nil
}

// handleImagePrune removes dangling (untagged, unused) images only.
func (m *Module) handleImagePrune(w http.ResponseWriter, r *http.Request) error {
	cli, err := m.client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), actionTimeout)
	defer cancel()
	rep, err := cli.ImagesPrune(ctx, filters.NewArgs(filters.Arg("dangling", "true")))
	if err != nil {
		m.deps.Audit.Log(ctx, auth.ActorFrom(r), "docker.image_prune", "", "başarısız", false)
		return apiError(err, errText{
			conflict: "Şu anda başka bir temizlik işlemi sürüyor.",
			fallback: "Etiketsiz imajlar temizlenemedi.",
		})
	}
	deleted := 0
	for _, d := range rep.ImagesDeleted {
		if d.Deleted != "" {
			deleted++
		}
	}
	m.deps.Audit.Log(ctx, auth.ActorFrom(r), "docker.image_prune", "",
		strconv.Itoa(deleted)+" katman silindi", true)
	httpx.OK(w, map[string]any{"deleted": deleted, "space_reclaimed": rep.SpaceReclaimed})
	return nil
}

/* ---------- pull ---------- */

const (
	pullTimeout     = 30 * time.Minute
	pullMaxActive   = 2
	pullKeepDone    = 10 * time.Minute
	pullMinInterval = 300 * time.Millisecond
)

// PullLayer is the progress of one image layer.
type PullLayer struct {
	ID      string `json:"id"`
	Status  string `json:"status"` // Docker's status text, e.g. "Downloading"
	Current int64  `json:"current"`
	Total   int64  `json:"total"`
}

// PullState is the payload of the "progress" and "done" SSE events.
type PullState struct {
	JobID   string      `json:"job_id"`
	Image   string      `json:"image"`
	Status  string      `json:"status"` // last general status line
	Layers  []PullLayer `json:"layers"`
	Percent float64     `json:"percent"`
	Done    bool        `json:"done"`
	Success bool        `json:"success"`
	Message string      `json:"message"` // Turkish result message once done
}

type pullJob struct {
	id    string
	image string

	mu         sync.Mutex
	layers     map[string]*PullLayer
	order      []string
	status     string
	done       bool
	success    bool
	message    string
	finishedAt time.Time
	subs       map[chan struct{}]struct{}
}

func (j *pullJob) snapshot() PullState {
	j.mu.Lock()
	defer j.mu.Unlock()
	st := PullState{
		JobID: j.id, Image: j.image, Status: j.status,
		Layers: make([]PullLayer, 0, len(j.order)),
		Done:   j.done, Success: j.success, Message: j.message,
	}
	var cur, total int64
	finished := 0
	for _, id := range j.order {
		l := *j.layers[id]
		st.Layers = append(st.Layers, l)
		switch l.Status {
		case "Pull complete", "Already exists":
			finished++
		}
		if l.Total > 0 {
			total += l.Total
			cur += min(l.Current, l.Total)
		}
	}
	switch {
	case j.done && j.success:
		st.Percent = 100
	case len(j.order) > 0 && finished == len(j.order):
		st.Percent = 100
	case total > 0:
		// Layer sizes become known gradually, so this is an estimate.
		st.Percent = round1(float64(cur) / float64(total) * 100)
		if st.Percent > 99 {
			st.Percent = 99
		}
	}
	return st
}

func (j *pullJob) notify() {
	for ch := range j.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (j *pullJob) subscribe() (<-chan struct{}, func()) {
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

type pullManager struct {
	m    *Module
	mu   sync.Mutex
	jobs map[string]*pullJob
}

func newPullManager(m *Module) *pullManager {
	return &pullManager{m: m, jobs: map[string]*pullJob{}}
}

func (p *pullManager) get(id string) *pullJob {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.jobs[id]
}

// start registers a job, or returns the running job for the same image.
func (p *pullManager) start(ref string) (*pullJob, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	active := 0
	for id, j := range p.jobs {
		j.mu.Lock()
		done, finishedAt := j.done, j.finishedAt
		j.mu.Unlock()
		switch {
		case done && time.Since(finishedAt) > pullKeepDone:
			delete(p.jobs, id)
		case !done:
			if j.image == ref {
				return j, false, nil
			}
			active++
		}
	}
	if active >= pullMaxActive {
		return nil, false, httpx.Conflict("Aynı anda en fazla " + strconv.Itoa(pullMaxActive) +
			" imaj indirilebilir. Süren indirmelerin bitmesini bekleyin.")
	}
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return nil, false, httpx.Internal(err)
	}
	j := &pullJob{
		id: hex.EncodeToString(b), image: ref,
		layers: map[string]*PullLayer{},
		subs:   map[chan struct{}]struct{}{},
		status: "Bağlanıyor",
	}
	p.jobs[j.id] = j
	return j, true, nil
}

// pullMessage is one line of the Engine's pull progress stream.
type pullMessage struct {
	Status         string `json:"status"`
	ID             string `json:"id"`
	ProgressDetail struct {
		Current int64 `json:"current"`
		Total   int64 `json:"total"`
	} `json:"progressDetail"`
	Error       string `json:"error"`
	ErrorDetail struct {
		Message string `json:"message"`
	} `json:"errorDetail"`
}

// pullFailureMessage turns a registry/daemon failure into a Turkish message
// without exposing the raw error.
func pullFailureMessage(raw string) string {
	l := strings.ToLower(raw)
	switch {
	case strings.Contains(l, "manifest unknown"), strings.Contains(l, "not found"),
		strings.Contains(l, "repository does not exist"):
		return "İmaj veya etiket bulunamadı. Adı kontrol edin; depo özel de olabilir."
	case strings.Contains(l, "denied"), strings.Contains(l, "unauthorized"), strings.Contains(l, "authentication required"):
		return "İmaja erişim reddedildi. Depo özel olabilir."
	case strings.Contains(l, "no space left"):
		return "Diskte yer kalmadığı için imaj indirilemedi."
	case strings.Contains(l, "toomanyrequests"), strings.Contains(l, "rate limit"):
		return "Depo indirme sınırına ulaşıldı. Daha sonra tekrar deneyin."
	case strings.Contains(l, "no such host"), strings.Contains(l, "timeout"), strings.Contains(l, "connection"),
		strings.Contains(l, "network"), strings.Contains(l, "tls"), strings.Contains(l, "eof"):
		return "Depoya ulaşılamadı. Sunucunun internet bağlantısını kontrol edin."
	case strings.Contains(l, "no matching manifest"):
		return "Bu imajın sunucunun işlemci mimarisine uygun sürümü yok."
	}
	return "İmaj indirilemedi."
}

func (p *pullManager) run(j *pullJob, actor audit.Actor) {
	ctx, cancel := context.WithTimeout(p.m.ctx, pullTimeout)
	defer cancel()
	msg, err := p.pull(ctx, j)
	success := err == nil
	if !success {
		slog.Warn("imaj indirilemedi", "image", j.image, "error", err.Error())
	}
	j.mu.Lock()
	j.done, j.success, j.message, j.finishedAt = true, success, msg, time.Now()
	j.notify()
	j.mu.Unlock()
	detail := ""
	if !success {
		detail = "başarısız"
	}
	p.m.deps.Audit.Log(context.WithoutCancel(ctx), actor, "docker.image_pull", j.image, detail, success)
}

func (p *pullManager) pull(ctx context.Context, j *pullJob) (msg string, err error) {
	defer func() {
		if v := recover(); v != nil {
			slog.Error("imaj indirme işlemi çöktü", "panic", v)
			msg, err = "İmaj indirilemedi.", errors.New("panic during image pull")
		}
	}()
	cli, err := p.m.client()
	if err != nil {
		return unavailableMessage, err
	}
	rc, err := cli.ImagePull(ctx, j.image, image.PullOptions{})
	if err != nil {
		if isUnavailable(err) {
			return unavailableMessage, err
		}
		return pullFailureMessage(err.Error()), err
	}
	defer rc.Close()
	dec := json.NewDecoder(rc)
	for {
		var pm pullMessage
		if err := dec.Decode(&pm); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if ctx.Err() != nil {
				return "İmaj indirme zaman aşımına uğradı veya iptal edildi.", ctx.Err()
			}
			return "İmaj indirme bağlantısı kesildi.", err
		}
		if pm.Error != "" || pm.ErrorDetail.Message != "" {
			raw := pm.ErrorDetail.Message
			if raw == "" {
				raw = pm.Error
			}
			return pullFailureMessage(raw), errors.New(raw)
		}
		j.mu.Lock()
		if pm.ID != "" && pm.ProgressDetail.Total >= 0 && !strings.HasPrefix(pm.Status, "Pulling from") {
			l, ok := j.layers[pm.ID]
			if !ok {
				if len(j.order) < 512 {
					l = &PullLayer{ID: pm.ID}
					j.layers[pm.ID] = l
					j.order = append(j.order, pm.ID)
				}
			}
			if l != nil {
				l.Status = pm.Status
				if pm.ProgressDetail.Total > 0 {
					l.Current, l.Total = pm.ProgressDetail.Current, pm.ProgressDetail.Total
				}
			}
		} else if pm.Status != "" {
			j.status = pm.Status
		}
		j.notify()
		j.mu.Unlock()
	}
	return "İmaj indirildi.", nil
}

func (m *Module) handleImagePull(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Image string `json:"image"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	ref := strings.TrimSpace(req.Image)
	if !validImageRef(ref) {
		return httpx.BadRequest("İmaj adı geçersiz. Örnek: nginx:latest veya ghcr.io/kullanici/imaj:1.2")
	}
	// Fail now, with the right status, if Docker is down.
	cli, err := m.client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if _, err := cli.Ping(ctx); err != nil {
		return apiError(err, errText{fallback: unavailableMessage})
	}
	job, created, err := m.pulls.start(ref)
	if err != nil {
		return err
	}
	if created {
		go m.pulls.run(job, auth.ActorFrom(r))
	}
	httpx.JSON(w, http.StatusAccepted, map[string]string{"job_id": job.id, "image": job.image})
	return nil
}

// handlePullStream reports the progress of a pull job:
//
//	event "progress" → PullState (complete state, so late subscribers catch up)
//	event "done"     → PullState with done=true; the stream ends after it
func (m *Module) handlePullStream(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("job")
	if !validJobID(id) {
		return httpx.BadRequest("İndirme işi kimliği geçersiz.")
	}
	job := m.pulls.get(id)
	if job == nil {
		return httpx.NotFound("İndirme işi bulunamadı.")
	}
	sse, err := startSSE(w)
	if err != nil || sse == nil {
		return err
	}
	wake, unsub := job.subscribe()
	defer unsub()
	keepalive := time.NewTicker(sseKeepalive)
	defer keepalive.Stop()
	for {
		st := job.snapshot()
		if st.Done {
			_ = sse.event("done", st)
			return nil
		}
		if sse.event("progress", st) != nil {
			return nil
		}
		// Progress lines arrive many times a second; send at a calm rate.
		select {
		case <-r.Context().Done():
			return nil
		case <-time.After(pullMinInterval):
		}
	wait:
		for {
			select {
			case <-r.Context().Done():
				return nil
			case <-m.ctx.Done():
				return nil
			case <-keepalive.C:
				if sse.comment("ping") != nil {
					return nil
				}
			case <-wake:
				break wait
			}
		}
	}
}
