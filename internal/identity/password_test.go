package identity

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPasswordSessionLifecycle(t *testing.T) {
	pool := identityPool(t)
	cfg := testConfig()
	svc, err := New(pool, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO accounts (id,tenant_key,app_id,open_id,name) VALUES
		('password-user',$1,$2,'ou_password','Local login')`, cfg.TenantKey, cfg.AppID); err != nil {
		t.Fatal(err)
	}
	const original = "original-test-password!"
	const replacement = "replacement-test-password!"
	if err := ProvisionPassword(t.Context(), pool, cfg.TenantKey, cfg.AppID, "password-user", "Password.User", original); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	svc.Register(mux)
	svc.RegisterPasswords(mux)
	request := func(method, path, body, origin, csrf string, cookie *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, cfg.PublicOrigin+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		out := httptest.NewRecorder()
		mux.ServeHTTP(out, req)
		return out
	}
	check := func(out *httptest.ResponseRecorder, want int) {
		t.Helper()
		if out.Code != want {
			t.Fatalf("status=%d want %d: %s", out.Code, want, out.Body.String())
		}
	}
	loginJSON := func(username, password string) string {
		data, _ := json.Marshal(map[string]string{"username": username, "password": password})
		return string(data)
	}
	check(request("POST", "/auth/login", loginJSON("password.user", original), "https://untrusted.example", "", nil), 403)
	unknown := request("POST", "/auth/login", loginJSON("missing.user", original), cfg.PublicOrigin, "", nil)
	wrong := request("POST", "/auth/login", loginJSON("password.user", "wrong"), cfg.PublicOrigin, "", nil)
	check(unknown, 401)
	check(wrong, 401)
	if unknown.Body.String() != wrong.Body.String() {
		t.Fatal("login failures revealed account validity")
	}
	first := request("POST", "/auth/login", loginJSON("Password.User", original), cfg.PublicOrigin, "", nil)
	check(first, 204)
	cookie := first.Result().Cookies()[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.Domain != "" {
		t.Fatal("session cookie missing security attributes")
	}
	me := request("GET", "/api/v1/me", "", "", "", cookie)
	check(me, 200)
	var profile struct {
		Token     string    `json:"csrf_token"`
		Principal Principal `json:"principal"`
	}
	if err := json.Unmarshal(me.Body.Bytes(), &profile); err != nil || profile.Principal.AuthMethod != "password" ||
		!profile.Principal.CanChangePassword || profile.Token == "" {
		t.Fatalf("unexpected password profile: %v", err)
	}
	data, _ := json.Marshal(map[string]string{"currentPassword": original, "newPassword": replacement})
	check(request("POST", "/auth/password", string(data), cfg.PublicOrigin, "", cookie), 403)
	changed := request("POST", "/auth/password", string(data), cfg.PublicOrigin, profile.Token, cookie)
	check(changed, 204)
	check(request("GET", "/api/v1/me", "", "", "", cookie), 401)
	check(request("POST", "/auth/login", loginJSON("password.user", original), cfg.PublicOrigin, "", nil), 401)
	check(request("POST", "/auth/login", loginJSON("password.user", replacement), cfg.PublicOrigin, "", nil), 204)
	check(request("POST", "/auth/logout", "", cfg.PublicOrigin, csrfToken(changed.Result().Cookies()[0].Value), changed.Result().Cookies()[0]), 204)
	check(request("GET", "/api/v1/me", "", "", "", changed.Result().Cookies()[0]), 401)
}

func TestPasswordRateLimit(t *testing.T) {
	pool := identityPool(t)
	svc, err := New(pool, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := svc.passwordLimit(t.Context(), "test", 2); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.passwordLimit(t.Context(), "test", 2); err != errPasswordLimited {
		t.Fatalf("want rate limit, got %v", err)
	}
}

func TestPasswordRoutesRequireTLS(t *testing.T) {
	svc := &Service{cfg: testConfig()}
	mux := http.NewServeMux()
	svc.RegisterPasswords(mux)
	for _, path := range []string{"/auth/login", "/auth/password"} {
		req := httptest.NewRequest(http.MethodPost, "http://relay.example.test"+path, nil)
		req.Header.Set("Origin", svc.cfg.PublicOrigin)
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("Forwarded", "proto=https")
		out := httptest.NewRecorder()
		mux.ServeHTTP(out, req)
		if out.Code != http.StatusForbidden || !strings.Contains(out.Body.String(), "https_required") {
			t.Errorf("%s accepted plain HTTP transport: status %d, body %s", path, out.Code, out.Body.String())
		}
		if len(out.Result().Cookies()) != 0 {
			t.Errorf("%s set cookies on a plain HTTP request", path)
		}
	}
}
