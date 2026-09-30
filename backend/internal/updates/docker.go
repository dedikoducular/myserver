package updates

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"

	"myserver/internal/audit"
	"myserver/internal/auth"
	"myserver/internal/httpx"
	"myserver/internal/notify"
)

const (
	appLabel = "io.myserver.app"

	imgCurrent   = "current"
	imgUpdate    = "update_available"
	imgUnchecked = "unchecked"

	dockerWorkers      = 4
	dockerImageTimeout = 25 * time.Second
)

type containerRef struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
	App   string `json:"app"`
}

type imageStatus struct {
	Ref          string         `json:"ref"`
	Name         string         `json:"name"`
	Tag          string         `json:"tag"`
	Status       string         `json:"status"`
	ReasonCode   string         `json:"reason_code"`
	Reason       string         `json:"reason"`
	App          string         `json:"app"`
	Containers   []containerRef `json:"containers"`
	LocalDigest  string         `json:"local_digest"`
	RemoteDigest string         `json:"remote_digest"`
	// Pulled: the newer image is already downloaded, but the containers
	// still run the old one until they are recreated.
	Pulled bool `json:"pulled"`

	imageID string
}

type dockerState struct {
	Images    []imageStatus `json:"images"`
	CheckedAt int64         `json:"checked_at"`
	Error     *stateError   `json:"error"`
}

type dockerView struct {
	Images    []imageStatus `json:"images"`
	Count     int           `json:"count"`
	Unchecked int           `json:"unchecked"`
	CheckedAt *int64        `json:"checked_at"`
	Error     *stateError   `json:"error"`
	Checking  bool          `json:"checking"`
	AutoCheck bool          `json:"auto_check"`
	Job       *JobMeta      `json:"job"`
}

var errDockerUnavailable = httpx.Unavailable("docker_unavailable", "Docker servisine ulaşılamıyor.")

func (m *Module) dockerClient() (*client.Client, error) {
	m.dockerMu.Lock()
	defer m.dockerMu.Unlock()
	if m.dockerCli != nil {
		return m.dockerCli, nil
	}
	cli, err := client.NewClientWithOpts(client.WithHost(m.deps.Cfg.DockerHost), client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	m.dockerCli = cli
	return cli, nil
}

func (m *Module) dockerView() dockerView {
	m.mu.RLock()
	st, checking := m.docker, m.dockerChecking
	m.mu.RUnlock()
	v := dockerView{
		Images:    []imageStatus{},
		CheckedAt: timePtr(st.CheckedAt),
		Error:     st.Error,
		Checking:  checking,
		AutoCheck: m.deps.Settings.Bool(KeyDockerAuto),
	}
	for _, img := range st.Images {
		if img.Containers == nil {
			img.Containers = []containerRef{}
		}
		switch img.Status {
		case imgUpdate:
			v.Count++
		case imgUnchecked:
			v.Unchecked++
		}
		v.Images = append(v.Images, img)
	}
	if j := m.jobs.latest(kindDockerPull); j != nil {
		meta := j.Meta()
		v.Job = &meta
	}
	return v
}

func (m *Module) handleDocker(w http.ResponseWriter, _ *http.Request) error {
	httpx.OK(w, m.dockerView())
	return nil
}

var imageIDRe = regexp.MustCompile(`^(sha256:)?[0-9a-f]{12,64}$`)

// splitRef splits an image reference into repository, tag and digest
// without normalising it.
func splitRef(ref string) (name, tag, digest string) {
	name = ref
	if i := strings.Index(name, "@"); i >= 0 {
		name, digest = name[:i], name[i+1:]
	}
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name, tag = name[:i], name[i+1:]
	}
	if tag == "" && digest == "" {
		tag = "latest"
	}
	return name, tag, digest
}

var refRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,300}$`)

// classifyRegistryError turns a registry failure into a reason the user can
// act on. The raw error is never shown.
func classifyRegistryError(err error) (code, reason string) {
	s := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(s, "deadline exceeded") || strings.Contains(s, "timeout"):
		return "timeout", "Kayıt defteri zamanında yanıt vermedi."
	case strings.Contains(s, "toomanyrequests") || strings.Contains(s, "too many requests") || strings.Contains(s, "rate limit"):
		return "rate_limited", "Kayıt defteri istek sınırına ulaşıldı (Docker Hub). Daha sonra yeniden deneyin."
	case strings.Contains(s, "unauthorized") || strings.Contains(s, "denied") || strings.Contains(s, "authentication required") || strings.Contains(s, "forbidden"):
		return "auth_required", "Kayıt defteri kimlik doğrulaması istiyor veya imaj özel."
	case strings.Contains(s, "manifest unknown") || strings.Contains(s, "not found") || strings.Contains(s, "name unknown"):
		return "not_found", "Bu etiket kayıt defterinde bulunamadı."
	case strings.Contains(s, "no such host") || strings.Contains(s, "connection refused") || strings.Contains(s, "network is unreachable") || strings.Contains(s, "dial tcp") || strings.Contains(s, "i/o timeout"):
		return "registry_unreachable", "Kayıt defterine ulaşılamadı. İnternet bağlantısını kontrol edin."
	case strings.Contains(s, "certificate") || strings.Contains(s, "x509"):
		return "tls_error", "Kayıt defterinin güvenlik sertifikası doğrulanamadı."
	}
	return "registry_error", "Kayıt defteri sorgulanırken bir hata oluştu."
}

func digestOf(repoDigest string) string {
	if i := strings.LastIndex(repoDigest, "@"); i >= 0 {
		return repoDigest[i+1:]
	}
	return repoDigest
}

func (m *Module) checkDocker(ctx context.Context) error {
	m.mu.Lock()
	if m.dockerChecking {
		m.mu.Unlock()
		return httpx.Conflict("Docker imaj denetimi zaten sürüyor.")
	}
	m.dockerChecking = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.dockerChecking = false
		m.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()

	images, err := m.collectImages(ctx)
	m.mu.Lock()
	prev := m.docker
	next := dockerState{Images: prev.Images, CheckedAt: time.Now().Unix()}
	if err != nil {
		next.Error = &stateError{Code: errDockerUnavailable.Code, Message: errDockerUnavailable.Message}
	} else {
		next.Images = images
	}
	m.docker = next
	m.mu.Unlock()
	m.store.saveState(areaDocker, next.CheckedAt, next)
	if err != nil {
		slog.Warn("docker imajları denetlenemedi", "error", err.Error())
		return errDockerUnavailable.Wrap(err)
	}

	old := map[string]bool{}
	for _, img := range prev.Images {
		if img.Status == imgUpdate {
			old[img.Ref+"@"+img.RemoteDigest] = true
		}
	}
	var ids, names []string
	fresh := false
	for _, img := range images {
		if img.Status != imgUpdate {
			continue
		}
		id := img.Ref + "@" + img.RemoteDigest
		ids = append(ids, id)
		names = append(names, img.Ref)
		if !old[id] {
			fresh = true
		}
	}
	if fresh {
		msg := itoa(len(names)) + " Docker imajı için yeni sürüm mevcut: " + strings.Join(names, ", ")
		if len(msg) > 400 {
			msg = itoa(len(names)) + " Docker imajı için yeni sürüm mevcut."
		}
		m.deps.Notify.PublishOnce(ctx, notify.Info, notifySource, "Güncelleme mevcut", msg,
			digestKey("updates.docker.", ids), 7*24*time.Hour)
	}
	return nil
}

// collectImages lists the images used by containers and checks each one
// against its registry. An error means Docker itself is unreachable.
func (m *Module) collectImages(ctx context.Context) ([]imageStatus, error) {
	cli, err := m.dockerClient()
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	_, err = cli.Ping(pingCtx)
	cancel()
	if err != nil {
		return nil, err
	}
	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	list, err := cli.ContainerList(listCtx, container.ListOptions{All: true})
	cancel()
	if err != nil {
		return nil, err
	}

	byKey := map[string]*imageStatus{}
	order := []string{}
	for _, c := range list {
		ref, imageID := c.Image, c.ImageID
		// The list shows an image ID once the tag has moved; the container's
		// own configuration keeps the reference it was created with.
		inCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if info, err := cli.ContainerInspect(inCtx, c.ID); err == nil {
			if info.Config != nil && info.Config.Image != "" {
				ref = info.Config.Image
			}
			if info.Image != "" {
				imageID = info.Image
			}
		}
		cancel()
		if !refRe.MatchString(ref) {
			continue
		}
		key := ref + "|" + imageID
		img := byKey[key]
		if img == nil {
			img = &imageStatus{Ref: ref, imageID: imageID, Containers: []containerRef{}}
			img.Name, img.Tag, _ = splitRef(ref)
			byKey[key] = img
			order = append(order, key)
		}
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		id := c.ID
		if len(id) > 12 {
			id = id[:12]
		}
		app := c.Labels[appLabel]
		if len(app) > 100 {
			app = app[:100]
		}
		img.Containers = append(img.Containers, containerRef{ID: id, Name: name, State: string(c.State), App: app})
		if img.App == "" {
			img.App = app
		}
	}

	sem := make(chan struct{}, dockerWorkers)
	var wg sync.WaitGroup
	for _, key := range order {
		img := byKey[key]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			m.checkImage(ctx, cli, img)
		}()
	}
	wg.Wait()

	out := make([]imageStatus, 0, len(order))
	for _, key := range order {
		out = append(out, *byKey[key])
	}
	rank := map[string]int{imgUpdate: 0, imgUnchecked: 1, imgCurrent: 2}
	sort.SliceStable(out, func(i, j int) bool {
		if rank[out[i].Status] != rank[out[j].Status] {
			return rank[out[i].Status] < rank[out[j].Status]
		}
		return out[i].Ref < out[j].Ref
	})
	return out, nil
}

func (img *imageStatus) unchecked(code, reason string) {
	img.Status, img.ReasonCode, img.Reason = imgUnchecked, code, reason
}

func (m *Module) checkImage(ctx context.Context, cli *client.Client, img *imageStatus) {
	ctx, cancel := context.WithTimeout(ctx, dockerImageTimeout)
	defer cancel()

	_, _, digest := splitRef(img.Ref)
	switch {
	case imageIDRe.MatchString(img.Ref):
		img.unchecked("image_id", "Konteyner bir imaj kimliğiyle oluşturulmuş; izlenecek bir etiket yok.")
		return
	case digest != "":
		img.unchecked("pinned", "İmaj özet (digest) ile sabitlenmiş; bilinçli olarak belirli bir sürümde tutuluyor.")
		return
	}
	local, err := cli.ImageInspect(ctx, img.imageID)
	if err != nil {
		img.unchecked("image_missing", "Konteynerin imajı yerelde bulunamadı.")
		return
	}
	if len(local.RepoDigests) == 0 {
		img.unchecked("local_build", "İmaj bu sunucuda oluşturulmuş; bir kayıt defterinden gelmiyor.")
		return
	}
	img.LocalDigest = digestOf(local.RepoDigests[0])

	remote, err := cli.DistributionInspect(ctx, img.Ref, "")
	if err != nil {
		if client.IsErrConnectionFailed(err) {
			img.unchecked("docker_unavailable", errDockerUnavailable.Message)
			return
		}
		code, reason := classifyRegistryError(err)
		slog.Debug("imaj kayıt defterinde denetlenemedi", "image", img.Ref, "code", code, "error", err.Error())
		img.unchecked(code, reason)
		return
	}
	img.RemoteDigest = remote.Descriptor.Digest.String()
	if img.RemoteDigest == "" {
		img.unchecked("registry_error", "Kayıt defteri imaj özetini bildirmedi.")
		return
	}
	for _, rd := range local.RepoDigests {
		if digestOf(rd) == img.RemoteDigest {
			img.LocalDigest = img.RemoteDigest
			img.Status = imgCurrent
			return
		}
	}
	img.Status = imgUpdate
	// Has the newer image been downloaded already?
	if tagged, err := cli.ImageInspect(ctx, img.Ref); err == nil && tagged.ID != local.ID {
		for _, rd := range tagged.RepoDigests {
			if digestOf(rd) == img.RemoteDigest {
				img.Pulled = true
			}
		}
	}
}

func (m *Module) handleDockerCheck(w http.ResponseWriter, r *http.Request) error {
	err := m.checkDocker(r.Context())
	if isConflict(err) {
		return err
	}
	m.deps.Audit.Log(r.Context(), auth.ActorFrom(r), "updates.docker_check", "", "", err == nil)
	if err != nil {
		return err
	}
	httpx.OK(w, m.dockerView())
	return nil
}

func (m *Module) handleDockerPull(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Image string `json:"image"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	// Only images found by the last check may be pulled.
	m.mu.RLock()
	found := false
	for _, img := range m.docker.Images {
		if img.Ref == req.Image && img.Status == imgUpdate {
			found = true
		}
	}
	m.mu.RUnlock()
	if !found {
		return httpx.BadRequest("Bu imaj için indirilecek bir güncelleme bulunmuyor.")
	}
	cli, err := m.dockerClient()
	if err != nil {
		return errDockerUnavailable.Wrap(err)
	}
	pingCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	_, err = cli.Ping(pingCtx)
	cancel()
	if err != nil {
		return errDockerUnavailable.Wrap(err)
	}

	m.mu.Lock()
	if m.jobs.running(kindDockerPull) != nil {
		m.mu.Unlock()
		return httpx.Conflict("Zaten süren bir imaj indirme işlemi var.")
	}
	actor := auth.ActorFrom(r)
	job := newJob(kindDockerPull, "Docker imajı indirme", req.Image, actor.Username)
	m.jobs.add(job)
	m.mu.Unlock()

	m.store.saveJob(job.Meta(), "")
	m.deps.Audit.Log(r.Context(), actor, "updates.docker_pull", req.Image, "başlatıldı", true)
	go m.runPull(cli, job, actor, req.Image)
	httpx.JSON(w, http.StatusAccepted, job.Meta())
	return nil
}

