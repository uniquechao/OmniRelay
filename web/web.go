// Package web serves the production workbench alongside its same-origin Go API.
package web

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"strings"

	"github.com/uniquechao/OmniRelay/design"
)

//go:embed index.html *.js *.css
var frontend embed.FS

func Register(mux *http.ServeMux) {
	shared := design.WorkbenchFiles()
	workbench := http.FileServer(http.FS(frontend))
	visuals := http.FileServer(http.FS(shared))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
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
func ServeSetup(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, setupPage)
}
