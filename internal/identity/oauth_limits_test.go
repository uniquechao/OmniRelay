package identity

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestOAuthSourceTrustAndStartWindows(t *testing.T) {
	const token = "fixture-proxy-token"
	svc := &Service{cfg: Config{OAuthProxyToken: token}}
	r := httptest.NewRequest(http.MethodGet, "https://relay.example.test/auth/feishu/start", nil)
	r.RemoteAddr = "[::ffff:192.0.2.1]:1234"
	r.Header.Set("Forwarded", "for=192.0.2.2")
	r.Header.Set("X-Forwarded-For", "192.0.2.2")
	r.Header.Set("X-Real-IP", "192.0.2.2")
	r.Header.Set("X-Omnirelay-Client-IP", "192.0.2.2")
	peer := netip.MustParseAddr("192.0.2.1")
	for _, presented := range []string{"", "incorrect"} {
		r.Header.Set("X-Omnirelay-Proxy-Token", presented)
		if source := svc.oauthSource(r); source != peer {
			t.Fatalf("untrusted headers changed source to %v", source)
		}
	}
	r.Header.Set("X-Omnirelay-Proxy-Token", token)
	tls := r.TLS
	r.TLS = nil
	if source := svc.oauthSource(r); source != peer {
		t.Fatalf("accepted upstream proof over plaintext from %v", source)
	}
	r.TLS = tls
	r.Header.Add("X-Omnirelay-Client-IP", "192.0.2.3")
	if source := svc.oauthSource(r); source != peer {
		t.Fatalf("accepted ambiguous source %v", source)
	}
	r.Header.Set("X-Omnirelay-Client-IP", "192.0.2.2")
	if source := svc.oauthSource(r); source.String() != "192.0.2.2" {
		t.Fatalf("trusted upstream source = %v", source)
	}

	now := time.Now()
	for i := 0; i < oauthStartsPerSource; i++ {
		if !svc.oauth.allowStart(now, peer) {
			t.Fatal("source quota exhausted early")
		}
	}
	if svc.oauth.allowStart(now, peer) {
		t.Fatal("source exceeded its quota")
	}
	for i := oauthStartsPerSource; i < oauthStartsPerInstance; i++ {
		source := netip.AddrFrom4([4]byte{198, 51, 100, byte(i)})
		if !svc.oauth.allowStart(now, source) {
			t.Fatal("instance quota exhausted early")
		}
	}
	if source := svc.oauthSource(r); svc.oauth.allowStart(now, source) {
		t.Fatal("instance exceeded its quota")
	}
	if !svc.oauth.allowStart(now.Add(time.Minute), peer) {
		t.Fatal("start window did not reset")
	}
}

func TestOAuthCompletionLimitPreservesTransactionForRetry(t *testing.T) {
	pool := identityPool(t)
	svc, err := New(pool, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	svc.Register(mux)
	request := func(path, browser string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.AddCookie(&http.Cookie{Name: browserCookie, Value: browser})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	store := func(state, browser string) string {
		t.Helper()
		if err := svc.storeLogin(t.Context(), state, browser, newSecret(), "/"); err != nil {
			t.Fatal(err)
		}
		return "/auth/feishu/callback?error=access_denied&state=" + url.QueryEscape(state)
	}
	legacyBrowser := newSecret()
	legacy := store(newSecret(), legacyBrowser)
	if w := request(legacy, legacyBrowser); w.Code != http.StatusSeeOther || !strings.HasSuffix(w.Header().Get("Location"), "error=cancelled") {
		t.Fatalf("pre-upgrade transaction did not cancel normally: %d", w.Code)
	}

	browser := newSecret()
	path := store(svc.newOAuthState(time.Now(), browser), browser)
	svc.oauth.completions = oauthWindow{started: time.Now(), count: oauthCompletionsPerInstance}
	if w := request(path, browser); w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "60" ||
		len(w.Result().Cookies()) != 0 {
		t.Fatalf("expected a retryable response retaining the browser cookie: %d", w.Code)
	}
	svc.oauth.completions.started = time.Now().Add(-time.Minute)
	if w := request(path, browser); w.Code != http.StatusSeeOther || !strings.HasSuffix(w.Header().Get("Location"), "error=cancelled") {
		t.Fatalf("retry did not consume and cancel the same transaction: %d", w.Code)
	}
	if w := request(path, browser); w.Code != http.StatusBadRequest || svc.oauth.completions.count != 1 {
		t.Fatalf("replay affected valid capacity: status=%d count=%d", w.Code, svc.oauth.completions.count)
	}
}
