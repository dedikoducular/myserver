package docker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"time"

	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"

	"myserver/internal/notify"
)

const (
	eventsBackoffMin  = 2 * time.Second
	eventsBackoffMax  = 60 * time.Second
	eventsHealthyTime = 30 * time.Second
	signalWindow      = 30 * time.Second
	crashDedupeWindow = 10 * time.Minute
)

// watchEvents follows the Docker event stream until ctx is cancelled. When
// the stream drops (Docker stopped or restarted) it reconnects with
// exponential backoff, so it never spins.
func (m *Module) watchEvents(ctx context.Context) {
	backoff := eventsBackoffMin
	wasDown := false
	for ctx.Err() == nil {
		connected := time.Now()
		err := m.followEvents(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(connected) >= eventsHealthyTime {
			backoff = eventsBackoffMin
			wasDown = false
		}
		if !wasDown {
			// Logged once per outage, not on every retry.
			cause := "akış kapandı"
			if err != nil {
				cause = err.Error()
			}
			slog.Warn("docker olay akışı kesildi, yeniden bağlanılacak", "error", cause)
			wasDown = true
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, eventsBackoffMax)
	}
}

func (m *Module) followEvents(ctx context.Context) (err error) {
	defer func() {
		if v := recover(); v != nil {
			slog.Error("docker olay izleyicisi çöktü", "panic", v)
			err = errors.New("panic in event watcher")
		}
	}()
	cli, err := m.client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	msgs, errs := cli.Events(ctx, events.ListOptions{
		Filters: filters.NewArgs(
			filters.Arg("type", "container"),
			filters.Arg("type", "image"),
			filters.Arg("type", "volume"),
			filters.Arg("type", "network"),
		),
	})
	// Containers that received a signal through the API (docker stop/kill,
	// from the panel or from the command line) or were killed for memory.
	signalled := newRecentSet(signalWindow)
	oom := newRecentSet(signalWindow)
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errs:
			if err == nil || errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case msg, ok := <-msgs:
			if !ok {
				return nil
			}
			m.handleEvent(ctx, msg, signalled, oom)
		}
	}
}

// relevant lists the actions after which a list in the UI is out of date.
var relevant = map[string]map[string]bool{
	"container": {
		"create": true, "start": true, "die": true, "stop": true, "destroy": true, "pause": true,
		"unpause": true, "rename": true, "restart": true, "kill": true, "oom": true, "update": true,
	},
	"image":   {"pull": true, "delete": true, "untag": true, "tag": true, "import": true, "load": true, "prune": true},
	"volume":  {"create": true, "destroy": true, "prune": true},
	"network": {"create": true, "destroy": true, "connect": true, "disconnect": true, "prune": true},
}

func (m *Module) handleEvent(ctx context.Context, msg events.Message, signalled, oom *recentSet) {
	kind, action, id := string(msg.Type), string(msg.Action), msg.Actor.ID
	if !relevant[kind][action] {
		return // exec_*, health_status, top, ... are frequent and irrelevant
	}
	m.changes.publish(changeEvent{Kind: kind, Action: action, ID: id})
	if kind != "container" {
		return
	}
	m.started.drop(id)
	switch action {
	case "kill":
		signalled.mark(id)
	case "oom":
		oom.mark(id)
	case "die":
		code, err := strconv.Atoi(msg.Actor.Attributes["exitCode"])
		if err != nil || code == 0 {
			return
		}
		outOfMemory := oom.has(id)
		if !outOfMemory && (m.expected.has(id) || signalled.has(id)) {
			return // somebody stopped it on purpose
		}
		name := msg.Actor.Attributes["name"]
		if name == "" || !validContainerID(name) {
			name = shortID(id)
		}
		text := "\"" + name + "\" konteyneri beklenmedik şekilde durdu (çıkış kodu " + strconv.Itoa(code) + ")."
		if outOfMemory {
			text = "\"" + name + "\" konteyneri bellek yetersizliği nedeniyle sonlandırıldı (çıkış kodu " + strconv.Itoa(code) + ")."
		}
		slog.Warn("konteyner çöktü", "container", name, "exit_code", code, "oom", outOfMemory)
		m.deps.Notify.PublishOnce(ctx, notify.Error, "docker", "Container çöktü", text,
			"docker.crash."+id, crashDedupeWindow)
	}
}
