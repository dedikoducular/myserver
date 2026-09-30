package backup

// Docker operations: reading and writing the contents of named volumes
// through short-lived helper containers that are created but never started.

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"

	"myserver/internal/apps"
)

const (
	labelRole  = "io.myserver.role"
	roleHelper = "backup-helper"

	// helperMount is where the volume is mounted inside the helper. The
	// path does not exist in any image, so nothing is copied into an empty
	// volume and nothing of the image is read.
	helperMount  = "/.myserver-backup"
	helperPrefix = ".myserver-backup"
	// helperEntrypoint does not exist; the helper is never started.
	helperEntrypoint = "/.myserver-backup-helper-is-never-started"
)

// userError is a failure with a Turkish message for the user; Err is only
// logged.
type userError struct {
	Message string
	Err     error
}

func (e *userError) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

func (e *userError) Unwrap() error { return e.Err }

func userErr(msg string, err error) error { return &userError{Message: msg, Err: err} }

func dockerErr(err error, fallback string) error {
	if err == nil {
		return nil
	}
	var ue *userError
	if errors.As(err, &ue) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if client.IsErrConnectionFailed(err) {
		return userErr("Docker servisine ulaşılamıyor.", err)
	}
	low := strings.ToLower(err.Error())
	switch {
	case strings.Contains(low, "no space left on device"):
		return userErr("Diskte yeterli boş alan yok.", err)
	case strings.Contains(low, "volume is in use"):
		return userErr("Veri birimi başka bir konteyner tarafından kullanılıyor.", err)
	case strings.Contains(low, "permission denied"):
		return userErr("Docker servisine erişim izni yok.", err)
	}
	return userErr(fallback, err)
}

type engine struct {
	host string

	mu  sync.Mutex
	cli *client.Client
}

func (e *engine) client() (*client.Client, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cli != nil {
		return e.cli, nil
	}
	opts := []client.Opt{client.WithAPIVersionNegotiation()}
	if e.host != "" {
		opts = append(opts, client.WithHost(e.host))
	}
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, userErr("Docker servisine ulaşılamıyor.", err)
	}
	e.cli = cli
	return cli, nil
}

// ready returns the client after confirming that the daemon answers.
func (e *engine) ready(ctx context.Context) (*client.Client, error) {
	cli, err := e.client()
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	if _, err := cli.Ping(pctx); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, userErr("Docker servisine ulaşılamıyor.", err)
	}
	return cli, nil
}

func helperLabels() map[string]string {
	return map[string]string{apps.LabelManaged: "true", labelRole: roleHelper}
}

// createHelper creates (and does not start) a container that mounts the
// volume. The image must be present on the host.
func createHelper(ctx context.Context, cli *client.Client, img, vol string, readOnly bool) (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	res, err := cli.ContainerCreate(ctx,
		&container.Config{
			Image:           img,
			Entrypoint:      []string{helperEntrypoint},
			Cmd:             []string{},
			Labels:          helperLabels(),
			NetworkDisabled: true,
		},
		&container.HostConfig{
			NetworkMode: "none",
			Mounts: []mount.Mount{{
				Type: mount.TypeVolume, Source: vol, Target: helperMount, ReadOnly: readOnly,
				VolumeOptions: &mount.VolumeOptions{NoCopy: true},
			}},
		},
		nil, nil, "myserver-backup-helper-"+hex.EncodeToString(b))
	if err != nil {
		return "", dockerErr(err, "Yedekleme için yardımcı konteyner oluşturulamadı.")
	}
	return res.ID, nil
}

// removeHelper deletes a helper container together with the anonymous
// volumes its image may have declared. Named volumes are never removed by
// this call.
func removeHelper(cli *client.Client, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	err := cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true, RemoveVolumes: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		slog.Warn("yedekleme yardımcı konteyneri kaldırılamadı", "id", id, "error", err.Error())
	}
}

// removeLeftoverHelpers deletes helper containers left by a crashed run.
func (e *engine) removeLeftoverHelpers(ctx context.Context) {
	cli, err := e.ready(ctx)
	if err != nil {
		return
	}
	list, err := cli.ContainerList(ctx, container.ListOptions{All: true, Filters: filters.NewArgs(
		filters.Arg("label", apps.LabelManaged+"=true"),
		filters.Arg("label", labelRole+"="+roleHelper),
	)})
	if err != nil {
		slog.Warn("yedekleme yardımcı konteynerleri listelenemedi", "error", err.Error())
		return
	}
	for _, c := range list {
		if c.Labels[labelRole] != roleHelper {
			continue
		}
		slog.Info("yarım kalmış yedekleme yardımcı konteyneri kaldırılıyor", "id", c.ID)
		removeHelper(cli, c.ID)
	}
}

