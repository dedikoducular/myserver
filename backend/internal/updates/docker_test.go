package updates

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func digest(c string) string { return "sha256:" + strings.Repeat(c, 64) }

type fakeImage struct {
	ref         string // what the container was created with
	container   string
	app         string
	imageID     string
	repoDigests []string
	// registry answer
	status int
	body   string
}

// add registers one container and everything the check asks about it.
func (f *fakeDocker) add(list *[]map[string]any, img fakeImage) {
	id := strings.Repeat(fmt.Sprintf("%02x", len(*list)+1), 32)
	labels := map[string]string{}
	if img.app != "" {
		labels[appLabel] = img.app
	}
	*list = append(*list, map[string]any{
		"Id": id, "Names": []string{"/" + img.container}, "Image": img.ref, "ImageID": img.imageID,
		"State": "running", "Labels": labels,
	})
	f.json("GET /containers/"+id+"/json", 200, mustJSON(map[string]any{
		"Id": id, "Image": img.imageID, "Config": map[string]any{"Image": img.ref},
	}))
	if img.imageID != "" {
		f.json("GET /images/"+img.imageID+"/json", 200, mustJSON(map[string]any{
			"Id": img.imageID, "RepoDigests": img.repoDigests, "RepoTags": []string{img.ref},
		}))
		// Asked for by tag to see whether the newer image is already here.
		f.json("GET /images/"+img.ref+"/json", 200, mustJSON(map[string]any{
			"Id": img.imageID, "RepoDigests": img.repoDigests, "RepoTags": []string{img.ref},
		}))
	}
	if img.status != 0 {
		f.json("GET /distribution/"+img.ref+"/json", img.status, img.body)
	}
	data := mustJSON(*list)
	f.json("GET /containers/json", 200, data)
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func descriptor(d string) string {
	return `{"Descriptor":{"mediaType":"application/vnd.oci.image.index.v1+json","digest":"` + d + `","size":10229},` +
		`"Platforms":[{"architecture":"amd64","os":"linux"}]}`
}

func daemonError(msg string) string { return mustJSON(map[string]string{"message": msg}) }

func byRef(list []imageStatus, ref string) *imageStatus {
	for i := range list {
		if list[i].Ref == ref {
			return &list[i]
		}
	}
	return nil
}

func mixedDocker(t *testing.T) (*fakeDocker, *[]map[string]any) {
	f := newFakeDocker(t)
	list := &[]map[string]any{}
	f.add(list, fakeImage{ref: "nginx:latest", container: "web", app: "nginx", imageID: digest("1"),
		repoDigests: []string{"nginx@" + digest("a")}, status: 200, body: descriptor(digest("a"))})
	f.add(list, fakeImage{ref: "ghcr.io/owner/app:1.4", container: "app", imageID: digest("2"),
		repoDigests: []string{"ghcr.io/owner/app@" + digest("b")}, status: 200, body: descriptor(digest("c"))})
	f.add(list, fakeImage{ref: "myapp:dev", container: "yerel", imageID: digest("3"), repoDigests: []string{}})
	f.add(list, fakeImage{ref: "postgres@" + digest("d"), container: "db", imageID: digest("4"),
		repoDigests: []string{"postgres@" + digest("d")}})
	f.add(list, fakeImage{ref: "registry.example.com/team/tool:2", container: "tool", imageID: digest("5"),
		repoDigests: []string{"registry.example.com/team/tool@" + digest("e")}, status: 500,
		body: daemonError("received unexpected HTTP status: 500 Internal Server Error")})
	f.add(list, fakeImage{ref: "redis:7", container: "cache", imageID: digest("6"),
		repoDigests: []string{"redis@" + digest("f")}, status: 429,
		body: daemonError("toomanyrequests: You have reached your unauthenticated pull rate limit. https://www.docker.com/increase-rate-limit")})
	f.add(list, fakeImage{ref: "private.example.com/secret/app:1", container: "ozel", imageID: digest("7"),
		repoDigests: []string{"private.example.com/secret/app@" + digest("0")}, status: 401,
		body: daemonError("unauthorized: authentication required")})
	f.add(list, fakeImage{ref: digest("8"), container: "kimlikli", imageID: digest("8"),
		repoDigests: []string{"busybox@" + digest("9")}})
	// A second container of the same image.
	f.add(list, fakeImage{ref: "nginx:latest", container: "web-2", app: "nginx", imageID: digest("1"),
		repoDigests: []string{"nginx@" + digest("a")}, status: 200, body: descriptor(digest("a"))})
	return f, list
}

func TestDockerCheck(t *testing.T) {
	f, _ := mixedDocker(t)
	e := newEnv(t, envOptions{dockerHost: f.host()})
	res := e.do("POST", "/updates/docker/check", "", "admin")
	if res.Status != 200 {
		t.Fatalf("status %d: %s", res.Status, res.Body)
	}
	var v dockerView
	res.into(t, &v)
	if v.Count != 1 || v.Unchecked != 6 || len(v.Images) != 8 || v.CheckedAt == nil || v.Error != nil {
		t.Errorf("count %d unchecked %d images %d error %+v", v.Count, v.Unchecked, len(v.Images), v.Error)
	}
	want := map[string][2]string{
		"nginx:latest":                     {imgCurrent, ""},
		"ghcr.io/owner/app:1.4":            {imgUpdate, ""},
		"myapp:dev":                        {imgUnchecked, "local_build"},
		"postgres@" + digest("d"):          {imgUnchecked, "pinned"},
		"registry.example.com/team/tool:2": {imgUnchecked, "registry_error"},
		"redis:7":                          {imgUnchecked, "rate_limited"},
		"private.example.com/secret/app:1": {imgUnchecked, "auth_required"},
		digest("8"):                        {imgUnchecked, "image_id"},
	}
	for ref, w := range want {
		img := byRef(v.Images, ref)
		if img == nil {
			t.Errorf("%s is missing", ref)
			continue
		}
		if img.Status != w[0] || img.ReasonCode != w[1] {
			t.Errorf("%s: status %q reason %q, want %q %q", ref, img.Status, img.ReasonCode, w[0], w[1])
		}
		if (img.Status == imgUnchecked) != (img.Reason != "") {
			t.Errorf("%s: reason %q", ref, img.Reason)
		}
		for _, leak := range []string{"HTTP status", "toomanyrequests", "unauthorized:", "docker.com"} {
			if strings.Contains(img.Reason, leak) {
				t.Errorf("%s: the raw registry error is shown: %q", ref, img.Reason)
			}
		}
	}
	// Updates first.
	if v.Images[0].Ref != "ghcr.io/owner/app:1.4" || v.Images[len(v.Images)-1].Ref != "nginx:latest" {
		t.Errorf("order: first %s, last %s", v.Images[0].Ref, v.Images[len(v.Images)-1].Ref)
	}
	up := byRef(v.Images, "ghcr.io/owner/app:1.4")
	if up.LocalDigest != digest("b") || up.RemoteDigest != digest("c") || up.Pulled || up.Tag != "1.4" || up.Name != "ghcr.io/owner/app" {
		t.Errorf("update = %+v", up)
	}
	cur := byRef(v.Images, "nginx:latest")
	if len(cur.Containers) != 2 || cur.Containers[0].Name != "web" || cur.Containers[1].Name != "web-2" ||
		cur.App != "nginx" || len(cur.Containers[0].ID) != 12 {
		t.Errorf("containers = %+v", cur.Containers)
	}
	if strings.Contains(res.Body, `"containers":null`) {
		t.Error("containers must be [] rather than null")
	}

	// The check only reads.
	for _, r := range f.seen() {
		if !strings.HasPrefix(r, "GET ") {
			t.Errorf("the check sent %s", r)
		}
	}
	// Pinned, local and id-only images are never looked up in a registry.
	for _, r := range f.seen() {
		for _, ref := range []string{"postgres", "myapp", digest("8")} {
			if strings.HasPrefix(r, "GET /distribution/"+ref) {
				t.Errorf("registry lookup for %s", ref)
			}
		}
	}

	if got, want := e.auditRows(), []auditRow{{"yonetici", "updates.docker_check", "", "", true}}; !reflect.DeepEqual(got, want) {
		t.Errorf("audit = %+v", got)
	}
	var s summaryView
	e.do("GET", "/updates/summary", "", "user").into(t, &s)
	if s.Docker.Count != 1 || s.Docker.Images != 8 || s.Docker.Unchecked != 6 || s.Total != 1 {
		t.Errorf("summary = %+v", s.Docker)
	}
	e.wantNoHelperCalls("docker check")
}

func TestDockerNotifiesOnce(t *testing.T) {
	f, list := mixedDocker(t)
	e := newEnv(t, envOptions{dockerHost: f.host()})
	for i := 0; i < 4; i++ {
		if err := e.mod.checkDocker(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	notes := e.notifications()
	if len(notes) != 1 || notes[0].Title != "Güncelleme mevcut" ||
		notes[0].Message != "1 Docker imajı için yeni sürüm mevcut: ghcr.io/owner/app:1.4" {
		t.Fatalf("notifications = %+v", notes)
	}
	// Another image gets an update.
	f.json("GET /distribution/nginx:latest/json", 200, descriptor(digest("e")))
	if err := e.mod.checkDocker(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.notificationTitles(); !reflect.DeepEqual(got, []string{"INFO Güncelleme mevcut", "INFO Güncelleme mevcut"}) {
		t.Errorf("notifications = %q", got)
	}
	// Everything up to date again: silence.
	f.json("GET /distribution/nginx:latest/json", 200, descriptor(digest("a")))
	f.json("GET /distribution/ghcr.io/owner/app:1.4/json", 200, descriptor(digest("b")))
	if err := e.mod.checkDocker(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(e.notifications()); n != 2 {
		t.Errorf("%d notifications", n)
	}
	_ = list
}

func TestDockerNoContainers(t *testing.T) {
	f := newFakeDocker(t)
	f.json("GET /containers/json", 200, "[]")
	e := newEnv(t, envOptions{dockerHost: f.host()})
	res := e.do("POST", "/updates/docker/check", "", "admin")
	if res.Status != 200 || !strings.Contains(res.Body, `"images":[]`) {
		t.Errorf("status %d: %s", res.Status, res.Body)
	}
	if n := len(e.notifications()); n != 0 {
		t.Errorf("%d notifications", n)
	}
}

func TestDockerUnavailable(t *testing.T) {
	e := newEnv(t) // nothing listens on the configured address
	e.mod.docker = dockerState{CheckedAt: 5, Images: []imageStatus{{Ref: "nginx:latest", Status: imgUpdate, RemoteDigest: digest("a")}}}
	res := e.do("POST", "/updates/docker/check", "", "admin")
	if res.Status != http.StatusServiceUnavailable || res.code() != "docker_unavailable" ||
		res.message() != "Docker servisine ulaşılamıyor." {
		t.Errorf("status %d code %q message %q", res.Status, res.code(), res.message())
	}
	if strings.Contains(res.Body, "127.0.0.1") || strings.Contains(res.Body, "connect") {
		t.Errorf("internal error leaked: %s", res.Body)
	}
	var v dockerView
	e.do("GET", "/updates/docker", "", "user").into(t, &v)
	if v.Error == nil || v.Error.Code != "docker_unavailable" || len(v.Images) != 1 {
		t.Errorf("view = %+v", v)
	}
	res = e.do("POST", "/updates/docker/pull", `{"image":"nginx:latest"}`, "admin")
	if res.Status != http.StatusServiceUnavailable || res.code() != "docker_unavailable" {
		t.Errorf("pull: status %d code %q", res.Status, res.code())
	}
	if e.mod.jobs.latest(kindDockerPull) != nil {
		t.Error("a pull job was created")
	}
	rows := e.auditRows()
	if len(rows) != 1 || rows[0].Success {
		t.Errorf("audit = %+v", rows)
	}
}

func TestDockerDaemonErrorIsNotARegistryProblem(t *testing.T) {
	f := newFakeDocker(t)
	f.json("GET /containers/json", 500, daemonError("daemon is shutting down"))
	e := newEnv(t, envOptions{dockerHost: f.host()})
	res := e.do("POST", "/updates/docker/check", "", "admin")
	if res.Status != http.StatusServiceUnavailable || res.code() != "docker_unavailable" {
		t.Errorf("status %d code %q", res.Status, res.code())
	}
}

func TestDockerCheckIsBounded(t *testing.T) {
	f := newFakeDocker(t)
	list := &[]map[string]any{}
	const images = 14
	var mu sync.Mutex
	inflight, peak, served := 0, 0, 0
	full := make(chan struct{})
	release := make(chan struct{})
	for i := 0; i < images; i++ {
		ref := fmt.Sprintf("app%02d:latest", i)
		f.add(list, fakeImage{ref: ref, container: fmt.Sprintf("c%02d", i), imageID: digest(fmt.Sprintf("%x", i)),
			repoDigests: []string{fmt.Sprintf("app%02d@%s", i, digest("a"))}})
		f.handle("GET /distribution/"+ref+"/json", func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			inflight++
			served++
			if inflight > peak {
				peak = inflight
			}
			if inflight == dockerWorkers && served == dockerWorkers {
				close(full)
			}
			mu.Unlock()
			<-release
			mu.Lock()
			inflight--
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, descriptor(digest("a")))
		})
	}
	e := newEnv(t, envOptions{dockerHost: f.host()})
	done := make(chan error, 1)
	go func() { done <- e.mod.checkDocker(context.Background()) }()
	select {
	case <-full:
	case err := <-done:
		t.Fatalf("the check ended before %d lookups were in flight: %v", dockerWorkers, err)
	case <-time.After(20 * time.Second):
		t.Fatalf("fewer than %d lookups run at once", dockerWorkers)
	}
	// Every lookup is held back, so a missing bound would show as more
	// lookups arriving now.
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	atOnce := inflight
	mu.Unlock()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if atOnce != dockerWorkers || peak != dockerWorkers {
		t.Errorf("%d lookups in flight (peak %d), want %d", atOnce, peak, dockerWorkers)
	}
	if served != images {
		t.Errorf("%d of %d images were looked up", served, images)
	}
	if v := e.mod.dockerView(); len(v.Images) != images || v.Unchecked != 0 || v.Count != 0 {
		t.Errorf("view: %d images, %d unchecked, %d updates", len(v.Images), v.Unchecked, v.Count)
	}
}

func TestDockerPullOnlyWhatTheCheckFound(t *testing.T) {
	f, _ := mixedDocker(t)
	e := newEnv(t, envOptions{dockerHost: f.host()})

	// Before any check nothing can be pulled.
	res := e.do("POST", "/updates/docker/pull", `{"image":"ghcr.io/owner/app:1.4"}`, "admin")
	if res.Status != http.StatusBadRequest {
		t.Errorf("pull before a check: status %d", res.Status)
	}
	if res := e.do("POST", "/updates/docker/check", "", "admin"); res.Status != 200 {
		t.Fatalf("check: %d %s", res.Status, res.Body)
	}
	before := len(f.seen())
	refused := []string{
		``, `{}`, `{"image":""}`, `{"image":"nginx:latest"}`, `{"image":"myapp:dev"}`, `{"image":"redis:7"}`,
		`{"image":"postgres@` + digest("d") + `"}`, `{"image":"private.example.com/secret/app:1"}`,
		`{"image":"ghcr.io/owner/app"}`, `{"image":"ghcr.io/owner/app:latest"}`, `{"image":"ghcr.io/owner/app:1.4 "}`,
		`{"image":"GHCR.IO/owner/app:1.4"}`, `{"image":"evil.example.com/owner/app:1.4"}`,
		`{"image":"ghcr.io/owner/app:1.4@` + digest("c") + `"}`, `{"image":"--all-tags"}`,
		`{"image":"ghcr.io/owner/app:1.4","all":true}`, `{"image":["ghcr.io/owner/app:1.4"]}`,
		`{"ref":"ghcr.io/owner/app:1.4"}`,
	}
	for _, body := range refused {
		res := e.do("POST", "/updates/docker/pull", body, "admin")
		if res.Status != http.StatusBadRequest {
			t.Errorf("body %q: status %d: %s", body, res.Status, res.Body)
		}
	}
	if got := f.seen()[before:]; len(got) != 0 {
		t.Errorf("refused pulls reached Docker: %q", got)
	}
	if e.mod.jobs.latest(kindDockerPull) != nil {
		t.Error("a refused pull created a job")
	}

	var query string
	f.handle("POST /images/create", func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"Pulling from owner/app","id":"1.4"}`+"\n"+
			`{"status":"Pulling fs layer","progressDetail":{},"id":"9f8b1c"}`+"\n"+
			`{"status":"Downloading","progressDetail":{"current":1,"total":10},"progress":"[>  ]","id":"9f8b1c"}`+"\n"+
			`{"status":"Downloading","progressDetail":{"current":9,"total":10},"progress":"[=> ]","id":"9f8b1c"}`+"\n"+
			`{"status":"Pull complete","progressDetail":{},"id":"9f8b1c"}`+"\n"+
			`{"status":"Digest: `+digest("c")+`"}`+"\n"+
			`{"status":"Status: Downloaded newer image for ghcr.io/owner/app:1.4\n\nevent: done"}`+"\n")
	})
	res = e.do("POST", "/updates/docker/pull", `{"image":"ghcr.io/owner/app:1.4"}`, "admin")
	if res.Status != http.StatusAccepted {
		t.Fatalf("status %d: %s", res.Status, res.Body)
	}
	var meta JobMeta
	res.into(t, &meta)
	job := e.mod.jobs.get(meta.ID)
	waitFor(t, "the pull to be audited", func() bool { return len(e.auditRows()) == 3 })
	if got := job.Meta(); got.Status != jobSuccess || got.Kind != kindDockerPull || got.Detail != "ghcr.io/owner/app:1.4" {
		t.Errorf("job = %+v", got)
	}
	if !strings.Contains(query, "fromImage=ghcr.io%2Fowner%2Fapp") || !strings.Contains(query, "tag=1.4") {
		t.Errorf("pull query = %q", query)
	}
	lines, _ := job.Snapshot(0)
	if len(lines) != 9 || lines[3] != "9f8b1c: Downloading" || lines[4] != "9f8b1c: Pull complete" {
		t.Errorf("lines = %q", lines)
	}
	if !strings.Contains(lines[len(lines)-2], "Çalışan konteynerler değiştirilmedi") {
		t.Errorf("lines = %q", lines)
	}
	var v dockerView
	e.do("GET", "/updates/docker", "", "user").into(t, &v)
	if img := byRef(v.Images, "ghcr.io/owner/app:1.4"); img == nil || !img.Pulled || img.Status != imgUpdate {
		t.Errorf("image = %+v", img)
	}
	// Pulling never touches a container.
	for _, r := range f.seen() {
		if !strings.HasPrefix(r, "GET ") && r != "POST /images/create" {
			t.Errorf("Docker received %s", r)
		}
	}
	e.wantNoHelperCalls("docker pull")
}

func TestDockerPullFailure(t *testing.T) {
	f, _ := mixedDocker(t)
	e := newEnv(t, envOptions{dockerHost: f.host()})
	if err := e.mod.checkDocker(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.handle("POST /images/create", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"Pulling from owner/app","id":"1.4"}`+"\n"+
			`{"errorDetail":{"message":"toomanyrequests: You have reached your pull rate limit."},"error":"toomanyrequests: You have reached your pull rate limit."}`+"\n")
	})
	res := e.do("POST", "/updates/docker/pull", `{"image":"ghcr.io/owner/app:1.4"}`, "admin")
	if res.Status != http.StatusAccepted {
		t.Fatalf("status %d: %s", res.Status, res.Body)
	}
	var meta JobMeta
	res.into(t, &meta)
	job := e.mod.jobs.get(meta.ID)
	waitFor(t, "the failure to be announced", func() bool { return len(e.notifications()) == 2 })
	got := job.Meta()
	if got.Status != jobFailed || !strings.Contains(got.Message, "istek sınırına ulaşıldı") {
		t.Errorf("job = %+v", got)
	}
	notes := e.notifications()
	if notes[1].Severity != "ERROR" || notes[1].Title != "Güncelleme başarısız oldu" ||
		strings.Contains(notes[1].Message, "toomanyrequests") {
		t.Errorf("notification = %+v", notes[1])
	}
	if img := byRef(e.mod.dockerView().Images, "ghcr.io/owner/app:1.4"); img.Pulled {
		t.Error("a failed pull is recorded as downloaded")
	}
	rows := e.auditRows()
	if last := rows[len(rows)-1]; last.Action != "updates.docker_pull" || last.Success {
		t.Errorf("audit = %+v", last)
	}
}

func TestSplitRef(t *testing.T) {
	cases := map[string][3]string{
		"nginx":                               {"nginx", "latest", ""},
		"nginx:1.27":                          {"nginx", "1.27", ""},
		"ghcr.io/owner/app:1.4":               {"ghcr.io/owner/app", "1.4", ""},
		"localhost:5000/app":                  {"localhost:5000/app", "latest", ""},
		"localhost:5000/app:2":                {"localhost:5000/app", "2", ""},
		"nginx@" + digest("a"):                {"nginx", "", digest("a")},
		"nginx:1.27@" + digest("a"):           {"nginx", "1.27", digest("a")},
		"localhost:5000/app@" + digest("a"):   {"localhost:5000/app", "", digest("a")},
		"registry.example.com:443/a/b/c:v1.0": {"registry.example.com:443/a/b/c", "v1.0", ""},
	}
	for ref, want := range cases {
		n, tg, d := splitRef(ref)
		if got := [3]string{n, tg, d}; got != want {
			t.Errorf("splitRef(%q) = %q, want %q", ref, got, want)
		}
	}
}
