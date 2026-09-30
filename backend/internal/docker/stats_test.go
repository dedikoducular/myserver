package docker

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/docker/docker/api/types/container"
)

func TestCPUPercent(t *testing.T) {
	cases := []struct {
		name                                       string
		prevTotal, prevSystem, curTotal, curSystem uint64
		cpus                                       float64
		want                                       float64
	}{
		{"one fifth of four CPUs", 100, 1000, 300, 2000, 4, 80},
		{"single CPU fully used", 0, 0, 1000, 1000, 1, 100},
		{"two CPUs fully used on an eight CPU host", 0, 0, 2000, 8000, 8, 200},
		{"idle container", 500, 1000, 500, 2000, 4, 0},
		{"no system time passed", 100, 1000, 300, 1000, 4, 0},
		{"both deltas zero", 100, 1000, 100, 1000, 4, 0},
		{"container counter went backwards (restart)", 900, 1000, 100, 2000, 4, 0},
		{"system counter went backwards", 100, 2000, 300, 1000, 4, 0},
		{"no CPU count known", 100, 1000, 300, 2000, 0, 0},
		{"large counters", 1 << 62, 1 << 62, 1<<62 + 1<<40, 1<<62 + 1<<42, 2, 50},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := cpuPercent(c.prevTotal, c.prevSystem, c.curTotal, c.curSystem, c.cpus)
			if got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestMemoryUsed(t *testing.T) {
	cases := []struct {
		name string
		mem  container.MemoryStats
		want uint64
	}{
		{"cgroup v1 subtracts total_inactive_file", container.MemoryStats{
			Usage: 1000, Stats: map[string]uint64{"total_inactive_file": 300, "inactive_file": 100, "cache": 500},
		}, 700},
		{"cgroup v2 subtracts inactive_file", container.MemoryStats{
			Usage: 1000, Stats: map[string]uint64{"inactive_file": 200, "file": 600, "anon": 300},
		}, 800},
		{"no detailed stats", container.MemoryStats{Usage: 1000}, 1000},
		{"empty stats map", container.MemoryStats{Usage: 1000, Stats: map[string]uint64{}}, 1000},
		{"v1 cache larger than usage is ignored", container.MemoryStats{
			Usage: 100, Stats: map[string]uint64{"total_inactive_file": 300},
		}, 100},
		{"v2 cache larger than usage is ignored", container.MemoryStats{
			Usage: 100, Stats: map[string]uint64{"inactive_file": 300},
		}, 100},
		{"cache equal to usage is ignored like docker stats does", container.MemoryStats{
			Usage: 300, Stats: map[string]uint64{"inactive_file": 300},
		}, 300},
		{"zero usage", container.MemoryStats{Usage: 0, Stats: map[string]uint64{"inactive_file": 0}}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := memoryUsed(c.mem); got != c.want {
				t.Errorf("got %d, want %d", got, c.want)
			}
		})
	}
}

func TestRound1(t *testing.T) {
	cases := map[float64]float64{0: 0, 12.34: 12.3, 12.35: 12.4, 99.96: 100, 0.04: 0}
	for in, want := range cases {
		if got := round1(in); got != want {
			t.Errorf("round1(%v) = %v, want %v", in, got, want)
		}
	}
}

// statsJSON is a one-shot stats response: the daemon sends no previous
// sample ("precpu_stats" is zero) when one-shot is requested.
func statsJSON(total, system uint64, onlineCPUs int, memory string) string {
	return fmt.Sprintf(`{
		"read":"2024-05-01T10:00:00Z","preread":"0001-01-01T00:00:00Z",
		"pids_stats":{"current":7},
		"cpu_stats":{"cpu_usage":{"total_usage":%d,"usage_in_kernelmode":1,"usage_in_usermode":2},
			"system_cpu_usage":%d,"online_cpus":%d},
		"precpu_stats":{"cpu_usage":{"total_usage":0},"system_cpu_usage":0},
		"memory_stats":%s
	}`, total, system, onlineCPUs, memory)
}

func TestCollectComputesCPUFromThePreviousSample(t *testing.T) {
	fake := newFakeDocker(t)
	fake.handleJSON("GET /containers/json", 200, `[{"Id":"`+idWeb+`","Names":["/web"],"State":"running"}]`)
	var mu sync.Mutex
	sample := 0
	fake.handle("GET /containers/"+idWeb+"/stats", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sample++
		n := sample
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		mem := `{"usage":1000000,"limit":4000000,"stats":{"inactive_file":200000}}`
		switch n {
		case 1:
			fmt.Fprint(w, statsJSON(100, 1000, 4, mem))
		case 2:
			fmt.Fprint(w, statsJSON(300, 2000, 4, mem))
		case 3:
			fmt.Fprint(w, statsJSON(300, 3000, 4, mem)) // idle since the last sample
		default:
			fmt.Fprint(w, statsJSON(50, 4000, 4, mem)) // counter reset: container restarted
		}
	})
	e := newTestEnv(t, fake.host())
	prev := map[string]cpuSample{}
	ctx := context.Background()

	first := e.mod.stats.collect(ctx, prev)
	if !first.Available || len(first.Items) != 1 {
		t.Fatalf("first snapshot = %+v", first)
	}
	it := first.Items[0]
	if it.CPUPercent != nil {
		t.Errorf("first sample has no previous sample, cpu_percent must be null, got %v", *it.CPUPercent)
	}
	if it.ID != idWeb || it.MemoryUsage != 800000 || it.MemoryLimit != 4000000 || it.MemoryPercent != 20 || it.Pids != 7 {
		t.Errorf("first item = %+v", it)
	}

	second := e.mod.stats.collect(ctx, prev)
	if len(second.Items) != 1 || second.Items[0].CPUPercent == nil {
		t.Fatalf("second snapshot = %+v", second)
	}
	if got := *second.Items[0].CPUPercent; got != 80 {
		t.Errorf("cpu_percent = %v, want 80", got)
	}

	third := e.mod.stats.collect(ctx, prev)
	if len(third.Items) != 1 || third.Items[0].CPUPercent == nil || *third.Items[0].CPUPercent != 0 {
		t.Errorf("idle sample must report 0, got %+v", third.Items)
	}

	// After a counter reset there is no usable previous sample.
	fourth := e.mod.stats.collect(ctx, prev)
	if len(fourth.Items) != 1 {
		t.Fatalf("fourth snapshot = %+v", fourth)
	}
	if p := fourth.Items[0].CPUPercent; p != nil && *p != 0 {
		t.Errorf("after a counter reset cpu_percent must be null or 0, got %v", *p)
	}

	if line, ok := fake.seenPath("GET", "/containers/"+idWeb+"/stats"); !ok {
		t.Errorf("stats endpoint was not called; saw %v", fake.seen())
	} else {
		t.Logf("stats request: %s", line)
	}
}