// volumeExists reports whether a named volume exists.
func volumeExists(ctx context.Context, cli *client.Client, name string) (bool, error) {
	_, err := cli.VolumeInspect(ctx, name)
	if err == nil {
		return true, nil
	}
	if cerrdefs.IsNotFound(err) {
		return false, nil
	}
	return false, dockerErr(err, "Veri birimi sorgulanamadı: "+name)
}

// volumeSizes asks Docker for the size of the volumes. It can take a while
// on large volumes and is only used for the free-space estimate; a failure
// returns an empty map.
func volumeSizes(ctx context.Context, cli *client.Client) map[string]int64 {
	out := map[string]int64{}
	dctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	du, err := cli.DiskUsage(dctx, types.DiskUsageOptions{Types: []types.DiskUsageObject{types.VolumeObject}})
	if err != nil {
		return out
	}
	for _, v := range du.Volumes {
		if v != nil && v.UsageData != nil && v.UsageData.Size >= 0 {
			out[v.Name] = v.UsageData.Size
		}
	}
	return out
}

// exportVolume writes the contents of a volume to dst as a normalized tar
// stream.
func exportVolume(ctx context.Context, cli *client.Client, img, vol string, dst io.Writer, progress func(int64)) (tarStats, error) {
	id, err := createHelper(ctx, cli, img, vol, true)
	if err != nil {
		return tarStats{}, err
	}
	defer removeHelper(cli, id)
	rc, _, err := cli.CopyFromContainer(ctx, id, helperMount)
	if err != nil {
		return tarStats{}, dockerErr(err, "Veri birimi okunamadı: "+vol)
	}
	defer rc.Close()
	tw := tar.NewWriter(dst)
	st, err := copyTar(ctx, tw, rc, helperPrefix, progress)
	if err != nil {
		return st, streamErr(err, "Veri birimi okunurken hata oluştu: "+vol)
	}
	if st.Items == 0 {
		return st, userErr("Docker, "+vol+" veri birimi için boş bir yanıt döndürdü.", nil)
	}
	if err := tw.Close(); err != nil {
		return st, err
	}
	return st, nil
}

// streamErr keeps errors that already carry their meaning and wraps others.
func streamErr(err error, fallback string) error {
	var ue *userError
	var ae *archiveError
	var se *unsafeEntryError
	switch {
	case errors.As(err, &ue), errors.As(err, &ae), errors.As(err, &se),
		errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, errEncCorrupt), errors.Is(err, errCorruptTar):
		return err
	}
	return dockerErr(err, fallback)
}

// importVolume extracts a validated tar stream into an (empty) volume.
func importVolume(ctx context.Context, cli *client.Client, img, vol string, src io.Reader, progress func(int64)) (tarStats, error) {
	id, err := createHelper(ctx, cli, img, vol, false)
	if err != nil {
		return tarStats{}, err
	}
	defer removeHelper(cli, id)

	pr, pw := io.Pipe()
	type result struct {
		st  tarStats
		err error
	}
	done := make(chan result, 1)
	go func() {
		tw := tar.NewWriter(pw)
		st, err := copyTar(ctx, tw, src, "", progress)
		if err == nil {
			err = tw.Close()
		}
		pw.CloseWithError(err)
		done <- result{st, err}
	}()
	cerr := cli.CopyToContainer(ctx, id, helperMount, pr, container.CopyToContainerOptions{})
	// Unblock the writer if Docker stopped reading early.
	pr.CloseWithError(errReaderGone)
	res := <-done
	switch {
	case res.err != nil && !errors.Is(res.err, errReaderGone):
		// The backup data itself was the problem.
		return res.st, streamErr(res.err, "Veri birimine yazılırken hata oluştu: "+vol)
	case cerr != nil:
		return res.st, dockerErr(cerr, "Veri birimine yazılamadı: "+vol)
	case res.err != nil:
		return res.st, userErr("Docker, "+vol+" veri biriminin verisini sonuna kadar okumadı.", res.err)
	}
	return res.st, nil
}

var errReaderGone = errors.New("backup: docker stopped reading")

