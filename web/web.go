// Package web serves the production workbench alongside its same-origin Go API.
package web

import (
	"embed"
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
	files := registerBuildAssets(mux)
	if dir := os.Getenv("OMNIRELAY_PREVIEW_DIR"); dir != "" {
		mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" {
				serveBuildFile(w, r, files, "index.html")
				return
			}
			http.NotFound(w, r)
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

//go:embed dist/setup.html dist/assets
var setupBuild embed.FS

func registerBuildAssets(mux *http.ServeMux) fs.FS {
	files, err := fs.Sub(setupBuild, "dist")
	if err != nil {
		panic(err)
	}
	if dir := os.Getenv("OMNIRELAY_PREVIEW_DIR"); dir != "" {
		files = os.DirFS(dir)
	}
	mux.HandleFunc("GET /assets/", func(w http.ResponseWriter, r *http.Request) {
		serveBuildFile(w, r, files, strings.TrimPrefix(r.URL.Path, "/"))
	})
	return files
}

// RegisterSetup 注册首次配置入口及其构建资源。
func RegisterSetup(mux *http.ServeMux) {
	files := registerBuildAssets(mux)
	page := func(w http.ResponseWriter, r *http.Request) {
		serveBuildFile(w, r, files, "setup.html")
	}
	mux.HandleFunc("GET /setup", page)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("setup") == "complete" {
			page(w, r)
			return
		}
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
	})
}

func serveBuildFile(w http.ResponseWriter, r *http.Request, files fs.FS, name string) {
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
