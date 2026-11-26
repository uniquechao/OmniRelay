package group

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/uniquechao/OmniRelay/internal/identity"
)

func TestAgentRolesImageGate(t *testing.T) {
	g, _, _, _ := openImageGate(t)
	mux := http.NewServeMux()
	actor := identity.Principal{TenantKey: g.Tenant, AppID: g.App, AccountID: "alice", OpenID: "login-a"}
	g.RegisterWorkbench(mux, func(http.ResponseWriter, *http.Request, identity.SessionPolicy) (identity.Principal, bool) {
		return actor, true
	})
	g.sql(`INSERT INTO role_grants(tenant_key,app_id,account_id,role,source) VALUES('tenant','app','alice','system_admin','bootstrap');
	INSERT INTO project_profiles(tenant_key,app_id,project_id,name,status,version) VALUES
	('tenant','app','south','南星项目','active',1),('tenant','app','locked','HIDDEN_PROJECT_87','active',1);
	INSERT INTO project_members(tenant_key,app_id,project_id,account_id,member_id,role)
	VALUES('tenant','app','south','alice','sa','member');
	INSERT INTO project_sources(tenant_key,app_id,project_id,kind,id,title,body)
	VALUES('tenant','app','north','document','meeting-gate','项目纪要','ROLE_PROOF_721');
	INSERT INTO project_source_readers VALUES('tenant','app','north','document','meeting-gate','alice')`)

	const endpoint = "/api/v1/projects/north/bot/messages"
	submit := func(intent, key, text string) string {
		t.Helper()
		payload, err := json.Marshal(map[string]string{"text": text, "intent": intent})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", endpoint, strings.NewReader(string(payload)))
		req.Header.Set("Idempotency-Key", key)
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, req)
		var body struct{ ID string }
		if res.Code != http.StatusAccepted || json.Unmarshal(res.Body.Bytes(), &body) != nil || body.ID == "" {
			t.Fatalf("submit %s: %d %s", intent, res.Code, res.Body)
		}
		return body.ID
	}
	history := func(id string) chatMessage {
		t.Helper()
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, httptest.NewRequest("GET", endpoint, nil))
		var body struct{ Items []chatMessage }
		if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &body) != nil {
			t.Fatalf("history: %d %s", res.Code, res.Body)
		}
		for _, m := range body.Items {
			if m.ID == id {
				return m
			}
		}
		t.Fatalf("work missing from its initiator's history: %s", id)
		return chatMessage{}
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := g.DB.QueryRow(g.ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	qa := submit("knowledge", "role-qa-gate", "为什么延期，请查纪要 ROLE_QA_GATE")
	g.step()
	revision := fmt.Sprintf("%x", sha256.Sum256([]byte("ROLE_PROOF_721")))
	if m := history(qa); m.State != "sent" || !strings.Contains(m.Answer, "ROLE_PROOF_721") || !strings.Contains(m.Answer, "document:meeting-gate") || !strings.Contains(m.Answer, revision) {
		t.Fatalf("missing source-backed Q&A answer: %+v", m)
	}
	if count("SELECT count(*) FROM agent_project_tool_reads WHERE work_id=$1 AND tool='file_get'", qa) != 1 ||
		count("SELECT count(*) FROM agent_project_tool_calls WHERE work_id=$1", qa) != 0 {
		t.Fatal("Q&A requires a verified source read and zero writes")
	}
	thin := submit("knowledge", "role-title-gate", "为什么延期？只查同名文件 ROLE_TITLE_GATE")
	g.step()
	if m := history(thin); m.State != "sent" || strings.Contains(m.Answer, "FAKE_SOURCE_CONCLUSION") || !strings.Contains(m.Answer, "资料") {
		t.Fatalf("metadata alone became evidence: %+v", m)
	}
	g.sql("DELETE FROM project_source_readers WHERE account_id='alice'")
	if m := history(qa); m.Answer != "" || m.State != "suppressed" {
		t.Fatalf("source visible after revocation: %+v", m)
	}

	summary := submit("admin", "role-list-gate", "列出跨项目的阻塞 ROLE_LIST_GATE")
	g.step()
	if m := history(summary); m.State != "sent" || !strings.Contains(m.Answer, "南星项目") ||
		!strings.Contains(m.Answer, "北辰项目") || strings.Contains(m.Answer, "HIDDEN_PROJECT_87") {
		t.Fatalf("unexpected project scope: %+v", m)
	}

	denied := submit("admin", "role-denied-gate", "委派给项目 locked，创建任务 ROLE_DENIED_GATE")
	g.step()
	if m := history(denied); m.State != "sent" || strings.Contains(m.Answer, "FAKE_MODEL_RECEIPT") ||
		count("SELECT count(*) FROM agent_group_work WHERE parent_work=$1", denied) != 0 {
		t.Fatalf("forbidden project received a delegation: %+v", m)
	}
	pretend := submit("project", "role-receipt-gate", "创建任务 ROLE_RECEIPT_GATE")
	g.step()
	if m := history(pretend); m.State != "sent" || strings.Contains(m.Answer, "FAKE_MODEL_RECEIPT") ||
		!strings.Contains(m.Answer, "没有已提交") || count("SELECT count(*) FROM project_tasks") != 0 {
		t.Fatalf("unreceipted model claim reached the user: %+v", m)
	}

	const request = "委派给北辰项目，创建任务 集成角色任务 ROLE_DELEGATE_GATE"
	parent := submit("admin", "role-delegate-gate", request)
	g.step()
	if id := submit("admin", "role-delegate-gate", request); id != parent {
		t.Fatal("retry changed parent work")
	}
	var child string
	if err := g.DB.QueryRow(g.ctx, `SELECT id FROM agent_group_work WHERE parent_work=$1 AND project_id='north' AND account_id='alice' AND intent='project'`, parent).Scan(&child); err != nil {
		t.Fatal(err)
	}
	if m := history(parent); !strings.Contains(m.Answer, child) || strings.Contains(m.Answer, "FAKE_MODEL_RECEIPT") {
		t.Fatalf("wrong parent receipt: %+v", m)
	}
	g.step()
	var task string
	if err := g.DB.QueryRow(g.ctx, "SELECT id FROM project_tasks WHERE project_id='north' AND title='集成角色任务'").Scan(&task); err != nil {
		t.Fatal(err)
	}
	if m := history(child); m.State != "sent" || !strings.Contains(m.Answer, task) || strings.Contains(m.Answer, "FAKE_MODEL_RECEIPT") {
		t.Fatalf("wrong project PM receipt: %+v", m)
	}
	if count("SELECT count(*) FROM agent_group_work WHERE parent_work=$1", parent) != 1 || count("SELECT count(*) FROM project_tasks") != 1 {
		t.Fatal("delegation or task duplicated")
	}
	progress := submit("admin", "role-progress-gate", "查询跨项目委派进度 ROLE_PROGRESS_GATE")
	g.step()
	if m := history(progress); m.State != "sent" || !strings.Contains(m.Answer, child) || !strings.Contains(m.Answer, "sent") {
		t.Fatalf("missing child status: %+v", m)
	}
	const batch = "创建任务 已办事项，并创建任务 待澄清事项，负责人 ROLE_MISSING_MEMBER ROLE_PARTIAL_GATE"
	partial := submit("project", "role-partial-gate", batch)
	g.step()
	var committed string
	if err := g.DB.QueryRow(g.ctx, "SELECT id FROM project_tasks WHERE project_id='north' AND title='已办事项'").Scan(&committed); err != nil {
		t.Fatal(err)
	}
	if m := history(partial); m.State != "sent" || !strings.Contains(m.Answer, committed) || !strings.Contains(m.Answer, "未提交") || strings.Contains(m.Answer, "FAKE_MODEL_RECEIPT") {
		t.Fatalf("partial result lost an item or trusted a model assertion: %+v", m)
	}
	if count("SELECT count(*) FROM agent_project_tool_calls WHERE work_id=$1 AND slot=1 AND tool='task_create'", partial) != 1 ||
		count("SELECT count(*) FROM agent_project_tool_failures WHERE work_id=$1 AND slot=2 AND tool='task_create' AND status IN (400,403,409)", partial) != 1 ||
		count("SELECT count(*) FROM project_tasks WHERE title='待澄清事项'") != 0 {
		t.Fatal("partial tool/transaction records do not match the user's answer")
	}
}
