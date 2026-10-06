package api

import (
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

const uiNotBuilt = `<!doctype html><meta charset="utf-8"><title>fleetmgr</title>
<p>The web UI has not been built into this binary. Run <code>make web</code> and rebuild.
The API is available under <code>/api/v1</code>.</p>`

// uiHandler serves the embedded single-page app. Unknown paths without a file
// extension fall back to index.html so client-side routes work on reload.
func (s *Server) uiHandler() http.Handler {
	ui := s.d.UI
	index, err := fs.ReadFile(ui, "index.html")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(uiNotBuilt))
		})
	}
	files := http.FileServerFS(ui)
	serveIndex := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(index)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" || p == "index.html" {
			serveIndex(w)
			return
		}
		if _, err := fs.Stat(ui, p); err != nil {
			if errors.Is(err, fs.ErrNotExist) && path.Ext(p) == "" {
				serveIndex(w)
				return
			}
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(p, "assets/") {
			// Vite emits content-hashed filenames under assets/.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}
