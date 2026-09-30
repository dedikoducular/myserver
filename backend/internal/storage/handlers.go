package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"myserver/internal/auth"
	"myserver/internal/httpx"
	"myserver/internal/privileged"
	"myserver/internal/settings"
	sc "myserver/internal/storage/storagecheck"
)

const inventoryTTL = 3 * time.Second

func lsblkError(err error) error {
	return httpx.Unavailable("lsblk_failed", "Disk bilgileri okunamadı (lsblk çalıştırılamadı).").Wrap(err)
}

// FormatOption is one filesystem offered for formatting.
type FormatOption struct {
	Type      string `json:"type"`
	MaxLabel  int    `json:"max_label"`
	Installed bool   `json:"installed"`
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

func formatOptions() []FormatOption {
	out := make([]FormatOption, 0, len(sc.FormatFilesystems))
	for _, f := range sc.FormatFilesystems {
		out = append(out, FormatOption{Type: f.Type, MaxLabel: f.MaxLabel, Installed: fileExists(f.Binary)})
	}
	return out
}

func (m *Module) handleDisks(w http.ResponseWriter, r *http.Request) error {
	snap, err := m.snapshot(r.Context(), inventoryTTL)
	if err != nil {
		return lsblkError(err)
	}
	all := r.URL.Query().Get("all") == "true"
	httpx.OK(w, map[string]any{
		"devices": snap.visible(all),
		// False when the device holding "/" could not be determined; every
		// device is then treated as protected.
		"system_detection":  snap.detection,
		"persistent_mounts": snap.persistent,
		"smart_installed":   fileExists(sc.SmartctlBinary),
		"filesystems":       formatOptions(),
		"temp_warning":      m.tempWarning(),
		"generated_at":      snap.at.Unix(),
	})
	return nil
}

// handleEvents pushes "devices changed" events over SSE.
func (m *Module) handleEvents(w http.ResponseWriter, r *http.Request) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return httpx.Internal(fmt.Errorf("response writer does not support streaming"))
	}
	ch, cancel := m.events.subscribe()
	defer cancel()
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": bağlandı\n\n")
	flusher.Flush()
	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return nil
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return nil
			}
			flusher.Flush()
		case reason := <-ch:
			b, _ := json.Marshal(map[string]any{"reason": reason, "at": time.Now().Unix()})
			if _, err := fmt.Fprintf(w, "event: devices\ndata: %s\n\n", b); err != nil {
				return nil
			}
			flusher.Flush()
		}
	}
}

/* ---------- SMART ---------- */

func (m *Module) smartDisk(r *http.Request) (Device, error) {
	name := r.PathValue("device")
	if !sc.ValidDiskName(name) {
		return Device{}, httpx.BadRequest("Disk adı geçersiz.")
	}
	snap, err := m.snapshot(r.Context(), inventoryTTL)
	if err != nil {
		return Device{}, lsblkError(err)
	}
	d, _ := snap.find(name)
	if d == nil {
		return Device{}, httpx.NotFound("Disk bulunamadı.")
	}
	if d.Type != "disk" {
		return Device{}, httpx.BadRequest("SMART bilgisi yalnızca diskin tamamı için okunabilir.")
	}
	return *d, nil
}

func smartError(err error) error {
	return httpx.NewError(http.StatusBadGateway, "smart_failed",
		privileged.UserMessage(err, "SMART bilgisi okunamadı.")).Wrap(err)
}

func (m *Module) handleSmart(w http.ResponseWriter, r *http.Request) error {
	d, err := m.smartDisk(r)
	if err != nil {
		return err
	}
	if rep, ok := m.smart.get(d.Name); ok && time.Since(time.Unix(rep.CheckedAt, 0)) < smartCacheTTL {
		httpx.OK(w, rep)
		return nil
	}
	// Reading never wakes a sleeping disk; an administrator can force it.
	rep, err := m.smartCheck(r.Context(), d, "standby")
	if err != nil {
		return smartError(err)
	}
	httpx.OK(w, rep)
	return nil
}

func (m *Module) handleSmartRefresh(w http.ResponseWriter, r *http.Request) error {
	d, err := m.smartDisk(r)
	if err != nil {
		return err
	}
	rep, err := m.smartCheck(r.Context(), d, "active")
	if err != nil {
		return smartError(err)
	}
	httpx.OK(w, rep)
	return nil
}

