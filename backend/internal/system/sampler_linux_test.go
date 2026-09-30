//go:build linux

package system

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"myserver/internal/httpx"
)

// These tests run the real sampler against the /proc of the machine (the
// test container). Only the pace of the loop is shortened.

const testWait = 5 * time.Second

func fastSampler() *sampler {
	s := newSampler(newCollector())
	s.interval = 10 * time.Millisecond
	s.baseWait = 2 * time.Millisecond
	return s
}

func startSampler(t *testing.T, s *sampler) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.run(ctx)
	}()
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case <-done:
		case <-time.After(testWait):
			t.Error("the sampler did not stop after its context was cancelled")
		}
	}
	t.Cleanup(stop)
	return stop
}

func lastSampleAt(s *sampler) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latest == nil {
		return time.Time{}
	}
	return s.latestAt
}

func waitEvent(t *testing.T, ch chan streamEvent, name string) streamEvent {
	t.Helper()
	deadline := time.After(testWait)
	for {
		select {
		case ev := <-ch:
			if ev.name == name {
				return ev
			}
		case <-deadline:
			t.Fatalf("no %q event within %s", name, testWait)
		}
	}
}

// waitIdle waits until the sampler has stopped producing samples.
func waitIdle(t *testing.T, s *sampler) time.Time {
	t.Helper()
	deadline := time.Now().Add(testWait)
	prev := lastSampleAt(s)
	for time.Now().Before(deadline) {
		time.Sleep(20 * s.interval)
		cur := lastSampleAt(s)
		if cur.Equal(prev) {
			return cur
		}
		prev = cur
	}
	t.Fatal("the sampler keeps sampling although nobody is subscribed")
	return time.Time{}
}

