package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uniquechao/OmniRelay/internal/identity"
)

type departureBrowserFixture struct {
	feishuFixture
	departed atomic.Bool
	narrowed atomic.Bool
}

func (f *departureBrowserFixture) RoundTrip(r *http.Request) (*http.Response, error) {
	status, body := http.StatusOK, ""
	switch r.URL.Path {
	case "/open-apis/contact/v3/scopes":
		if !f.narrowed.Load() {
			return f.feishuFixture.RoundTrip(r)
		}
		body = `{"code":0,"data":{"department_ids":[],"has_more":false}}`
	case "/open-apis/contact/v3/users/find_by_department":
		items := `[{"open_id":"ou_admin","name":"同步管理员","status":{"is_activated":true}},
			{"open_id":"ou_member","name":"样例成员","status":{"is_activated":true},"department_ids":["team"]}]`
		if f.departed.Load() {
			items = "[]"
		}
		body = `{"code":0,"data":{"items":` + items + `,"has_more":false}}`
	case "/open-apis/contact/v3/users/ou_admin", "/open-apis/contact/v3/users/ou_member":
		status, body = http.StatusBadRequest, `{"code":40007,"msg":"user does not exist"}`
	case "/open-apis/authen/v1/user_info":
		if r.Header.Get("Authorization") != "Bearer fixture-access-token-ou_member" {
			return f.feishuFixture.RoundTrip(r)
		}
		body = `{"code":0,"data":{"tenant_key":"tenant_fixture","open_id":"ou_member","name":"样例成员"}}`
	default:
		return f.feishuFixture.RoundTrip(r)
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func TestDirectoryDeparturesBrowser(t *testing.T) {
	dsn := os.Getenv("OMNIRELAY_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("OMNIRELAY_PLAYWRIGHT_MODULE") == "" {
		t.Skip("浏览器联调需要隔离 PostgreSQL 和 Playwright")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Second)
	defer cancel()
	pool := backendTestPool(t, ctx, dsn)
	server := httptest.NewUnstartedServer(nil)
	defer server.Close()
	origin := "https://" + server.Listener.Addr().String()
	cfg := configuration{
		publicOrigin: origin, appID: "cli_fixture", appSecret: "fixture-only", tenantKey: "tenant_fixture",
		bootstrap: "ou_admin", syncInterval: time.Hour, credentialsGeneration: 1, botHeartbeatTTL: 30 * time.Second,
	}
	fixture := &departureBrowserFixture{feishuFixture: feishuFixture{callbackOrigin: origin}}
	handler, workers, err := newBackend(ctx, pool, cfg, fixture)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE accounts SET name='同步管理员' WHERE open_id='ou_admin';
		INSERT INTO accounts(tenant_key,app_id,open_id,name) VALUES ('tenant_fixture','cli_fixture','ou_member','样例成员');
		INSERT INTO role_grants(tenant_key,app_id,account_id,role,source,granted_by)
		SELECT member.tenant_key,member.app_id,member.id,'audit_viewer','platform_admin',admin.id
		FROM accounts member, accounts admin WHERE member.open_id='ou_member' AND admin.open_id='ou_admin'`)
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []struct{ openID, username string }{{"ou_admin", "admin"}, {"ou_member", "member"}} {
		var id string
		if err := pool.QueryRow(ctx, `SELECT id FROM accounts WHERE open_id=$1`, user.openID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if err := identity.ProvisionPassword(ctx, pool, cfg.tenantKey, cfg.appID, id, user.username, "fixture-password-only"); err != nil {
			t.Fatal(err)
		}
	}
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /test-fixture/depart":
			fixture.departed.Store(true)
			w.WriteHeader(http.StatusNoContent)
		case "POST /test-fixture/narrow":
			fixture.narrowed.Store(true)
			w.WriteHeader(http.StatusNoContent)
		case "POST /test-fixture/return":
			fixture.narrowed.Store(false)
			fixture.departed.Store(false)
			w.WriteHeader(http.StatusNoContent)
		default:
			handler.ServeHTTP(w, r)
		}
	})
	server.StartTLS()
	workersDone := make(chan struct{})
	go func() {
		defer close(workersDone)
		workers.Run(ctx)
	}()
	defer func() { cancel(); <-workersDone }()
	command := exec.CommandContext(ctx, "node", "--test", "tests/integration/directory_departures_browser.test.mjs")
	command.Dir = "../.."
	command.Env = append(os.Environ(), "OMNIRELAY_BROWSER_ORIGIN="+origin)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("离职回收浏览器联调失败 %v\n%s", err, output)
	}
	t.Logf("%s", output)
}
