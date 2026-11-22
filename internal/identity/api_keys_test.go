package identity

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPersonalAPIKeyLifecycle(t *testing.T) {
	pool := identityPool(t)
	cfg := testConfig()
	svc, err := New(pool, cfg)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	svc.Register(mux)
	mux.HandleFunc("POST /api/v1/example", func(w http.ResponseWriter, r *http.Request) {
		p, ok := svc.Authenticate(w, r, RefreshSessionWithCSRF)
		if ok {
			writeJSON(w, 200, p)
		}
	})
	session := func(id string) *http.Cookie {
		_, err := pool.Exec(t.Context(), `INSERT INTO accounts(id,tenant_key,app_id,open_id) VALUES ($1,$2,$3,$1)`, id, cfg.TenantKey, cfg.AppID)
		if err != nil {
			t.Fatal(err)
		}
		token := newSecret()
		p := Principal{TenantKey: cfg.TenantKey, AppID: cfg.AppID, OpenID: id, Name: id}
		if err := svc.createSession(t.Context(), &p, token, ""); err != nil {
			t.Fatal(err)
		}
		return &http.Cookie{Name: sessionCookie, Value: token}
	}
	one, two := session("one"), session("two")
	call := func(method, path string, cookie *http.Cookie, csrf bool, key string, want int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, cfg.PublicOrigin+path, strings.NewReader(`{"label":"debug"}`))
		r.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			r.AddCookie(cookie)
			if csrf {
				r.Header.Set("Origin", cfg.PublicOrigin)
				r.Header.Set("X-CSRF-Token", csrfToken(cookie.Value))
			}
		}
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s status=%d want=%d", method, path, w.Code, want)
		}
		return w.Body.Bytes()
	}
	create := func() (string, string) {
		t.Helper()
		var got struct{ ID, Key string }
		if err := json.Unmarshal(call("POST", keyPath, one, true, "", 201), &got); err != nil || got.ID == "" || !strings.HasPrefix(got.Key, keyPrefix) {
			t.Fatal("key issuance failed")
		}
		return got.ID, got.Key
	}
	call("POST", keyPath, one, false, "", 403)
	id, key := create()
	list := string(call("GET", keyPath, one, false, "", 200))
	if strings.Contains(list, key) || !strings.Contains(list, id) || strings.Contains(string(call("GET", keyPath, two, false, "", 200)), id) {
		t.Fatal("key list ownership or redaction")
	}
	call("DELETE", keyPath+"/"+id, two, true, "", 404)
	call("POST", keyPath, nil, false, key, 401)
	me := string(call("GET", "/api/v1/me", nil, false, key, 200))
	if !strings.Contains(me, `"auth_method":"api_key"`) || strings.Contains(me, "csrf_token") {
		t.Fatal("key profile")
	}
	if !strings.Contains(string(call("POST", "/api/v1/example", nil, false, key, 200)), `"account_id":"one"`) {
		t.Fatal("incorrect caller")
	}
	call("POST", "/api/v1/example", two, true, key, 401)
	call("DELETE", keyPath+"/"+id, one, true, "", 204)
	call("POST", "/api/v1/example", nil, false, key, 401)
	id, key = create()
	if _, err := pool.Exec(t.Context(), `UPDATE account_api_keys SET expires_at=now() WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	call("GET", "/api/v1/me", nil, false, key, 401)
	_, key = create()
	if _, err := pool.Exec(t.Context(), `UPDATE accounts SET active=false WHERE id='one'; UPDATE accounts SET active=true WHERE id='one'`); err != nil {
		t.Fatal(err)
	}
	call("GET", "/api/v1/me", nil, false, key, 401)
}
