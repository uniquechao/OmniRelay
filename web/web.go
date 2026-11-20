// Package web serves the production workbench alongside its same-origin Go API.
package web

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"

	"github.com/uniquechao/OmniRelay/design"
)

//go:embed index.html *.js *.css
var frontend embed.FS

const contentPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

func Register(mux *http.ServeMux) {
	if dir := os.Getenv("OMNIRELAY_PREVIEW_DIR"); dir != "" {
		files := os.DirFS(dir)
		mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" {
				servePreview(w, r, files, "index.html")
				return
			}
			name := strings.TrimPrefix(r.URL.Path, "/")
			if !strings.HasPrefix(name, "assets/") || !fs.ValidPath(name) {
				http.NotFound(w, r)
				return
			}
			servePreview(w, r, files, name)
		})
		return
	}
	shared := design.WorkbenchFiles()
	workbench := http.FileServer(http.FS(frontend))
	visuals := http.FileServer(http.FS(shared))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", contentPolicy)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-cache")
		if r.URL.Path == "/" {
			workbench.ServeHTTP(w, r)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		for _, asset := range []struct {
			fs      fs.FS
			handler http.Handler
		}{{frontend, workbench}, {shared, visuals}} {
			info, err := fs.Stat(asset.fs, name)
			if err == nil && !info.IsDir() {
				asset.handler.ServeHTTP(w, r)
				return
			}
		}
		http.NotFound(w, r)
	})
}

//go:embed setup.html
var setupPage string

// ServeSetup serves the initial configuration form while the backend is unconfigured.
func ServeSetup(w http.ResponseWriter, r *http.Request) {
	if dir := os.Getenv("OMNIRELAY_PREVIEW_DIR"); dir != "" {
		servePreview(w, r, os.DirFS(dir), "setup.html")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", contentPolicy)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, setupPage)
}

func servePreview(w http.ResponseWriter, r *http.Request, files fs.FS, name string) {
	w.Header().Set("Content-Security-Policy", contentPolicy)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	info, err := fs.Stat(files, name)
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.ServeFileFS(w, r, files, name)
}