func (m *Module) handleSmartTest(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Type string `json:"type"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	if req.Type != "short" && req.Type != "long" {
		return httpx.BadRequest("Test türü 'short' veya 'long' olmalıdır.")
	}
	d, err := m.smartDisk(r)
	if err != nil {
		return err
	}
	m.smart.mu.Lock()
	devType := "auto"
	if m.smart.sat[d.Name] {
		devType = "sat"
	}
	m.smart.mu.Unlock()
	actor := auth.ActorFrom(r)
	if _, err := m.deps.Priv.Run(r.Context(), "storage-smart-test", d.Name, devType, req.Type); err != nil {
		m.deps.Audit.Log(r.Context(), actor, "storage.smart_test", d.Path, "başarısız", false)
		return httpx.NewError(http.StatusBadGateway, "smart_test_failed",
			privileged.UserMessage(err, "SMART testi başlatılamadı.")).Wrap(err)
	}
	m.deps.Audit.Log(r.Context(), actor, "storage.smart_test", d.Path, req.Type, true)
	httpx.OK(w, map[string]any{"started": true, "type": req.Type})
	return nil
}

/* ---------- mount / unmount ---------- */

// opContext detaches a state-changing operation from the request, so that
// a closed browser tab cannot interrupt it half way.
func opContext(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), d)
}

// target looks a device up for a state-changing request. The checks made
// here give the user a precise message early; the helper repeats them.
func (m *Module) target(r *http.Request, name string, valid func(string) bool) (*snapshot, *Device, *Device, error) {
	if !valid(name) {
		return nil, nil, nil, httpx.BadRequest("Disk adı geçersiz.")
	}
	snap, err := m.snapshot(r.Context(), 0)
	if err != nil {
		return nil, nil, nil, lsblkError(err)
	}
	d, root := snap.find(name)
	if d == nil {
		return nil, nil, nil, httpx.NotFound("Disk bulunamadı. Çıkarılmış olabilir.")
	}
	if !snap.detection {
		return nil, nil, nil, httpx.Conflict("Sistem diski belirlenemedi; güvenlik nedeniyle disk işlemleri kapalı.")
	}
	if d.System {
		return nil, nil, nil, httpx.NewError(http.StatusForbidden, "system_device",
			d.Name+" bir sistem diskine ait. Sistem diskleri panelden değiştirilemez.")
	}
	return snap, d, root, nil
}

func (m *Module) changed(reason string) {
	m.invalidate()
	m.events.publish(reason)
}

func (m *Module) handleMount(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Device     string `json:"device"`
		Name       string `json:"name"`
		Persistent bool   `json:"persistent"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	_, d, _, err := m.target(r, req.Device, sc.ValidMountDevice)
	if err != nil {
		return err
	}
	if req.Name == "" {
		req.Name = d.SuggestedName
	}
	if !sc.ValidMountName(req.Name) {
		return httpx.BadRequest("Bağlama adı geçersiz. Yalnızca harf, rakam, '-' ve '_' kullanılabilir.")
	}
	if len(d.MountPoints) > 0 {
		return httpx.Conflict("Disk zaten bağlı.")
	}
	if d.FSType == "" {
		return httpx.BadRequest("Diskte tanınan bir dosya sistemi yok. Önce biçimlendirin.")
	}
	if !sc.MountableFS(d.FSType) {
		return httpx.BadRequest("Bu dosya sistemi desteklenmiyor: " + d.FSType)
	}
	persist := "0"
	if req.Persistent {
		persist = "1"
	}
	ctx, cancel := opContext(r, 2*time.Minute)
	defer cancel()
	actor := auth.ActorFrom(r)
	out, err := m.deps.Priv.Run(ctx, "storage-mount", d.Name, req.Name, persist)
	if err != nil {
		m.deps.Audit.Log(ctx, actor, "storage.mount", d.Path, "başarısız", false)
		m.changed("mount")
		return httpx.NewError(http.StatusBadGateway, "mount_failed",
			privileged.UserMessage(err, "Disk bağlanamadı.")).Wrap(err)
	}
	var res struct {
		Device     string `json:"device"`
		MountPoint string `json:"mountpoint"`
		FSType     string `json:"fstype"`
		Options    string `json:"options"`
		Persistent bool   `json:"persistent"`
		Warning    string `json:"warning"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		m.changed("mount")
		return httpx.Internal(err)
	}
	detail := res.MountPoint
	if res.Persistent {
		detail += " (kalıcı)"
	}
	m.deps.Audit.Log(ctx, actor, "storage.mount", d.Path, detail, true)
	m.changed("mount")
	roots := m.deps.Settings.Strings(settings.KeyAllowedRoots)
	httpx.OK(w, map[string]any{
		"device": res.Device, "mountpoint": res.MountPoint, "fstype": res.FSType,
		"options": res.Options, "persistent": res.Persistent, "warning": res.Warning,
		"browsable": insideRoots(res.MountPoint, roots),
	})
	return nil
}

func (m *Module) handleUnmount(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Device string `json:"device"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	_, d, _, err := m.target(r, req.Device, sc.ValidMountDevice)
	if err != nil {
		return err
	}
	if len(d.MountPoints) == 0 {
		return httpx.Conflict("Disk bağlı değil.")
	}
	ctx, cancel := opContext(r, 2*time.Minute)
	defer cancel()
	actor := auth.ActorFrom(r)
	if _, err := m.deps.Priv.Run(ctx, "storage-unmount", d.Name); err != nil {
		m.deps.Audit.Log(ctx, actor, "storage.unmount", d.Path, "başarısız", false)
		m.changed("mount")
		return httpx.NewError(http.StatusConflict, "unmount_failed",
			privileged.UserMessage(err, "Disk ayrılamadı.")).Wrap(err)
	}
	m.deps.Audit.Log(ctx, actor, "storage.unmount", d.Path, d.MountPoints[0].Path, true)
	m.changed("mount")
	httpx.OK(w, map[string]any{"device": d.Name})
	return nil
}

func (m *Module) handlePersistAdd(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Device string `json:"device"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	_, d, _, err := m.target(r, req.Device, sc.ValidMountDevice)
	if err != nil {
		return err
	}
	if len(d.MountPoints) == 0 {
		return httpx.Conflict("Disk bağlı değil. Önce diski bağlayın.")
	}
	ctx, cancel := opContext(r, 2*time.Minute)
	defer cancel()
	actor := auth.ActorFrom(r)
	if _, err := m.deps.Priv.Run(ctx, "storage-persist-add", d.Name); err != nil {
		m.deps.Audit.Log(ctx, actor, "storage.persist_add", d.Path, "başarısız", false)
		return httpx.NewError(http.StatusBadGateway, "persist_failed",
			privileged.UserMessage(err, "Kalıcı bağlama kaydı yazılamadı.")).Wrap(err)
	}
	m.deps.Audit.Log(ctx, actor, "storage.persist_add", d.Path, d.MountPoints[0].Path, true)
	m.changed("mount")
	httpx.OK(w, map[string]any{"device": d.Name, "persistent": true})
	return nil
}

func (m *Module) handlePersistRemove(w http.ResponseWriter, r *http.Request) error {
	uuid := r.PathValue("uuid")
	if !sc.ValidUUID(uuid) {
		return httpx.BadRequest("UUID geçersiz.")
	}
	ctx, cancel := opContext(r, 2*time.Minute)
	defer cancel()
	actor := auth.ActorFrom(r)
	if _, err := m.deps.Priv.Run(ctx, "storage-persist-remove", uuid); err != nil {
		m.deps.Audit.Log(ctx, actor, "storage.persist_remove", "UUID="+uuid, "başarısız", false)
		return httpx.NewError(http.StatusBadGateway, "persist_failed",
			privileged.UserMessage(err, "Kalıcı bağlama kaydı kaldırılamadı.")).Wrap(err)
	}
	m.deps.Audit.Log(ctx, actor, "storage.persist_remove", "UUID="+uuid, "", true)
	m.changed("mount")
	httpx.OK(w, map[string]any{"uuid": uuid, "persistent": false})
	return nil
}

/* ---------- format ---------- */

// FormatContent is one existing partition or volume that will be lost.
type FormatContent struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Size        int64    `json:"size"`
	FSType      string   `json:"fstype"`
	Label       string   `json:"label"`
	MountPoints []string `json:"mountpoints"`
}

func formatContents(d *Device) []FormatContent {
	out := []FormatContent{}
	var walk func(n *Device)
	walk = func(n *Device) {
		mps := []string{}
		for _, mp := range n.MountPoints {
			mps = append(mps, mp.Path)
		}
		out = append(out, FormatContent{Name: n.Name, Type: n.Type, Size: n.Size, FSType: n.FSType, Label: n.Label, MountPoints: mps})
		for i := range n.Children {
			walk(&n.Children[i])
		}
	}
	for i := range d.Children {
		walk(&d.Children[i])
	}
	return out
}

// formatTarget applies the checks shared by both steps.
func (m *Module) formatTarget(r *http.Request, name string) (*Device, *Device, error) {
	_, d, root, err := m.target(r, name, sc.ValidDevice)
	if err != nil {
		return nil, nil, err
	}
	if d.Type != "disk" && d.Type != "part" {
		return nil, nil, httpx.BadRequest("Bu aygıt türü biçimlendirilemez.")
	}
	if root.System {
		return nil, nil, httpx.NewError(http.StatusForbidden, "system_device",
			root.Name+" bir sistem diski. Sistem diskleri biçimlendirilemez.")
	}
	if d.ReadOnly {
		return nil, nil, httpx.Conflict("Disk yazmaya karşı korumalı.")
	}
	var busy func(n *Device) error
	busy = func(n *Device) error {
		if n.System {
			return httpx.NewError(http.StatusForbidden, "system_device",
				n.Name+" sistem tarafından kullanılıyor. Bu disk biçimlendirilemez.")
		}
		if len(n.MountPoints) > 0 {
			return httpx.Conflict(n.Name + " bağlı durumda (" + n.MountPoints[0].Path + "). Biçimlendirmeden önce diski ayırın.")
		}
		if n.Swap {
			return httpx.Conflict(n.Name + " takas alanı olarak kullanılıyor.")
		}
		if n.Type != "disk" && n.Type != "part" {
			return httpx.Conflict(n.Name + " etkin bir birim (LVM, RAID veya şifreli birim). Önce bu birimi devre dışı bırakın.")
		}
		for i := range n.Children {
			if err := busy(&n.Children[i]); err != nil {
				return err
			}
		}
		return nil
	}
	if err := busy(d); err != nil {
		return nil, nil, err
	}
	if d.Size <= 0 {
		return nil, nil, httpx.Conflict("Disk boyutu okunamadı.")
	}
	if !sc.ValidSerial(root.Serial) {
		return nil, nil, httpx.Conflict("Diskin seri numarası okunamadı.")
	}
	return d, root, nil
}

// handleFormatPrepare is step 1: it describes exactly what would be
// destroyed and issues a short-lived single-use token bound to the device
// name, serial number and size.
func (m *Module) handleFormatPrepare(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Device string `json:"device"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	d, root, err := m.formatTarget(r, req.Device)
	if err != nil {
		return err
	}
	actor := auth.ActorFrom(r)
	token, expires, err := m.tokens.issue(formatTicket{
		device: d.Name, serial: root.Serial, size: d.Size, username: actor.Username,
	})
	if err != nil {
		return httpx.Internal(err)
	}
	httpx.OK(w, map[string]any{
		"token":      token,
		"expires_at": expires.Unix(),
		"device":     d.Name,
		"path":       d.Path,
		"type":       d.Type,
		"whole_disk": d.Type == "disk",
		"size":       d.Size,
		"fstype":     d.FSType,
		"label":      d.Label,
		"disk": map[string]any{
			"name": root.Name, "model": root.Model, "vendor": root.Vendor, "serial": root.Serial,
			"size": root.Size, "transport": root.Transport, "removable": root.Removable || root.Hotplug,
		},
		"contents":    formatContents(d),
		"filesystems": formatOptions(),
		"sfdisk":      fileExists("/usr/sbin/sfdisk"),
	})
	return nil
}

// handleFormat is step 2.
func (m *Module) handleFormat(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Device      string `json:"device"`
		Token       string `json:"token"`
		ConfirmName string `json:"confirm_name"`
		FSType      string `json:"fstype"`
		Label       string `json:"label"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	actor := auth.ActorFrom(r)
	// The token is destroyed by being presented, valid or not.
	ticket, ok := m.tokens.redeem(req.Token)
	if !ok {
		return httpx.NewError(http.StatusForbidden, "confirmation_invalid",
			"Onay süresi dolmuş veya geçersiz. Biçimlendirme işlemini baştan başlatın.")
	}
	if !sc.ValidDevice(req.Device) {
		return httpx.BadRequest("Disk adı geçersiz.")
	}
	if !ticket.boundTo(req.Device, actor.Username) {
		return httpx.NewError(http.StatusForbidden, "confirmation_invalid",
			"Onay bu disk için verilmemiş. Biçimlendirme işlemini baştan başlatın.")
	}
	if req.ConfirmName != req.Device {
		return httpx.BadRequest("Onay için yazılan disk adı uyuşmuyor.")
	}
	fs, ok := sc.LookupFormatFS(req.FSType)
	if !ok {
		return httpx.BadRequest("Dosya sistemi türü geçersiz.")
	}
	if !sc.ValidLabel(fs, req.Label) {
		return httpx.BadRequest("Etiket geçersiz. En fazla " + strconv.Itoa(fs.MaxLabel) +
			" karakter; yalnızca harf, rakam, '-' ve '_' kullanılabilir.")
	}
	if !fileExists(fs.Binary) {
		return httpx.Conflict(fs.Type + " dosya sistemi için gerekli araç sunucuda kurulu değil.")
	}
	d, root, err := m.formatTarget(r, req.Device)
	if err != nil {
		return err
	}
	if !ticket.sameDisk(root.Serial, d.Size) {
		m.deps.Audit.Log(r.Context(), actor, "storage.format", d.Path, "disk onaydan sonra değişmiş; işlem yapılmadı", false)
		return httpx.Conflict("Disk, onay verildikten sonra değişmiş. İşlem yapılmadı; listeyi yenileyip tekrar deneyin.")
	}
	ctx, cancel := opContext(r, 35*time.Minute)
	defer cancel()
	detail := fs.Type
	if req.Label != "" {
		detail += ", etiket: " + req.Label
	}
	if root.Serial != "" {
		detail += ", seri: " + root.Serial
	}
	// The helper receives the size and serial the user confirmed and
	// compares them with what it finds itself.
	out, err := m.deps.Priv.Run(ctx, "storage-format", d.Name, fs.Type, req.Label,
		strconv.FormatInt(ticket.size, 10), ticket.serial)
	if err != nil {
		m.deps.Audit.Log(ctx, actor, "storage.format", d.Path, detail+" — başarısız", false)
		m.changed("devices")
		return httpx.NewError(http.StatusBadGateway, "format_failed",
			privileged.UserMessage(err, "Disk biçimlendirilemedi.")).Wrap(err)
	}
	var res struct {
		Device    string `json:"device"`
		Partition string `json:"partition"`
		FSType    string `json:"fstype"`
		Label     string `json:"label"`
	}
	_ = json.Unmarshal(out, &res)
	m.deps.Audit.Log(ctx, actor, "storage.format", d.Path, detail, true)
	m.changed("devices")
	httpx.OK(w, map[string]any{
		"device": d.Name, "partition": res.Partition, "fstype": fs.Type, "label": req.Label,
	})
	return nil
}

/* ---------- file manager roots ---------- */

var forbiddenRoots = []string{"/", "/etc", "/proc", "/sys", "/dev", "/boot", "/root", "/run", "/var", "/usr", "/bin", "/sbin", "/lib", "/lib64"}

// handleAllowRoot adds the mount point of a data disk to the file
// manager's allowed roots.
func (m *Module) handleAllowRoot(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Path string `json:"path"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	p := req.Path
	if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\x00\n") || strings.Contains(p, "/../") ||
		strings.HasSuffix(p, "/..") || len(p) > 1024 || (len(p) > 1 && strings.HasSuffix(p, "/")) {
		return httpx.BadRequest("Dizin yolu geçersiz.")
	}
	for _, f := range forbiddenRoots {
		if p == f || (f != "/" && strings.HasPrefix(p, f+"/")) {
			return httpx.BadRequest("Bu dizin güvenlik nedeniyle dosya yöneticisine açılamaz.")
		}
	}
	snap, err := m.snapshot(r.Context(), 0)
	if err != nil {
		return lsblkError(err)
	}
	found := false
	var walk func(d *Device)
	walk = func(d *Device) {
		for _, mp := range d.MountPoints {
			if mp.Path == p && !d.System {
				found = true
			}
		}
		for i := range d.Children {
			walk(&d.Children[i])
		}
	}
	for i := range snap.devices {
		walk(&snap.devices[i])
	}
	if !found {
		return httpx.BadRequest("Bu yol, bağlı bir veri diskinin bağlama noktası değil.")
	}
	roots := m.deps.Settings.Strings(settings.KeyAllowedRoots)
	if !insideRoots(p, roots) {
		if len(roots) >= 32 {
			return httpx.Conflict("En fazla 32 dizin tanımlanabilir.")
		}
		roots = append(roots, p)
		if err := m.deps.Settings.SetStrings(r.Context(), settings.KeyAllowedRoots, roots); err != nil {
			return httpx.Internal(err)
		}
		m.deps.Audit.Log(r.Context(), auth.ActorFrom(r), "storage.allow_root", p, "", true)
		m.changed("mount")
	}
	httpx.OK(w, map[string]any{"path": p, "allowed_roots": roots})
	return nil
}
