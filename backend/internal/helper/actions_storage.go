package helper

// Storage actions. Every action re-validates its arguments and determines
// the system devices again from /proc and /sys immediately before acting;
// nothing the caller says about a device is trusted.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	sc "myserver/internal/storage/storagecheck"
)

const (
	storageMount    = "/usr/bin/mount"
	storageUmount   = "/usr/bin/umount"
	storageWipefs   = "/usr/sbin/wipefs"
	storageSfdisk   = "/usr/sbin/sfdisk"
	storageUdevadm  = "/usr/bin/udevadm"
	storageFindmnt  = "/usr/bin/findmnt"
	storageSystemd  = "/usr/bin/systemctl"
	storageLockFile = "/run/myserver-storage.lock"
	storageFstabBak = "/etc/fstab.myserver.bak"
)

func init() {
	Register("storage-smart", storageSmart)
	Register("storage-smart-test", storageSmartTest)
	Register("storage-mount", storageMountAction)
	Register("storage-unmount", storageUnmountAction)
	Register("storage-persist-add", storagePersistAdd)
	Register("storage-persist-remove", storagePersistRemove)
	Register("storage-format", storageFormat)
}

func storageExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// storageRun runs a command and returns its exit code and combined output.
func storageRun(ctx context.Context, stdin io.Reader, name string, args ...string) (int, string, error) {
	cmd := Command(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdin = stdin
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	text := strings.TrimSpace(out.String())
	if len(text) > 4000 {
		text = text[len(text)-4000:]
	}
	if err == nil {
		return 0, text, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), text, nil
	}
	return -1, text, err
}

func storageLsblk(ctx context.Context) ([]*sc.BlockDevice, error) {
	var lastErr error
	for _, legacy := range []bool{false, true} {
		cmd := Command(ctx, sc.LsblkBinary, sc.LsblkArgs(legacy)...)
		out, err := cmd.Output()
		if err != nil {
			lastErr = err
			continue
		}
		return sc.ParseLsblk(out)
	}
	return nil, lastErr
}

// storageState is the helper's own view of the machine.
type storageState struct {
	sys    sc.Sys
	prot   sc.Protection
	mounts []sc.Mount
	swaps  []string
}

func storageLoad() (*storageState, error) {
	sys := sc.DefaultSys
	// PID 1's table is read as well, in case the panel service runs in its
	// own mount namespace.
	prot, mounts, swaps, err := sc.LoadState(sys, "/proc/self/mountinfo", "/proc/1/mountinfo")
	if err != nil {
		return nil, Userf("Bağlama tablosu okunamadı; güvenlik nedeniyle işlem yapılmadı.")
	}
	return &storageState{sys: sys, prot: prot, mounts: mounts, swaps: swaps}, nil
}

// guard refuses system devices. It is the single place every destructive
// action passes through.
func (st *storageState) guard(name string) error {
	if !st.prot.OK {
		return Userf("Sistem diski belirlenemedi; güvenlik nedeniyle işlem yapılmadı.")
	}
	if protected, reason := st.prot.Protected(st.sys, name); protected {
		if reason != "" {
			return Userf("%s bir sistem diskine ait (%s). Sistem diskleri panelden değiştirilemez.", name, reason)
		}
		return Userf("%s bir sistem diskine ait. Sistem diskleri panelden değiştirilemez.", name)
	}
	return nil
}

func storageLock() (func(), error) {
	unlock, err := sc.Lock(storageLockFile)
	if err != nil {
		return nil, Userf("Başka bir disk işlemi sürüyor. Lütfen bitmesini bekleyin.")
	}
	return unlock, nil
}

/* ---------- SMART ---------- */

func storageSmartArgs(args []string) (devPath string, typeArgs []string, err error) {
	if !sc.ValidDiskName(args[0]) {
		return "", nil, Userf("Disk adı geçersiz.")
	}
	// Every argument is validated before the system is looked at.
	switch args[1] {
	case "auto":
	case "sat":
		typeArgs = []string{"-d", "sat"}
	default:
		return "", nil, Userf("Aygıt türü geçersiz.")
	}
	sys := sc.DefaultSys
	if sys.IsPartition(args[0]) {
		return "", nil, Userf("SMART bilgisi yalnızca diskin tamamı için okunabilir.")
	}
	p, ok := sys.VerifyNode(args[0])
	if !ok {
		return "", nil, Userf("Disk bulunamadı: %s", args[0])
	}
	return p, typeArgs, nil
}

