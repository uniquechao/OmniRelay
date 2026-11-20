package main

import (
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

func TestSessionIdlePolicy(t *testing.T) {
	ctx, pool := botTestPool(t)
	server := httptest.NewUnstartedServer(nil)
	defer server.Close()
	origin := "https://" + server.Listener.Addr().String()
	cfg := configuration{
		publicOrigin: origin, appID: "cli_fixture", appSecret: "fixture-only",
		tenantKey: "tenant_fixture", bootstrap: "ou_admin", syncInterval: time.Hour,
		credentialsGeneration: 1, botHeartbeatTTL: 30 * time.Second, sessionIdleTTL: 12 * time.Second,
	}
	handler, _, err := newBackend(ctx, pool, cfg, &feishuFixture{})
	if err != nil {
		t.Fatal(err)
	}
	admin := seedBotSession(t, ctx, pool, "ou_admin", "")
	hash := sha256.Sum256([]byte(admin.cookie.Value))
	deadlines := func() (idle, absolute time.Time) {
		t.Helper()
		err := pool.QueryRow(ctx, "SELECT idle_expires_at, expires_at FROM web_sessions WHERE token_digest=$1", hash[:]).Scan(&idle, &absolute)
		if err != nil {
			t.Fatal(err)
		}
		return idle, absolute
	}
	request := func(method, path, csrf string, want int, activity ...string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, origin+path, strings.NewReader("{}"))
		req.AddCookie(admin.cookie)
		if len(activity) > 0 {
			req.Header.Set("X-OmniRelay-Activity", activity[0])
		}
		req.Header.Set("Origin", origin)
		req.Header.Set("X-CSRF-Token", csrf)
		req.Header.Set("Idempotency-Key", "session-idle-fixture")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: got %d, want %d (%s)", method, path, res.Code, want, res.Body.String())
		}
		return res
	}

	const me = "/api/v1/me"
	const systemPath = "/api/v1/system"
	const directory = systemPath + "/directory"
	checks := botPath + "/checks"
	setDeadlines := func(absoluteSeconds, idleSeconds float64) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE web_sessions SET expires_at=statement_timestamp()+make_interval(secs => $2),
			idle_expires_at=statement_timestamp()+make_interval(secs => $3) WHERE token_digest=$1`, hash[:], absoluteSeconds, idleSeconds); err != nil {
			t.Fatal(err)
		}
	}
	setDeadlines(60, 5)
	idle, absolute := deadlines()
	csrf := decodeBot[struct {
		Token string `json:"csrf_token"`
	}](t, request(http.MethodGet, me, "", 200)).Token
	if after, _ := deadlines(); !after.Equal(idle) {
		t.Fatal("session probe extended the idle deadline")
	}
	request(http.MethodGet, systemPath+"/roles", "", 200, "poll")
	if after, _ := deadlines(); !after.Equal(idle) {
		t.Fatal("后台读取延长了会话空闲期限")
	}
	request(http.MethodPost, checks, "invalid", 403)
	if after, _ := deadlines(); !after.Equal(idle) {
		t.Fatal("invalid CSRF extended the idle deadline")
	}
	started := decodeBot[struct{ ID string }](t, request(http.MethodPost, checks, csrf, 202))
	refreshed, end := deadlines()
	if !refreshed.After(idle) || !end.Equal(absolute) {
		t.Fatalf("unexpected deadlines after active write: idle %v, absolute %v", refreshed, end)
	}
	passive := []string{me, directory + "/sync-runs", directory + "/departments", directory + "/people",
		systemPath + "/accounts", systemPath + "/role-grants", botPath, checks + "/" + started.ID}
	for _, path := range passive {
		request(http.MethodGet, path, "", 200)
		if after, end := deadlines(); !after.Equal(refreshed) || !end.Equal(absolute) {
			t.Fatalf("passive GET %s changed a session deadline", path)
		}
	}
	setDeadlines(6, 4)
	_, absolute = deadlines()
	request(http.MethodGet, systemPath+"/roles", "", 200)
	if after, end := deadlines(); !after.Equal(absolute) || !end.Equal(absolute) {
		t.Fatalf("active GET did not cap idle expiry at absolute expiry: idle %v, absolute %v", after, end)
	}
	for _, path := range passive {
		setDeadlines(60, -1)
		request(http.MethodGet, path, "", 401)
		setDeadlines(-1, -2)
		request(http.MethodGet, path, "", 401)
	}

	t.Run("browser", func(t *testing.T) {
		if os.Getenv("OMNIRELAY_PLAYWRIGHT_MODULE") == "" {
			t.Skip("需要 Playwright")
		}
		for _, fixture := range []struct{ openID, username, role string }{
			{"ou_admin", "admin", ""}, {"ou_auditor", "auditor", access.RoleAuditViewer},
		} {
			seedBotSession(t, ctx, pool, fixture.openID, fixture.role)
			var id string
			if err := pool.QueryRow(ctx, "SELECT id FROM accounts WHERE open_id=$1", fixture.openID).Scan(&id); err != nil {
				t.Fatal(err)
			}
			if err := identity.ProvisionPassword(ctx, pool, cfg.tenantKey, cfg.appID, id, fixture.username, "fixture-password-only"); err != nil {
				t.Fatal(err)
			}
		}
		// 不启动 Worker，让真实 Bot 检测一直处于排队状态并持续轮询。
		server.Config.Handler = handler
		server.StartTLS()
		command := exec.CommandContext(t.Context(), "node", "--test", "--test-name-pattern=后台轮询", "tests/integration/system_roles_browser.test.mjs")
		command.Dir = "../.."
		command.Env = append(os.Environ(), "OMNIRELAY_BROWSER_ORIGIN="+origin,
			"OMNIRELAY_IDLE_BROWSER=1")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("会话空闲浏览器联调失败 %v\n%s", err, output)
		}
		t.Logf("%s", output)
	})
}
