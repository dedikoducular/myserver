package system

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"sync"
	"time"
)

const (
	sampleInterval = 2 * time.Second
	baselineDelay  = 500 * time.Millisecond
	// Sampling continues this long after the last snapshot request.
	requestGrace = 20 * time.Second
	staleAfter   = 10 * time.Second
	fineLimit    = 60
	netInterval  = 15 * time.Second
	netKeep      = 60 * time.Minute
)

type streamEvent struct {
	name string
	data []byte
}

// netHistory is the coarse network history. Each point is the average rate
// between two readings of the interface counters, so it stays correct
// whether it is fed every 2 s by the sampler or once a minute while idle.
type netHistory struct {
	mu     sync.Mutex
	points []NetPoint
	have   bool
	prevRx uint64
	prevTx uint64
	prevAt time.Time
}

func counterDelta(prev, cur uint64) uint64 {
	if cur < prev { // counter reset or interface removed
		return 0
	}
	return cur - prev
}

func (h *netHistory) observe(now time.Time, rx, tx uint64) (NetPoint, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.have {
		h.have, h.prevRx, h.prevTx, h.prevAt = true, rx, tx, now
		return NetPoint{}, false
	}
	elapsed := now.Sub(h.prevAt)
	if elapsed < netInterval {
		return NetPoint{}, false
	}
	secs := elapsed.Seconds()
	p := NetPoint{
		T:      now.Unix(),
		RxRate: float64(counterDelta(h.prevRx, rx)) / secs,
		TxRate: float64(counterDelta(h.prevTx, tx)) / secs,
	}
	h.prevRx, h.prevTx, h.prevAt = rx, tx, now
	h.points = append(h.points, p)
	cutoff := now.Add(-netKeep).Unix()
	drop := 0
	for drop < len(h.points) && h.points[drop].T < cutoff {
		drop++
	}
	if drop > 0 {
		h.points = append([]NetPoint(nil), h.points[drop:]...)
	}
	return p, true
}

func (h *netHistory) since(d time.Duration) []NetPoint {
	cutoff := time.Now().Add(-d).Unix()
	h.mu.Lock()
	defer h.mu.Unlock()
	out := []NetPoint{}
	for _, p := range h.points {
		if p.T >= cutoff {
			out = append(out, p)
		}
	}
	return out
}

// sampler produces snapshots while somebody is watching and sleeps
// otherwise.
type sampler struct {
	col  *collector
	hist netHistory
	wake chan struct{}

	// Timing of the run loop; fixed after construction.
	interval time.Duration
	baseWait time.Duration

	mu       sync.Mutex
	subs     map[chan streamEvent]struct{}
	lastReq  time.Time
	latest   *Snapshot
	latestAt time.Time
	fine     []FinePoint

	// Used only by the run goroutine.
	prevCPU   cpuTimes
	havePrev  bool
	prevNet   map[string]netCounters
	prevNetAt time.Time
	failing   bool
}

func newSampler(col *collector) *sampler {
	return &sampler{
		col: col, wake: make(chan struct{}, 1), subs: map[chan streamEvent]struct{}{},
		interval: sampleInterval, baseWait: baselineDelay,
	}
}

func (s *sampler) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *sampler) dropStaleLocked() {
	if s.latest != nil && time.Since(s.latestAt) > staleAfter {
		s.latest = nil
		s.fine = nil
	}
}

// subscribe registers a listener and returns the current state.
func (s *sampler) subscribe() (ch chan streamEvent, latest *Snapshot, fine []FinePoint, cancel func()) {
	ch = make(chan streamEvent, 8)
	s.mu.Lock()
	s.dropStaleLocked()
	s.subs[ch] = struct{}{}
	latest = s.latest
	fine = append([]FinePoint{}, s.fine...)
	s.mu.Unlock()
	s.signal()
	return ch, latest, fine, func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}
}

// touch records a snapshot request so sampling continues for a while.
func (s *sampler) touch() {
	s.mu.Lock()
	s.lastReq = time.Now()
	s.mu.Unlock()
	s.signal()
}

func (s *sampler) active() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs) > 0 || (!s.lastReq.IsZero() && time.Since(s.lastReq) < requestGrace)
}

func (s *sampler) subscriberCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}