func storageSmart(ctx context.Context, args []string, _ io.Reader, stdout io.Writer) error {
	if err := ArgCount(args, 3); err != nil {
		return err
	}
	cmdArgs := []string{"--json", "-a"}
	switch args[2] {
	case "active":
	case "standby":
		// Do not spin up a sleeping disk for a periodic check.
		cmdArgs = append(cmdArgs, "-n", "standby")
	default:
		return Userf("Sorgu kipi geçersiz.")
	}
	devPath, typeArgs, err := storageSmartArgs(args)
	if err != nil {
		return err
	}
	res := sc.HelperSmartOutput{Installed: storageExists(sc.SmartctlBinary)}
	if res.Installed {
		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		cmdArgs = append(cmdArgs, typeArgs...)
		cmdArgs = append(cmdArgs, "--", devPath)
		cmd := Command(ctx, sc.SmartctlBinary, cmdArgs...)
		out, err := cmd.Output()
		var ee *exec.ExitError
		switch {
		case err == nil:
		case errors.As(err, &ee):
			// The exit status is a bit mask; the JSON is usually valid.
			res.ExitStatus = ee.ExitCode()
		default:
			return Userf("smartctl çalıştırılamadı.")
		}
		if json.Valid(out) {
			res.Output = out
		}
	}
	return json.NewEncoder(stdout).Encode(res)
}

func storageSmartTest(ctx context.Context, args []string, _ io.Reader, stdout io.Writer) error {
	if err := ArgCount(args, 3); err != nil {
		return err
	}
	if args[2] != "short" && args[2] != "long" {
		return Userf("Test türü geçersiz.")
	}
	devPath, typeArgs, err := storageSmartArgs(args)
	if err != nil {
		return err
	}
	if !storageExists(sc.SmartctlBinary) {
		return Userf("smartmontools kurulu değil.")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmdArgs := append([]string{"-t", args[2]}, typeArgs...)
	cmdArgs = append(cmdArgs, "--", devPath)
	code, _, err := storageRun(ctx, nil, sc.SmartctlBinary, cmdArgs...)
	if err != nil {
		return Userf("smartctl çalıştırılamadı.")
	}
	if code&(sc.SmartExitCmdLine|sc.SmartExitOpenFailed|sc.SmartExitCmdFailed) != 0 {
		return Userf("SMART testi başlatılamadı. Disk bu testi desteklemiyor olabilir.")
	}
	_, err = io.WriteString(stdout, "{\"started\":true}\n")
	return err
}

/* ---------- fstab ---------- */

func storageWriteAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".myserver-tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
			_ = os.Remove(name)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	ok = true
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// storageFstabUpdate replaces /etc/fstab atomically: backup copy, temporary
// file in /etc, fsync, validation with findmnt, rename.
func storageFstabUpdate(ctx context.Context, edit func(old []byte) ([]byte, error)) error {
	fi, err := os.Lstat(sc.FstabPath)
	if err != nil || !fi.Mode().IsRegular() {
		return Userf("/etc/fstab okunamadı veya normal bir dosya değil.")
	}
	old, err := os.ReadFile(sc.FstabPath)
	if err != nil {
		return Userf("/etc/fstab okunamadı.")
	}
	updated, err := edit(old)
	if err != nil {
		return err
	}
	if bytes.Equal(old, updated) {
		return nil
	}
	if err := storageWriteAtomic(storageFstabBak, old, 0o644); err != nil {
		return Userf("/etc/fstab yedeği alınamadı; dosya değiştirilmedi.")
	}
	tmp, err := os.CreateTemp("/etc", ".fstab.myserver-*")
	if err != nil {
		return Userf("/etc içinde geçici dosya oluşturulamadı.")
	}
	name := tmp.Name()
	done := false
	defer func() {
		if !done {
			_ = os.Remove(name)
		}
	}()
	_, werr := tmp.Write(updated)
	if werr == nil {
		werr = tmp.Chmod(0o644)
	}
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return Userf("/etc/fstab yazılamadı.")
	}
	if storageExists(storageFindmnt) {
		vctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		// Only judge the new file when the current one is itself valid,
		// so that an old unrelated mistake does not block the change.
		if code, _, err := storageRun(vctx, nil, storageFindmnt, "--verify", "--tab-file", sc.FstabPath); err == nil && code == 0 {
			if code, _, err := storageRun(vctx, nil, storageFindmnt, "--verify", "--tab-file", name); err != nil || code != 0 {
				return Userf("Yeni /etc/fstab doğrulamadan geçmedi; dosya değiştirilmedi.")
			}
		}
	}
	if err := os.Rename(name, sc.FstabPath); err != nil {
		return Userf("/etc/fstab güncellenemedi.")
	}
	done = true
	if d, err := os.Open("/etc"); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	if storageExists(storageSystemd) {
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		_, _, _ = storageRun(rctx, nil, storageSystemd, "daemon-reload")
	}
	return nil
}

