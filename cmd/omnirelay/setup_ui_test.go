package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uniquechao/OmniRelay/web"
)

func TestEmbeddedSetupHTTP(t *testing.T) {
	t.Setenv("OMNIRELAY_PREVIEW_DIR", "")
	setup := newWebSetupHandler(webSetupStore{}, "https://relay.example.test", nil, func() {})
	normal := http.NewServeMux()
	web.Register(normal)
	get := func(handler http.Handler, path string, status int) *httptest.ResponseRecorder {
		t.Helper()
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest("GET", path, nil))
		if res.Code != status {
			t.Fatalf("%s 返回 %d，预期 %d", path, res.Code, status)
		}
		return res
	}
	if get(setup, "/", 303).Header().Get("Location") != "/setup" {
		t.Fatal("未进入首次配置")
	}
	for _, path := range []string{"/setup", "/?setup=complete"} {
		res := get(setup, path, 200)
		if res.Header().Get("Cache-Control") != "no-store" || !strings.Contains(res.Header().Get("Content-Security-Policy"), "script-src 'self'") {
			t.Fatal("初始化入口缺少安全响应头")
		}
		assets := regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`).FindAllStringSubmatch(res.Body.String(), -1)
		if len(assets) == 0 {
			t.Fatal("页面缺少嵌入构建资源")
		}
		for _, asset := range assets {
			get(normal, asset[1], 200)
			if !strings.Contains(get(setup, asset[1], 200).Header().Get("Cache-Control"), "immutable") {
				t.Fatal("哈希资源缺少缓存响应头")
			}
		}
	}
	get(normal, "/setup", 404)
	get(setup, "/assets/missing.js", 404)
}

func TestEmbeddedSetupBrowser(t *testing.T) {
	dsn := os.Getenv("OMNIRELAY_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("OMNIRELAY_PLAYWRIGHT_MODULE") == "" {
		t.Skip("需要独立 PostgreSQL 与浏览器")
	}
	t.Setenv("OMNIRELAY_PREVIEW_DIR", "")
	server := httptest.NewUnstartedServer(nil)
	defer server.Close()
	origin := "https://" + server.Listener.Addr().String()
	pool := backendTestPool(t, t.Context(), dsn)
	store := newWebSetupStore(pool, make([]byte, 32))
	provider := &feishuFixture{callbackOrigin: origin}
	cfg := configuration{publicOrigin: origin, appID: "cli_fixture", appSecret: "fixture-only",
		tenantKey: "tenant_fixture", bootstrap: "ou_admin", syncInterval: time.Hour, credentialsGeneration: 1, botHeartbeatTTL: time.Second}
	normal, _, err := newBackend(t.Context(), pool, cfg, provider)
	if err != nil {
		t.Fatal(err)
	}
	var saved, ready atomic.Bool
	setup := newWebSetupHandler(store, origin, provider, func() { saved.Store(true) })
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/test/service-ready" && saved.Load() {
			ready.Store(true)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if ready.Load() {
			normal.ServeHTTP(w, r)
			return
		}
		setup.ServeHTTP(w, r)
	})
	server.StartTLS()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "--test", "tests/integration/setup_browser.test.mjs")
	cmd.Dir = "../.."
	cmd.Env = append(os.Environ(), "OMNIRELAY_SETUP_ORIGIN="+origin, "OMNIRELAY_TEST_SETUP_CODE="+setupToken(store.key))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("浏览器验证失败 %v\n%s", err, out)
	}
}
