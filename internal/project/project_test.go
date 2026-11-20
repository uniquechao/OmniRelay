package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

type gridFixture struct {
	identityLost     atomic.Bool
	reportChanged    atomic.Bool
	projectLost      atomic.Bool
	secondDenied     atomic.Bool
	readOnlyProject  atomic.Pointer[string]
	secondRenamed    atomic.Bool
	sourceLost       atomic.Bool
	changeDuringRead atomic.Bool
	changed          atomic.Bool
}

func (f *gridFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer ogt_test_reader" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var body string
	switch r.URL.Path {
	case "/api/v1/auth/me":
		body = `{"id":"grid-user","role":"member"}`
		if f.identityLost.Load() {
			body = `{"id":"different-user","role":"member"}`
		}
	case "/api/v1/projects":
		body = `{"items":[{"id":"allowed","name":"可读"},{"id":"organization","name":"不可读"}],"total":2}`
		if f.secondRenamed.Load() {
			body = `{"items":[{"id":"second","name":"已更名"}],"total":1}`
		}
	case "/api/v1/projects/second/state":
		if f.secondDenied.Load() {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		body = `{"project_id":"second","current_member_id":"member2","actions":["read","manage_access"],"versions":{"access":"v1"}}`
	case "/api/v1/projects/second/members":
		body = `{"project_id":"second","members":[{"id":"member2","project_id":"second","active":true,"roster_status":"formal"}]}`
	case "/api/v1/projects/organization/state":
		body = `{"project_id":"organization","actions":["read"],"versions":{"access":"v1"}}`
	case "/api/v1/projects/allowed/state":
		if f.projectLost.Load() {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		version := "v1"
		if f.changed.Load() {
			version = "v2"
		}
		body = fmt.Sprintf(`{"project_id":"allowed","current_member_id":"member","business_date":"2026-11-19","actions":["read","manage_access"],"versions":{"access":"v1","docs":%q,"reports":"r1","members":"m1","minutes":"mi1","tasks":"t1","header":"h1","progress":"p1"}}`, version)
	case "/api/v1/projects/allowed/members":
		body = `{"project_id":"allowed","members":[{"id":"member","project_id":"allowed","active":true,"roster_status":"formal"}]}`
	case "/api/v1/projects/allowed/workspace":
		body = `{"project":{"id":"allowed","name":"可读"},"has_linked_chats":true}`
	case "/api/v1/projects/allowed/milestones":
		body = `{"items":[],"progress":{"state":"missing"}}`
	case "/api/v1/projects/allowed/minutes":
		body = `{"items":[],"total":0}`
	case "/api/v1/projects/allowed/documents":
		body = `{"items":[{"id":"visible-doc"}],"total":1}`
		if f.sourceLost.Load() {
			body = `{"items":[],"total":0}`
		}
		if f.changeDuringRead.Load() {
			f.changed.Store(true)
		}
	case "/api/v1/projects/allowed/daily-reports/unified":
		if r.URL.Query().Get("source") != "member" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		body = `{"items":[{"record_id":"submitted-1","submission_state":"submitted"}],"total":1,"revision":"report-rev","coverage":{"state":"complete"}}`
		if f.reportChanged.Load() {
			body = strings.Replace(body, "report-rev", "report-new-rev", 1)
		}
	case "/api/v1/projects/allowed/tasks/task_late":
		body = `{"id":"task_late","project_id":"allowed","title":"联调任务"}`
	case "/api/v1/projects/allowed/source-content":
		body = `{"items":[{"ref":{"kind":"document","source_id":"minute1"},"status":"succeeded"}]}`
	case "/api/v1/projects/allowed/task-candidates":
		if r.URL.Query().Get("status") == "confirmed" && r.URL.Query().Get("cursor") == "" {
			body = `{"items":[],"next_cursor":"next"}`
		} else {
			body = `{"items":[{"project_id":"allowed","task_id":"task_late","evidence":[{"resource_type":"minutes","resource_token":"minute1"}]}]}`
		}
	case "/api/v1/projects/allowed/board":
		body = `{"project_id":"allowed","columns":[],"total":0}`
	default:
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if project := f.readOnlyProject.Load(); project != nil && r.URL.Path == "/api/v1/projects/"+*project+"/state" {
		body = strings.Replace(body, `"read","manage_access"`, `"read"`, 1)
		body = strings.Replace(body, `"access":"v1"`, `"access":"v2"`, 1)
	}
	_, _ = w.Write([]byte(body))
}

func testGrid(t *testing.T) (*Grid, *gridFixture) {
	t.Helper()
	fixture := &gridFixture{}
	upstream := httptest.NewTLSServer(fixture)
	t.Cleanup(upstream.Close)
	file := filepath.Join(t.TempDir(), "identities.json")
	if err := os.WriteFile(file, []byte(`[{"account_id":"relay-user","open_id":"ou_reader","user_id":"grid-user","token":"ogt_test_reader"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	grid, err := NewGrid(upstream.URL, file, "tenant", "app", *upstream.Client())
	if err != nil {
		t.Fatal(err)
	}
	return grid, fixture
}

func TestProjectRecordLinks(t *testing.T) {
	grid, f := testGrid(t)
	ctx := t.Context()
	lookup := func(id string) (storedTask, error) { return storedTask{ID: id, Project: "allowed"}, nil }
	v, err := grid.record(ctx, testActor, "allowed", "task", "task_late", "", lookup)
	if err != nil || v.(storedTask).ID != "task_late" {
		t.Fatalf("任务定位失败 %v %v", v, err)
	}
	for _, tc := range []struct {
		kind string
		want int
	}{{"minutes", 1}, {"document", 0}} {
		v, err := grid.record(ctx, testActor, "allowed", tc.kind, "minute1", "", lookup)
		if err != nil || v.(map[string]any)["total"] != tc.want {
			t.Fatalf("关联任务不符 %v %v", v, err)
		}
	}
	if _, err := grid.record(ctx, testActor, "allowed", "document", "revoked", "", lookup); !errors.Is(err, errDenied) {
		t.Fatalf("失权来源未识别 %v", err)
	}
	f.projectLost.Store(true)
	if _, err := grid.record(ctx, testActor, "allowed", "task", "task_late", "", lookup); !errors.Is(err, errDenied) {
		t.Fatalf("失权后仍可读取 %v", err)
	}
}

func TestProjectGridFiltersProjectsAndRechecksSources(t *testing.T) {
	grid, fixture := testGrid(t)
	ctx := context.Background()
	page, err := grid.directory(ctx, testActor, nil)
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != "allowed" {
		t.Fatalf("directory: %#v, %v", page, err)
	}
	grant, err := grid.ResolveProjectAccess(ctx, testActor, "allowed")
	if err != nil || !grant.Member || !grant.Manager || grant.CanAssign || grant.MembershipGeneration == "" {
		t.Fatalf("grant: %#v, %v", grant, err)
	}
	docs, err := grid.view(ctx, testActor, "allowed", "docs", nil)
	if err != nil || docs.Total == nil || *docs.Total != 1 {
		t.Fatalf("documents: %#v, %v", docs, err)
	}
	fixture.sourceLost.Store(true)
	docs, err = grid.view(ctx, testActor, "allowed", "docs", nil)
	if err != nil || docs.Total == nil || *docs.Total != 0 || strings.Contains(string(docs.Data), "visible-doc") {
		t.Fatalf("revoked source: %#v, %v", docs, err)
	}
	fixture.changeDuringRead.Store(true)
	if _, err := grid.view(ctx, testActor, "allowed", "docs", nil); err != errConflict {
		t.Fatalf("concurrent revocation: %v", err)
	}
	fixture.projectLost.Store(true)
	page, err = grid.directory(ctx, testActor, nil)
	if err != nil || page.Total != 0 {
		t.Fatalf("revoked project: %#v, %v", page, err)
	}
	fixture.identityLost.Store(true)
	if _, err := grid.directory(ctx, testActor, nil); err != errUnmapped {
		t.Fatalf("identity mismatch: %v", err)
	}
}

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
		VALUES ('relay-user','tenant','app','ou_reader',true),('inactive','tenant','app','ou_inactive',false)`)
	exec(`INSERT INTO role_grants (tenant_key,app_id,account_id,role,source)
		VALUES ('tenant','app','relay-user','system_admin','bootstrap')`)
	exec(`INSERT INTO feishu_bot_configs (tenant_key,app_id,credentials_generation) VALUES ('tenant','app',1)`)
	exec(`INSERT INTO feishu_chat_conversations (tenant_key,app_id,chat_id,kind)
		VALUES ('tenant','app','oc_primary','group'),('tenant','app','oc_hidden','group'),('tenant','app','oc_second','group')`)
	grid, fixture := testGrid(t)
	roles := access.New(pool, "tenant", "app")
	roles.ProjectAccess = grid
	chats := &projectChat{hidden: "oc_hidden"}
	bot := &projectBot{inGroup: true}
	active := true
	service := New(grid, pool, roles, func(context.Context) (system.Status, error) {
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
	before := countWrites()
	if r := check("", "project.read", "submission_count"); !r.Passed {
		t.Fatalf("check: %#v", r)
	}
	if r := check("inactive", "project.read", "submission_count"); r.Account != "inactive_or_missing" || r.Passed {
		t.Fatalf("inactive: %#v", r)
	}
	if r := check("", "project.assign", "submission_count"); r.Action != "denied" || r.Passed {
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
	fixture.reportChanged.Store(true)
	key := run.receipt("allowed")
	receipts, err := service.LookupAuthorizedReceipts(ctx, testActor, []chat.BusinessReceiptKey{key})
	if err != nil || len(receipts) != 1 || receipts[0] != key {
		t.Fatalf("receipt after report change: %#v, %v", receipts, err)
	}
	request("POST", path+"/reconcile", "", 200)
	fixture.reportChanged.Store(false)
	if chats.reconciled != chats.operation || chats.sends != 1 {
		t.Fatalf("reconciliation: %s, sends=%d", chats.reconciled, chats.sends)
	}
	request("PUT", "/allowed/feishu/settings", strings.NewReplacer("oc_primary", "oc_second", `"version":0`, `"version":1`, `"public_submission_count":true`, `"public_submission_count":false`).Replace(settingsJSON), 200)
	if r := check("", "project.read", "submission_count"); r.Scope != "denied" || r.Passed {
		t.Fatalf("scope: %#v", r)
	}
	request("POST", path+"/publish", "", 404)
	if chats.sends != 1 {
		t.Fatal("changed target was sent")
	}
	fixture.projectLost.Store(true)
	request("GET", "/allowed/docs", "", 404)
}
