package docker

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/events"

	"myserver/internal/notify"
)

func containerEvent(action, id, name string, attrs map[string]string) events.Message {
	a := map[string]string{"name": name, "image": "nginx:latest"}
	for k, v := range attrs {
		a[k] = v
	}
	return events.Message{
		Type:   events.ContainerEventType,
		Action: events.Action(action),
		Actor:  events.Actor{ID: id, Attributes: a},
	}
}

func dieEvent(id, name string, code int) events.Message {
	return containerEvent("die", id, name, map[string]string{"exitCode": strconv.Itoa(code)})
}

func killEvent(id, name string, signal int) events.Message {
	return containerEvent("kill", id, name, map[string]string{"signal": strconv.Itoa(signal)})
}

// watcher holds the per-connection state followEvents keeps.
type watcher struct {
	e         *testEnv
	signalled *recentSet
	oom       *recentSet
}

func newWatcher(t *testing.T) *watcher {
	e := newTestEnv(t, "unix:///nonexistent/docker.sock")
	return &watcher{e: e, signalled: newRecentSet(signalWindow), oom: newRecentSet(signalWindow)}
}

func (w *watcher) feed(msgs ...events.Message) {
	for _, m := range msgs {
		w.e.mod.handleEvent(context.Background(), m, w.signalled, w.oom)
	}
}

