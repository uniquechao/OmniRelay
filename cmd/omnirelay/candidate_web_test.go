package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
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
	run := webRun{Pod: "fixture-pod", Run: strings.Repeat("a", 32), Credential: strings.Repeat("z", 48),
		Expires: time.Now().Unix() + 40, MessageProject: "fixture-project"}
	var ready, acknowledged atomic.Bool
	at := time.Now().UTC()
	item := &orchestrator.CandidateMessage{Run: run.Run, Project: run.MessageProject, Source: "om_signup", Lease: "test-lease",
		Revision: 1, Expires: time.Unix(run.Expires, 0), LeaseExpires: time.Unix(run.Expires, 0), Event: orchestrator.InboundEvent{
			AppID: "cli_fixture", TenantID: "tenant_fixture", MessageID: "om_signup", EventID: "event_signup", ChatID: "oc_private",
			ChatKind: orchestrator.ChatPrivate, SenderOpenID: "ou_dedicated", ReceivedAt: at, ProviderAt: at, Text: "候选私聊",
			Connection: orchestrator.ConnectionFence{ConfigVersion: 1, ConnectionGeneration: 1},
			Archive: &orchestrator.ArchiveEvent{ChatID: "oc_private", PeerOpenID: "ou_dedicated", Kind: orchestrator.ChatPrivate,
				Message: &orchestrator.ChatMessage{ID: "om_signup", ChatID: "oc_private", SentAt: at, Type: "text", Body: `{"text":"候选私聊"}`}},
		}}
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
				switch req.Operation {
				case orchestrator.CandidateClaim:
					reply.Error = ""
					if ready.Load() && !acknowledged.Load() {
						reply.Message = item
					}
				case orchestrator.CandidateAck:
					if req.Payload["source"] == item.Source && req.Payload["lease"] == item.Lease && req.Payload["revision"] == "1" {
						reply.Error = ""
						acknowledged.Store(true)
					}
				case orchestrator.CandidateReply:
					if acknowledged.Load() && req.Payload["source"] == item.Source && req.Payload["text"] == "已收到" {
						reply.Error = ""
						reply.Delivery = &chat.DeliveryStatus{State: "success"}
					}
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
		CSRFToken string `json:"csrf_token"`
	}
	if me.Code != 200 || json.Unmarshal(me.Body.Bytes(), &profile) != nil || profile.Principal.OpenID != "ou_dedicated" || profile.Access.DefaultRoute != done.Header().Get("Location") {
		t.Fatal("候选会话身份或落点不一致")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO project_members(tenant_key,app_id,project_id,account_id,member_id,role)
		VALUES('tenant_fixture','cli_fixture',$1,$2,'fixture-member','editor')`, run.MessageProject, profile.Principal.AccountID); err != nil {
		t.Fatal(err)
	}
	ready.Store(true)
	deadline := time.Now().Add(6 * time.Second)
	for !acknowledged.Load() && time.Now().Before(deadline) {
		time.Sleep(30 * time.Millisecond)
	}
	if !acknowledged.Load() {
		t.Fatal("候选轮询未在本地提交后确认")
	}
	list := request("/api/v1/candidate/messages", session)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), fmt.Sprintf(`"account_id":%q`, profile.Principal.AccountID)) ||
		strings.Count(list.Body.String(), item.Source) != 1 {
		t.Fatalf("候选工作账本与 Web 账号不一致: %d %s", list.Code, list.Body)
	}
	post := func(raw string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, origin+"/api/v1/candidate/messages/"+item.Source+"/reply", strings.NewReader(raw))
		req.AddCookie(session)
		req.Header.Set("Origin", origin)
		req.Header.Set("X-CSRF-Token", profile.CSRFToken)
		w := httptest.NewRecorder()
		app.ServeHTTP(w, req)
		return w
	}
	if got := post(`{"text":"已收到","target":"oc_other"}`); got.Code != http.StatusBadRequest {
		t.Fatal("工作台回复接受了来源之外的目标")
	}
	if got := post(`{"text":"已收到"}`); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"state":"success"`) {
		t.Fatalf("候选工作台受限回执失败: %d %s", got.Code, got.Body)
	}
	if err = os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if request("/api/v1/me", session).Code != 503 {
		t.Fatal("撤销运行后仍可访问")
	}
}
