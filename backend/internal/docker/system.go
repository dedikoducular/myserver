package docker

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"

	"myserver/internal/httpx"
	"myserver/internal/module"
)

// Info describes the Docker engine.
type Info struct {
	Version           string `json:"version"`
	APIVersion        string `json:"api_version"`
	StorageDriver     string `json:"storage_driver"`
	RootDir           string `json:"root_dir"`
	LoggingDriver     string `json:"logging_driver"`
	CgroupDriver      string `json:"cgroup_driver"`
	CgroupVersion     string `json:"cgroup_version"`
	OperatingSystem   string `json:"operating_system"`
	KernelVersion     string `json:"kernel_version"`
	Architecture      string `json:"architecture"`
	CPUs              int    `json:"cpus"`
	MemoryTotal       int64  `json:"memory_total"`
	Containers        int    `json:"containers"`
	ContainersRunning int    `json:"containers_running"`
	ContainersPaused  int    `json:"containers_paused"`
	ContainersStopped int    `json:"containers_stopped"`
	Images            int    `json:"images"`
}

func (m *Module) handleInfo(w http.ResponseWriter, r *http.Request) error {
	cli, err := m.client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), listTimeout)
	defer cancel()
	in, err := cli.Info(ctx)
	if err != nil {
		return apiError(err, errText{fallback: "Docker bilgileri alınamadı."})
	}
	httpx.OK(w, Info{
		Version:           in.ServerVersion,
		APIVersion:        cli.ClientVersion(),
		StorageDriver:     in.Driver,
		RootDir:           in.DockerRootDir,
		LoggingDriver:     in.LoggingDriver,
		CgroupDriver:      in.CgroupDriver,
		CgroupVersion:     in.CgroupVersion,
		OperatingSystem:   in.OperatingSystem,
		KernelVersion:     in.KernelVersion,
		Architecture:      in.Architecture,
		CPUs:              in.NCPU,
		MemoryTotal:       in.MemTotal,
		Containers:        in.Containers,
		ContainersRunning: in.ContainersRunning,
		ContainersPaused:  in.ContainersPaused,
		ContainersStopped: in.ContainersStopped,
		Images:            in.Images,
	})
	return nil
}

// socketMissing reports whether the configured unix socket does not exist,
// which means Docker is not installed or has never been started.
func (m *Module) socketMissing() bool {
	path, ok := strings.CutPrefix(m.deps.Cfg.DockerHost, "unix://")
	if !ok || path == "" {
		return false
	}
	_, err := os.Stat(path)
	return errors.Is(err, fs.ErrNotExist)
}

func joinNames(names []string, limit int) string {
	if len(names) > limit {
		return strings.Join(names[:limit], ", ") + " ve " + strconv.Itoa(len(names)-limit) + " diğer"
	}
	return strings.Join(names, ", ")
}

// Health implements module.HealthReporter. The server core caches the
// result, and the probe itself is two cheap API calls.
func (m *Module) Health(ctx context.Context) []module.HealthCheck {
	engine := module.HealthCheck{ID: "docker.engine", Name: "Docker", Status: module.Healthy, Message: "Docker çalışıyor."}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cli, err := m.client()
	if err == nil {
		_, err = cli.Ping(ctx)
	}
	if err != nil {
		if m.socketMissing() {
			engine.Status = module.WarningLevel
			engine.Message = "Docker kurulu değil veya hiç başlatılmamış."
		} else {
			engine.Status = module.CriticalLvl
			engine.Message = unavailableMessage
		}
		return []module.HealthCheck{engine}
	}

	checks := []module.HealthCheck{engine}
	list, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return append(checks, module.HealthCheck{
			ID: "docker.containers", Name: "Docker Konteynerleri",
			Status: module.WarningLevel, Message: "Konteyner durumu okunamadı.",
		})
	}
	var restarting, failed []string
	for _, c := range list {
		name := containerName(c.Names, c.ID)
		switch string(c.State) {
		case "restarting":
			restarting = append(restarting, name)
		case "exited", "dead":
			// Exit by SIGTERM/SIGKILL/SIGINT is what a deliberate stop
			// looks like, so it is not reported as a failure.
			if code, ok := exitCodeFromStatus(c.Status); ok && code != 0 && !signalExit(code) {
				failed = append(failed, name)
			} else if string(c.State) == "dead" {
				failed = append(failed, name)
			}
		}
	}
	cs := module.HealthCheck{
		ID: "docker.containers", Name: "Docker Konteynerleri",
		Status: module.Healthy, Message: "Konteynerlerde sorun yok.",
	}
	var parts []string
	if len(restarting) > 0 {
		parts = append(parts, "Sürekli yeniden başlıyor: "+joinNames(restarting, 3))
	}
	if len(failed) > 0 {
		parts = append(parts, "Hata koduyla durdu: "+joinNames(failed, 3))
	}
	if len(parts) > 0 {
		cs.Status = module.WarningLevel
		cs.Message = strings.Join(parts, ". ") + "."
	}
	return append(checks, cs)
}
