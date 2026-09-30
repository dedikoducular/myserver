package apps

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// InvalidManifest reports a manifest file that could not be loaded.
type InvalidManifest struct {
	File  string `json:"file"`
	Error string `json:"error"`
}

// Catalog is the set of manifests loaded from the manifest directory, plus
// the custom applications the administrator added from compose files
// (stored in the database, see custom.go).
type Catalog struct {
	dir string

	mu       sync.RWMutex
	apps     map[string]*Manifest
	order    []string
	invalid  []InvalidManifest
	loadedAt int64
	loadErr  string

	custom        map[string]*Manifest
	customInvalid []InvalidManifest
}

func NewCatalog(dir string) *Catalog {
	return &Catalog{dir: dir, apps: map[string]*Manifest{}, custom: map[string]*Manifest{}}
}

// errReserved is reported for a shipped manifest that uses the slug prefix
// or the category of custom applications.
var errReserved = fmt.Errorf("%q öneki ve %q kategorisi panelden eklenen özel uygulamalara ayrılmıştır", CustomPrefix, CustomCategory)

// Reload reads every *.yaml / *.yml file of the manifest directory. A broken
// manifest is skipped and reported; it never prevents the others loading.
func (c *Catalog) Reload() {
	apps := map[string]*Manifest{}
	files := map[string]string{}
	invalid := []InvalidManifest{}
	loadErr := ""

	entries, err := os.ReadDir(c.dir)
	if err != nil {
		loadErr = "Uygulama tanımları klasörü okunamadı."
		slog.Warn("uygulama tanımları klasörü okunamadı", "dir", c.dir, "error", err.Error())
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		n := e.Name()
		ext := strings.ToLower(filepath.Ext(n))
		if e.IsDir() || strings.HasPrefix(n, ".") || (ext != ".yaml" && ext != ".yml") {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		m, err := loadFile(filepath.Join(c.dir, n))
		if err == nil && (IsCustomSlug(m.Slug) || m.Category == CustomCategory) {
			err = errReserved
		}
		if err == nil {
			if prev, dup := files[m.Slug]; dup {
				err = fmt.Errorf("slug %q zaten %s dosyasında tanımlı", m.Slug, prev)
			}
		}
		if err != nil {
			slog.Warn("uygulama tanımı geçersiz, atlandı", "file", n, "error", err.Error())
			invalid = append(invalid, InvalidManifest{File: n, Error: err.Error()})
			continue
		}
		apps[m.Slug] = m
		files[m.Slug] = n
	}
	order := make([]string, 0, len(apps))
	for slug := range apps {
		order = append(order, slug)
	}
	sort.Slice(order, func(i, j int) bool {
		return strings.ToLower(apps[order[i]].Name) < strings.ToLower(apps[order[j]].Name)
	})

	c.mu.Lock()
	c.apps, c.order, c.invalid, c.loadErr = apps, order, invalid, loadErr
	c.loadedAt = time.Now().Unix()
	c.mu.Unlock()
	slog.Info("uygulama kataloğu yüklendi", "valid", len(apps), "invalid", len(invalid))
}

func loadFile(path string) (*Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("dosya açılamadı")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return nil, fmt.Errorf("normal bir dosya değil")
	}
	data, err := io.ReadAll(io.LimitReader(f, 256<<10+1))
	if err != nil {
		return nil, fmt.Errorf("dosya okunamadı")
	}
	return Parse(data)
}

// Get returns the manifest of a slug, or nil.
func (c *Catalog) Get(slug string) *Manifest {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if m := c.apps[slug]; m != nil {
		return m
	}
	return c.custom[slug]
}

// List returns the manifests, shipped and custom, ordered by name.
func (c *Catalog) List() []*Manifest {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*Manifest, 0, len(c.order)+len(c.custom))
	for _, slug := range c.order {
		out = append(out, c.apps[slug])
	}
	if len(c.custom) == 0 {
		return out
	}
	for _, m := range c.custom {
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].Slug < out[j].Slug
	})
	return out
}

// Invalid returns the manifests skipped by the last reload and the stored
// custom definitions that could not be loaded.
func (c *Catalog) Invalid() []InvalidManifest {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := append([]InvalidManifest{}, c.invalid...)
	return append(out, c.customInvalid...)
}

// SetCustom replaces the custom applications.
func (c *Catalog) SetCustom(list []*Manifest, invalid []InvalidManifest) {
	custom := make(map[string]*Manifest, len(list))
	for _, m := range list {
		custom[m.Slug] = m
	}
	c.mu.Lock()
	c.custom, c.customInvalid = custom, append([]InvalidManifest{}, invalid...)
	c.mu.Unlock()
}

// PutCustom adds one custom application.
func (c *Catalog) PutCustom(m *Manifest) {
	c.mu.Lock()
	c.custom[m.Slug] = m
	c.mu.Unlock()
}

// RemoveCustom removes a custom application, and the report of its stored
// definition being invalid, if any.
func (c *Catalog) RemoveCustom(slug string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.custom, slug)
	kept := c.customInvalid[:0]
	for _, i := range c.customInvalid {
		if i.File != customInvalidName(slug) {
			kept = append(kept, i)
		}
	}
	c.customInvalid = kept
}

// customInvalidName labels a stored custom definition in the list of
// invalid manifests.
func customInvalidName(slug string) string { return "Özel uygulama: " + slug }

// Status returns the time of the last reload and its directory error, if any.
func (c *Catalog) Status() (loadedAt int64, loadErr string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.loadedAt, c.loadErr
}
