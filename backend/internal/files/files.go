// Package files is the web file manager.
//
// Security model: the panel process can read and write anywhere (it holds
// CAP_DAC_OVERRIDE), so this package is the only barrier. A client path is
// first matched lexically against the admin's allowed roots (paths.go) and
// then EVERY filesystem operation is performed through an *os.Root opened
// on that allowed root. os.Root resolves each path component with openat
// and refuses any path or symlink that would leave the root, so neither
// "../", absolute symlinks nor rename races can escape. No plain os.* call
// is made on a client-derived path.
package files

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"syscall"

	"myserver/internal/auth"
	"myserver/internal/httpx"
	"myserver/internal/module"
	"myserver/internal/settings"
)

const (
	KeyMaxUploadMB  = "files.max_upload_mb"
	KeyMaxExtractMB = "files.max_extract_mb"

	defaultMaxUploadMB  = 10240 // 10 GiB
	defaultMaxExtractMB = 51200 // 50 GiB

	textMaxBytes = 1 << 20
)

type Module struct {
	deps   module.Deps
	jobs   *jobManager
	owners *ownerCache
}

func New(deps module.Deps, st *settings.API) (module.Module, error) {
	settings.RegisterDefault(KeyMaxUploadMB, "10240")
	settings.RegisterDefault(KeyMaxExtractMB, "51200")
	if st != nil {
		st.Allow(KeyMaxUploadMB, settings.IntRange(1, 1048576), nil)
		st.Allow(KeyMaxExtractMB, settings.IntRange(1, 4194304), nil)
	}
	return &Module{deps: deps, jobs: newJobManager(deps.Audit), owners: newOwnerCache()}, nil
}

func (m *Module) Name() string { return "files" }

func (m *Module) Register(api, _ *httpx.Router) {
	g := api.Group("/files")
	g.Get("/roots", m.handleRoots)
	g.Get("/list", m.handleList)
	g.Get("/stat", m.handleStat)
	g.Get("/count", m.handleCount)
	g.Get("/download", m.handleDownload)
	g.Get("/preview", m.handlePreview)
	g.Get("/text", m.handleTextRead)
	g.Post("/size", m.handleSize)
	g.Get("/jobs", m.handleJobs)
	g.Get("/jobs/stream", m.handleJobStream)
	g.Post("/jobs/{id}/cancel", m.handleJobCancel)

	a := api.Group("/files", auth.RequireAdmin)
	a.Post("/mkdir", m.handleMkdir)
	a.Post("/rename", m.handleRename)
	a.Post("/chmod", m.handleChmod)
	a.Post("/delete", m.handleDelete)
	a.Post("/copy", m.handleCopy)
	a.Post("/move", m.handleMove)
	a.Post("/zip", m.handleZip)
	a.Post("/extract", m.handleExtract)
	a.Post("/upload", m.handleUpload)
	a.Put("/text", m.handleTextWrite)
}

// Start cancels running jobs when the panel shuts down.
func (m *Module) Start(ctx context.Context) {
	<-ctx.Done()
	m.jobs.cancelAll()
}

func (m *Module) roots() []string {
	return cleanRoots(m.deps.Settings.Strings(settings.KeyAllowedRoots))
}

func (m *Module) maxUploadBytes() int64 {
	n := m.deps.Settings.Int(KeyMaxUploadMB, defaultMaxUploadMB)
	if n < 1 {
		n = defaultMaxUploadMB
	}
	return int64(n) << 20
}

func (m *Module) maxExtractBytes() int64 {
	n := m.deps.Settings.Int(KeyMaxExtractMB, defaultMaxExtractMB)
	if n < 1 {
		n = defaultMaxExtractMB
	}
	return int64(n) << 20
}

