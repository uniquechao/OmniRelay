package group

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/agentadmin"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

func TestAgentWorkbenchConfigurationAndSessions(t *testing.T) {
	g := newGate(t)
	g.sql("UPDATE project_members SET role='manager' WHERE account_id='alice'")
	actor := identity.Principal{TenantKey: g.Tenant, AppID: g.App, AccountID: "alice", OpenID: "login-a"}
	mux := http.NewServeMux()
	auth := func(_ http.ResponseWriter, r *http.Request, p identity.SessionPolicy) (identity.Principal, bool) {
		if r.Method != "GET" && p != identity.RefreshSessionWithCSRF {
			t.Fatal("missing mutation validation")
		}
		return actor, true
	}
	agentadmin.Service{DB: g.DB, Roles: g.Roles}.Register(mux, auth)
	g.RegisterWorkbench(mux, auth)
	call := func(method, path, body, key string, status int) string {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: got %d (%s), want %d", method, path, w.Code, w.Body.String(), status)
		}
		return w.Body.String()
	}
	const config = "/api/v1/projects/north/agents/configurations/project-pm"
	const change = `{"name":"北辰 PM","persona":"跟进任务","tools":[],"enabled":true,"reason":"项目启用","expected_version":0}`
	call("PUT", config, change, "", 200)
	call("PUT", config, change, "", 409)
	if err := pgx.BeginFunc(g.ctx, g.DB, func(tx pgx.Tx) error { return agentadmin.Applied(g.ctx, tx, g.Tenant, g.App, "north", "project-pm", 1) }); err != nil {
		t.Fatal(err)
	}
	if result := call("GET", config, "", "", 200); !strings.Contains(result, `"applied_version":1`) || !strings.Contains(result, `"tools":[]`) {
		t.Fatal(result)
	}
	const prefix = "/api/v1/projects/north/bot/"
	makeThread := func(title, key string) string {
		raw := call("POST", prefix+"conversations", `{"title":"`+title+`"}`, key, 200)
		var item conversation
		if err := json.Unmarshal([]byte(raw), &item); err != nil || item.Title != title || item.ID == "" {
			t.Fatal("create", raw, err)
		}
		return item.ID
	}
	a, b := makeThread("甲", "create-thread-a"), makeThread("乙", "create-thread-b")
	if a == b || a != makeThread("甲", "create-thread-a") {
		t.Fatal("thread identity")
	}
	call("POST", prefix+"conversations", `{"title":"丙"}`, "create-thread-a", 409)
	write := func(id, key, q string) {
		call("POST", prefix+"messages?conversation="+id, `{"text":"`+q+`","intent":"project"}`, key, 202)
	}
	write(a, "thread-a-message", "甲会话问题")
	write(b, "thread-b-message", "乙会话问题")
	call("POST", prefix+"messages", `{"text":"默认对话的存量入口"}`, "omni-cli-existing", 202)
	if result := call("GET", prefix+"messages?conversation="+a, "", "", 200); !strings.Contains(result, "甲会话问题") || strings.Contains(result, "乙会话问题") {
		t.Fatal(result)
	}
	if result := call("GET", prefix+"messages", "", "", 200); strings.Contains(result, "会话问题") || !strings.Contains(result, "默认对话的存量入口") {
		t.Fatal(result)
	}
	if result := call("GET", "/api/v1/agent-runs", "", "", 200); !strings.Contains(result, "project_chat") {
		t.Fatal(result)
	}
	var partialID string
	if err := g.DB.QueryRow(g.ctx, "SELECT id FROM agent_group_work WHERE message_id='thread-a-message'").Scan(&partialID); err != nil {
		t.Fatal(err)
	}
	g.sql(`INSERT INTO agent_project_tool_calls(work_id,slot,tool,fingerprint,receipt)
		VALUES($1,1,'task_create','sensitive-hash','{"title":"private-receipt-text"}')`, partialID)
	g.sql(`INSERT INTO agent_project_tool_failures(work_id,slot,tool,fingerprint,status,target_hint)
		VALUES($1,1,'task_create','sensitive-hash',409,'private-title'),
			  ($1,2,'task_update','sensitive-hash',403,'private-title')`, partialID)
	result := call("GET", "/api/v1/agent-runs?work_id="+partialID, "", "", 200)
	var rec struct {
		Items []struct {
			Writes   int              `json:"writes"`
			Failures []map[string]any `json:"write_failures"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(result), &rec); err != nil || len(rec.Items) != 1 || rec.Items[0].Writes != 1 || len(rec.Items[0].Failures) != 1 {
		t.Fatal("partial write counts", result, err)
	}
	f := rec.Items[0].Failures[0]
	if len(f) != 3 || f["slot"] != float64(2) || f["status"] != float64(403) || f["tool"] != "task_update" || strings.Contains(result, "sensitive-hash") || strings.Contains(result, "private-") {
		t.Fatal("unsafe or incomplete failure projection", result)
	}
	t.Run("browser reads PostgreSQL receipts", func(t *testing.T) {
		if os.Getenv("OMNIRELAY_PLAYWRIGHT_MODULE") == "" {
			t.Skip("requires configured Chromium")
		}
		mux.HandleFunc("GET /api/v1/me", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"principal":{"account_id":"alice"},"csrf_token":"fixture","access":{"navigation":{"workbench":true,"runs":true},"default_route":"/#/workbench"}}`))
		})
		mux.HandleFunc("GET /api/v1/me/followed-projects", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"items":[]}`))
		})
		mux.Handle("/", http.FileServer(http.Dir("../../webui/dist")))
		server := httptest.NewServer(mux)
		defer server.Close()
		command := exec.CommandContext(t.Context(), "node", "--test", "--test-name-pattern=真实数据库运行记录展示逐槽位失败", "tests/integration/project_workbench_browser.test.mjs")
		command.Dir = "../../.."
		command.Env = append(os.Environ(), "OMNIRELAY_RUNS_ORIGIN="+server.URL, "OMNIRELAY_RUNS_WORK="+partialID)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("browser: %v\n%s", err, output)
		}
	})
	actor.AccountID, actor.OpenID = "bob", "login-b"
	if hidden := call("GET", "/api/v1/agent-runs?work_id="+partialID, "", "", 200); strings.Contains(hidden, "task_update") {
		t.Fatal("foreign failure", hidden)
	}
	if result := call("GET", config, "", "", 200); strings.Contains(result, "项目启用") || !strings.Contains(result, `"can_edit":false`) {
		t.Fatal("private config history", result)
	}
	call("PUT", config, change, "", 403)
	call("GET", prefix+"messages?conversation="+a, "", "", 403)
	if result := call("GET", prefix+"conversations", "", "", 200); strings.Contains(result, a) {
		t.Fatal(result)
	}
	if result := call("GET", "/api/v1/agent-runs", "", "", 200); strings.Contains(result, "project_chat") {
		t.Fatal(result)
	}
	actor.AccountID, actor.OpenID = "alice", "login-a"
	g.sql("UPDATE project_members SET generation='new-generation' WHERE account_id='alice'")
	call("GET", prefix+"messages?conversation="+a, "", "", 403)
	if result := call("GET", prefix+"conversations", "", "", 200); strings.Contains(result, a) {
		t.Fatal("prior membership conversation", result)
	}
}
