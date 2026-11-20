package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uniquechao/OmniRelay/web"
)

func TestPreviewUI(t *testing.T) {
	dir, err := filepath.Abs("../../web/dist")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "index.html")); os.IsNotExist(err) {
		t.Skip("请先运行 npm ci 和 npm run build")
	}
	t.Setenv("OMNIRELAY_PREVIEW_DIR", dir)
	mux := http.NewServeMux()
	web.Register(mux)
	mux.HandleFunc("GET /api/v1/me", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	normal := secureHeaders(mux)
	setup := newWebSetupHandler(webSetupStore{}, "https://relay.example.test", nil, func() {})
	check := func(handler http.Handler, path string, status int) *httptest.ResponseRecorder {
		t.Helper()
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, httptest.NewRequest("GET", path, nil))
		if out.Code != status {
			t.Fatalf("%s 状态码 %d，预期 %d", path, out.Code, status)
		}
		return out
	}
	for _, entry := range []struct {
		handler     http.Handler
		path, title string
	}{
		{normal, "/", "工作台"}, {setup, "/setup", "首次配置"}, {setup, "/?setup=complete", "首次配置"},
	} {
		page := check(entry.handler, entry.path, 200)
		if !strings.Contains(page.Body.String(), entry.title) || !strings.Contains(page.Body.String(), "/assets/") ||
			!strings.HasPrefix(page.Header().Get("Content-Type"), "text/html") ||
			!strings.Contains(page.Header().Get("Content-Security-Policy"), "script-src 'self'") {
			t.Fatalf("%s 入口、资源或响应头错误", entry.path)
		}
	}
	if got := check(setup, "/", 303).Header().Get("Location"); got != "/setup" {
		t.Fatal(got)
	}
	check(normal, "/setup", 404)
	files, err := os.ReadDir(filepath.Join(dir, "assets"))
	if err != nil || len(files) == 0 {
		t.Fatal("未生成前端资源", err)
	}
	for _, handler := range []http.Handler{normal, setup} {
		for _, file := range files {
			path := "/assets/" + file.Name()
			out := check(handler, path, 200)
			mime := map[string]string{".js": "javascript", ".css": "text/css", ".png": "image/png"}[filepath.Ext(path)]
			if mime == "" || !strings.Contains(out.Header().Get("Content-Type"), mime) || !strings.Contains(out.Header().Get("Cache-Control"), "immutable") {
				t.Fatal(path, out.Header())
			}
		}
		for _, path := range []string{"/missing.js", "/assets/missing.js", "/assets/", "/setup.html"} {
			check(handler, path, 404)
		}
	}
	if os.Getenv("OMNIRELAY_PLAYWRIGHT_MODULE") != "" {
		workbench := httptest.NewServer(normal)
		defer workbench.Close()
		init := httptest.NewServer(setup)
		defer init.Close()
		cmd := exec.CommandContext(t.Context(), "node", "--test", "tests/integration/preview_browser.test.mjs")
		cmd.Dir = "../.."
		cmd.Env = append(os.Environ(), "OMNIRELAY_PREVIEW_ORIGIN="+workbench.URL, "OMNIRELAY_SETUP_ORIGIN="+init.URL)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, output)
		}
	}
}