func waitGoroutines(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(testWait)
	n := 0
	for time.Now().Before(deadline) {
		if n = runtime.NumGoroutine(); n <= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	buf := make([]byte, 1<<16)
	buf = buf[:runtime.Stack(buf, true)]
	t.Fatalf("%d goroutines, %d before the test:\n%s", n, want, buf)
}

func TestSamplerLifecycle(t *testing.T) {
	before := runtime.NumGoroutine()
	s := fastSampler()
	stop := startSampler(t, s)

	// Nobody is subscribed: no sample.
	time.Sleep(30 * s.interval)
	if at := lastSampleAt(s); !at.IsZero() {
		t.Fatal("the sampler sampled without a subscriber")
	}
	if s.active() {
		t.Fatal("active without subscriber or request")
	}

	// The first subscriber starts it.
	ch, latest, fine, cancel := s.subscribe()
	if latest != nil || len(fine) != 0 {
		t.Errorf("state before the first sample: %v %v", latest, fine)
	}
	ev := waitEvent(t, ch, "metrics")
	var snap Snapshot
	if err := json.Unmarshal(ev.data, &snap); err != nil {
		t.Fatalf("event is not a snapshot: %v", err)
	}
	if snap.Memory.Total == 0 || snap.CPU.Threads == 0 || snap.Time == 0 {
		t.Errorf("empty snapshot: %+v", snap)
	}
	if snap.RootDisk == nil || snap.RootDisk.Mount != "/" || snap.RootDisk.Total == 0 {
		t.Errorf("root disk missing in the snapshot: %+v (disks %+v)", snap.RootDisk, snap.Disks)
	}
	if !strings.Contains(string(ev.data), `"nvme":[`) || !strings.Contains(string(ev.data), `"interfaces":[`) {
		t.Errorf("lists must be arrays, not null: %s", ev.data)
	}
	// Rates need two readings; the second sample has a CPU percentage.
	for i := 0; ; i++ {
		ev = waitEvent(t, ch, "metrics")
		var next Snapshot
		if err := json.Unmarshal(ev.data, &next); err != nil {
			t.Fatal(err)
		}
		if next.CPU.Percent != nil {
			if p := *next.CPU.Percent; p < 0 || p > 100 {
				t.Errorf("cpu percent %v", p)
			}
			break
		}
		if i > 200 {
			t.Fatal("no CPU percentage after 200 samples")
		}
	}

	// A second subscriber; the first leaving does not stop the sampler.
	ch2, latest2, _, cancel2 := s.subscribe()
	if latest2 == nil {
		t.Error("a later subscriber should receive the current snapshot")
	}
	cancel()
	if n := s.subscriberCount(); n != 1 {
		t.Fatalf("subscribers = %d, want 1", n)
	}
	for len(ch2) > 0 {
		<-ch2
	}
	waitEvent(t, ch2, "metrics")

	// The last one leaves: sampling stops.
	cancel2()
	cancel2() // leaving twice is harmless
	idleAt := waitIdle(t, s)
	time.Sleep(30 * s.interval)
	if at := lastSampleAt(s); !at.Equal(idleAt) {
		t.Fatal("a sample was taken after the last subscriber left")
	}

	// And starts again.
	ch3, _, _, cancel3 := s.subscribe()
	waitEvent(t, ch3, "metrics")
	if at := lastSampleAt(s); !at.After(idleAt) {
		t.Error("no new sample after subscribing again")
	}
	cancel3()

	stop()
	waitGoroutines(t, before)
}

func TestSamplerStopsWhileSubscribed(t *testing.T) {
	before := runtime.NumGoroutine()
	s := fastSampler()
	stop := startSampler(t, s)
	ch, _, _, cancel := s.subscribe()
	defer cancel()
	waitEvent(t, ch, "metrics")
	stop()
	waitGoroutines(t, before)
}

func TestSamplerStopsWhileIdle(t *testing.T) {
	before := runtime.NumGoroutine()
	s := fastSampler()
	stop := startSampler(t, s)
	time.Sleep(5 * s.interval)
	stop()
	waitGoroutines(t, before)
}

func TestSamplerHistoryIsBounded(t *testing.T) {
	s := fastSampler()
	s.interval = time.Millisecond
	startSampler(t, s)
	ch, _, _, cancel := s.subscribe()
	defer cancel()
	for i := 0; i < fineLimit+25; i++ {
		waitEvent(t, ch, "metrics")
	}
	_, fine := s.current(time.Minute)
	if len(fine) != fineLimit {
		t.Fatalf("history holds %d points, want %d", len(fine), fineLimit)
	}
	s.mu.Lock()
	n, c := len(s.fine), cap(s.fine)
	s.mu.Unlock()
	if n > fineLimit || c > 4*fineLimit {
		t.Errorf("len=%d cap=%d", n, c)
	}
	for i := 1; i < len(fine); i++ {
		if fine[i].T < fine[i-1].T {
			t.Fatalf("history is not in time order at %d", i)
		}
	}
	// The copy handed out is independent of the sampler's buffer.
	fine[0].CPU = -1
	if _, again := s.current(time.Minute); again[0].CPU == -1 {
		t.Error("current returns the internal buffer")
	}
}

func TestSamplerSlowSubscriberDoesNotBlock(t *testing.T) {
	s := fastSampler()
	s.interval = time.Millisecond
	startSampler(t, s)
	slow, _, _, cancelSlow := s.subscribe() // never read
	defer cancelSlow()
	ch, _, _, cancel := s.subscribe()
	defer cancel()
	for i := 0; i < 40; i++ {
		waitEvent(t, ch, "metrics")
	}
	if len(slow) != cap(slow) {
		t.Errorf("slow subscriber queue %d/%d", len(slow), cap(slow))
	}
}

func TestSamplerRequestKeepsItActive(t *testing.T) {
	s := fastSampler()
	if s.active() {
		t.Fatal("active before any request")
	}
	s.touch()
	if !s.active() {
		t.Fatal("a snapshot request must activate the sampler")
	}
	s.mu.Lock()
	s.lastReq = time.Now().Add(-requestGrace - time.Second)
	s.mu.Unlock()
	if s.active() {
		t.Fatal("still active after the grace period")
	}
}

func TestSamplerDropsStaleState(t *testing.T) {
	s := fastSampler()
	s.mu.Lock()
	s.latest = &Snapshot{Time: 1}
	s.latestAt = time.Now().Add(-staleAfter - time.Second)
	s.fine = []FinePoint{{T: 1}}
	s.mu.Unlock()
	if snap, _ := s.current(time.Hour); snap == nil {
		t.Fatal("current with a long maxAge should still return it")
	}
	_, latest, fine, cancel := s.subscribe()
	defer cancel()
	if latest != nil || len(fine) != 0 {
		t.Errorf("a stale snapshot was handed to a new subscriber: %v %v", latest, fine)
	}
}

func TestNetHistory(t *testing.T) {
	var h netHistory
	t0 := time.Now().Add(-3 * time.Hour)
	if _, ok := h.observe(t0, 1000, 1000); ok {
		t.Fatal("the first reading cannot produce a rate")
	}
	// Fed every 2 s by the sampler: one point per netInterval.
	points := 0
	rx := uint64(1000)
	for i := 1; i <= 30; i++ {
		rx += 2000
		if p, ok := h.observe(t0.Add(time.Duration(i)*2*time.Second), rx, 1000); ok {
			points++
			if p.RxRate != 1000 || p.TxRate != 0 {
				t.Errorf("point %d: %+v, want 1000 B/s", i, p)
			}
		}
	}
	if points != 3 { // 60 s in steps of 16 s
		t.Errorf("%d points in 60 s, want 3", points)
	}

	// Counter reset (interface removed or reloaded): no negative or huge rate.
	p, ok := h.observe(t0.Add(2*time.Minute), 10, 5)
	if !ok || p.RxRate != 0 || p.TxRate != 0 {
		t.Errorf("after reset: %+v ok=%v", p, ok)
	}
	p, ok = h.observe(t0.Add(3*time.Minute), 6010, 605)
	if !ok || p.RxRate != 100 || p.TxRate != 10 {
		t.Errorf("after recovery: %+v ok=%v", p, ok)
	}
}

func TestNetHistoryIsBounded(t *testing.T) {
	var h netHistory
	start := time.Now().Add(-6 * time.Hour)
	max := int(netKeep/netInterval) + 1
	for i := 0; i <= int(6*time.Hour/netInterval); i++ {
		h.observe(start.Add(time.Duration(i)*netInterval), uint64(i)*1500, uint64(i)*300)
		h.mu.Lock()
		n := len(h.points)
		h.mu.Unlock()
		if n > max {
			t.Fatalf("history grew to %d points, limit %d", n, max)
		}
	}
	h.mu.Lock()
	n, c := len(h.points), cap(h.points)
	h.mu.Unlock()
	if n < max-1 {
		t.Errorf("only %d points kept, want about %d", n, max)
	}
	if c > 4*max {
		t.Errorf("capacity %d for %d points", c, n)
	}
	if got := h.since(5 * time.Minute); len(got) < 19 || len(got) > 21 {
		t.Errorf("since(5m) returned %d points, want about 20", len(got))
	}
	if got := h.since(time.Hour); len(got) > max {
		t.Errorf("since(1h) returned %d points", len(got))
	}
	var empty netHistory
	if got := empty.since(time.Hour); got == nil || len(got) != 0 {
		t.Errorf("empty history: %v", got)
	}
}

// ---- HTTP ----

func TestStreamHandler(t *testing.T) {
	m := newTestModule(t, nil, newSysTree(t))
	before := runtime.NumGoroutine() // after the database has started its own
	m.sampler.interval = 10 * time.Millisecond
	m.sampler.baseWait = 2 * time.Millisecond
	stop := startSampler(t, m.sampler)

	ctx, cancel := context.WithCancel(context.Background())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/stream", nil).WithContext(ctx)
	done := make(chan error, 1)
	go func() { done <- m.handleStream(rec, req) }()

	deadline := time.Now().Add(testWait)
	for m.sampler.subscriberCount() == 0 || lastSampleAt(m.sampler).IsZero() {
		if time.Now().After(deadline) {
			t.Fatal("the stream did not start the sampler")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(5 * m.sampler.interval)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("handler returned %v", err)
		}
	case <-time.After(testWait):
		t.Fatal("the stream did not end when the client went away")
	}
	if n := m.sampler.subscriberCount(); n != 0 {
		t.Errorf("%d subscribers left after the client went away", n)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content type %q", ct)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "event: history\ndata: {") {
		t.Errorf("the stream must begin with the history event: %.80q", body)
	}
	if !strings.Contains(body, "\n\nevent: metrics\ndata: {") {
		t.Errorf("no metrics event in the stream: %.200q", body)
	}
	if !strings.HasSuffix(body, "\n\n") {
		t.Errorf("last event is not terminated: %q", body[len(body)-20:])
	}
	stop()
	waitGoroutines(t, before)
}

func TestMetricsHandlerStartsSampler(t *testing.T) {
	m := newTestModule(t, nil, newSysTree(t))
	m.sampler.interval = 10 * time.Millisecond
	m.sampler.baseWait = 2 * time.Millisecond
	startSampler(t, m.sampler)

	rec := httptest.NewRecorder()
	if err := m.handleMetrics(rec, httptest.NewRequest(http.MethodGet, "/api/v1/system/metrics", nil)); err != nil {
		t.Fatalf("handler returned %v", err)
	}
	var env struct {
		Success bool `json:"success"`
		Data    struct {
			Snapshot *Snapshot   `json:"snapshot"`
			Interval int         `json:"interval"`
			History  []FinePoint `json:"history"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	if !env.Success || env.Data.Snapshot == nil || env.Data.Interval != 2 || len(env.Data.History) == 0 {
		t.Errorf("response: %s", rec.Body.String())
	}
	if n := m.sampler.subscriberCount(); n != 0 {
		t.Errorf("%d subscribers left by the request", n)
	}
}

func TestMetricsHandlerWithoutSampler(t *testing.T) {
	// The sampler is not running (module not started): the request ends
	// with a Turkish error instead of hanging.
	m := newTestModule(t, nil, newSysTree(t))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/metrics", nil).WithContext(ctx)
	start := time.Now()
	err := m.handleMetrics(httptest.NewRecorder(), req)
	if time.Since(start) > 2*time.Second {
		t.Errorf("took %s", time.Since(start))
	}
	he, ok := err.(*httpx.Error)
	if !ok || he.Status != http.StatusServiceUnavailable || he.Code != "metrics_unavailable" || he.Message != "Sistem ölçümleri okunamadı." {
		t.Errorf("got %#v", err)
	}
	if n := m.sampler.subscriberCount(); n != 0 {
		t.Errorf("%d subscribers left", n)
	}
}

func TestNetworkHistoryHandler(t *testing.T) {
	m := newTestModule(t, nil, newSysTree(t))
	for _, q := range []string{"", "?range=5m", "?range=15m", "?range=30m", "?range=1h"} {
		rec := httptest.NewRecorder()
		if err := m.handleNetworkHistory(rec, httptest.NewRequest(http.MethodGet, "/api/v1/system/network/history"+q, nil)); err != nil {
			t.Errorf("%q: %v", q, err)
			continue
		}
		if !strings.Contains(rec.Body.String(), `"points":[]`) || !strings.Contains(rec.Body.String(), `"interval":15`) {
			t.Errorf("%q: %s", q, rec.Body.String())
		}
	}
	for _, q := range []string{"?range=2h", "?range=1d", "?range=-5m", "?range=5m%00", "?range=1H"} {
		err := m.handleNetworkHistory(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/system/network/history"+q, nil))
		he, ok := err.(*httpx.Error)
		if !ok || he.Status != http.StatusBadRequest || he.Message != "Zaman aralığı geçersiz." {
			t.Errorf("%q: got %#v", q, err)
		}
	}
}

func TestInfoHandler(t *testing.T) {
	m := newTestModule(t, nil, newSysTree(t))
	rec := httptest.NewRecorder()
	if err := m.handleInfo(rec, httptest.NewRequest(http.MethodGet, "/api/v1/system/info", nil)); err != nil {
		t.Fatalf("handler returned %v", err)
	}
	var env struct {
		Data Info `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	i := env.Data
	if !strings.HasPrefix(i.OS, "Ubuntu 24.04") || i.Kernel == "" || i.CPUThreads == 0 || i.CPUCores == 0 ||
		i.CPUCores > i.CPUThreads || i.MemoryTotal == 0 || i.Hostname == "" || i.Uptime <= 0 || i.Time == 0 {
		t.Errorf("info: %+v", i)
	}
}
