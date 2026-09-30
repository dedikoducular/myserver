package storage

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"myserver/internal/notify"
	sc "myserver/internal/storage/storagecheck"
)

// broker fans "devices changed" events out to the SSE clients.
type broker struct {
	mu   sync.Mutex
	subs map[chan string]struct{}
}

func newBroker() *broker { return &broker{subs: map[chan string]struct{}{}} }

func (b *broker) subscribe() (<-chan string, func()) {
	ch := make(chan string, 4)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

func (b *broker) publish(reason string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- reason:
		default: // the client already has an update pending
		}
	}
}

const pollInterval = 10 * time.Second

// fingerprint summarizes the attached disks and the mount table. Both are
// tiny reads from /sys and /proc.
func (m *Module) fingerprint() [32]byte {
	h := sha256.New()
	h.Write([]byte(strings.Join(m.sys.DiskNames(), ",")))
	h.Write([]byte{0})
	if b, err := os.ReadFile("/proc/self/mountinfo"); err == nil {
		for _, mt := range sc.ParseMountInfo(b) {
			h.Write([]byte(mt.MajMin + " " + mt.MountPoint + " " + mt.Source + " " + mt.Options + "\n"))
		}
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// watchLoop reacts to kernel block-device events and, as a fallback and to
// notice mounts made outside the panel, compares a cheap fingerprint
// every ten seconds.
func (m *Module) watchLoop(ctx context.Context) {
	signal := make(chan struct{}, 1)
	notifyChange := func() {
		select {
		case signal <- struct{}{}:
		default:
		}
	}
	go func() {
		err := watchUevents(ctx, func(ev sc.Uevent) {
			if ev.Subsystem != "block" || hiddenName(ev.DevName) {
				return
			}
			switch ev.Action {
			case "add", "remove", "change":
				notifyChange()
			}
		})
		if err != nil && ctx.Err() == nil {
			slog.Info("çekirdek aygıt olayları dinlenemiyor, diskler yoklamayla izlenecek", "error", err.Error())
		}
	}()

	known := map[string]bool{}
	for _, n := range m.sys.DiskNames() {
		known[n] = true
	}
	last := m.fingerprint()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	debounce := time.NewTimer(time.Hour)
	debounce.Stop()
	defer debounce.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-signal:
			// udev needs a moment to probe the new device.
			debounce.Reset(1500 * time.Millisecond)
		case <-ticker.C:
			if fp := m.fingerprint(); fp != last {
				last = fp
				known = m.devicesChanged(ctx, known)
			}
		case <-debounce.C:
			last = m.fingerprint()
			known = m.devicesChanged(ctx, known)
		}
	}
}

// devicesChanged refreshes the inventory, tells the UI and announces newly
// attached removable disks. It returns the new set of disk names.
func (m *Module) devicesChanged(ctx context.Context, known map[string]bool) map[string]bool {
	m.invalidate()
	current := map[string]bool{}
	var added []string
	for _, n := range m.sys.DiskNames() {
		current[n] = true
		if !known[n] && !hiddenName(n) {
			added = append(added, n)
		}
	}
	m.events.publish("devices")
	if len(added) == 0 {
		return current
	}
	snap, err := m.snapshot(ctx, 0)
	if err != nil {
		return current
	}
	for _, name := range added {
		d, _ := snap.find(name)
		if d == nil || d.Type != "disk" {
			continue
		}
		if (d.Removable || d.Hotplug || d.Transport == "usb") && m.deps.Settings.Bool(KeyNotifyRemovable) {
			label := strings.TrimSpace(d.Vendor + " " + d.Model)
			if label == "" {
				label = "Çıkarılabilir disk"
			}
			m.deps.Notify.Publish(ctx, notify.Info, source, "Çıkarılabilir disk takıldı",
				label+" ("+d.Name+", "+humanSize(d.Size)+") sunucuya takıldı. Depolama sayfasından bağlayabilirsiniz.")
		}
		if sc.ValidDiskName(d.Name) {
			dev := *d
			go func() {
				// A disk that was just attached is awake anyway.
				cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				defer cancel()
				select {
				case <-cctx.Done():
					return
				case <-time.After(5 * time.Second):
				}
				if _, err := m.smartCheck(cctx, dev, "active"); err != nil {
					slog.Warn("SMART bilgisi okunamadı", "device", dev.Name, "error", err.Error())
				}
			}()
		}
	}
	return current
}

// humanSize formats bytes for notification texts.
func humanSize(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	whole := int64(v)
	frac := int64((v - float64(whole)) * 10)
	s := itoa(whole)
	if i > 0 && frac > 0 && whole < 100 {
		s += "," + itoa(frac)
	}
	return s + " " + units[i]
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