func storageFstabAdd(ctx context.Context, uuid, mountPoint, fstype, options string) error {
	return storageFstabUpdate(ctx, func(old []byte) ([]byte, error) {
		out, err := sc.FstabAdd(old, uuid, mountPoint, fstype, options)
		switch {
		case errors.Is(err, sc.ErrFstabConflict):
			return nil, Userf("/etc/fstab içinde bu disk veya bağlama noktası için panelin yazmadığı bir kayıt var. Kayıt değiştirilmedi.")
		case err != nil:
			return nil, Userf("Kalıcı bağlama kaydı geçersiz.")
		}
		return out, nil
	})
}

func storageFstabHasMountPoint(mountPoint string) bool {
	b, err := os.ReadFile(sc.FstabPath)
	if err != nil {
		return true // unknown: keep the directory
	}
	for _, e := range sc.ParseFstab(b) {
		if e.MountPoint == mountPoint {
			return true
		}
	}
	return false
}

/* ---------- mount ---------- */

type storageMountResult struct {
	Device     string `json:"device"`
	MountPoint string `json:"mountpoint"`
	FSType     string `json:"fstype"`
	Options    string `json:"options"`
	Persistent bool   `json:"persistent"`
	Warning    string `json:"warning"`
}

func storageMountMessage(output string) string {
	o := strings.ToLower(output)
	switch {
	case strings.Contains(o, "unknown filesystem type"):
		return "Bu dosya sistemi için sürücü sunucuda kurulu değil."
	case strings.Contains(o, "wrong fs type"), strings.Contains(o, "bad superblock"):
		return "Dosya sistemi tanınmadı veya hasarlı. Diskin denetlenmesi gerekebilir."
	case strings.Contains(o, "already mounted"):
		return "Disk zaten bağlı."
	case strings.Contains(o, "write-protected"), strings.Contains(o, "read-only"):
		return "Disk yazmaya karşı korumalı."
	case strings.Contains(o, "unclean"), strings.Contains(o, "dirty"), strings.Contains(o, "hibernat"):
		return "Dosya sistemi düzgün kapatılmamış. Diski kullanıldığı cihazda güvenli şekilde çıkarıp tekrar deneyin."
	}
	return "Disk bağlanamadı."
}

