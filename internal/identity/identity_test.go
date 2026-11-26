package identity

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestReturnPathStaysOnWorkbench(t *testing.T) {
	for _, path := range []string{"", "/", "/#/feishu/drive", "/#/projects", "/#/projects/p-1/reports/brief/r-1", "/#/projects/p-1/tasks/source/message/m-1?chat_id=oc_1", "/#/workbench", "/#/system/users", "/#/system/roles", "/#/system/audit", "/#/system/feishu-bot", "/#/system/settings"} {
		if _, err := localReturnPath(path); err != nil {
			t.Errorf("valid path %q was rejected", path)
		}
	}
	for _, old := range []string{"/#/settings", "/#/system/bot", "/#/bot-chats", "/#/projects/p/tasks/list", "/#/projects/p/feishu", "/#/projects/p/brief/r1"} {
		if _, err := localReturnPath(old); err == nil {
			t.Errorf("retired route %q accepted", old)
		}
	}
	if (WorkbenchAccess{Navigation: map[string]bool{"workbench": true}}).canReturnTo("/#/feishu/drive") ||
		!(WorkbenchAccess{Navigation: map[string]bool{"bot_chats": true}}).canReturnTo("/#/feishu/drive") {
		t.Fatal("网盘回跳授权不符")
	}
	for _, suffix := range []string{"", "/oc_example", "/oc_example/om_example"} {
		target := "/#/feishu/bot-chats" + suffix
		if got, err := localReturnPath(target); err != nil || got != target {
			t.Errorf("chat path %q resolved to %q: %v", target, got, err)
		}
		if !(WorkbenchAccess{Navigation: map[string]bool{"bot_chats": true}}).canReturnTo(target) ||
			(WorkbenchAccess{Navigation: map[string]bool{"workbench": true}}).canReturnTo(target) {
			t.Errorf("chat path %q requires bot_chats navigation", target)
		}
	}

	for _, path := range []string{"https://evil.test", "//evil.test", "///evil.test", "/\\evil.test", "/%5cevil.test", "/%2fevil.test", "/one/..//evil.test", "/path%0d%0aInjected", "/\tevil.test",
		"/auth/logout", "/api/v1/me", "/#/login", "/?next=https://evil.test#/workbench", "/#/workbench?next=https://evil.test",
		"/#//evil.test", "/#/system/feishu-bot/../users", "/#/%77orkbench", "/#/workbench%0d%0aInjected",
		"/#/settings?next=https://evil.test", "/#/system/bot/../settings",
		"/#/bot-chats/../workbench", "/#/feishu/bot-chats/oc/om/extra", "/#/feishu/bot-chats/oc%2fom", "/#/bot-chats?next=https://evil.test"} {
		if target, err := localReturnPath(path); err == nil {
			t.Errorf("unsafe path %q was accepted as %q", path, target)
		}
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (rt roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return rt(r) }

func providerResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestFeishuRejectsUnverifiedUserInfo(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"foreign tenant", `{"code":0,"data":{"open_id":"ou_123","tenant_key":"other"}}`},
		{"missing tenant", `{"code":0,"data":{"open_id":"ou_123"}}`},
		{"different app", `{"code":0,"data":{"open_id":"ou_123","tenant_key":"tenant","app_id":"other"}}`},
		{"frozen user", `{"code":20022,"data":{"open_id":"ou_123","tenant_key":"tenant"}}`},
		{"disabled field", `{"code":0,"data":{"open_id":"ou_123","tenant_key":"tenant","status":{"is_frozen":true}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig()
			cfg.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/open-apis/authen/v2/oauth/token" {
					return providerResponse(200, `{"code":0,"access_token":"user-access-token","token_type":"Bearer"}`), nil
				}
				return providerResponse(200, tc.body), nil
			})
			svc, err := New(&pgxpool.Pool{}, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.feishuIdentity(t.Context(), "one-time-code", "verifier"); err == nil {
				t.Fatal("accepted user without verified identity")
			}
		})
	}
}

func TestFeishuDoesNotFollowCredentialRedirects(t *testing.T) {
	cfg := testConfig()
	var calls int
	cfg.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		response := providerResponse(307, "")
		response.Header.Set("Location", "https://unexpected.test/redirect")
		return response, nil
	})
	svc, err := New(&pgxpool.Pool{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.feishuIdentity(t.Context(), "one-time-code", "verifier"); err == nil || calls != 1 {
		t.Fatalf("err=%v outbound calls=%d", err, calls)
	}
}

func TestFeishuRejectsTokenExchangeForAnotherApp(t *testing.T) {
	cfg := testConfig()
	cfg.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/open-apis/authen/v2/oauth/token" {
			t.Fatal("looked up a profile after a rejected token exchange")
		}
		return providerResponse(400, `{"code":20024,"error_description":"private provider diagnostic"}`), nil
	})
	svc, err := New(&pgxpool.Pool{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.feishuIdentity(t.Context(), "one-time-code", "verifier")
	if !errors.Is(err, ErrNotAdmitted) {
		t.Fatalf("expected a generic denial for a code from another app, got %v", err)
	}
	var failure *OAuthFailure
	if !errors.As(err, &failure) || failure.Stage != "token_exchange" ||
		failure.HTTPStatus != http.StatusBadRequest || failure.ProviderCode != 20024 {
		t.Fatal("missing sanitized Feishu token error metadata")
	}
	if strings.Contains(err.Error(), "private provider diagnostic") {
		t.Fatal("error disclosed the upstream response body")
	}
}

func TestFeishuOutageTakesPrecedenceOverProviderErrorCode(t *testing.T) {
	for _, failTokenExchange := range []bool{true, false} {
		cfg := testConfig()
		cfg.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/open-apis/authen/v2/oauth/token" {
				if failTokenExchange {
					return providerResponse(http.StatusTooManyRequests, `{"code":20010}`), nil
				}
				return providerResponse(http.StatusOK, `{"code":0,"access_token":"user-access-token","token_type":"Bearer"}`), nil
			}
			return providerResponse(http.StatusServiceUnavailable, `{"code":20022}`), nil
		})
		svc, err := New(&pgxpool.Pool{}, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.feishuIdentity(t.Context(), "one-time-code", "verifier"); !errors.Is(err, errFeishu) {
			t.Fatalf("expected an upstream failure, got %v", err)
		}
	}
}

func testConfig() Config {
	return Config{AppID: "app", AppSecret: "app-secret", TenantKey: "tenant", PublicOrigin: "https://workbench.example"}
}

func identityPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("OMNIRELAY_IDENTITY_TEST_DSN")
	if dsn == "" {
		t.Skip("set OMNIRELAY_IDENTITY_TEST_DSN to exercise PostgreSQL login/session behavior")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid identity test database configuration")
	}
	admin, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := pgx.Identifier{fmt.Sprintf("identity_test_%d", time.Now().UnixNano())}.Sanitize()
	if _, err = admin.Exec(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	cfg = cfg.Copy()
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, path := range []string{"0001_identity.sql", "0002_access.sql", "0004_passwords.sql", "0033_account_api_keys.sql", "0046_local_administrator.sql"} {
		migration, err := os.ReadFile("../../migrations/" + path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(t.Context(), string(migration)); err != nil {
			t.Fatal(err)
		}
	}
	return pool
}

func TestOAuthSessionLifecycleWithPostgres(t *testing.T) {
	pool := identityPool(t)
	if _, err := pool.Exec(t.Context(), "CREATE TABLE test_audit (id serial PRIMARY KEY, account_id text, action text)"); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig()
	var exchanges atomic.Int32
	var rejectExchange atomic.Bool
	var challenge string
	cfg.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case "https://open.feishu.cn/open-apis/authen/v2/oauth/token":
			exchanges.Add(1)
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			verifier := sha256.Sum256([]byte(payload["code_verifier"]))
			if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json; charset=utf-8" ||
				base64.RawURLEncoding.EncodeToString(verifier[:]) != challenge ||
				payload["client_id"] != cfg.AppID || payload["client_secret"] != cfg.AppSecret ||
				payload["grant_type"] != "authorization_code" || payload["code"] != "code-ok" ||
				payload["redirect_uri"] != cfg.PublicOrigin+"/auth/feishu/callback" {
				t.Error("token exchange request did not match OAuth transaction")
			}
			if rejectExchange.Load() {
				return providerResponse(400, `{"code":20010,"error_description":"private provider diagnostic"}`), nil
			}
			return providerResponse(200, `{"code":0,"access_token":"provider-token","token_type":"Bearer"}`), nil
		case "https://open.feishu.cn/open-apis/authen/v1/user_info":
			if rejectExchange.Load() {
				t.Error("requested user info after token exchange denied login")
			}
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer provider-token" {
				t.Error("userinfo did not use the exchanged user credential")
			}
			return providerResponse(200, `{"code":0,"data":{"open_id":"ou_member","tenant_key":"tenant","name":"Member","avatar_url":"https://img.example/avatar"}}`), nil
		}
		return nil, fmt.Errorf("unexpected outbound request")
	})
	cfg.Audit = func(ctx context.Context, tx pgx.Tx, p Principal, action string) error {
		_, err := tx.Exec(ctx, "INSERT INTO test_audit (account_id, action) VALUES ($1, $2)", p.AccountID, action)
		return err
	}
	svc, err := New(pool, cfg)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	svc.Register(mux)
	request := func(method, path string, cookie *http.Cookie, origin, token string, additionalCookies ...*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, cfg.PublicOrigin+path, nil)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		for _, additional := range additionalCookies {
			r.AddCookie(additional)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if token != "" {
			r.Header.Set("X-CSRF-Token", token)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	start := func() (string, *http.Cookie) {
		w := request("GET", "/auth/feishu/start?return_to="+url.QueryEscape("/#/workbench"), nil, "", "")
		location, err := url.Parse(w.Header().Get("Location"))
		cookies := w.Result().Cookies()
		if err != nil || w.Code != 302 || len(cookies) != 1 {
			t.Fatalf("start status=%d with %d cookies", w.Code, len(cookies))
		}
		if location.Host != "accounts.feishu.cn" || location.Path != "/open-apis/authen/v1/authorize" ||
			location.Query().Get("code_challenge_method") != "S256" || location.Query().Get("target_tenant") != cfg.TenantKey ||
			location.Query().Get("redirect_uri") != cfg.PublicOrigin+"/auth/feishu/callback" {
			t.Fatal("unexpected authorization location")
		}
		challenge = location.Query().Get("code_challenge")
		requireCookieFlags(t, cookies[0])
		return "/auth/feishu/callback?code=code-ok&state=" + url.QueryEscape(location.Query().Get("state")), cookies[0]
	}

	path, browser := start()
	if _, err := pool.Exec(t.Context(), "UPDATE oauth_login_transactions SET expires_at = clock_timestamp() - interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	if w := request("GET", path, browser, "", ""); w.Code != 400 || exchanges.Load() != 0 {
		t.Fatal("expired OAuth transaction was accepted")
	}
	path, browser = start()
	var pending int
	if err := pool.QueryRow(t.Context(), "SELECT COUNT(*) FROM oauth_login_transactions").Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("expired OAuth transaction was not pruned: pending=%d, err=%v", pending, err)
	}
	requireDenied := func(path string, browser *http.Cookie) {
		t.Helper()
		w := request("GET", path, browser, "", "")
		if w.Code != 303 || w.Header().Get("Location") != "/#/login?error=denied" || w.Body.Len() != 0 {
			t.Fatalf("expected the generic login denial, got status %d", w.Code)
		}
		for _, cookie := range w.Result().Cookies() {
			if cookie.Value != "" {
				t.Fatal("login denial issued a cookie")
			}
		}
		if err := pool.QueryRow(t.Context(), "SELECT COUNT(*) FROM web_sessions").Scan(&pending); err != nil || pending != 0 {
			t.Fatalf("login denial created sessions: %d, err=%v", pending, err)
		}
	}
	requireDenied(path, browser)
	if err := pool.QueryRow(t.Context(), "SELECT COUNT(*) FROM accounts").Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("login created an account: %d, err=%v", pending, err)
	}
	var provisionedID string
	if err := pool.QueryRow(t.Context(), `
INSERT INTO accounts (tenant_key, app_id, open_id) VALUES ($1, $2, 'ou_member') RETURNING id
`, cfg.TenantKey, cfg.AppID).Scan(&provisionedID); err != nil {
		t.Fatal(err)
	}
	rejectExchange.Store(true)
	path, browser = start()
	requireDenied(path, browser)
	rejectExchange.Store(false)
	path, browser = start()
	exchangesBefore := exchanges.Load()
	wrong := *browser
	wrong.Value = newSecret()
	if w := request("GET", path, &wrong, "", ""); w.Code != 400 || exchanges.Load() != exchangesBefore {
		t.Fatal("wrong browser consumed OAuth transaction")
	}
	var results [2]*httptest.ResponseRecorder
	var callbacks sync.WaitGroup
	for i := range results {
		callbacks.Go(func() { results[i] = request("GET", path, browser, "", "") })
	}
	callbacks.Wait()
	var session *http.Cookie
	for _, w := range results {
		if w.Code == 303 {
			if w.Header().Get("Location") != "/#/workbench" || w.Header().Get("Referrer-Policy") != "no-referrer" {
				t.Fatal("callback did not redirect to the recorded workbench safely")
			}
			for _, c := range w.Result().Cookies() {
				if c.Value != "" {
					requireCookieFlags(t, c)
					session = c
				}
			}
		} else if w.Code != 400 {
			t.Fatalf("unexpected callback status %d: %s", w.Code, w.Body.String())
		}
	}
	if session == nil || exchanges.Load() != exchangesBefore+1 {
		t.Fatal("OAuth state did not produce exactly one session")
	}

	me := request("GET", "/api/v1/me", session, "", "")
	var profile struct {
		Principal Principal `json:"principal"`
		Token     string    `json:"csrf_token"`
	}
	if me.Code != 200 || json.Unmarshal(me.Body.Bytes(), &profile) != nil || profile.Principal.OpenID != "ou_member" ||
		profile.Principal.AccountID != provisionedID || profile.Token == "" || profile.Token == session.Value {
		t.Fatalf("unexpected authenticated profile: %d %s", me.Code, me.Body.String())
	}
	var rawStored, expectedStored bool
	if err := pool.QueryRow(t.Context(), `
SELECT token_digest = convert_to($1, 'UTF8'), token_digest = $2 FROM web_sessions WHERE account_id = $3
`, session.Value, digest(session.Value), profile.Principal.AccountID).Scan(&rawStored, &expectedStored); err != nil || rawStored || !expectedStored {
		t.Fatalf("session storage did not use token digest: %v", err)
	}
	for _, trial := range []struct{ origin, token string }{
		{"https://evil.test", profile.Token}, {"", profile.Token}, {cfg.PublicOrigin, ""}, {cfg.PublicOrigin, "incorrect"},
	} {
		if w := request("POST", "/auth/logout", session, trial.origin, trial.token); w.Code != 403 {
			t.Fatalf("invalid logout returned %d instead of 403", w.Code)
		}
	}
	if w := request("POST", "/auth/logout", session, cfg.PublicOrigin, profile.Token); w.Code != 204 {
		t.Fatalf("valid logout returned %d", w.Code)
	}
	if w := request("GET", "/api/v1/me", session, "", ""); w.Code != 401 {
		t.Fatalf("logged out session returned %d instead of 401", w.Code)
	}
	var audit []string
	if err := pool.QueryRow(t.Context(), "SELECT array_agg(action ORDER BY id) FROM test_audit WHERE account_id = $1",
		profile.Principal.AccountID).Scan(&audit); err != nil || strings.Join(audit, ",") != "identity.login,identity.logout" {
		t.Fatalf("unexpected identity audit contents: %v, err=%v", audit, err)
	}

	path, browser = start()
	if _, err := pool.Exec(t.Context(), "UPDATE oauth_login_transactions SET return_path = '/legacy-path?view=all'"); err != nil {
		t.Fatal(err)
	}
	w := request("GET", path, browser, "", "")
	if w.Code != 303 || w.Header().Get("Location") != "/" {
		t.Fatalf("returning account was not reused: %d %s", w.Code, w.Body.String())
	}
	for _, c := range w.Result().Cookies() {
		if c.Value != "" {
			session = c
		}
	}
	previousSession := session
	path, browser = start()
	w = request("GET", path, browser, "", "", previousSession)
	if w.Code != 303 || w.Header().Get("Location") != "/#/workbench" {
		t.Fatalf("session replacement returned %d", w.Code)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			session = c
		}
	}
	if session == previousSession || session.Value == previousSession.Value ||
		request("GET", "/api/v1/me", previousSession, "", "").Code != 401 ||
		request("GET", "/api/v1/me", session, "", "").Code != 200 {
		t.Fatal("OAuth did not replace the previous session")
	}
	if _, err := pool.Exec(t.Context(), "UPDATE web_sessions SET idle_expires_at = clock_timestamp() - interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	if w := request("GET", "/api/v1/me", session, "", ""); w.Code != 401 {
		t.Fatalf("idle-expired session returned %d instead of 401", w.Code)
	}
	path, browser = start()
	w = request("GET", path, browser, "", "")
	if w.Code != 303 {
		t.Fatalf("login after idle expiry returned %d instead of 303", w.Code)
	}
	if err := pool.QueryRow(t.Context(), "SELECT COUNT(*) FROM web_sessions").Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("expired session was not pruned: sessions=%d, err=%v", pending, err)
	}
	for _, c := range w.Result().Cookies() {
		if c.Value != "" {
			session = c
		}
	}
	if _, err := pool.Exec(t.Context(), `
UPDATE web_sessions SET expires_at = clock_timestamp() - interval '1 second', idle_expires_at = clock_timestamp() - interval '2 seconds'
`); err != nil {
		t.Fatal(err)
	}
	if w := request("GET", "/api/v1/me", session, "", ""); w.Code != 401 {
		t.Fatalf("absolute-expired session returned %d instead of 401", w.Code)
	}
	path, browser = start()
	w = request("GET", path, browser, "", "")
	if w.Code != 303 {
		t.Fatalf("login after absolute expiry returned %d instead of 303", w.Code)
	}
	for _, c := range w.Result().Cookies() {
		if c.Value != "" {
			session = c
		}
	}
	if _, err := pool.Exec(t.Context(), "UPDATE accounts SET active=false WHERE id=$1", profile.Principal.AccountID); err != nil {
		t.Fatal(err)
	}
	if w := request("GET", "/api/v1/me", session, "", ""); w.Code != 401 {
		t.Fatalf("disabled account retained session, received %d", w.Code)
	}
	path, browser = start()
	if w := request("GET", path, browser, "", ""); w.Code != 303 || w.Header().Get("Location") != "/#/login?error=denied" {
		t.Fatalf("disabled account obtained a session, received %d", w.Code)
	}
}

func requireCookieFlags(t *testing.T, c *http.Cookie) {
	t.Helper()
	if !strings.HasPrefix(c.Name, "__Host-") || !c.HttpOnly || !c.Secure || c.Path != "/" ||
		c.Domain != "" || c.SameSite != http.SameSiteLaxMode {
		t.Error("cookie has unsafe security attributes")
	}
}

func TestSharedWorkbenchTargets(t *testing.T) {
	body, err := os.ReadFile("../../tests/fixtures/workbench-paths.json")
	if err != nil {
		t.Fatal(err)
	}
	var samples struct {
		Access   map[string]WorkbenchAccess
		Cases    [][3]json.RawMessage
		Patterns [][3]json.RawMessage
	}
	if err = json.Unmarshal(body, &samples); err != nil {
		t.Fatal(err)
	}
	decode := func(data json.RawMessage, v any) {
		t.Helper()
		if e := json.Unmarshal(data, v); e != nil {
			t.Fatal(e)
		}
	}
	for _, row := range samples.Cases {
		var path, want string
		var roles []string
		decode(row[0], &path)
		decode(row[1], &want)
		decode(row[2], &roles)
		got, e := localReturnPath(path)
		if got != want || (e == nil) != (want != "") {
			t.Fatalf("路径 %q 返回 %q，预期 %q", path, got, want)
		}
		for name, access := range samples.Access {
			granted := false
			for _, role := range roles {
				if name == role {
					granted = true
				}
			}
			if access.canReturnTo(got) != granted {
				t.Fatalf("角色 %s 的路径 %q 权限不符", name, path)
			}
		}
	}
	for _, row := range samples.Patterns {
		var pattern, path string
		var valid bool
		decode(row[0], &pattern)
		decode(row[1], &path)
		decode(row[2], &valid)
		if matchPathPattern(pattern, path, ValidProjectID) != valid {
			t.Fatal(path)
		}
		if _, err := localReturnPath(path); err == nil {
			t.Fatalf("测试路径进入生产白名单 %q", path)
		}
	}
}

func TestReturnRechecksResourceAccess(t *testing.T) {
	access := WorkbenchAccess{Navigation: map[string]bool{"workbench": true, "bot_chats": true}, DefaultRoute: "/#/workbench"}
	for _, tc := range []struct{ path, project, chat string }{
		{"/#/projects/p/overview", "p", ""}, {"/#/projects/p/chats/c/m", "p", "c"}, {"/#/feishu/bot-chats/c/m", "", "c"},
	} {
		s := &Service{}
		for _, granted := range []bool{false, true} {
			called := false
			s.cfg.ResourceRead = func(_ context.Context, p Principal, project, chat string) (bool, error) {
				called = true
				if p.AccountID != "actor" || project != tc.project || chat != tc.chat {
					t.Fatal("回跳校验使用了错误的资源或账号")
				}
				return granted, nil
			}
			result := s.returnForAccess(t.Context(), Principal{AccountID: "actor"}, access, tc.path)
			want := access.DefaultRoute
			if granted {
				want, _ = localReturnPath(tc.path)
			}
			if !called || result != want {
				t.Fatalf("回跳 %q 返回 %q，预期 %q", tc.path, result, want)
			}
		}
	}
}

func TestCandidateSameOriginIsolation(t *testing.T) {
	prodDB, candidateDB := identityPool(t), identityPool(t)
	cfg := testConfig()
	prod, err := New(prodDB, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AppSecret = ""
	cfg.Candidate = &CandidateOAuth{Run: strings.Repeat("a", 32), StateKey: newSecret(), Resolve: func(context.Context, string, string) (Principal, error) {
		return Principal{AppID: cfg.AppID, TenantKey: cfg.TenantKey, OpenID: "ou_member"}, nil
	}}
	candidate, err := New(candidateDB, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = candidateDB.Exec(t.Context(), "INSERT INTO accounts(tenant_key,app_id,open_id) VALUES($1,$2,'ou_member')", cfg.TenantKey, cfg.AppID); err != nil {
		t.Fatal(err)
	}
	request := func(s *Service, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		mux := http.NewServeMux()
		s.Register(mux)
		r := httptest.NewRequest("GET", cfg.PublicOrigin+path, nil)
		for _, c := range cookies {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	begin := func(s *Service) (string, *http.Cookie) {
		w := request(s, "/auth/feishu/start")
		if w.Code != 302 {
			t.Fatal("登录事务未建立")
		}
		u, _ := url.Parse(w.Header().Get("Location"))
		return callbackPath + "?state=" + url.QueryEscape(u.Query().Get("state")), w.Result().Cookies()[0]
	}
	fromProd, prodCookie := begin(prod)
	path, cookie := begin(candidate)
	if prodCookie.Name == cookie.Name || request(candidate, fromProd+"&code=x", prodCookie, cookie).Code != 400 || request(prod, path+"&code=x", cookie, prodCookie).Code != 400 {
		t.Fatal("同源登录事务隔离失败")
	}
	done := request(candidate, path+"&code=x", cookie, prodCookie)
	if done.Code != 303 || request(candidate, path+"&code=x", cookie).Code != 400 {
		t.Fatal("候选换码或重放校验失败")
	}
	var session *http.Cookie
	for _, c := range done.Result().Cookies() {
		if c.Value != "" {
			session = c
		}
	}
	if session == nil || session.Name == sessionCookie || request(prod, "/api/v1/me", session).Code != 401 || request(candidate, "/api/v1/me", session).Code != 200 {
		t.Fatal("同源会话隔离失败")
	}
	path, cookie = begin(candidate)
	if w := request(candidate, path+"&error=access_denied", cookie); w.Code != 303 || !strings.Contains(w.Header().Get("Location"), "cancelled") {
		t.Fatal("取消回跳失败")
	}
}