// open resolves a client path and opens the allowed root that contains it.
// The caller must Close the returned root.
func (m *Module) open(p string) (*os.Root, location, error) {
	loc, err := resolve(m.roots(), p)
	if err != nil {
		return nil, location{}, err
	}
	if err := settings.CheckRootReal(loc.Root); err != nil {
		return nil, location{}, httpx.NewError(http.StatusForbidden, "root_is_link",
			"Bu dizin bir sembolik bağlantı olduğu için güvenlik nedeniyle açılamıyor: "+loc.Root).Wrap(err)
	}
	rt, err := os.OpenRoot(loc.Root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, location{}, httpx.NotFound("Bu dizin sunucuda mevcut değil: " + loc.Root)
		}
		return nil, location{}, fsError(err, "Dizin açılamadı.")
	}
	return rt, loc, nil
}

// fsMessage turns a filesystem error into a specific Turkish sentence and an
// HTTP status. Raw error text never reaches the user.
func fsMessage(err error, fallback string) (int, string, string) {
	switch {
	case err == nil:
		return http.StatusInternalServerError, "internal_error", fallback
	case strings.Contains(err.Error(), "path escapes"):
		return http.StatusForbidden, "outside_allowed_roots", "Bu yol veya içerdiği sembolik bağlantı izin verilen dizinin dışına çıkıyor."
	case errors.Is(err, syscall.ENOTEMPTY):
		return http.StatusConflict, "not_empty", "Dizin boş değil."
	case errors.Is(err, fs.ErrNotExist):
		return http.StatusNotFound, "not_found", "Dosya veya dizin bulunamadı."
	case errors.Is(err, fs.ErrExist):
		return http.StatusConflict, "exists", "Aynı adda bir öğe zaten var."
	case errors.Is(err, fs.ErrPermission):
		return http.StatusForbidden, "permission_denied", "Bu işlem için dosya sistemi izni yok."
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		return http.StatusInsufficientStorage, "no_space", "Diskte yeterli boş alan yok."
	case errors.Is(err, syscall.EROFS):
		return http.StatusConflict, "read_only", "Dosya sistemi salt okunur."
	case errors.Is(err, syscall.ENOTDIR):
		return http.StatusBadRequest, "not_directory", "Yol bir dizin değil."
	case errors.Is(err, syscall.EISDIR):
		return http.StatusBadRequest, "is_directory", "Yol bir dizin."
	case errors.Is(err, syscall.ELOOP):
		return http.StatusBadRequest, "symlink_loop", "Sembolik bağlantı çözülemedi."
	case errors.Is(err, syscall.ENAMETOOLONG):
		return http.StatusBadRequest, "name_too_long", "Dosya adı veya yolu çok uzun."
	case errors.Is(err, syscall.EBUSY):
		return http.StatusConflict, "busy", "Öğe şu anda kullanımda (bağlama noktası olabilir)."
	case errors.Is(err, syscall.EIO):
		return http.StatusBadGateway, "io_error", "Diskten okuma veya diske yazma hatası oluştu."
	case errors.Is(err, context.Canceled):
		return http.StatusConflict, "cancelled", "İşlem iptal edildi."
	}
	return http.StatusInternalServerError, "fs_error", fallback
}

func fsError(err error, fallback string) *httpx.Error {
	var he *httpx.Error
	if errors.As(err, &he) {
		return he
	}
	status, code, msg := fsMessage(err, fallback)
	return httpx.NewError(status, code, msg).Wrap(err)
}

// parentOwner returns the owner of the directory that will contain a new
// entry, so files created through the panel belong to the directory's
// owner rather than to the panel user.
func parentOwner(rt *os.Root, dirRel string) (uid, gid int, ok bool) {
	fi, err := rt.Stat(dirRel)
	if err != nil {
		return 0, 0, false
	}
	return ownerIDs(fi)
}

// adopt gives rel the owner of its parent directory. Failure is not fatal:
// some filesystems (FAT, exFAT, NTFS) have no ownership.
func adopt(rt *os.Root, rel string, uid, gid int, ok bool) {
	if ok {
		_ = rt.Lchown(rel, uid, gid)
	}
}