func TestCrashDetection(t *testing.T) {
	const crashed = "beklenmedik şekilde durdu"
	const oomText = "bellek yetersizliği"
	cases := []struct {
		name     string
		events   []events.Message
		expected bool   // the panel itself stopped the container
		want     string // fragment of the notification, "" for none
		code     int
	}{
		{name: "exit 0 is a normal end", events: []events.Message{dieEvent(idWeb, "web", 0)}},
		{name: "exit 0 after stop", events: []events.Message{killEvent(idWeb, "web", 15), dieEvent(idWeb, "web", 0)}},
		{name: "exit 1 is a crash", events: []events.Message{dieEvent(idWeb, "web", 1)}, want: crashed, code: 1},
		{name: "exit 137 without a kill request is a crash", events: []events.Message{dieEvent(idWeb, "web", 137)}, want: crashed, code: 137},
		{name: "exit 143 without a kill request is a crash", events: []events.Message{dieEvent(idWeb, "web", 143)}, want: crashed, code: 143},
		{name: "exit 137 after docker kill is deliberate", events: []events.Message{killEvent(idWeb, "web", 9), dieEvent(idWeb, "web", 137)}},
		{name: "exit 143 after docker stop is deliberate", events: []events.Message{killEvent(idWeb, "web", 15), dieEvent(idWeb, "web", 143)}},
		{name: "exit 1 after docker stop is deliberate", events: []events.Message{killEvent(idWeb, "web", 15), dieEvent(idWeb, "web", 1)}},
		{name: "stop then forced kill", events: []events.Message{killEvent(idWeb, "web", 15), killEvent(idWeb, "web", 9), dieEvent(idWeb, "web", 137)}},
		{name: "a kill of another container does not excuse this one", events: []events.Message{killEvent(idDB, "db", 9), dieEvent(idWeb, "web", 137)}, want: crashed, code: 137},
		{name: "exit 137 after a panel action is deliberate", events: []events.Message{dieEvent(idWeb, "web", 137)}, expected: true},
		{name: "exit 1 after a panel action is deliberate", events: []events.Message{dieEvent(idWeb, "web", 1)}, expected: true},
		{name: "out of memory", events: []events.Message{containerEvent("oom", idWeb, "web", nil), dieEvent(idWeb, "web", 137)}, want: oomText, code: 137},
		{name: "out of memory is reported even with a kill event", events: []events.Message{containerEvent("oom", idWeb, "web", nil), killEvent(idWeb, "web", 9), dieEvent(idWeb, "web", 137)}, want: oomText, code: 137},
		{name: "out of memory is reported even after a panel action", events: []events.Message{containerEvent("oom", idWeb, "web", nil), dieEvent(idWeb, "web", 137)}, expected: true, want: oomText, code: 137},
		{name: "out of memory of another container", events: []events.Message{containerEvent("oom", idDB, "db", nil), dieEvent(idWeb, "web", 1)}, want: crashed, code: 1},
		{name: "die without an exit code", events: []events.Message{containerEvent("die", idWeb, "web", nil)}},
		{name: "die with a malformed exit code", events: []events.Message{containerEvent("die", idWeb, "web", map[string]string{"exitCode": "x"})}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWatcher(t)
			if c.expected {
				w.e.mod.expected.mark(idWeb)
			}
			w.feed(c.events...)
			got := w.e.notifications()
			if c.want == "" {
				if len(got) != 0 {
					t.Fatalf("unexpected notification: %+v", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("got %d notifications, want 1: %+v", len(got), got)
			}
			n := got[0]
			if n.Severity != notify.Error || n.Source != "docker" || n.Title != "Container çöktü" {
				t.Errorf("notification = %+v", n)
			}
			if !strings.Contains(n.Message, c.want) {
				t.Errorf("message %q lacks %q", n.Message, c.want)
			}
			if !strings.Contains(n.Message, `"web"`) {
				t.Errorf("message %q does not name the container", n.Message)
			}
			if !strings.Contains(n.Message, "çıkış kodu "+strconv.Itoa(c.code)+")") {
				t.Errorf("message %q lacks the exit code %d", n.Message, c.code)
			}
		})
	}
}

func TestCrashIsReportedOncePerContainer(t *testing.T) {
	w := newWatcher(t)
	// A container in a restart loop dies again and again.
	w.feed(dieEvent(idWeb, "web", 1), dieEvent(idWeb, "web", 1), dieEvent(idWeb, "web", 1))
	w.feed(dieEvent(idDB, "db", 2))
	got := w.e.notifications()
	if len(got) != 2 {
		t.Fatalf("got %d notifications, want one per container: %+v", len(got), got)
	}
}

func TestCrashNameFallsBackToShortID(t *testing.T) {
	for _, name := range []string{"", `bad"name`, "bad name", "<script>"} {
		w := newWatcher(t)
		w.feed(dieEvent(idWeb, name, 1))
		got := w.e.notifications()
		if len(got) != 1 {
			t.Fatalf("name %q: got %+v", name, got)
		}
		if !strings.Contains(got[0].Message, `"`+idWeb[:12]+`"`) {
			t.Errorf("name %q: message %q does not use the short ID", name, got[0].Message)
		}
		if name != "" && strings.Contains(got[0].Message, name) {
			t.Errorf("unvalidated name %q appears in %q", name, got[0].Message)
		}
	}
}

func TestSignalMarksExpire(t *testing.T) {
	s := newRecentSet(time.Minute)
	s.mark("a")
	if !s.has("a") || s.has("b") {
		t.Fatal("mark/has")
	}
	s.unmark("a")
	if s.has("a") {
		t.Error("unmark did not remove the key")
	}
	// An entry older than the window no longer counts.
	s.items["old"] = time.Now().Add(-2 * time.Minute)
	if s.has("old") {
		t.Error("expired entry still counts")
	}
	s.mark("new")
	if _, ok := s.items["old"]; ok {
		t.Error("expired entry was not swept on mark")
	}
}

func TestEventsArePublishedAsChanges(t *testing.T) {
	w := newWatcher(t)
	ch, unsub := w.e.mod.changes.subscribe()
	defer unsub()

	w.e.mod.started.put(idWeb, nil)
	w.feed(
		containerEvent("exec_start: bash", idWeb, "web", nil), // frequent and irrelevant
		containerEvent("health_status: healthy", idWeb, "web", nil),
		containerEvent("top", idWeb, "web", nil),
		containerEvent("start", idWeb, "web", nil),
		events.Message{Type: events.ImageEventType, Action: "pull", Actor: events.Actor{ID: "nginx:latest"}},
		events.Message{Type: events.VolumeEventType, Action: "mount", Actor: events.Actor{ID: "data"}},
		events.Message{Type: events.NetworkEventType, Action: "destroy", Actor: events.Actor{ID: "net1"}},
		events.Message{Type: "plugin", Action: "install", Actor: events.Actor{ID: "p"}},
	)
	want := []changeEvent{
		{Kind: "container", Action: "start", ID: idWeb},
		{Kind: "image", Action: "pull", ID: "nginx:latest"},
		{Kind: "network", Action: "destroy", ID: "net1"},
	}
	for i, exp := range want {
		select {
		case got := <-ch:
			if got != exp {
				t.Errorf("change %d = %+v, want %+v", i, got, exp)
			}
		default:
			t.Fatalf("change %d missing", i)
		}
	}
	select {
	case extra := <-ch:
		t.Errorf("unexpected change %+v", extra)
	default:
	}
	if _, ok := w.e.mod.started.get(idWeb); ok {
		t.Error("a container event must invalidate the cached start time")
	}
	if got := w.e.notifications(); len(got) != 0 {
		t.Errorf("unexpected notifications: %+v", got)
	}
}

func TestSlowChangeSubscriberDoesNotBlock(t *testing.T) {
	h := newChangeHub()
	_, unsub := h.subscribe()
	defer unsub()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			h.publish(changeEvent{Kind: "container", Action: "start", ID: "x"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publish blocked on a subscriber that does not read")
	}
}
