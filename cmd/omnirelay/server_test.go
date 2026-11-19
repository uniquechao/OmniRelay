package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSetupCallbackWindowBoundsAnInstance(t *testing.T) {
	var limiter requestWindow
	now := time.Unix(100, 0)
	for i := 0; i < 3; i++ {
		if !limiter.allow(now, 3) {
			t.Fatal("burst should fit the configured bound")
		}
	}
	if limiter.allow(now.Add(59*time.Second), 3) {
		t.Fatal("request above the window limit was admitted")
	}
	if !limiter.allow(now.Add(time.Minute), 3) {
		t.Fatal("new window should admit a request")
	}
}

func TestBackendOAuthLimitsIsolateSourcesAndValidCallbacks(t *testing.T) {
	dsn := os.Getenv("OMNIRELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL")
	}
	pool := backendTestPool(t, t.Context(), dsn)
	cfg := configuration{
		publicOrigin: fixtureOrigin, appID: "cli_fixture", appSecret: "fixture-only",
		tenantKey: "tenant_fixture", bootstrap: "ou_admin", syncInterval: time.Hour,
		credentialsGeneration: 1, botHeartbeatTTL: 30 * time.Second, oauthProxyToken: strings.Repeat("ab", 32),
	}
	handler, _, err := newBackend(t.Context(), pool, cfg, &feishuFixture{})
	if err != nil {
		t.Fatal(err)
	}
	request := func(path, source, token string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, fixtureOrigin+path, nil)
		r.RemoteAddr = "192.0.2.10:12345"
		r.Header.Set("X-Omnirelay-Client-IP", source)
		r.Header.Set("X-Omnirelay-Proxy-Token", token)
		r.Header.Set("X-Forwarded-For", source)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	want := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status || status == http.StatusTooManyRequests && w.Header().Get("Retry-After") == "" {
			t.Fatalf("expected HTTP %d with retry feedback when limited, got %d", status, w.Code)
		}
	}
	const start = "/auth/feishu/start"
	for i := 1; i <= 20; i++ {
		want(request(start, fmt.Sprintf("203.0.113.%d", i), "forged", nil), http.StatusFound)
	}
	want(request(start, "198.51.100.1", "forged", nil), http.StatusTooManyRequests)
	for i := 0; i < 20; i++ {
		want(request(start, "198.51.100.1", cfg.oauthProxyToken, nil), http.StatusFound)
	}
	want(request(start, "198.51.100.1", cfg.oauthProxyToken, nil), http.StatusTooManyRequests)
	begin := request(start, "198.51.100.2", cfg.oauthProxyToken, nil)
	want(begin, http.StatusFound)
	authorization, err := url.Parse(begin.Header().Get("Location"))
	if err != nil || len(begin.Result().Cookies()) != 1 {
		t.Fatal("start did not issue an OAuth transaction")
	}
	proof := begin.Result().Cookies()[0]
	state := authorization.Query().Get("state")
	const callback = "/auth/feishu/callback?"
	success := callback + url.Values{"state": {state}, "code": {"ou_admin"}}.Encode()
	want(request(success, "", "", nil), http.StatusBadRequest)
	wrongState := callback + url.Values{"state": {strings.Repeat("A", 43)}, "code": {"ou_admin"}}.Encode()
	for i := 1; i < 240; i++ {
		want(request(wrongState, "", "", proof), http.StatusBadRequest)
	}
	want(request(wrongState, "", "", proof), http.StatusTooManyRequests)
	w := request(success, "", "", proof)
	want(w, http.StatusSeeOther)
	foundSession := false
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == "__Host-omnirelay_session" && cookie.Value != "" {
			foundSession = true
		}
	}
	if !foundSession {
		t.Fatal("valid callback did not create a session after unverified capacity was exhausted")
	}
	for i := 0; i < 240; i++ {
		want(request(success, "", "", proof), http.StatusBadRequest)
	}
	cancel := request(start, "198.51.100.3", cfg.oauthProxyToken, nil)
	want(cancel, http.StatusFound)
	authorization, err = url.Parse(cancel.Header().Get("Location"))
	if err != nil || len(cancel.Result().Cookies()) != 1 {
		t.Fatal("start did not issue a cancellation transaction")
	}
	cancelled := request(callback+url.Values{"state": {authorization.Query().Get("state")}, "error": {"access_denied"}}.Encode(),
		"", "", cancel.Result().Cookies()[0])
	want(cancelled, http.StatusSeeOther)
	if cancelled.Header().Get("Location") != "/#/login?error=cancelled" {
		t.Fatal("valid cancellation did not retain its redirect")
	}

	abnormal := request(start, "198.51.100.3", cfg.oauthProxyToken, nil)
	want(abnormal, http.StatusFound)
	authorization, err = url.Parse(abnormal.Header().Get("Location"))
	if err != nil || len(abnormal.Result().Cookies()) != 1 {
		t.Fatal("start did not issue an OAuth transaction")
	}
	abnormalPath := callback + url.Values{
		"state": {authorization.Query().Get("state")}, "error": {"access_denied"}, "code": {"ou_admin"},
	}.Encode()
	abnormalProof := abnormal.Result().Cookies()[0]
	w = request(abnormalPath, "", "", abnormalProof)
	want(w, http.StatusSeeOther)
	if w.Header().Get("Location") != "/#/login?error=unavailable" {
		t.Fatal("conflicting callback parameters did not retain the safe redirect")
	}
	want(request(abnormalPath, "", "", abnormalProof), http.StatusBadRequest)
}
