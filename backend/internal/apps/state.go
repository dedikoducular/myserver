package apps

import "strings"

// Application runtime states, derived from the real container states.
const (
	StateRunning   = "running"
	StateStopped   = "stopped"
	StateStarting  = "starting"
	StateUnhealthy = "unhealthy"
	StatePartial   = "partial" // some services run, some do not
	StateMissing   = "missing" // no container exists
	StateUnknown   = "unknown" // Docker could not be asked
)

// ContainerInfo is the part of a container listing the state derivation
// needs. State and Status are the values reported by Docker
// ("running", "Up 2 hours (healthy)").
type ContainerInfo struct {
	ID      string
	Name    string
	Service string
	Image   string
	State   string
	Status  string
}

// ServiceState is the runtime state of one service.
type ServiceState struct {
	Name          string `json:"name"`
	ContainerName string `json:"container_name"`
	ContainerID   string `json:"container_id"`
	Image         string `json:"image"`
	// State is running, stopped, starting, unhealthy or missing.
	State string `json:"state"`
	// Status is Docker's own description, e.g. "Up 2 hours (healthy)".
	Status string `json:"status"`
}

func serviceState(c ContainerInfo) string {
	status := strings.ToLower(c.Status)
	switch strings.ToLower(c.State) {
	case "running":
		switch {
		case strings.Contains(status, "(unhealthy)"):
			return StateUnhealthy
		case strings.Contains(status, "health: starting"):
			return StateStarting
		}
		return StateRunning
	case "restarting":
		return StateStarting
	default: // created, exited, paused, removing, dead
		return StateStopped
	}
}

// DeriveState computes the state of an application from its containers.
func DeriveState(cfg *Config, containers []ContainerInfo) (string, []ServiceState) {
	byService := map[string]ContainerInfo{}
	for _, c := range containers {
		byService[c.Service] = c
	}
	services := make([]ServiceState, 0, len(cfg.Services))
	count := map[string]int{}
	for _, s := range cfg.Services {
		st := ServiceState{Name: s.Name, ContainerName: s.ContainerName, Image: s.Image, State: StateMissing}
		if c, ok := byService[s.Name]; ok {
			st.ContainerID = c.ID
			st.Status = c.Status
			st.State = serviceState(c)
			if c.Name != "" {
				st.ContainerName = c.Name
			}
		}
		count[st.State]++
		services = append(services, st)
	}
	total := len(services)
	up := count[StateRunning] + count[StateStarting] + count[StateUnhealthy]
	switch {
	case count[StateMissing] == total:
		return StateMissing, services
	case up == 0:
		// Nothing runs. A container that is missing next to stopped ones
		// does not make the application "partially running".
		return StateStopped, services
	case up < total:
		return StatePartial, services
	case count[StateUnhealthy] > 0:
		return StateUnhealthy, services
	case count[StateStarting] > 0:
		return StateStarting, services
	}
	return StateRunning, services
}

func unknownState(cfg *Config) []ServiceState {
	out := make([]ServiceState, 0, len(cfg.Services))
	for _, s := range cfg.Services {
		out = append(out, ServiceState{Name: s.Name, ContainerName: s.ContainerName, Image: s.Image, State: StateUnknown})
	}
	return out
}