func storageMountAction(ctx context.Context, args []string, _ io.Reader, stdout io.Writer) error {
	if err := ArgCount(args, 3); err != nil {
		return err
	}
	dev, name := args[0], args[1]
	if !sc.ValidMountDevice(dev) {
		return Userf("Disk adı geçersiz.")
	}
	if !sc.ValidMountName(name) {
		return Userf("Bağlama adı geçersiz. Yalnızca harf, rakam, '-' ve '_' kullanılabilir.")
	}
	if args[2] != "0" && args[2] != "1" {
		return Userf("Kalıcılık değeri geçersiz.")
	}
	persist := args[2] == "1"
	unlock, err := storageLock()
	if err != nil {
		return err
	}
	defer unlock()

	st, err := storageLoad()
	if err != nil {
		return err
	}
	devPath, ok := st.sys.VerifyNode(dev)
	if !ok {
		return Userf("Disk bulunamadı: %s", dev)
	}
	if err := st.guard(dev); err != nil {
		return err
	}
	if len(sc.MountsOf(st.sys, st.mounts, dev)) > 0 {
		return Userf("Disk zaten bağlı.")
	}
	if sc.SwapOn(st.sys, st.swaps, dev) || len(st.sys.Holders(dev)) > 0 {
		return Userf("Disk başka bir sistem bileşeni tarafından kullanılıyor.")
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	devs, err := storageLsblk(ctx)
	if err != nil {
		return Userf("Disk bilgileri okunamadı.")
	}
	node, root := sc.FindDevice(devs, dev)
	if node == nil {
		return Userf("Disk bulunamadı: %s", dev)
	}
	if node.FSType == "" {
		return Userf("Diskte tanınan bir dosya sistemi yok. Önce biçimlendirin.")
	}
	if !sc.MountableFS(node.FSType) {
		return Userf("Bu dosya sistemi desteklenmiyor: %s", node.FSType)
	}
	if persist && !sc.ValidUUID(node.UUID) {
		return Userf("Diskin UUID değeri okunamadı; kalıcı bağlama yapılamaz.")
	}
	removable := sc.IsRemovable(root)
	base := sc.MountBase(removable)
	options := sc.MountOptions(removable)
	mountPoint := base + "/" + name

	if fi, err := os.Lstat(base); err != nil || !fi.IsDir() {
		return Userf("%s dizini bulunamadı.", base)
	}
	for _, m := range st.mounts {
		if m.MountPoint == mountPoint {
			return Userf("%s konumunda başka bir disk bağlı.", mountPoint)
		}
	}
	created := false
	fi, err := os.Lstat(mountPoint)
	switch {
	case os.IsNotExist(err):
		if err := os.Mkdir(mountPoint, 0o755); err != nil {
			return Userf("%s dizini oluşturulamadı.", mountPoint)
		}
		created = true
	case err != nil:
		return Userf("%s dizini denetlenemedi.", mountPoint)
	case !fi.IsDir():
		return Userf("%s bir dizin değil.", mountPoint)
	default:
		entries, err := os.ReadDir(mountPoint)
		if err != nil {
			return Userf("%s dizini okunamadı.", mountPoint)
		}
		if len(entries) > 0 {
			return Userf("%s dizini boş değil. Başka bir ad seçin.", mountPoint)
		}
	}

	types := []string{node.FSType}
	if node.FSType == "ntfs" || node.FSType == "ntfs3" {
		types = []string{"ntfs3"}
		if storageExists("/usr/sbin/mount.ntfs-3g") || storageExists("/usr/sbin/mount.ntfs") || storageExists("/sbin/mount.ntfs-3g") {
			types = append(types, "ntfs-3g")
		}
	}
	var output, used string
	mounted := false
	for _, t := range types {
		code, out, err := storageRun(ctx, nil, storageMount, "-t", t, "-o", options, "--", devPath, mountPoint)
		if err == nil && code == 0 {
			mounted, used = true, t
			break
		}
		output = out
	}
	if !mounted {
		if created {
			_ = os.Remove(mountPoint)
		}
		return Userf("%s", storageMountMessage(output))
	}
	res := storageMountResult{Device: dev, MountPoint: mountPoint, FSType: used, Options: options}
	if persist {
		fstabType := used
		if fstabType == "ntfs-3g" {
			fstabType = "ntfs"
		}
		if err := storageFstabAdd(ctx, node.UUID, mountPoint, fstabType, options); err != nil {
			var ue *UserError
			if errors.As(err, &ue) {
				res.Warning = "Disk bağlandı ancak kalıcı kayıt yazılamadı: " + ue.Message
			} else {
				res.Warning = "Disk bağlandı ancak kalıcı kayıt yazılamadı."
			}
		} else {
			res.Persistent = true
		}
	}
	return json.NewEncoder(stdout).Encode(res)
}

/* ---------- unmount ---------- */

// storageBusyProcesses lists processes whose working directory, root or an
// open file is inside the mount point. It runs only after a failed unmount.
func storageBusyProcesses(mountPoint string) []string {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	inside := func(p string) bool {
		return p == mountPoint || strings.HasPrefix(p, mountPoint+"/")
	}
	var out []string
	scanned := 0
	for _, e := range entries {
		pid := e.Name()
		if _, err := strconv.Atoi(pid); err != nil {
			continue
		}
		if scanned++; scanned > 8192 || len(out) >= 5 {
			break
		}
		dir := "/proc/" + pid
		hit := false
		for _, l := range []string{"cwd", "root"} {
			if t, err := os.Readlink(dir + "/" + l); err == nil && inside(t) {
				hit = true
			}
		}
		if !hit {
			if fds, err := os.ReadDir(dir + "/fd"); err == nil {
				for i, fd := range fds {
					if i > 2048 {
						break
					}
					if t, err := os.Readlink(dir + "/fd/" + fd.Name()); err == nil && inside(t) {
						hit = true
						break
					}
				}
			}
		}
		if hit {
			comm, _ := os.ReadFile(dir + "/comm")
			name := strings.TrimSpace(string(comm))
			if name == "" {
				name = "?"
			}
			out = append(out, name+" (PID "+pid+")")
		}
	}
	return out
}

func storageUnmountAction(ctx context.Context, args []string, _ io.Reader, stdout io.Writer) error {
	if err := ArgCount(args, 1); err != nil {
		return err
	}
	dev := args[0]
	if !sc.ValidMountDevice(dev) {
		return Userf("Disk adı geçersiz.")
	}
	unlock, err := storageLock()
	if err != nil {
		return err
	}
	defer unlock()
	st, err := storageLoad()
	if err != nil {
		return err
	}
	if !st.sys.Exists(dev) {
		return Userf("Disk bulunamadı: %s", dev)
	}
	if err := st.guard(dev); err != nil {
		return err
	}
	mounts := sc.MountsOf(st.sys, st.mounts, dev)
	if len(mounts) == 0 {
		return Userf("Disk bağlı değil.")
	}
	seen := map[string]bool{}
	var points []string
	for _, m := range mounts {
		if sc.IsCriticalMountPoint(m.MountPoint) {
			return Userf("%s bir sistem bağlama noktasıdır ve ayrılamaz.", m.MountPoint)
		}
		if !strings.HasPrefix(m.MountPoint, "/") || strings.ContainsAny(m.MountPoint, "\x00\n") {
			return Userf("Bağlama noktası geçersiz.")
		}
		if !seen[m.MountPoint] {
			seen[m.MountPoint] = true
			points = append(points, m.MountPoint)
		}
	}
	// Deepest first, so nested mounts of the same device come off in order.
	sort.Slice(points, func(i, j int) bool { return len(points[i]) > len(points[j]) })
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	for _, mp := range points {
		// Never lazy (-l) or forced (-f): a busy disk stays mounted.
		code, out, err := storageRun(ctx, nil, storageUmount, "--", mp)
		if err != nil || code != 0 {
			lo := strings.ToLower(out)
			if strings.Contains(lo, "busy") {
				if procs := storageBusyProcesses(mp); len(procs) > 0 {
					return Userf("Disk kullanımda, ayrılamadı. Kullanan işlemler: %s. Önce bunları kapatın.", strings.Join(procs, ", "))
				}
				return Userf("Disk kullanımda, ayrılamadı. Diskteki açık dosyaları ve bu diski kullanan uygulamaları kapatıp tekrar deneyin.")
			}
			if strings.Contains(lo, "not mounted") {
				continue
			}
			return Userf("Disk ayrılamadı.")
		}
		if sc.ManagedMountPoint(mp) && !storageFstabHasMountPoint(mp) {
			_ = os.Remove(mp) // only succeeds on an empty directory
		}
	}
	return json.NewEncoder(stdout).Encode(map[string]any{"device": dev, "unmounted": points})
}

/* ---------- persistent mounts ---------- */

func storagePersistAdd(ctx context.Context, args []string, _ io.Reader, stdout io.Writer) error {
	if err := ArgCount(args, 1); err != nil {
		return err
	}
	dev := args[0]
	if !sc.ValidMountDevice(dev) {
		return Userf("Disk adı geçersiz.")
	}
	unlock, err := storageLock()
	if err != nil {
		return err
	}
	defer unlock()
	st, err := storageLoad()
	if err != nil {
		return err
	}
	if _, ok := st.sys.VerifyNode(dev); !ok {
		return Userf("Disk bulunamadı: %s", dev)
	}
	if err := st.guard(dev); err != nil {
		return err
	}
	var mountPoint, fstype string
	for _, m := range sc.MountsOf(st.sys, st.mounts, dev) {
		if sc.ManagedMountPoint(m.MountPoint) && m.Root == "/" {
			mountPoint, fstype = m.MountPoint, m.FSType
			break
		}
	}
	if mountPoint == "" {
		return Userf("Disk panel üzerinden bağlanmış değil. Önce diski bağlayın.")
	}
	if fstype == "fuseblk" {
		fstype = "ntfs"
	}
	if !sc.MountableFS(fstype) {
		return Userf("Bu dosya sistemi desteklenmiyor: %s", fstype)
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	devs, err := storageLsblk(ctx)
	if err != nil {
		return Userf("Disk bilgileri okunamadı.")
	}
	node, root := sc.FindDevice(devs, dev)
	if node == nil || !sc.ValidUUID(node.UUID) {
		return Userf("Diskin UUID değeri okunamadı; kalıcı bağlama yapılamaz.")
	}
	if err := storageFstabAdd(ctx, node.UUID, mountPoint, fstype, sc.MountOptions(sc.IsRemovable(root))); err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(map[string]any{"uuid": node.UUID, "mountpoint": mountPoint})
}

func storagePersistRemove(ctx context.Context, args []string, _ io.Reader, stdout io.Writer) error {
	if err := ArgCount(args, 1); err != nil {
		return err
	}
	uuid := args[0]
	if !sc.ValidUUID(uuid) {
		return Userf("UUID geçersiz.")
	}
	unlock, err := storageLock()
	if err != nil {
		return err
	}
	defer unlock()
	removed := false
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	err = storageFstabUpdate(ctx, func(old []byte) ([]byte, error) {
		out, ok := sc.FstabRemove(old, uuid)
		if !ok {
			return nil, Userf("Bu disk için panelin yazdığı bir kalıcı bağlama kaydı yok.")
		}
		removed = true
		return out, nil
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(map[string]any{"uuid": uuid, "removed": removed})
}

/* ---------- format ---------- */

func storageSettle(ctx context.Context) {
	if storageExists(storageUdevadm) {
		_, _, _ = storageRun(ctx, nil, storageUdevadm, "settle", "--timeout=15")
	}
}

// storageFormat creates a filesystem. Arguments: device, filesystem type,
// label (may be empty), size in bytes and serial number as they were shown
// to the user when the operation was confirmed.
func storageFormat(ctx context.Context, args []string, _ io.Reader, stdout io.Writer) error {
	if err := ArgCount(args, 5); err != nil {
		return err
	}
	dev, label, serial := args[0], args[2], args[4]
	if !sc.ValidDevice(dev) {
		return Userf("Disk adı geçersiz.")
	}
	fs, ok := sc.LookupFormatFS(args[1])
	if !ok {
		return Userf("Dosya sistemi türü geçersiz.")
	}
	if !sc.ValidLabel(fs, label) {
		return Userf("Etiket geçersiz. En fazla %d karakter; yalnızca harf, rakam, '-' ve '_' kullanılabilir.", fs.MaxLabel)
	}
	size, err := strconv.ParseInt(args[3], 10, 64)
	if err != nil || size <= 0 {
		return Userf("Disk boyutu geçersiz.")
	}
	if !sc.ValidSerial(serial) {
		return Userf("Seri numarası geçersiz.")
	}
	if !storageExists(fs.Binary) {
		return Userf("%s dosya sistemi için gerekli araç sunucuda kurulu değil.", fs.Type)
	}
	if !storageExists(storageWipefs) {
		return Userf("wipefs aracı sunucuda kurulu değil.")
	}
	unlock, err := storageLock()
	if err != nil {
		return err
	}
	defer unlock()

	// Everything below is determined by the helper itself, right now.
	st, err := storageLoad()
	if err != nil {
		return err
	}
	devPath, ok := st.sys.VerifyNode(dev)
	if !ok {
		return Userf("Disk bulunamadı: %s", dev)
	}
	if err := st.guard(dev); err != nil {
		return err
	}
	if st.sys.ReadOnly(dev) {
		return Userf("Disk yazmaya karşı korumalı.")
	}
	family := st.sys.Family(dev)
	for _, n := range family {
		// Each member is checked on its own as well: a partition of the
		// target that belongs to the system blocks the whole operation.
		if err := st.guard(n); err != nil {
			return err
		}
		if len(sc.MountsOf(st.sys, st.mounts, n)) > 0 {
			return Userf("%s bağlı durumda. Biçimlendirmeden önce diski ayırın.", n)
		}
		if sc.SwapOn(st.sys, st.swaps, n) {
			return Userf("%s takas alanı olarak kullanılıyor.", n)
		}
		if len(st.sys.Holders(n)) > 0 {
			return Userf("%s başka bir birim (LVM, RAID veya şifreli birim) tarafından kullanılıyor.", n)
		}
	}
	lctx, lcancel := context.WithTimeout(ctx, 30*time.Second)
	devs, err := storageLsblk(lctx)
	lcancel()
	if err != nil {
		return Userf("Disk bilgileri okunamadı; güvenlik nedeniyle işlem yapılmadı.")
	}
	node, root := sc.FindDevice(devs, dev)
	if node == nil || root == nil {
		return Userf("Disk bulunamadı: %s", dev)
	}
	whole := !st.sys.IsPartition(dev)
	if (whole && node.Type != "disk") || (!whole && node.Type != "part") {
		return Userf("Bu aygıt türü biçimlendirilemez.")
	}
	if node.Size != size || st.sys.SizeBytes(dev) != size {
		return Userf("Disk, onay verildikten sonra değişmiş (boyut uyuşmuyor). İşlem yapılmadı; listeyi yenileyip tekrar deneyin.")
	}
	if root.Serial != serial {
		return Userf("Disk, onay verildikten sonra değişmiş (seri numarası uyuşmuyor). İşlem yapılmadı; listeyi yenileyip tekrar deneyin.")
	}
	if len(node.MountPoints) > 0 {
		return Userf("%s bağlı durumda. Biçimlendirmeden önce diski ayırın.", dev)
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	target, targetPath := dev, devPath
	if whole {
		if !storageExists(storageSfdisk) {
			return Userf("Diskin tamamını biçimlendirmek için gereken sfdisk aracı kurulu değil.")
		}
		for _, p := range st.sys.Partitions(dev) {
			if pp, ok := st.sys.VerifyNode(p); ok {
				_, _, _ = storageRun(ctx, nil, storageWipefs, "--all", "--", pp)
			}
		}
		script := "label: gpt\ntype=" + fs.PartType + "\n"
		code, _, err := storageRun(ctx, strings.NewReader(script), storageSfdisk,
			"--quiet", "--wipe", "always", "--wipe-partitions", "always", "--", devPath)
		if err != nil || code != 0 {
			return Userf("Bölüm tablosu oluşturulamadı.")
		}
		storageSettle(ctx)
		target = sc.PartitionName(dev, 1)
		found := false
		for i := 0; i < 40; i++ {
			if p, ok := st.sys.VerifyNode(target); ok {
				if parent, ok := st.sys.Parent(target); ok && parent == dev {
					targetPath, found = p, true
					break
				}
			}
			select {
			case <-ctx.Done():
				return Userf("İşlem zaman aşımına uğradı.")
			case <-time.After(250 * time.Millisecond):
			}
		}
		if !found {
			return Userf("Yeni bölüm oluşturuldu ancak sistem tarafından görülemedi.")
		}
	}
	if code, _, err := storageRun(ctx, nil, storageWipefs, "--all", "--", targetPath); err != nil || code != 0 {
		return Userf("Diskteki eski imzalar silinemedi. Disk kullanımda olabilir.")
	}
	code, _, err := storageRun(ctx, nil, fs.Binary, sc.MkfsArgs(fs, label, targetPath)...)
	if err != nil || code != 0 {
		return Userf("Dosya sistemi oluşturulamadı (%s).", fs.Type)
	}
	storageSettle(ctx)
	return json.NewEncoder(stdout).Encode(map[string]any{
		"device": dev, "partition": target, "fstype": fs.Type, "label": label,
	})
}
