package apps

import (
	"strings"
	"testing"
)

func stateConfig(services ...string) *Config {
	cfg := &Config{Slug: "uyg"}
	for _, s := range services {
		cfg.Services = append(cfg.Services, ServiceConfig{
			Name: s, ContainerName: ContainerName("uyg", s, len(services) == 1), Image: "example/" + s,
		})
	}
	return cfg
}

func ci(service, state, status string) ContainerInfo {
	return ContainerInfo{ID: "id-" + service, Name: "myserver-uyg-" + service, Service: service, State: state, Status: status}
}

func TestDeriveStateSingleService(t *testing.T) {
	cases := []struct {
		name       string
		containers []ContainerInfo
		want       string
	}{
		{"running", []ContainerInfo{ci("app", "running", "Up 2 hours")}, StateRunning},
		{"running and healthy", []ContainerInfo{ci("app", "running", "Up 2 hours (healthy)")}, StateRunning},
		{"health check starting", []ContainerInfo{ci("app", "running", "Up 3 seconds (health: starting)")}, StateStarting},
		{"unhealthy", []ContainerInfo{ci("app", "running", "Up 2 hours (unhealthy)")}, StateUnhealthy},
		{"restarting", []ContainerInfo{ci("app", "restarting", "Restarting (1) 4 seconds ago")}, StateStarting},
		{"exited", []ContainerInfo{ci("app", "exited", "Exited (0) 2 hours ago")}, StateStopped},
		{"created", []ContainerInfo{ci("app", "created", "Created")}, StateStopped},
		{"paused", []ContainerInfo{ci("app", "paused", "Up 2 hours (Paused)")}, StateStopped},
		{"dead", []ContainerInfo{ci("app", "dead", "Dead")}, StateStopped},
		{"state in upper case", []ContainerInfo{ci("app", "Running", "Up 1 second")}, StateRunning},
		{"no container", nil, StateMissing},
		{"container of an unknown service", []ContainerInfo{ci("other", "running", "Up")}, StateMissing},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, services := DeriveState(stateConfig("app"), c.containers)
			if got != c.want {
				t.Errorf("state %q, want %q", got, c.want)
			}
			if len(services) != 1 || services[0].State != c.want || services[0].Name != "app" {
				t.Errorf("services: %+v", services)
			}
			if c.want == StateMissing && (services[0].ContainerID != "" || services[0].ContainerName != "myserver-uyg") {
				t.Errorf("missing service: %+v", services[0])
			}
		})
	}
}

func TestDeriveStateMultiService(t *testing.T) {
	up := func(s string) ContainerInfo { return ci(s, "running", "Up 1 hour") }
	down := func(s string) ContainerInfo { return ci(s, "exited", "Exited (0) 1 hour ago") }
	cases := []struct {
		name       string
		containers []ContainerInfo
		want       string
		services   string
	}{
		{"all running", []ContainerInfo{up("web"), up("db"), up("cache")}, StateRunning, "running,running,running"},
		{"all stopped", []ContainerInfo{down("web"), down("db"), down("cache")}, StateStopped, "stopped,stopped,stopped"},
		{"all missing", nil, StateMissing, "missing,missing,missing"},
		{"one stopped", []ContainerInfo{up("web"), down("db"), up("cache")}, StatePartial, "running,stopped,running"},
		{"one missing", []ContainerInfo{up("web"), up("cache")}, StatePartial, "running,missing,running"},
		{"one running", []ContainerInfo{down("web"), up("db"), down("cache")}, StatePartial, "stopped,running,stopped"},
		{"one unhealthy", []ContainerInfo{up("web"), ci("db", "running", "Up 1 hour (unhealthy)"), up("cache")},
			StateUnhealthy, "running,unhealthy,running"},
		{"one starting", []ContainerInfo{up("web"), ci("db", "running", "Up 1 second (health: starting)"), up("cache")},
			StateStarting, "running,starting,running"},
		{"one restarting", []ContainerInfo{up("web"), ci("db", "restarting", "Restarting (1) 1 second ago"), up("cache")},
			StateStarting, "running,starting,running"},
		{"unhealthy wins over starting", []ContainerInfo{ci("web", "running", "Up (unhealthy)"), ci("db", "restarting", ""), up("cache")},
			StateUnhealthy, "unhealthy,starting,running"},
		{"stopped wins over unhealthy", []ContainerInfo{ci("web", "running", "Up (unhealthy)"), down("db"), up("cache")},
			StatePartial, "unhealthy,stopped,running"},
		// Nothing runs: the application is not "partially running".
		{"stopped and missing", []ContainerInfo{down("web"), down("cache")}, StateStopped, "stopped,missing,stopped"},
		{"one stopped, rest missing", []ContainerInfo{down("db")}, StateStopped, "missing,stopped,missing"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, services := DeriveState(stateConfig("web", "db", "cache"), c.containers)
			var each []string
			for _, s := range services {
				each = append(each, s.State)
			}
			if got != c.want || strings.Join(each, ",") != c.services {
				t.Errorf("state %q (%v), want %q (%s)", got, each, c.want, c.services)
			}
		})
	}
}

func TestDeriveStateKeepsContainerDetails(t *testing.T) {
	_, services := DeriveState(stateConfig("web", "db"), []ContainerInfo{
		{ID: "abc", Name: "renamed-web", Service: "web", State: "running", Status: "Up 5 minutes"},
	})
	if s := services[0]; s.ContainerID != "abc" || s.ContainerName != "renamed-web" || s.Status != "Up 5 minutes" || s.Image != "example/web" {
		t.Errorf("service: %+v", s)
	}
	if s := services[1]; s.ContainerName != "myserver-uyg-db" || s.State != StateMissing {
		t.Errorf("service: %+v", s)
	}
	for _, s := range unknownState(stateConfig("web", "db")) {
		if s.State != StateUnknown {
			t.Errorf("unknown state: %+v", s)
		}
	}
}