func TestCollectUsesTheDaemonsPreviousSampleWhenPresent(t *testing.T) {
	fake := newFakeDocker(t)
	fake.handleJSON("GET /containers/json", 200, `[{"Id":"`+idWeb+`","Names":["/web"],"State":"running"}]`)
	// cgroup v1 style: per-CPU usage list, no online_cpus, total_inactive_file.
	fake.handleJSON("GET /containers/"+idWeb+"/stats", 200, `{
		"cpu_stats":{"cpu_usage":{"total_usage":600,"percpu_usage":[300,300]},"system_cpu_usage":3000},
		"precpu_stats":{"cpu_usage":{"total_usage":100,"percpu_usage":[50,50]},"system_cpu_usage":1000},
		"memory_stats":{"usage":5000,"limit":0,"stats":{"total_inactive_file":1000,"inactive_file":10}}
	}`)
	e := newTestEnv(t, fake.host())
	snap := e.mod.stats.collect(context.Background(), map[string]cpuSample{})
	if len(snap.Items) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	it := snap.Items[0]
	// (600-100)/(3000-1000) * 2 CPUs * 100 = 50
	if it.CPUPercent == nil || *it.CPUPercent != 50 {
		t.Errorf("cpu_percent = %v, want 50", it.CPUPercent)
	}
	if it.MemoryUsage != 4000 {
		t.Errorf("memory_usage = %d, want 4000", it.MemoryUsage)
	}
	if it.MemoryLimit != 0 || it.MemoryPercent != 0 {
		t.Errorf("no limit must give 0 percent, got limit=%d percent=%v", it.MemoryLimit, it.MemoryPercent)
	}
}

func TestCollectWhenDockerIsDown(t *testing.T) {
	e := newTestEnv(t, refusedTCPHost(t))
	prev := map[string]cpuSample{"stale": {total: 1, system: 1}}
	snap := e.mod.stats.collect(context.Background(), prev)
	if snap.Available {
		t.Error("snapshot must say Docker is unavailable")
	}
	if snap.Items == nil || len(snap.Items) != 0 {
		t.Errorf("items = %#v, want empty list", snap.Items)
	}
	if len(prev) != 0 {
		t.Error("stale CPU samples must be dropped while Docker is down")
	}
}
