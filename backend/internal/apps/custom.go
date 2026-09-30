package apps

// Custom applications: added by an administrator from a docker-compose file.
//
// The compose file is converted into a manifest (compose.go). The manifest
// is stored in apps_custom and joins the catalog under the category "Özel";
// from then on the application is handled exactly like a shipped one, and
// the stored manifest is what ConformToManifest checks a restored backup
// against.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"myserver/internal/auth"
	"myserver/internal/httpx"
)

/* ---------- storage ---------- */

type customRow struct {
	Slug     string
	Manifest string
}

func (s *store) listCustom(ctx context.Context) ([]customRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT slug, manifest FROM apps_custom ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []customRow{}
	for rows.Next() {
		var r customRow
		if err := rows.Scan(&r.Slug, &r.Manifest); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// addCustom stores a definition. It returns false when the slug is taken.
func (s *store) addCustom(ctx context.Context, slug, name string, manifest []byte, by string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO apps_custom (slug, name, manifest, created_by, created_at)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT(slug) DO NOTHING`,
		slug, name, string(manifest), by, time.Now().Unix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// removeCustom deletes a definition. It returns false when there was none.
func (s *store) removeCustom(ctx context.Context, slug string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM apps_custom WHERE slug = ?`, slug)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// parseCustom validates a stored definition again, with the same strict
// validation as a shipped manifest.
func parseCustom(slug string, data []byte) (*Manifest, error) {
	m, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if m.Slug != slug || !IsCustomSlug(slug) || m.Category != CustomCategory {
		return nil, errors.New("kayıtlı tanım kendi kısa adıyla uyuşmuyor")
	}
	return m, nil
}

// loadCustom puts the stored custom applications into the catalog. A
// definition that no longer validates (for example after a schema change)
// is reported to administrators like an invalid manifest file.
func (m *Module) loadCustom(ctx context.Context) {
	rows, err := m.store.listCustom(ctx)
	if err != nil {
		slog.Warn("özel uygulama tanımları okunamadı", "error", err.Error())
		m.catalog.SetCustom(nil, []InvalidManifest{{File: "Özel uygulamalar", Error: "Özel uygulama tanımları veritabanından okunamadı."}})
		return
	}
	list := make([]*Manifest, 0, len(rows))
	invalid := []InvalidManifest{}
	for _, r := range rows {
		man, err := parseCustom(r.Slug, []byte(r.Manifest))
		if err != nil {
			slog.Warn("özel uygulama tanımı geçersiz, atlandı", "slug", r.Slug, "error", err.Error())
			invalid = append(invalid, InvalidManifest{File: customInvalidName(r.Slug), Error: err.Error()})
			continue
		}
		list = append(list, man)
	}
	m.catalog.SetCustom(list, invalid)
}

/* ---------- handlers ---------- */

type customRequest struct {
	Name    string `json:"name"`
	Compose string `json:"compose"`
	// Digest of the previewed manifest. When sent, the definition is only
	// stored if the conversion still gives exactly that manifest.
	Digest string `json:"digest"`
}

// customPreview is the detail of the converted application, in the shape
// of GET /apps/catalog/{slug}, plus what the conversion changed.
type customPreview struct {
	detailView
	// ConversionNotes explain what the conversion changed. (The manifest's
	// own notes, shown in the install dialog, stay in "notes".)
	ConversionNotes []ComposeIssue `json:"conversion_notes"`
	Digest          string         `json:"digest"`
	ManifestYAML    string         `json:"manifest_yaml"`
}

// slugTaken explains why slug cannot be used, or returns "".
func (m *Module) slugTaken(ctx context.Context, slug string) string {
	if m.catalog.Get(slug) != nil {
		return fmt.Sprintf("Bu adla (%s) bir uygulama zaten var; başka bir ad seçin.", slug)
	}
	it, err := m.store.get(ctx, slug)
	if err != nil {
		slog.Warn("kurulu uygulama sorgulanamadı", "slug", slug, "error", err.Error())
		return "Uygulama adının kullanılıp kullanılmadığı denetlenemedi."
	}
	if it != nil {
		return fmt.Sprintf("Bu adla (%s) kurulu bir uygulama var; başka bir ad seçin.", slug)
	}
	return ""
}

// convertCustom converts the compose file of a request. A refused file is a
// 400 whose message lists every problem, one per line.
func (m *Module) convertCustom(ctx context.Context, req customRequest) (*Conversion, error) {
	conv, err := ConvertCompose([]byte(req.Compose), ConvertOptions{
		Name: req.Name, Roots: m.roots(), Protected: protectedDirs(m.eng.dataDir),
		Taken: func(slug string) string { return m.slugTaken(ctx, slug) },
	})
	var ce *ComposeError
	if errors.As(err, &ce) {
		return nil, httpx.NewError(http.StatusBadRequest, "compose_refused", ce.Error())
	}
	if err != nil {
		return nil, httpx.Internal(err)
	}
	if clash := m.volumeClash(conv.Manifest); clash != "" {
		ce := &ComposeError{Problems: []ComposeIssue{{Key: "volumes", Message: clash}}}
		return nil, httpx.NewError(http.StatusBadRequest, "compose_refused", ce.Error())
	}
	return conv, nil
}

// volumeClash reports a Docker volume of man whose name another application
// of the catalog also produces ("myserver-custom-a" + "b-data" and
// "myserver-custom-a-b" + "data"). The engine reuses an existing volume, so
// the new application would otherwise mount the other one's data.
func (m *Module) volumeClash(man *Manifest) string {
	mine := map[string]bool{}
	for _, s := range man.Services {
		for _, v := range s.Volumes {
			if v.Type == VolumeNamed {
				mine[VolumeName(man.Slug, v.Source)] = true
			}
		}
	}
	for _, other := range m.catalog.List() {
		if other.Slug == man.Slug {
			continue
		}
		for _, s := range other.Services {
			for _, v := range s.Volumes {
				if name := VolumeName(other.Slug, v.Source); v.Type == VolumeNamed && mine[name] {
					return fmt.Sprintf("%s Docker birimi %s uygulamasının birimiyle aynı ada sahip olur; birimi veya uygulamayı yeniden adlandırın.", name, other.Name)
				}
			}
		}
	}
	return ""
}

func (m *Module) handleCustomPreview(w http.ResponseWriter, r *http.Request) error {
	var req customRequest
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	conv, err := m.convertCustom(r.Context(), req)
	if err != nil {
		return err
	}
	d, err := m.detailOf(r.Context(), conv.Manifest, nil, true)
	if err != nil {
		return err
	}
	httpx.OK(w, customPreview{detailView: d, ConversionNotes: conv.Notes, Digest: conv.Digest, ManifestYAML: string(conv.YAML)})
	return nil
}

func (m *Module) handleCustomCreate(w http.ResponseWriter, r *http.Request) error {
	var req customRequest
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	actor := auth.ActorFrom(r)
	target := customSlug(req.Name)
	if target == "" {
		target = req.Name
	}
	target = truncate(target, 60)
	conv, err := m.convertCustom(r.Context(), req)
	if err != nil {
		m.deps.Audit.Log(r.Context(), actor, "apps.custom_create", target, "compose dosyası reddedildi", false)
		return err
	}
	man := conv.Manifest
	if req.Digest != "" && req.Digest != conv.Digest {
		return httpx.NewError(http.StatusConflict, "custom_changed",
			"Tanım önizlemeden sonra değişti. Kaydetmeden önce yeniden önizleyin.")
	}
	added, err := m.store.addCustom(r.Context(), man.Slug, man.Name, conv.YAML, actor.Username)
	if err != nil {
		return httpx.Internal(err)
	}
	if !added {
		m.deps.Audit.Log(r.Context(), actor, "apps.custom_create", man.Slug, "ad zaten kullanılıyor", false)
		return httpx.Conflict(fmt.Sprintf("Bu adla (%s) bir uygulama zaten var; başka bir ad seçin.", man.Slug))
	}
	m.catalog.PutCustom(man)
	m.deps.Audit.Log(r.Context(), actor, "apps.custom_create", man.Slug, man.Name, true)
	m.hub.notify()
	httpx.JSON(w, http.StatusCreated, m.catalogItem(man, false))
	return nil
}

func (m *Module) handleCustomDelete(w http.ResponseWriter, r *http.Request) error {
	slug := r.PathValue("slug")
	if !ValidSlug(slug) || !IsCustomSlug(slug) {
		return httpx.BadRequest("Özel uygulama adı geçersiz.")
	}
	actor := auth.ActorFrom(r)
	ctx, cancel := detached(r, time.Minute)
	defer cancel()
	release, err := m.lock(slug, "custom_delete")
	if err != nil {
		return err
	}
	defer release()
	it, err := m.store.get(ctx, slug)
	if err != nil {
		return httpx.Internal(err)
	}
	if it != nil {
		m.deps.Audit.Log(ctx, actor, "apps.custom_delete", slug, "uygulama kurulu", false)
		return httpx.NewError(http.StatusConflict, "custom_installed",
			"Bu özel uygulama kurulu. Tanımını silmeden önce uygulamayı kaldırın.")
	}
	removed, err := m.store.removeCustom(ctx, slug)
	if err != nil {
		return httpx.Internal(err)
	}
	if !removed {
		return httpx.NotFound("Özel uygulama tanımı bulunamadı.")
	}
	m.catalog.RemoveCustom(slug)
	m.deps.Audit.Log(ctx, actor, "apps.custom_delete", slug, "", true)
	httpx.OK(w, map[string]string{"slug": slug})
	return nil
}
