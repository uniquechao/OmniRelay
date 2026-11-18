package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type setupProviderFunc func(*http.Request) (*http.Response, error)

func (f setupProviderFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWebSetupRequiresOAuthAndSavesCredentialsOnce(t *testing.T) {
	ctx, pool := botTestPool(t)
	key := bytes.Repeat([]byte{1}, 32)
	store := newWebSetupStore(pool, key)
	const origin = "https://relay.example.test"
	setupCode := setupToken(key)
	credentials := setupCredentials{
		AppID: "cli_example", AppSecret: "private-fixture-secret", TenantKey: "tenant_fixture", Bootstrap: "ou_admin",
	}
	var providerCalls, saves, verified atomic.Int32
	bothVerifying := make(chan struct{})
	provider := setupProviderFunc(func(r *http.Request) (*http.Response, error) {
		providerCalls.Add(1)
		var body string
		switch r.URL.Path {
		case "/oauth/v3/token":
			if err := r.ParseForm(); err != nil || r.Form.Get("client_id") != credentials.AppID ||
				r.Form.Get("client_secret") != credentials.AppSecret ||
				r.Form.Get("redirect_uri") != origin+"/auth/feishu/callback" ||
				len(r.Form.Get("code_verifier")) != 43 {
				t.Error("unexpected OAuth token exchange")
			}
			body = `{"code":0,"access_token":"fixture_token","token_type":"Bearer"}`
		case "/open-apis/authen/v1/user_info":
			if verified.Add(1) == 2 {
				close(bothVerifying)
			}
			select {
			case <-bothVerifying:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			if r.Header.Get("Authorization") != "Bearer fixture_token" {
				t.Error("unexpected Feishu user-info authorization")
			}
			body = `{"code":0,"data":{"tenant_key":"tenant_fixture","open_id":"ou_admin"}}`
		default:
			t.Error("unexpected OAuth request")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	handler := newWebSetupHandler(store, origin, provider, func() { saves.Add(1) })

	request := func(method, path string, body io.Reader, source string, cookie *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, body).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", source)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}
	submit := func(source, token string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(setupRequest{SetupCode: token, AppID: credentials.AppID, AppSecret: credentials.AppSecret})
		return request(http.MethodPost, "/setup", bytes.NewReader(body), source, nil)
	}
	if home := request(http.MethodGet, "/", nil, "", nil); home.Header().Get("Location") != "/setup" {
		t.Fatal("fresh instance did not open the setup form")
	}
	if page := request(http.MethodGet, "/setup", nil, "", nil); page.Code != http.StatusOK ||
		!strings.Contains(page.Body.String(), "首次配置") || page.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("setup form is missing or publicly cacheable")
	}
	for _, attempt := range []struct{ source, token string }{
		{origin, "wrong"}, {"https://other.example.test", setupCode}, {"", setupCode},
	} {
		if response := submit(attempt.source, attempt.token); response.Code != http.StatusForbidden {
			t.Fatalf("setup accepted unauthorized request: %d", response.Code)
		}
	}

	start := func() (string, *http.Cookie) {
		t.Helper()
		response := submit(origin, setupCode)
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), credentials.AppSecret) ||
			strings.Contains(response.Body.String(), setupCode) {
			t.Fatalf("unexpected setup initiation status %d or credential disclosure", response.Code)
		}
		var payload map[string]string
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		auth, err := url.Parse(payload["authorization_url"])
		if err != nil || auth.Host != "accounts.feishu.cn" || auth.Query().Has("target_tenant") ||
			auth.Query().Get("code_challenge_method") != "S256" || auth.Query().Get("code_challenge") == "" {
			t.Fatal("invalid setup OAuth authorization URL")
		}
		cookies := response.Result().Cookies()
		if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly ||
			cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Path != "/" {
			t.Fatal("setup login was not securely bound to the browser")
		}
		return auth.Query().Get("state"), cookies[0]
	}
	firstState, firstCookie := start()
	secondState, secondCookie := start()
	_, _ = start() // An abandoned authorization must be discarded after setup.
	if found, err := store.configured(ctx); err != nil || found || providerCalls.Load() != 0 {
		t.Fatal("provider identity was not required before saving")
	}
	callback := func(state string, cookie *http.Cookie) *httptest.ResponseRecorder {
		return request(http.MethodGet, "/auth/feishu/callback?code=fixture_code&state="+state, nil, "", cookie)
	}
	if response := callback(firstState, secondCookie); response.Header().Get("Location") != "/setup?error=authorization" {
		t.Fatal("setup accepted a different browser")
	}

	var wg sync.WaitGroup
	for _, pair := range []struct {
		state  string
		cookie *http.Cookie
	}{{firstState, firstCookie}, {secondState, secondCookie}} {
		wg.Go(func() {
			if response := callback(pair.state, pair.cookie); response.Header().Get("Location") != "/?setup=complete" {
				t.Errorf("OAuth callback did not complete setup: %d, %q", response.Code, response.Header().Get("Location"))
			}
		})
	}
	wg.Wait()
	if saves.Load() != 1 || providerCalls.Load() != 4 {
		t.Fatalf("concurrent OAuth completed %d setup writes and %d provider requests", saves.Load(), providerCalls.Load())
	}
	if response := callback(firstState, firstCookie); response.Header().Get("Location") != "/setup?error=authorization" {
		t.Fatal("setup OAuth state was reusable")
	}
	if response := submit(origin, setupCode); response.Code != http.StatusConflict {
		t.Fatalf("setting up again returned %d", response.Code)
	}

	loaded, found, err := newWebSetupStore(pool, key).load(ctx)
	if err != nil || !found || loaded != credentials {
		t.Fatal("failed to load the configuration and verified identity after restart")
	}
	var remaining int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM omnirelay_web_setup_logins").Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("setup retained abandoned authorizations")
	}
	var ciphertext []byte
	if err := pool.QueryRow(ctx, "SELECT credentials FROM omnirelay_web_setup").Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte(credentials.AppSecret)) {
		t.Fatal("persisted setup contains a plaintext secret")
	}
	if _, _, err := newWebSetupStore(pool, bytes.Repeat([]byte{2}, 32)).load(ctx); err == nil {
		t.Fatal("setup credentials decrypted with a different key")
	}

	backend, _, err := newBackend(ctx, pool, configuration{
		appID: loaded.AppID, appSecret: loaded.AppSecret, tenantKey: loaded.TenantKey, bootstrap: loaded.Bootstrap,
		publicOrigin: origin, syncInterval: time.Hour, pageBudget: 1000, credentialsGeneration: 1, botHeartbeatTTL: 30 * time.Second,
	}, provider)
	if err != nil {
		t.Fatal(err)
	}
	page := httptest.NewRecorder()
	backend.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/setup", nil))
	if page.Code != http.StatusNotFound {
		t.Fatal("first-run form remained enabled in the configured backend")
	}
	login := httptest.NewRecorder()
	backend.ServeHTTP(login, httptest.NewRequest(http.MethodGet, "/auth/feishu/start", nil))
	auth, err := url.Parse(login.Header().Get("Location"))
	if err != nil || login.Code != http.StatusFound || auth.Query().Get("target_tenant") != credentials.TenantKey {
		t.Fatal("normal Feishu login did not use the verified tenant after setup")
	}
}
