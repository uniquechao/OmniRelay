package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/migrations"
)

const fixtureOrigin = "https://relay.example.test"

func TestBackendFeishuAdmissionAndRevocation(t *testing.T) {
	dsn := os.Getenv("OMNIRELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL")
	}
	var logs bytes.Buffer
	previousLogger := slog.Default()
	previousWriter, previousFlags := log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() {
		slog.SetDefault(previousLogger)
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	pool := backendTestPool(t, ctx, dsn)
	cfg := configuration{
		publicOrigin: fixtureOrigin, appID: "cli_fixture", appSecret: "fixture-only",
		tenantKey: "tenant_fixture", bootstrap: "ou_admin", syncInterval: time.Hour, credentialsGeneration: 1, botHeartbeatTTL: 30 * time.Second,
	}
	fixture := &feishuFixture{}
	handler, workers, err := newBackend(ctx, pool, cfg, fixture)
	if err != nil {
		t.Fatal(err)
	}
	workersDone := make(chan struct{})
	go func() {
		defer close(workersDone)
		workers.Run(ctx)
	}()
	defer func() {
		stop()
		<-workersDone
	}()

	do := func(method, path, body string, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, fixtureOrigin+path, strings.NewReader(body))
		if cookie != nil {
			req.AddCookie(cookie)
		}
		req.Header.Set("Origin", fixtureOrigin)
		req.Header.Set("Content-Type", "application/json")
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	wantStatus := func(res *httptest.ResponseRecorder, status int) {
		t.Helper()
		if res.Code != status {
			t.Fatalf("got HTTP %d, expected %d: %s", res.Code, status, res.Body.String())
		}
	}
	findCookie := func(res *httptest.ResponseRecorder, name string) *http.Cookie {
		t.Helper()
		for _, cookie := range res.Result().Cookies() {
			if cookie.Name == name && cookie.Value != "" {
				if !cookie.Secure || !cookie.HttpOnly || cookie.Path != "/" || cookie.Domain != "" ||
					cookie.SameSite != http.SameSiteLaxMode {
					t.Fatal("session or browser proof cookie lacked secure attributes")
				}
				return cookie
			}
		}
		t.Fatalf("missing cookie %s", name)
		return nil
	}
	const returnPath = "/#/system/feishu-bot"
	checkFailure := func(res *httptest.ResponseRecorder, reason string) {
		t.Helper()
		wantStatus(res, http.StatusSeeOther)
		if res.Header().Get("Location") != "/#/login?error="+reason || res.Body.Len() != 0 {
			t.Fatalf("unexpected OAuth failure response for %s", reason)
		}
		cookies := res.Result().Cookies()
		if len(cookies) != 1 || cookies[0].Name != "__Host-omnirelay_oauth" || cookies[0].Value != "" || cookies[0].MaxAge >= 0 {
			t.Fatal("OAuth failure did not only clear the browser proof")
		}
	}
	var sensitive []string
	login := func(code, reason string) (*http.Cookie, identity.Principal, string) {
		t.Helper()
		start := do("GET", "/auth/feishu/start?return_to="+url.QueryEscape(returnPath), "", nil, "")
		wantStatus(start, http.StatusFound)
		authorization, err := url.Parse(start.Header().Get("Location"))
		if err != nil || authorization.Host != "accounts.feishu.cn" ||
			authorization.Query().Get("code_challenge_method") != "S256" || authorization.Query().Get("state") == "" {
			t.Fatal("missing Feishu state or PKCE challenge")
		}
		proof := findCookie(start, "__Host-omnirelay_oauth")
		state := authorization.Query().Get("state")
		callback := "/auth/feishu/callback?" + url.Values{"code": {code}, "state": {state}}.Encode()
		finish := do("GET", callback, "", proof, "")
		sensitive = append(sensitive, callback, state, proof.Value, "fixture-access-token-"+code)
		wantStatus(do("GET", callback, "", proof, ""), http.StatusBadRequest)
		if reason != "" {
			checkFailure(finish, reason)
			return nil, identity.Principal{}, ""
		}
		wantStatus(finish, http.StatusSeeOther)
		if finish.Header().Get("Location") != returnPath || finish.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatal("unexpected redirect or referrer policy")
		}
		session := findCookie(finish, "__Host-omnirelay_session")
		sensitive = append(sensitive, session.Value)
		me := do("GET", "/api/v1/me", "", session, "")
		wantStatus(me, http.StatusOK)
		var response struct {
			Principal identity.Principal `json:"principal"`
			CSRF      string             `json:"csrf_token"`
		}
		err = json.Unmarshal(me.Body.Bytes(), &response)
		if err != nil || response.Principal.OpenID != code || response.CSRF == "" {
			t.Fatalf("unexpected authenticated session: %v", err)
		}
		return session, response.Principal, response.CSRF
	}

	home := do("GET", "/", "", nil, "")
	wantStatus(home, http.StatusOK)
	if !strings.Contains(home.Body.String(), `id="password-login"`) ||
		!strings.Contains(home.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatal("workbench or its content security policy is missing")
	}
	wantStatus(do("GET", "/omni-relay-workbench.html", "", nil, ""), http.StatusNotFound)
	for _, path := range []string{"https://offsite.test/", "/%2Foffsite.test", "/#/workbench?next=//offsite.test"} {
		wantStatus(do("GET", "/auth/feishu/start?return_to="+url.QueryEscape(path), "", nil, ""), http.StatusBadRequest)
	}
	begin := do("GET", "/auth/feishu/start", "", nil, "")
	wantStatus(begin, http.StatusFound)
	authURL, err := url.Parse(begin.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	const providerDiagnostic = "private-fixture-provider-diagnostic"
	cancelPath := "/auth/feishu/callback?" + url.Values{"error": {"access_denied"}, "error_description": {providerDiagnostic}, "state": {authURL.Query().Get("state")}}.Encode()
	proof := findCookie(begin, "__Host-omnirelay_oauth")
	sensitive = append(sensitive, cancelPath, authURL.Query().Get("state"), proof.Value,
		providerDiagnostic, "fixture-code-userinfo-outage", "fixture-only")
	invalid := do("GET", cancelPath, "", nil, "")
	wantStatus(invalid, http.StatusBadRequest)
	if invalid.Header().Get("Location") != "" || len(invalid.Header().Values("Set-Cookie")) != 0 {
		t.Fatal("invalid transaction redirected or issued a cookie")
	}
	wantStatus(do("GET", cancelPath+"&state="+url.QueryEscape(authURL.Query().Get("state")), "", proof, ""), http.StatusBadRequest)
	checkFailure(do("GET", cancelPath, "", proof, ""), "cancelled")
	wantStatus(do("GET", cancelPath, "", proof, ""), http.StatusBadRequest)
	login("fixture-code-userinfo-outage", "unavailable")
	login("fixture-code-foreign-tenant", "denied")
	login("fixture-code-foreign-app", "denied")
	var sessions int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM web_sessions").Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("failed callbacks left %d sessions: %v", sessions, err)
	}
	wantStatus(do("GET", "/readyz", "", nil, ""), http.StatusNoContent)
	wantStatus(do("GET", "/api/v1/system/directory/people", "", nil, ""), http.StatusUnauthorized)
	const botAPI = "/api/v1/system/feishu-bot"
	wantStatus(do("GET", botAPI, "", nil, ""), http.StatusUnauthorized)
	admin, _, csrf := login("ou_admin", "")
	wantStatus(do("GET", botAPI, "", admin, ""), http.StatusOK)
	peoplePath := "/api/v1/system/directory/people?department_id=team"
	for {
		people := do("GET", peoplePath, "", admin, "")
		if people.Code == http.StatusOK && strings.Contains(people.Body.String(), "ou_member") {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("initial directory snapshot was not published: HTTP %d %s", people.Code, people.Body.String())
		case <-time.After(25 * time.Millisecond):
		}
	}

	login("ou_member", "denied")
	accountsPath := "/api/v1/system/accounts"
	wantStatus(do("POST", accountsPath, `{"openId":"ou_member"}`, admin, ""), http.StatusForbidden)
	wantStatus(do("POST", accountsPath, `{"openId":"ou_member"}`, admin, csrf), http.StatusCreated)
	wantStatus(do("POST", accountsPath, `{"openId":"ou_member"}`, admin, csrf), http.StatusConflict)
	member, target, memberCSRF := login("ou_member", "")
	wantStatus(do("GET", botAPI, "", member, ""), http.StatusForbidden)
	syncPath := "/api/v1/system/directory/sync-runs"
	wantStatus(do("POST", syncPath, "", member, memberCSRF), http.StatusForbidden)
	fixture.resigned.Store(true)
	submitted := do("POST", syncPath, "", admin, csrf)
	wantStatus(submitted, http.StatusAccepted)
	var syncRun struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(submitted.Body.Bytes(), &syncRun); err != nil || syncRun.ID == 0 {
		t.Fatalf("invalid synchronization response: %s", submitted.Body.String())
	}
	for {
		status := do("GET", fmt.Sprintf("%s/%d", syncPath, syncRun.ID), "", admin, "")
		wantStatus(status, http.StatusOK)
		var progress struct {
			State string `json:"state"`
		}
		if err := json.Unmarshal(status.Body.Bytes(), &progress); err != nil || progress.State == "failed" {
			t.Fatalf("directory synchronization failed: %s", status.Body.String())
		}
		if progress.State == "succeeded" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("directory synchronization did not finish: %s", status.Body.String())
		case <-time.After(25 * time.Millisecond):
		}
	}
	people := do("GET", peoplePath, "", admin, "")
	wantStatus(people, http.StatusOK)
	if !strings.Contains(people.Body.String(), `"directory_status":"resigned"`) ||
		!strings.Contains(people.Body.String(), `"account_status":"active"`) ||
		!strings.Contains(people.Body.String(), `"platform_roles":[]`) {
		t.Fatalf("directory status changed the local account or roles: %s", people.Body.String())
	}
	wantStatus(do("GET", "/api/v1/me", "", member, ""), http.StatusOK)
	wantStatus(do("GET", "/api/v1/system/accounts", "", member, ""), http.StatusForbidden)
	grantPath := "/api/v1/system/role-grants"
	grant := do("POST", grantPath, fmt.Sprintf(`{"accountId":%q,"role":"audit_viewer"}`, target.AccountID), admin, csrf)
	wantStatus(grant, http.StatusCreated)
	member, _, _ = login("ou_member", "")
	wantStatus(do("GET", "/api/v1/system/audit-events", "", member, ""), http.StatusOK)
	var binding struct{ ID string }
	if err := json.Unmarshal(grant.Body.Bytes(), &binding); err != nil || binding.ID == "" {
		t.Fatalf("malformed grant response: %v", err)
	}
	wantStatus(do("DELETE", grantPath+"/"+binding.ID, "", admin, csrf), http.StatusNoContent)
	wantStatus(do("GET", "/api/v1/me", "", member, ""), http.StatusUnauthorized)
	member, _, _ = login("ou_member", "")
	wantStatus(do("GET", "/api/v1/system/audit-events", "", member, ""), http.StatusForbidden)
	accountPath := accountsPath + "/" + target.AccountID
	wantStatus(do("PATCH", accountPath, `{"active":false}`, admin, ""), http.StatusForbidden)
	wantStatus(do("PATCH", accountPath, `{"active":false}`, admin, csrf), http.StatusOK)
	wantStatus(do("GET", "/api/v1/me", "", member, ""), http.StatusUnauthorized)
	login("ou_member", "denied")
	wantStatus(do("POST", "/auth/logout", "", admin, csrf), http.StatusNoContent)
	wantStatus(do("GET", "/api/v1/me", "", admin, ""), http.StatusUnauthorized)
	stop()
	<-workersDone
	output := logs.String()
	for _, value := range sensitive {
		if strings.Contains(output, value) {
			t.Fatal("backend logs captured OAuth request or credential data")
		}
	}
	for _, code := range []string{"cancelled", "denied", "unavailable"} {
		if !strings.Contains(output, "code="+code) {
			t.Fatalf("missing sanitized diagnostic for %s", code)
		}
	}
}

