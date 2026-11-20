package project

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/system"
)

type briefingGroup struct{ assignmentSource }

func (*briefingGroup) Bot(context.Context) (chat.Member, error) {
	return chat.Member{OpenID: "ou_bot", Name: "测试机器人"}, nil
}

type rejectedDelivery struct {
	chat.DeliverySource
	targets []string
}

func (*rejectedDelivery) BotInGroup(context.Context, string) error { return nil }
func (d *rejectedDelivery) Deliver(_ context.Context, target, _, _, _, _ string) chat.DeliveryResult {
	d.targets = append(d.targets, target)
	return chat.DeliveryResult{State: "failed", Reason: "platform_rejected"}
}

func TestProjectBriefingFailuresAndIsolation(t *testing.T) {
	pool := testProjectPool(t, "f3_briefing_regression")
	ctx := t.Context()
	exec := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO accounts(id,tenant_key,app_id,open_id,active) VALUES
		('relay-user','tenant','app','ou_reader',true),('viewer','tenant','app','ou_viewer',true);
		INSERT INTO project_members(tenant_key,app_id,project_id,account_id,member_id,role) VALUES
		('tenant','app','allowed','relay-user','a1','manager'),('tenant','app','second','relay-user','b1','reader'),
		('tenant','app','allowed','viewer','a2','reader');
		INSERT INTO feishu_bot_configs(tenant_key,app_id,credentials_generation,enabled) VALUES ('tenant','app',1,true);
		INSERT INTO feishu_chat_conversations(tenant_key,app_id,chat_id,kind) VALUES ('tenant','app','oc_alpha','group')`)
	roles := access.New(pool, "tenant", "app")
	roles.ProjectAccess = access.LocalProjects{Pool: pool}
	grid, upstream := testGrid(t)
	delivery := &rejectedDelivery{}
	chats := chat.New(pool, "tenant", "app", 1, &briefingGroup{}, nil, roles.Check, chat.ReceiptSources{})
	chats.ConfigureDelivery(delivery, true)
	configured := true
	svc := New(grid, pool, roles, func(context.Context) (system.Status, error) {
		return system.Status{Config: system.Config{CredentialsConfigured: configured}, Enabled: true}, nil
	}, &projectBot{inGroup: true}, chats, true)
	chats.ConfigureBusinessReceipts(svc)
	manager := testActor
	viewer := identity.Principal{TenantKey: "tenant", AppID: "app", AccountID: "viewer", OpenID: "ou_viewer"}
	actor := manager
	mux := http.NewServeMux()
	svc.Register(mux, func(http.ResponseWriter, *http.Request, identity.SessionPolicy) (identity.Principal, bool) {
		return actor, true
	})
	request := func(method, path, body string, want int) map[string]any {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1/projects/"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, req)
		var result map[string]any
		if res.Code != want || json.Unmarshal(res.Body.Bytes(), &result) != nil {
			t.Fatalf("%s %s 得到 %d %s", method, path, res.Code, res.Body)
		}
		return result
	}
	const settings = `{"target_chat_id":"oc_alpha","timezone":"Asia/Shanghai","local_time":"18:00","public_submission_count":true,"version":0}`
	const check = `{"action":"project.settings","public_scope":"submission_count"}`
	const date = `{"business_date":"2026-11-19"}`
	const briefs = "allowed/reports/briefings"
	if r := request("POST", "allowed/feishu/check", check, 200); r["passed"] != false || r["group"] != "not_bound" {
		t.Fatal(r)
	}
	request("PUT", "allowed/feishu/settings", settings, 200)
	configured = false
	if r := request("POST", "allowed/feishu/check", check, 200); r["passed"] != false || r["bot"] != "not_configured" {
		t.Fatal(r)
	}
	configured = true
	if r := request("POST", "allowed/feishu/check", check, 200); r["passed"] != true {
		t.Fatal(r)
	}
	if r := request("GET", "second/feishu", "", 200); r["can_check"] != false || r["state"] != "not_bound" {
		t.Fatal(r)
	}
	request("POST", "second/feishu/check", check, 404)
	actor = viewer
	request("POST", "allowed/feishu/check", check, 404)
	request("PUT", "allowed/feishu/settings", settings, 404)
	request("POST", briefs, date, 404)
	for _, panel := range []string{"overview", "members", "chats", "meetings", "docs", "reports", "tasks", "feishu"} {
		request("GET", "second/"+panel, "", 404)
	}
	actor = manager
	upstream.reportOffline.Store(true)
	if r := request("POST", briefs, date, 503); r["error"] != "briefing_generation_unavailable" {
		t.Fatal(r)
	}
	var runs, sends int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM project_briefing_runs),
		(SELECT count(*) FROM feishu_send_operations)`).Scan(&runs, &sends); err != nil || runs != 0 || sends != 0 || len(delivery.targets) != 0 {
		t.Fatalf("生成失败后简报 %d、投递 %d、平台调用 %v：%v", runs, sends, delivery.targets, err)
	}
	upstream.reportOffline.Store(false)
	run := request("POST", briefs, date, 200)
	runID := run["id"].(string)
	path := briefs + "/" + runID
	if run["delivery_state"] != "not_sent" || run["generated_at"] == nil {
		t.Fatal(run)
	}
	actor = viewer
	request("POST", path+"/publish", "", 404)
	request("GET", path, "", 404)
	actor = manager
	request("GET", "second/reports/briefings/"+runID, "", 404)
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM feishu_send_operations").Scan(&sends); err != nil || sends != 0 || len(delivery.targets) != 0 {
		t.Fatalf("授权拒绝后投递记录 %d、平台调用 %v：%v", sends, delivery.targets, err)
	}
	for range 2 {
		result := request("POST", path+"/publish", "", 200)
		if result["state"] != "failed" || result["operation_key"] != "briefing:"+runID {
			t.Fatal(result)
		}
	}
	if len(delivery.targets) != 1 || delivery.targets[0] != "oc_alpha" {
		t.Fatalf("投递目标或次数不符 %v", delivery.targets)
	}
	for _, items := range []any{
		request("GET", path, "", 200)["items"],
		request("GET", "allowed/feishu", "", 200)["briefings"].(map[string]any)["items"],
	} {
		rows := items.([]any)
		if len(rows) != 1 {
			t.Fatal(rows)
		}
		row := rows[0].(map[string]any)
		if row["id"] != run["id"] || row["generated_at"] != run["generated_at"] || row["delivery_state"] != "failed" || row["delivery_confirmed_at"] != nil {
			t.Fatal(row)
		}
	}
	exec(`UPDATE project_members SET role='reader' WHERE project_id='allowed' AND account_id='relay-user'`)
	request("POST", path+"/publish", "", 404)
	request("POST", "allowed/feishu/check", check, 404)
	if len(delivery.targets) != 1 {
		t.Fatalf("撤权后发生投递 %v", delivery.targets)
	}
	svc.grid = nil
	for _, panel := range []string{"docs", "meetings", "reports", "feishu"} {
		if r := request("GET", "allowed/"+panel, "", 503); r["error"] != "project_source_not_connected" {
			t.Fatal(r)
		}
	}
	actor = viewer
	request("GET", "second/docs", "", 404)
}