// resetVolume replaces a volume by an empty one with the same name, driver
// and labels, so that files deleted since the backup do not linger. A volume
// that was not created by the panel for this application is refused.
// checkVolumeOwner refuses a volume that exists and was not created by the
// panel for this application. A restore calls it before it changes anything.
func checkVolumeOwner(ctx context.Context, cli *client.Client, name, slug string) error {
	ins, err := cli.VolumeInspect(ctx, name)
	switch {
	case err == nil:
		if ins.Labels[apps.LabelManaged] != "true" || ins.Labels[apps.LabelApp] != slug {
			return userErr("\""+name+"\" adında, bu panelin bu uygulama için oluşturmadığı bir veri birimi var. Üzerine yazılmadı.", nil)
		}
	case cerrdefs.IsNotFound(err):
	default:
		return dockerErr(err, "Veri birimi sorgulanamadı: "+name)
	}
	return nil
}

func resetVolume(ctx context.Context, cli *client.Client, name, slug string) error {
	opts := volume.CreateOptions{Name: name, Labels: map[string]string{apps.LabelApp: slug, apps.LabelManaged: "true"}}
	ins, err := cli.VolumeInspect(ctx, name)
	switch {
	case err == nil:
		if ins.Labels[apps.LabelManaged] != "true" || ins.Labels[apps.LabelApp] != slug {
			return userErr("\""+name+"\" adında, bu panelin bu uygulama için oluşturmadığı bir veri birimi var. Üzerine yazılmadı.", nil)
		}
		opts.Labels, opts.Driver, opts.DriverOpts = ins.Labels, ins.Driver, ins.Options
		if err := cli.VolumeRemove(ctx, name, false); err != nil {
			return dockerErr(err, "Veri birimi temizlenemedi: "+name)
		}
	case cerrdefs.IsNotFound(err):
	default:
		return dockerErr(err, "Veri birimi sorgulanamadı: "+name)
	}
	if _, err := cli.VolumeCreate(ctx, opts); err != nil {
		return dockerErr(err, "Veri birimi oluşturulamadı: "+name)
	}
	return nil
}

// removeAppContainers removes the containers of an application so that its
// volumes can be replaced. The application is recreated afterwards from the
// configuration stored in the backup.
func removeAppContainers(ctx context.Context, cli *client.Client, slug string) error {
	list, err := cli.ContainerList(ctx, container.ListOptions{All: true, Filters: filters.NewArgs(
		filters.Arg("label", apps.LabelManaged+"=true"),
		filters.Arg("label", apps.LabelApp+"="+slug),
	)})
	if err != nil {
		return dockerErr(err, "Konteyner listesi alınamadı.")
	}
	timeout := 30
	for _, c := range list {
		if c.Labels[apps.LabelApp] != slug || c.Labels[apps.LabelManaged] != "true" {
			continue
		}
		if c.State == container.StateRunning || c.State == container.StateRestarting || c.State == container.StatePaused {
			if err := cli.ContainerStop(ctx, c.ID, container.StopOptions{Timeout: &timeout}); err != nil && !cerrdefs.IsNotFound(err) {
				return dockerErr(err, "Uygulamanın konteyneri durdurulamadı.")
			}
		}
		if err := cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
			return dockerErr(err, "Uygulamanın konteyneri kaldırılamadı.")
		}
	}
	return nil
}

// ensureImage makes sure an image is present, pulling it when it is not.
// Needed when restoring an application that is not installed.
func ensureImage(ctx context.Context, cli *client.Client, ref string) error {
	if _, err := cli.ImageInspect(ctx, ref); err == nil {
		return nil
	} else if !cerrdefs.IsNotFound(err) {
		return dockerErr(err, "Görüntü sorgulanamadı: "+ref)
	}
	if !apps.ValidImage(ref) {
		return userErr("Yedekteki görüntü adı geçersiz: "+ref, nil)
	}
	rc, err := cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return dockerErr(err, "Uygulama görüntüsü indirilemedi: "+ref)
	}
	defer rc.Close()
	dec := json.NewDecoder(rc)
	for {
		var m struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return dockerErr(err, "Uygulama görüntüsü indirilirken bağlantı kesildi: "+ref)
		}
		if m.Error != "" {
			return userErr("Uygulama görüntüsü indirilemedi: "+ref, errors.New(m.Error))
		}
	}
	if _, err := cli.ImageInspect(ctx, ref); err != nil {
		return dockerErr(err, "Uygulama görüntüsü indirildi ancak bulunamadı: "+ref)
	}
	return nil
}