func backendTestPool(t *testing.T, ctx context.Context, dsn string) *pgxpool.Pool {
	t.Helper()
	control, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(control.Close)
	var unique [8]byte
	if _, err := rand.Read(unique[:]); err != nil {
		t.Fatal(err)
	}
	schema := "backend_" + hex.EncodeToString(unique[:])
	if _, err := control.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = control.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	configuration, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	configuration.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, configuration)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

type feishuFixture struct{ resigned atomic.Bool }

func (f *feishuFixture) RoundTrip(r *http.Request) (*http.Response, error) {
	var body string
	switch r.URL.Path {
	case "/oauth/v3/token":
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		token := "tenant-fixture"
		if r.Form.Get("grant_type") == "authorization_code" {
			if r.Form.Get("code_verifier") == "" || r.Form.Get("redirect_uri") != fixtureOrigin+"/auth/feishu/callback" {
				return nil, fmt.Errorf("incorrect OAuth exchange")
			}
			token = "fixture-access-token-" + r.Form.Get("code")
		}
		body = fmt.Sprintf(`{"code":0,"access_token":%q,"token_type":"Bearer"}`, token)
	case "/open-apis/authen/v1/user_info":
		openID := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer fixture-access-token-")
		switch openID {
		case "fixture-code-userinfo-outage":
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(
				`{"code":20050,"msg":"private-fixture-provider-diagnostic"}`))}, nil
		case "fixture-code-foreign-tenant":
			body = `{"code":0,"data":{"tenant_key":"other_tenant","open_id":"ou_admin"}}`
		case "fixture-code-foreign-app":
			body = `{"code":0,"data":{"tenant_key":"tenant_fixture","app_id":"other_app","open_id":"ou_admin"}}`
		case "ou_admin", "ou_member":
			body = fmt.Sprintf(`{"code":0,"data":{"tenant_key":"tenant_fixture","open_id":%q,"name":"Example user"}}`, openID)
		default:
			return nil, fmt.Errorf("unknown fixture credential")
		}
	case "/open-apis/contact/v3/scopes":
		body = `{"code":0,"data":{"department_ids":["team"],"has_more":false}}`
	case "/open-apis/contact/v3/departments/team":
		body = `{"code":0,"data":{"department":{"open_department_id":"team","parent_department_id":"0","name":"Example team"}}}`
	case "/open-apis/contact/v3/departments/team/children":
		body = `{"code":0,"data":{"items":[],"has_more":false}}`
	case "/open-apis/contact/v3/users/find_by_department":
		body = fmt.Sprintf(`{"code":0,"data":{"items":[{"open_id":"ou_member","name":"Example member","status":{"is_activated":true,"is_resigned":%t},"department_ids":["team"]}],"has_more":false}}`, f.resigned.Load())
	default:
		return nil, fmt.Errorf("unexpected fixture route")
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)),
		Header: http.Header{"Content-Type": {"application/json"}}, Request: r}, nil
}
