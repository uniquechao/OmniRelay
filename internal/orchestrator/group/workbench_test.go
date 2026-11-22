package group

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/uniquechao/OmniRelay/internal/identity"
)

func TestWorkbenchChatGate(t *testing.T) {
	g, _, _, _ := openImageGate(t)
	mux := http.NewServeMux()
	actor := identity.Principal{TenantKey: g.Tenant, AppID: g.App, AccountID: "alice", OpenID: "login-a"}
	g.RegisterWorkbench(mux, func(_ http.ResponseWriter, r *http.Request, p identity.SessionPolicy) (identity.Principal, bool) {
		if r.Method == "POST" && p != identity.RefreshSessionWithCSRF {
			t.Fatal("missing session CSRF")
		}
		return actor, true
	})
	call := func(method, project, key, body string, want int) string {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1/projects/"+project+"/bot/messages", strings.NewReader(body))
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d want %d", method, project, w.Code, want)
		}
		return w.Body.String()
	}
	const question = `{"text":"请帮我梳理一下今天的工作"}`
	call("POST", "north", "bot-key-1", `{"text":"你好","account":"bob"}`, 400)
	call("POST", "other", "bot-key-1", question, 403)
	first := call("POST", "north", "bot-key-1", question, 202)
	if call("POST", "north", "bot-key-1", question, 202) != first {
		t.Fatal("replayed request created work")
	}
	call("POST", "north", "bot-key-2", question, 409)
	g.step()
	history := call("GET", "north", "", "", 200)
	var result struct{ Items []chatMessage }
	if err := json.Unmarshal([]byte(history), &result); err != nil || len(result.Items) != 1 || result.Items[0].State != "sent" || result.Items[0].Answer != "项目 PM · 北辰项目\n已核对" {
		t.Fatal("missing signed model answer", err)
	}
	call("POST", "north", "bot-key-2", `{"text":"继续"}`, 202)
	g.step()
	var sessions int
	if err := g.DB.QueryRow(g.ctx, "SELECT count(DISTINCT session_id) FROM agent_group_work WHERE mode='project_chat' AND state='sent'").Scan(&sessions); err != nil || sessions != 1 {
		t.Fatal("follow-up session", err)
	}
	actor.AccountID, actor.OpenID = "bob", "login-b"
	if strings.Contains(call("GET", "north", "", "", 200), "今天的工作") {
		t.Fatal("another member read a private conversation")
	}
	actor.AccountID, actor.OpenID = "alice", "login-a"
	g.sql("UPDATE project_members SET role='revoked' WHERE account_id='alice'")
	call("GET", "north", "", "", 403)
}
