// Package webui serves the production frontend build embedded in the
// binary. `make frontend` writes the Vite build into dist/.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Handler serves static assets and falls back to index.html for client-side
// routes.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		if fi, err := fs.Stat(sub, name); err != nil || fi.IsDir() {
			// Missing asset files are real 404s; anything else is a
			// client-side route.
			if strings.HasPrefix(name, "assets/") {
				http.NotFound(w, r)
				return
			}
			serveIndex(w, r, sub)
			return
		}
		if strings.HasPrefix(name, "assets/") {
			// Vite asset names contain a content hash.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, sub fs.FS) {
	w.Header().Set("Cache-Control", "no-cache")
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		// The binary was built without the frontend.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("MyServer arayüzü bu derlemeye dahil edilmemiş. 'make build' ile yeniden derleyin.\n"))
		return
	}
	http.ServeFileFS(w, r, sub, "index.html")
}