func (s *sampler) broadcast(name string, v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.subs) == 0 {
		return
	}
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	ev := streamEvent{name: name, data: data}
	for ch := range s.subs {
		select {
		case ch <- ev:
		default: // slow subscriber: drop rather than block sampling
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (s *sampler) run(ctx context.Context) {
	for {
		if !s.active() {
			s.havePrev = false
			s.prevNet = nil
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
			}
			continue
		}
		if !s.havePrev {
			s.baseline()
			if !sleepCtx(ctx, s.baseWait) {
				return
			}
		}
		s.sample()
		if !sleepCtx(ctx, s.interval) {
			return
		}
	}
}

// baseline takes the first counter readings after an idle period; rates
// need two readings.
func (s *sampler) baseline() {
	s.mu.Lock()
	s.dropStaleLocked()
	s.mu.Unlock()
	if t, ok := readCPUTimes(); ok {
		s.prevCPU, s.havePrev = t, true
	}
	if n, ok := readNetCounters(); ok {
		s.prevNet, s.prevNetAt = n, time.Now()
	}
}

func (s *sampler) sample() {
	now := time.Now()
	cur, okCPU := readCPUTimes()
	mem, okMem := readMemory()
	if !okCPU || !okMem {
		if !s.failing {
			s.failing = true
			slog.Warn("sistem ölçümleri okunamadı", "source", "/proc")
		}
		return
	}
	s.failing = false

	ci := s.col.cpuInfo()
	snap := Snapshot{
		Time:   now.Unix(),
		Uptime: readUptime(),
		Memory: mem,
		CPU:    CPU{Cores: ci.cores, Threads: ci.threads, Model: ci.model},
	}
	if load, ok := readLoad(); ok {
		snap.CPU.Load1, snap.CPU.Load5, snap.CPU.Load15 = load[0], load[1], load[2]
	}
	if s.havePrev {
		if p, ok := cpuPercent(s.prevCPU, cur); ok {
			snap.CPU.Percent = &p
		}
	}
	s.prevCPU, s.havePrev = cur, true

	snap.Disks = s.col.disks()
	if snap.Disks == nil {
		snap.Disks = []Disk{}
	}
	snap.RootDisk = rootDisk(snap.Disks)
	snap.Temperature = s.col.temperature()
	snap.Network = s.network(now)

	point := FinePoint{
		T: snap.Time, Memory: mem.Percent, Temperature: snap.Temperature.CPU,
		RxRate: snap.Network.RxRate, TxRate: snap.Network.TxRate,
	}
	if snap.CPU.Percent != nil {
		point.CPU = *snap.CPU.Percent
	}
	if snap.RootDisk != nil {
		p := snap.RootDisk.Percent
		point.Disk = &p
	}

	s.mu.Lock()
	s.latest, s.latestAt = &snap, now
	s.fine = append(s.fine, point)
	if len(s.fine) > fineLimit {
		s.fine = append([]FinePoint(nil), s.fine[len(s.fine)-fineLimit:]...)
	}
	s.mu.Unlock()
	s.broadcast("metrics", snap)
}

func (s *sampler) network(now time.Time) Network {
	net := Network{Interfaces: []Interface{}}
	counters, ok := readNetCounters()
	if !ok {
		return net
	}
	net = netRates(s.prevNet, counters, now.Sub(s.prevNetAt).Seconds(), s.col.isVirtual)
	s.prevNet, s.prevNetAt = counters, now
	s.recordNet(now, counters)
	return net
}

// netRates turns two readings of the interface counters, secs apart, into
// per-interface rates. An interface without a previous reading has no rate
// yet; totals leave out virtual interfaces.
func netRates(prev, cur map[string]netCounters, secs float64, virtual func(string) bool) Network {
	net := Network{Interfaces: []Interface{}}
	for name, c := range cur {
		ifc := Interface{Name: name, RxBytes: c.rx, TxBytes: c.tx, Virtual: virtual(name)}
		if p, had := prev[name]; had && secs > 0 {
			ifc.RxRate = float64(counterDelta(p.rx, c.rx)) / secs
			ifc.TxRate = float64(counterDelta(p.tx, c.tx)) / secs
		}
		if !ifc.Virtual {
			net.RxRate += ifc.RxRate
			net.TxRate += ifc.TxRate
		}
		net.Interfaces = append(net.Interfaces, ifc)
	}
	sort.Slice(net.Interfaces, func(i, j int) bool { return net.Interfaces[i].Name < net.Interfaces[j].Name })
	return net
}

// recordNet feeds the coarse history and announces a new point.
func (s *sampler) recordNet(now time.Time, counters map[string]netCounters) {
	rx, tx := s.col.netTotals(counters)
	if p, ok := s.hist.observe(now, rx, tx); ok {
		s.broadcast("network", p)
	}
}

// current returns the latest snapshot if it is recent.
func (s *sampler) current(maxAge time.Duration) (*Snapshot, []FinePoint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latest == nil || time.Since(s.latestAt) > maxAge {
		return nil, nil
	}
	return s.latest, append([]FinePoint{}, s.fine...)
}
