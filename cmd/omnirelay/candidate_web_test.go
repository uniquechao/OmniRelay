package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uniquechao/OmniRelay/internal/identity"
)

func TestCandidateWebRun(t *testing.T) {
	dsn := os.Getenv("OMNIRELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("需要隔离数据库")
	}
	pool := backendTestPool(t, t.Context(), dsn)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	const origin = "https://candidate.example.test"
	t.Setenv("OMNIRELAY_CANDIDATE_ORIGIN", origin)
	t.Setenv("OMNIRELAY_CANDIDATE_POD", "fixture-pod")
	t.Setenv("OMNIRELAY_FEISHU_APP_ID", "cli_fixture")
	t.Setenv("OMNIRELAY_FEISHU_TENANT_KEY", "tenant_fixture")
	t.Setenv("OMNIRELAY_BOOTSTRAP_OPEN_ID", "ou_dedicated")
	// Unix 套接字路径受系统长度限制。
	bridge, err := os.MkdirTemp(".", "bridge-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(bridge)
	server, err := net.Listen("unix", filepath.Join(bridge, "service.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	run := webRun{Pod: "fixture-pod", Run: strings.Repeat("a", 32), Credential: strings.Repeat("z", 48), Expires: time.Now().Unix() + 40}
	go func() {
		for {
			conn, err := server.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				var req struct {
					Run, Credential, Operation string
					Payload                    map[string]string
				}
				if json.NewDecoder(conn).Decode(&req) != nil || req.Run != run.Run || req.Credential != run.Credential {
					return
				}
				reply := candidateReply{Finished: true, Component: "fixture", Error: "denied"}
				if req.Operation == exchangeOperation && req.Payload["code"] == "fixture" && req.Payload["verifier"] != "" {
					reply.Error = ""
					reply.Identity = &identity.Principal{TenantKey: "tenant_fixture", AppID: "cli_fixture", OpenID: "ou_dedicated", Name: "验收成员"}
				}
				json.NewEncoder(conn).Encode(reply)
			}()
		}
	}()
	app := candidateWeb(ctx, pool, http.NotFoundHandler(), bridge)
	request := func(path string, cookie *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", origin+path, nil)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		app.ServeHTTP(w, req)
		return w
	}
	if request("/", nil).Code != 503 {
		t.Fatal("无运行时仍可访问")
	}
	data, _ := json.Marshal(run)
	file := filepath.Join(bridge, "web.json")
	if err = os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	if w := request("/", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `id="root"`) {
		t.Fatal("候选 React 入口不可用")
	}
	begin := request("/auth/feishu/start", nil)
	u, err := url.Parse(begin.Header().Get("Location"))
	if err != nil || begin.Code != 302 || u.Query().Get("redirect_uri") != origin+"/auth/feishu/callback" {
		t.Fatal("回调绑定错误")
	}
	done := request("/auth/feishu/callback?code=fixture&state="+url.QueryEscape(u.Query().Get("state")), begin.Result().Cookies()[0])
	if done.Code != 303 || done.Header().Get("Location") != "/#/workbench" {
		t.Fatal("候选未进入授权落点")
	}
	var session *http.Cookie
	for _, c := range done.Result().Cookies() {
		if c.Value != "" {
			session = c
		}
	}
	me := request("/api/v1/me", session)
	var profile struct {
		Principal identity.Principal
		Access    identity.WorkbenchAccess
	}
	if me.Code != 200 || json.Unmarshal(me.Body.Bytes(), &profile) != nil || profile.Principal.OpenID != "ou_dedicated" || profile.Access.DefaultRoute != done.Header().Get("Location") {
		t.Fatal("候选会话身份或落点不一致")
	}
	if err = os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if request("/api/v1/me", session).Code != 503 {
		t.Fatal("撤销运行后仍可访问")
	}
}
