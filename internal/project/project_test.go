package project

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/system"
	"github.com/uniquechao/OmniRelay/migrations"
)

var testActor = identity.Principal{TenantKey: "tenant", AppID: "app", AccountID: "relay-user", OpenID: "ou_reader"}

type projectBot struct{ inGroup bool }

func (p *projectBot) Bot(context.Context) (chat.Member, error) {
	return chat.Member{OpenID: "ou_bot", Name: "Bot"}, nil
}
func (p *projectBot) BotInGroup(context.Context, string) error {
	if !p.inGroup {
		return chat.ErrAccessLost
	}
	return nil
}

type projectChat struct {
	hidden     string
	operation  string
	reconciled string
	sends      int
}

func (p *projectChat) ProjectGroup(_ context.Context, _ identity.Principal, id string) (chat.Group, error) {
	if id == p.hidden {
		return chat.Group{}, chat.ErrAccessLost
	}
	return chat.Group{ID: id, Name: id}, nil
}
func (p *projectChat) ProjectGroupForMutation(ctx context.Context, _ pgx.Tx, actor identity.Principal, id string) (chat.Group, error) {
	return p.ProjectGroup(ctx, actor, id)
}

func (p *projectChat) SendBriefing(ctx context.Context, actor identity.Principal, target, operation string, content chat.DeliveryContent,
	key chat.BusinessReceiptKey, authorize chat.BriefingAuthority) (chat.DeliveryStatus, error) {
	ok, err := authorize(ctx, actor, key, target, content)
	if err != nil {
		return chat.DeliveryStatus{}, err
	}
	if !ok {
		return chat.DeliveryStatus{}, chat.ErrAccessLost
	}
	p.operation, p.sends = operation, p.sends+1
	return chat.DeliveryStatus{OperationKey: operation, State: "unknown"}, nil
}
func (p *projectChat) BriefingDeliveryStatus(_ context.Context, _ identity.Principal, _, operation string) (chat.DeliveryStatus, error) {
	p.reconciled = operation
	return chat.DeliveryStatus{OperationKey: operation, State: "success"}, nil
}

