package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
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
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	pool := backendTestPool(t, ctx, dsn)
	cfg := configuration{
		publicOrigin: fixtureOrigin, appID: "cli_fixture", appSecret: "fixture-only",
		tenantKey: "tenant_fixture", bootstrap: "ou_admin", syncInterval: time.Hour,
	}
	handler, directory, err := newBackend(ctx, pool, cfg, feishuFixture{})
	if err != nil {
		t.Fatal(err)
	}
	directoryDone := make(chan struct{})
	go func() {
		defer close(directoryDone)
		directory.Run(ctx)
	}()
	defer func() {
		stop()
		<-directoryDone
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
	login := func(code string, status int) (*http.Cookie, identity.Principal, string) {
		t.Helper()
		start := do("GET", "/auth/feishu/start?return_to=%2F%23%2Fworkbench", "", nil, "")
		wantStatus(start, http.StatusFound)
		authorization, err := url.Parse(start.Header().Get("Location"))
		if err != nil || authorization.Host != "accounts.feishu.cn" ||
			authorization.Query().Get("code_challenge_method") != "S256" || authorization.Query().Get("state") == "" {
			t.Fatal("missing Feishu state or PKCE challenge")
		}
		proof := findCookie(start, "__Host-omnirelay_oauth")
		callback := "/auth/feishu/callback?" + url.Values{"code": {code}, "state": {authorization.Query().Get("state")}}.Encode()
		finish := do("GET", callback, "", proof, "")
		wantStatus(finish, status)
		if status == http.StatusForbidden {
			for _, cookie := range finish.Result().Cookies() {
				if cookie.Name == "__Host-omnirelay_session" && cookie.Value != "" {
					t.Fatal("denied login issued a session cookie")
				}
			}
			return nil, identity.Principal{}, ""
		}
		if finish.Header().Get("Location") != "/#/workbench" || finish.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatal("unexpected redirect or referrer policy")
		}
		wantStatus(do("GET", callback, "", proof, ""), http.StatusBadRequest)
		session := findCookie(finish, "__Host-omnirelay_session")
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
	begin := do("GET", "/auth/feishu/start", "", nil, "")
	wantStatus(begin, http.StatusFound)
	authURL, err := url.Parse(begin.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	cancelPath := "/auth/feishu/callback?" + url.Values{"error": {"access_denied"}, "state": {authURL.Query().Get("state")}}.Encode()
	proof := findCookie(begin, "__Host-omnirelay_oauth")
	cancelled := do("GET", cancelPath, "", proof, "")
	wantStatus(cancelled, http.StatusSeeOther)
	if cancelled.Header().Get("Location") != "/#/login?error=feishu" {
		t.Fatal("cancelled Feishu authorization did not return to the login page")
	}
	wantStatus(do("GET", cancelPath, "", proof, ""), http.StatusBadRequest)
	wantStatus(do("GET", "/readyz", "", nil, ""), http.StatusNoContent)
	wantStatus(do("GET", "/api/v1/system/directory/people", "", nil, ""), http.StatusUnauthorized)
	admin, _, csrf := login("ou_admin", http.StatusSeeOther)
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

	login("ou_member", http.StatusForbidden)
	accountsPath := "/api/v1/system/accounts"
	wantStatus(do("POST", accountsPath, `{"openId":"ou_member"}`, admin, ""), http.StatusForbidden)
	wantStatus(do("POST", accountsPath, `{"openId":"ou_member"}`, admin, csrf), http.StatusCreated)
	wantStatus(do("POST", accountsPath, `{"openId":"ou_member"}`, admin, csrf), http.StatusConflict)
	member, target, memberCSRF := login("ou_member", http.StatusSeeOther)
	syncPath := "/api/v1/system/directory/sync-runs"
	wantStatus(do("POST", syncPath, "", member, memberCSRF), http.StatusForbidden)
	wantStatus(do("POST", syncPath, "", admin, csrf), http.StatusAccepted)
	wantStatus(do("GET", "/api/v1/system/accounts", "", member, ""), http.StatusForbidden)
	grantPath := "/api/v1/system/role-grants"
	grant := do("POST", grantPath, fmt.Sprintf(`{"accountId":%q,"role":"audit_viewer"}`, target.AccountID), admin, csrf)
	wantStatus(grant, http.StatusCreated)
	member, _, _ = login("ou_member", http.StatusSeeOther)
	wantStatus(do("GET", "/api/v1/system/audit-events", "", member, ""), http.StatusOK)
	var binding struct{ ID string }
	if err := json.Unmarshal(grant.Body.Bytes(), &binding); err != nil || binding.ID == "" {
		t.Fatalf("malformed grant response: %v", err)
	}
	wantStatus(do("DELETE", grantPath+"/"+binding.ID, "", admin, csrf), http.StatusNoContent)
	wantStatus(do("GET", "/api/v1/me", "", member, ""), http.StatusUnauthorized)
	member, _, _ = login("ou_member", http.StatusSeeOther)
	wantStatus(do("GET", "/api/v1/system/audit-events", "", member, ""), http.StatusForbidden)
	accountPath := accountsPath + "/" + target.AccountID
	wantStatus(do("PATCH", accountPath, `{"active":false}`, admin, ""), http.StatusForbidden)
	wantStatus(do("PATCH", accountPath, `{"active":false}`, admin, csrf), http.StatusOK)
	wantStatus(do("GET", "/api/v1/me", "", member, ""), http.StatusUnauthorized)
	login("ou_member", http.StatusForbidden)
	wantStatus(do("POST", "/auth/logout", "", admin, csrf), http.StatusNoContent)
	wantStatus(do("GET", "/api/v1/me", "", admin, ""), http.StatusUnauthorized)
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

type feishuFixture struct{}

func (feishuFixture) RoundTrip(r *http.Request) (*http.Response, error) {
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
			token = r.Form.Get("code")
		}
		body = fmt.Sprintf(`{"code":0,"access_token":%q,"token_type":"Bearer"}`, token)
	case "/open-apis/authen/v1/user_info":
		openID := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if openID != "ou_admin" && openID != "ou_member" {
			return nil, fmt.Errorf("unknown fixture credential")
		}
		body = fmt.Sprintf(`{"code":0,"data":{"tenant_key":"tenant_fixture","open_id":%q,"name":"Example user"}}`, openID)
	case "/open-apis/contact/v3/scopes":
		body = `{"code":0,"data":{"department_ids":["team"],"has_more":false}}`
	case "/open-apis/contact/v3/departments/team":
		body = `{"code":0,"data":{"department":{"open_department_id":"team","parent_department_id":"0","name":"Example team"}}}`
	case "/open-apis/contact/v3/departments/team/children":
		body = `{"code":0,"data":{"items":[],"has_more":false}}`
	case "/open-apis/contact/v3/users/find_by_department":
		body = `{"code":0,"data":{"items":[{"open_id":"ou_member","name":"Example member","status":{"is_activated":true},"department_ids":["team"]}],"has_more":false}}`
	default:
		return nil, fmt.Errorf("unexpected fixture route")
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)),
		Header: http.Header{"Content-Type": {"application/json"}}, Request: r}, nil
}