type pullMessage struct {
	Status      string `json:"status"`
	ID          string `json:"id"`
	Error       string `json:"error"`
	ErrorDetail struct {
		Message string `json:"message"`
	} `json:"errorDetail"`
}

func (m *Module) runPull(cli *client.Client, job *Job, actor audit.Actor, ref string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	job.Println("İmaj indiriliyor: " + ref)

	failure := ""
	var cause error
	rc, err := cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		cause = err
	} else {
		cause = followPull(rc, job)
		_ = rc.Close()
	}
	if cause != nil {
		if client.IsErrConnectionFailed(cause) {
			failure = errDockerUnavailable.Message
		} else {
			_, failure = classifyRegistryError(cause)
			if failure == "Kayıt defteri sorgulanırken bir hata oluştu." {
				failure = "İmaj indirilemedi."
			}
		}
		slog.Error("imaj indirilemedi", "image", ref, "error", cause.Error())
	} else {
		job.Println("İmaj indirildi. Çalışan konteynerler değiştirilmedi; yeni imajı kullanmak için konteyner yeniden oluşturulmalıdır.")
		m.mu.Lock()
		for i := range m.docker.Images {
			if m.docker.Images[i].Ref == ref && m.docker.Images[i].Status == imgUpdate {
				m.docker.Images[i].Pulled = true
			}
		}
		st := m.docker
		m.mu.Unlock()
		m.store.saveState(areaDocker, st.CheckedAt, st)
	}
	m.finishJob(job, failure)
	m.deps.Audit.Log(ctx, actor, "updates.docker_pull", ref, failure, cause == nil)
	if cause != nil {
		m.deps.Notify.Publish(ctx, notify.Error, notifySource, "Güncelleme başarısız oldu",
			ref+" imajı indirilemedi: "+failure)
	}
}

// followPull reads Docker's progress stream, logging each layer's state
// changes (not every progress tick).
func followPull(r io.Reader, job *Job) error {
	dec := json.NewDecoder(io.LimitReader(r, 64<<20))
	last := map[string]string{}
	for {
		var msg pullMessage
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if msg.Error != "" || msg.ErrorDetail.Message != "" {
			text := msg.ErrorDetail.Message
			if text == "" {
				text = msg.Error
			}
			return errors.New(text)
		}
		if msg.Status == "" || last[msg.ID] == msg.Status {
			continue
		}
		if len(last) < 4096 {
			last[msg.ID] = msg.Status
		}
		if msg.ID != "" {
			job.Println(msg.ID + ": " + msg.Status)
		} else {
			job.Println(msg.Status)
		}
	}
}