func testProjectPool(t *testing.T, schema string) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("OMNIRELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL")
	}
	ctx := context.Background()
	control, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(control.Close)
	if _, err := control.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = control.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE") })
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestProjectHTTPReadOnlyCheckAndBriefingBoundary(t *testing.T) {
	pool := testProjectPool(t, "f1_project_contract")
	ctx := t.Context()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO accounts (id,tenant_key,app_id,open_id,active)
		VALUES ('relay-user','tenant','app','ou_reader',true),('inactive','tenant','app','ou_inactive',false),('viewer','tenant','app','ou_viewer',true)`)
	exec(`INSERT INTO role_grants (tenant_key,app_id,account_id,role,source)
		VALUES ('tenant','app','relay-user','system_admin','bootstrap')`)
	exec(`INSERT INTO feishu_bot_configs (tenant_key,app_id,credentials_generation) VALUES ('tenant','app',1)`)
	exec(`INSERT INTO feishu_chat_conversations (tenant_key,app_id,chat_id,kind)
		VALUES ('tenant','app','oc_primary','group'),('tenant','app','oc_hidden','group'),('tenant','app','oc_second','group')`)
	exec(`INSERT INTO project_members (tenant_key,app_id,project_id,account_id,member_id,role)
		VALUES ('tenant','app','allowed','relay-user','member','manager'),('tenant','app','allowed','viewer','viewer','reader')`)
	roles := access.New(pool, "tenant", "app")
	roles.ProjectAccess = access.LocalProjects{Pool: pool}
	chats := &projectChat{hidden: "oc_hidden"}
	bot := &projectBot{inGroup: true}
	active := true
	service := New(pool, roles, func(context.Context) (system.Status, error) {
		return system.Status{Config: system.Config{CredentialsConfigured: true}, Enabled: active}, nil
	}, bot, chats, true)
	mux := http.NewServeMux()
	service.Register(mux, func(_ http.ResponseWriter, _ *http.Request, policy identity.SessionPolicy) (identity.Principal, bool) {
		return testActor, true
	})
	request := func(method, path, payload string, status int) string {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1/projects"+path, strings.NewReader(payload))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: status=%d, body=%s", method, path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	const settingsJSON = `{"target_chat_id":"oc_primary","timezone":"Asia/Shanghai","local_time":"18:00","plan_enabled":false,"public_submission_count":true,"version":0}`
	directory := request("GET", "?page=2&page_size=1", "", 200)
	if !strings.Contains(directory, `"total":1`) || !strings.Contains(directory, `"items":[]`) {
		t.Fatalf("directory total after pagination: %s", directory)
	}
	request("PUT", "/allowed/feishu/settings", settingsJSON, 200)
	exec(`INSERT INTO project_chat_links VALUES ('tenant','app','allowed','oc_hidden')`)
	for _, panel := range []string{"overview", "members", "chats", "meetings", "docs", "reports", "tasks", "feishu"} {
		body := request("GET", "/allowed/"+panel, "", 200)
		if strings.Contains(body, "oc_hidden") || strings.Contains(body, "has_linked_chats") {
			t.Fatalf("source leaked in %s: %s", panel, body)
		}
		if panel == "chats" && !strings.Contains(body, `"total":1`) {
			t.Fatalf("group count: %s", body)
		}
	}
	for _, source := range []struct{ kind, panel string }{{"document", "docs"}, {"minutes", "meetings"}} {
		path := "/allowed/sources/" + source.kind + "/local-note"
		request("PUT", path, `{"title":"本地资料","body":"受限正文","reader_account_ids":["relay-user"]}`, 200)
		if body := request("GET", "/allowed/"+source.panel+"?q=本地", "", 200); !strings.Contains(body, "本地资料") || strings.Contains(body, "受限正文") {
			t.Fatal(body)
		}
		if source.kind == "document" {
			body := request("GET", "/allowed/files?category=attachments&q=本地&sort=shared_at&order=desc", "", 200)
			if !strings.Contains(body, `"id":"document:local-note"`) || strings.Contains(body, "受限正文") {
				t.Fatal(body)
			}
			if body := request("GET", "/allowed/files/documents/local-note", "", 200); !strings.Contains(body, "受限正文") {
				t.Fatal(body)
			}
		}
		request("GET", path+"/tasks", "", 200)
		request("PUT", path+"/readers", `{"reader_account_ids":[]}`, 200)
		if body := request("GET", "/allowed/"+source.panel, "", 200); strings.Contains(body, "本地资料") {
			t.Fatal(body)
		}
		if body := request("GET", "/allowed/files", "", 200); strings.Contains(body, "本地资料") {
			t.Fatal(body)
		}
		request("GET", "/allowed/files/documents/local-note", "", 404)
		request("GET", path+"/tasks", "", 404)
	}
	// The list classifies authorized documents without changing their source identity.
	weeklyPath := "/allowed/sources/document/weekly"
	request("PUT", weeklyPath, `{"title":"联调会议记录","body":"受限正文","reader_account_ids":["relay-user"]}`, 200)
	for _, list := range []string{"/allowed/meetings?q=联调", "/allowed/files?category=minutes&q=联调"} {
		body := request("GET", list, "", 200)
		if !strings.Contains(body, "联调会议记录") || strings.Contains(body, "受限正文") {
			t.Fatal(body)
		}
	}
	if body := request("GET", "/allowed/meetings", "", 200); !strings.Contains(body, `"kind":"document"`) {
		t.Fatal(body)
	}
	request("PUT", weeklyPath+"/readers", `{"reader_account_ids":[]}`, 200)
	if body := request("GET", "/allowed/meetings", "", 200); strings.Contains(body, "联调会议记录") {
		t.Fatal(body)
	}
	const checkPath = "/allowed/feishu/check"
	check := func(account, action, scope string) checkResult {
		t.Helper()
		body := request("POST", checkPath, fmt.Sprintf(`{"account_id":%q,"action":%q,"public_scope":%q}`, account, action, scope), 200)
		var result checkResult
		if err := json.Unmarshal([]byte(body), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	countWrites := func() [5]int {
		t.Helper()
		var counts [5]int
		err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM audit_events),(SELECT count(*) FROM project_briefing_runs),
			(SELECT count(*) FROM feishu_send_operations),(SELECT count(*) FROM project_relay_settings),
			(SELECT count(*) FROM project_chat_links)`).Scan(&counts[0], &counts[1], &counts[2], &counts[3], &counts[4])
		if err != nil {
			t.Fatal(err)
		}
		return counts
	}
	request("PUT", "/allowed/profile", `{"name":"可读","status":"active","expected_version":0}`, 200)
	request("PUT", "/allowed/profile", `{"name":"禁止覆盖","status":"active","expected_version":0}`, 409)
	request("PUT", "/allowed/reports/mine/2026-11-19", `{"note":"原始日报","expected_version":0}`, 200)
	request("PUT", "/allowed/reports/mine/2026-11-19", `{"note":"禁止覆盖","expected_version":0}`, 409)
	if body := request("GET", "?q=可读&status=active", "", 200); !strings.Contains(body, `"name":"可读"`) {
		t.Fatal(body)
	}
	if body := request("GET", "/allowed/reports?date_from=2026-11-19&date_to=2026-11-19", "", 200); !strings.Contains(body, "原始日报") {
		t.Fatal(body)
	}
	before := countWrites()
	if r := check("", "project.read", "submission_count"); !r.Passed {
		t.Fatalf("check: %#v", r)
	}
	if r := check("inactive", "project.read", "submission_count"); r.Account != "inactive_or_missing" || r.Passed {
		t.Fatalf("inactive: %#v", r)
	}
	if r := check("viewer", "project.assign", "submission_count"); r.Action != "denied" || r.Passed {
		t.Fatalf("action: %#v", r)
	}
	bot.inGroup = false
	if r := check("", "project.read", "submission_count"); r.Group != "bot_not_member" || r.Passed {
		t.Fatalf("group: %#v", r)
	}
	bot.inGroup, active = true, false
	if r := check("", "project.read", "submission_count"); r.Bot != "disabled" || r.Passed {
		t.Fatalf("bot: %#v", r)
	}
	active = true
	if after := countWrites(); after != before || chats.sends != 0 {
		t.Fatalf("check wrote state: %v -> %v", before, after)
	}

	var run briefing
	body := request("POST", "/allowed/reports/briefings", `{"business_date":"2026-11-19"}`, 200)
	if err := json.Unmarshal([]byte(body), &run); err != nil || run.ID == "" || run.Delivery != "not_sent" || run.VisibleCount != 1 {
		t.Fatalf("generated: %s, %v", body, err)
	}
	retry := request("POST", "/allowed/reports/briefings", `{"business_date":"2026-11-19"}`, 200)
	if !strings.Contains(retry, run.ID) {
		t.Fatalf("generation retry returned a different receipt: %s", retry)
	}
	path := "/allowed/reports/briefings/" + run.ID
	if !strings.Contains(request("GET", path, "", 200), run.ID) {
		t.Fatal("历史简报未按 ID 返回")
	}
	request("GET", "/allowed/reports/briefings/missing", "", 404)
	request("POST", path+"/publish", "", 200)
	if chats.operation != "briefing:"+run.ID {
		t.Fatalf("operation: %s", chats.operation)
	}
	exec(`INSERT INTO feishu_send_operations (tenant_key,app_id,operation_key,account_id,chat_id,generation,fingerprint,business_receipt)
		VALUES ('tenant','app',$1,'relay-user','oc_primary',1,'test','{}')`, chats.operation)
	exec(`INSERT INTO feishu_send_parts (tenant_key,app_id,operation_key,position,kind,state,attempted)
		VALUES ('tenant','app',$1,0,'text','unknown',true)`, chats.operation)
	request("PUT", "/allowed/reports/mine/2026-11-19", `{"note":"更新后的日报","expected_version":1}`, 200)
	request("POST", path+"/publish", "", 409)
	key := run.receipt("allowed")
	receipts, err := service.LookupAuthorizedReceipts(ctx, testActor, []chat.BusinessReceiptKey{key})
	if err != nil || len(receipts) != 1 || receipts[0] != key {
		t.Fatalf("receipt after report change: %#v, %v", receipts, err)
	}
	request("POST", path+"/reconcile", "", 200)
	if chats.reconciled != chats.operation || chats.sends != 1 {
		t.Fatalf("reconciliation: %s, sends=%d", chats.reconciled, chats.sends)
	}
	request("PUT", "/allowed/feishu/settings", strings.NewReplacer("oc_primary", "oc_second", `"version":0`, `"version":1`, `"public_submission_count":true`, `"public_submission_count":false`).Replace(settingsJSON), 200)
	if r := check("", "project.read", "submission_count"); r.Scope != "denied" || r.Passed {
		t.Fatalf("scope: %#v", r)
	}
	request("POST", path+"/publish", "", 409)
	if chats.sends != 1 {
		t.Fatal("changed target was sent")
	}
	exec(`UPDATE project_members SET role='revoked' WHERE project_id='allowed' AND account_id='relay-user'`)
	request("GET", "/allowed/docs", "", 404)
	request("GET", "/allowed/files", "", 404)
}
