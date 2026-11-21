package identity

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestPreviewFeishuAuthorization(t *testing.T) {
	pool := identityPool(t)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts(id,tenant_key,app_id,open_id) VALUES ('reader','tenant','app','ou_reader')`); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig()
	var exchangeCount int
	upstreamUser := "ou_reader"
	cfg.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case tokenEndpoint:
			exchangeCount++
			var form map[string]string
			if err := json.NewDecoder(r.Body).Decode(&form); err != nil || form["code_verifier"] == "" || form["redirect_uri"] != cfg.PublicOrigin+callbackPath {
				t.Fatal("授权换码缺少 PKCE 或现有回调", err)
			}
			return providerResponse(200, `{"code":0,"access_token":"private-credential","token_type":"Bearer","expires_in":3600}`), nil
		case userInfoEndpoint:
			if r.Header.Get("Authorization") != "Bearer private-credential" {
				t.Fatal("授权缺少用户身份校验")
			}
			return providerResponse(200, `{"code":0,"data":{"tenant_key":"tenant","open_id":"`+upstreamUser+`"}}`), nil
		default:
			t.Fatal("非预期飞书请求", r.URL.Path)
			return nil, nil
		}
	})
	s, err := New(pool, cfg)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.Register(mux)
	p := Principal{TenantKey: "tenant", AppID: "app", OpenID: "ou_reader"}
	session, otherSession := newSecret(), newSecret()
	for _, key := range []string{session, otherSession} {
		if err := s.createSession(ctx, &p, key, ""); err != nil {
			t.Fatal(err)
		}
	}
	get := func(path, session string, extra ...*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", cfg.PublicOrigin+path, nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
		for _, cookie := range extra {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	const target = "/#/projects/p1/chats/oc_group/om_doc"
	start := func(kind string) (string, *http.Cookie) {
		t.Helper()
		w := get("/auth/feishu/preview?kind="+kind+"&return_to="+url.QueryEscape(target), session)
		redirect, err := url.Parse(w.Header().Get("Location"))
		if err != nil || w.Code != 302 || redirect.Query().Get("scope") != previewScopes[kind] || redirect.Query().Get("code_challenge_method") != "S256" {
			t.Fatalf("只读授权发起失败 %d %s", w.Code, w.Body)
		}
		return callbackPath + "?state=" + url.QueryEscape(redirect.Query().Get("state")), w.Result().Cookies()[0]
	}
	lookup := func(key, kind string) string {
		r := httptest.NewRequest("GET", cfg.PublicOrigin, nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: key})
		return s.DocumentPreviewToken(r, p, kind)
	}
	path, browser := start("wiki")
	if w := get(path+"&code=ok", otherSession, browser); w.Code != 403 || exchangeCount != 0 {
		t.Fatal("其他会话完成了授权", w.Code)
	}
	path, browser = start("docx")
	upstreamUser = "ou_other"
	if w := get(path+"&code=ok", session, browser); w.Code != 303 || !strings.Contains(w.Header().Get("Location"), "document_preview=unavailable") || lookup(session, "docx") != "" {
		t.Fatal("授权账号与工作台账号不一致时应拒绝", w.Code, w.Header())
	}
	path, browser = start("docx")
	upstreamUser = "ou_reader"
	w := get(path+"&code=ok", session, browser)
	if w.Code != 303 || w.Header().Get("Location") != "/?document_preview=authorized"+target[1:] || strings.Contains(w.Body.String(), "private-credential") {
		t.Fatal("未安全回跳到原消息", w.Code, w.Header())
	}
	if lookup(session, "docx") != "private-credential" || lookup(otherSession, "docx") != "" || lookup(session, "wiki") != "" {
		t.Fatal("凭据未按会话及授权类型隔离")
	}
	if replay := get(path+"&code=ok", session, browser); replay.Code != 400 {
		t.Fatal("授权回调可重放", replay.Code)
	}
	path, browser = start("minutes")
	if w := get(path+"&error=access_denied", session, browser); !strings.Contains(w.Header().Get("Location"), "document_preview=cancelled") || lookup(session, "minutes") != "" {
		t.Fatal("取消授权后应回到原消息", w.Code, w.Header())
	}
	s.clearPreviewGrants(session)
	if lookup(session, "docx") != "" {
		t.Fatal("会话退出后仍保留预览凭据")
	}
}
