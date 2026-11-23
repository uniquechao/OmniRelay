package project

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

func TestConcurrentShortCodeAllocation(t *testing.T) {
	pool := testProjectPool(t, "concurrent_project_codes")
	const projects = 8
	start := make(chan struct{})
	done := make(chan error, projects)
	ctx := t.Context()
	for i := range projects {
		go func() {
			<-start
			_, err := pool.Exec(ctx, `INSERT INTO project_profiles(tenant_key,app_id,project_id,name,status,version)
				VALUES('tenant','app',$1,'ABC 同名项目','active',1)`, fmt.Sprintf("project-%d", i))
			done <- err
		}()
	}
	close(start)
	for range projects {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	var distinct int
	err := pool.QueryRow(ctx, `SELECT count(DISTINCT project_task_code(short_code,1)) FROM project_short_codes`).Scan(&distinct)
	if err != nil || distinct != projects {
		t.Fatalf("并发建项后不同任务编号数 %d，错误 %v", distinct, err)
	}
}

func TestProjectTypeSelection(t *testing.T) {
	for _, tc := range []struct {
		name, existing, want string
		input                *string
		invalid              bool
	}{
		{name: "catalog choice", input: new("研发"), want: "研发"},
		{name: "arbitrary new type", input: new("自定义类型"), invalid: true},
		{name: "existing custom type", existing: "自定义类型", input: new("自定义类型"), want: "自定义类型"},
		{name: "missing type preserves existing", existing: "自定义类型", want: "自定义类型"},
		{name: "explicitly clear", existing: "销售", input: new("")},
		{name: "replace legacy", existing: "自定义类型", input: new("销售"), want: "销售"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := profileInput{Name: "项目", Status: "planned", Type: tc.input}
			got, err := in.apply(Project{Type: tc.existing})
			if (err == errInput) != tc.invalid || got.Type != tc.want {
				t.Fatalf("type = %q, error = %v, want type %q, invalid=%t", got.Type, err, tc.want, tc.invalid)
			}
		})
	}
}

func TestProjectProfileLifecycle(t *testing.T) {
	pool := testProjectPool(t, "project_profile_lifecycle")
	ctx := t.Context()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO accounts(id,tenant_key,app_id,open_id) VALUES
		('relay-user','tenant','app','ou_reader'),('reader','tenant','app','ou_viewer');
		INSERT INTO role_grants(tenant_key,app_id,account_id,role,source)
		VALUES('tenant','app','relay-user','system_admin','bootstrap');
		INSERT INTO project_members(tenant_key,app_id,project_id,account_id,member_id,role)
		VALUES('tenant','app','legacy','relay-user','relay-user','manager');
		INSERT INTO feishu_bot_configs (tenant_key,app_id,credentials_generation) VALUES ('tenant','app',1);
		INSERT INTO feishu_chat_conversations(tenant_key,app_id,chat_id,kind) VALUES('tenant','app','oc_primary','group')`)
	roles := access.New(pool, "tenant", "app")
	roles.ProjectAccess = access.LocalProjects{Pool: pool}
	svc := New(pool, roles, nil, nil, &projectChat{}, true)
	actor := testActor
	reader := identity.Principal{TenantKey: "tenant", AppID: "app", AccountID: "reader", OpenID: "ou_viewer"}
	mux := http.NewServeMux()
	svc.Register(mux, func(_ http.ResponseWriter, r *http.Request, policy identity.SessionPolicy) (identity.Principal, bool) {
		if r.Method != http.MethodGet && policy != identity.RefreshSessionWithCSRF {
			t.Errorf("%s %s 未要求 CSRF", r.Method, r.URL.Path)
		}
		return actor, true
	})
	const key = "1bc78901-2345-4123-8123-0123456789ab"
	request := func(method, path, payload string, status int) string {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1/projects"+path, strings.NewReader(payload))
		r.Header.Set("Content-Type", "application/json")
		if method == "POST" && path == "" {
			r.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: status=%d, body=%s", method, path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	checkJSON := func(value string, parts ...string) {
		t.Helper()
		if !json.Valid([]byte(value)) {
			t.Fatal(value)
		}
		for _, part := range parts {
			if !strings.Contains(value, part) {
				t.Errorf("%s 缺少 %s", value, part)
			}
		}
	}
	const create = `{"name":"本地试点","project_type":"交付","background":"项目背景","planned_start":"2026-11-22","planned_end":"2026-12-01","business_timezone":"UTC"}`
	actor = reader
	request("POST", "", create, 404)
	checkJSON(request("GET", "", "", 200), `"can_create":false`, `"total":0`)
	actor = testActor
	checkJSON(request("GET", "", "", 200), `"can_create":true`)
	request("POST", "", `{"name":"  "}`, 400)
	request("POST", "", `{"name":"计划倒置","planned_start":"2026-12-01","planned_end":"2026-11-22"}`, 400)
	request("POST", "", `{"name":"时区错误","business_timezone":"Local"}`, 400)
	first := request("POST", "", create, 200)
	checkJSON(first, `"project_id":"`+key+`"`, `"version":1`)
	if replay := request("POST", "", create, 200); replay != first {
		t.Fatal(first, replay)
	}
	request("POST", "", `{"name":"其他请求"}`, 409)
	path := "/" + key
	checkJSON(request("GET", path+"/overview", "", 200), `"can_manage_profile":true`, `"status":"planned"`,
		`"planned_start":"2026-11-22"`, `"planned_end":"2026-12-01"`, `"business_timezone":"UTC"`, `"updated_at":`)
	checkJSON(request("GET", path+"/members", "", 200), `"id":"relay-user"`, `"role":"manager"`)
	checkJSON(request("GET", "?q=本地试点", "", 200), `"total":1`, `"name":"本地试点"`)
	actor = reader
	request("GET", path+"/overview", "", 404)
	exec(`INSERT INTO project_members(tenant_key,app_id,project_id,account_id,member_id,role)
		VALUES('tenant','app',$1,'reader','reader','reader')`, key)
	checkJSON(request("GET", path+"/overview", "", 200), `"can_manage_profile":false`)
	request("PUT", path+"/profile", `{"name":"越权写入","status":"active","expected_version":1}`, 404)
	request("POST", path+"/archive", `{"expected_version":1}`, 404)
	actor = testActor
	request("PUT", path+"/tasks/board/timezone", `{"business_timezone":"Asia/Tokyo","expected_board_version":1}`, 200)
	const edit = `{"name":"试点交付","status":"active","project_type":"交付","background":"已更新背景","business_timezone":"Asia/Shanghai","expected_version":1,"expected_board_version":%d}`
	request("PUT", path+"/profile", fmt.Sprintf(edit, 1), 409)
	request("PUT", path+"/profile", fmt.Sprintf(edit, 2), 200)
	request("PUT", path+"/profile", fmt.Sprintf(edit, 2), 409)
	checkJSON(request("GET", path+"/overview", "", 200), `"name":"试点交付"`, `"planned_end":"2026-12-01"`, `"business_timezone":"Asia/Shanghai"`)
	exec(`INSERT INTO project_profiles (tenant_key,app_id,project_id,name,status,version,updated_at)
		VALUES ('tenant','app','older','A 较早项目','active',1,'2020-01-01T00:00:00Z');
		INSERT INTO project_members (tenant_key,app_id,project_id,account_id,member_id,role)
		VALUES ('tenant','app','older','relay-user','relay-user','manager')`)
	for i, want := range []string{key, "older", "legacy"} {
		body := request("GET", fmt.Sprintf("?status=active&page_size=1&page=%d", i+1), "", 200)
		var page ProjectPage
		if err := json.Unmarshal([]byte(body), &page); err != nil || page.Total != 3 ||
			len(page.Items) != 1 || page.Items[0].ID != want || (page.Items[0].UpdatedAt == nil) != (want == "legacy") {
			t.Fatalf("project update order on page %d: %s (%v)", i+1, body, err)
		}
	}
	const settings = `{"target_chat_id":"oc_primary","timezone":"Asia/Shanghai","local_time":"18:00","plan_enabled":true,"public_submission_count":true,"version":%d}`
	for _, order := range []struct{ direction, first string }{{"asc", "older"}, {"desc", key}} {
		body := request("GET", "?status=active&page_size=1&sort=updated_at&direction="+order.direction, "", 200)
		var page ProjectPage
		if err := json.Unmarshal([]byte(body), &page); err != nil || len(page.Items) != 1 || page.Items[0].ID != order.first {
			t.Fatalf("unexpected first project for %s: %s", order.direction, body)
		}
	}
	request("GET", "?sort=unrecognized&direction=asc", "", 400)
	request("GET", "?sort=name&direction=random", "", 400)
	request("PUT", path+"/feishu/settings", fmt.Sprintf(settings, 0), 200)
	request("PUT", path+"/reports/mine/2026-11-22", `{"note":"保留的日报","expected_version":0}`, 200)
	var run briefing
	generated := request("POST", path+"/reports/briefings", `{"business_date":"2026-11-22"}`, 200)
	if err := json.Unmarshal([]byte(generated), &run); err != nil || run.ID == "" {
		t.Fatal(generated, err)
	}
	request("POST", path+"/archive", `{"expected_version":1}`, 409)
	checkJSON(request("POST", path+"/archive", `{"expected_version":2}`, 200), `"version":3`)
	checkJSON(request("POST", path+"/archive", `{"expected_version":2}`, 200), `"version":3`)
	request("PUT", path+"/profile", `{"name":"重新激活","status":"active","expected_version":3}`, 409)
	request("PUT", path+"/tasks/board/timezone", `{"business_timezone":"UTC","expected_board_version":3}`, 409)
	request("PUT", path+"/feishu/settings", fmt.Sprintf(settings, 2), 409)
	request("POST", path+"/reports/briefings", `{"business_date":"2026-11-22"}`, 409)
	request("POST", path+"/reports/briefings/"+run.ID+"/publish", ``, 404)
	checkJSON(request("GET", path+"/reports/briefings/"+run.ID, "", 200), `"delivery_state":"not_sent"`)
	checkJSON(request("GET", path+"/reports?date_from=2026-11-22&date_to=2026-11-22", "", 200), `"note":"保留的日报"`)
	actor = reader
	checkJSON(request("GET", path+"/overview", "", 200), `"can_manage_profile":false`, `"status":"archived"`)
	checkJSON(request("GET", "?status=archived", "", 200), `"total":1`, `"name":"试点交付"`)
	actor = testActor
	request("POST", "/legacy/archive", `{"expected_version":0}`, 200)
	checkJSON(request("GET", "/legacy/overview", "", 200), `"name":"legacy"`, `"status":"archived"`)
	var planEnabled bool
	var relayVersion int
	if err := pool.QueryRow(ctx, `SELECT plan_enabled,version FROM project_relay_settings WHERE project_id=$1`, key).
		Scan(&planEnabled, &relayVersion); err != nil || planEnabled || relayVersion != 2 {
		t.Fatalf("归档后的简报配置：%v，版本 %d，错误 %v", planEnabled, relayVersion, err)
	}
	var actions []string
	if err := pool.QueryRow(ctx, `SELECT array_agg(action || ':' || change_version ORDER BY id)
		FROM audit_events WHERE resource_id=$1 AND action IN ('project.create','project.profile.update','project.archive')`, key).
		Scan(&actions); err != nil || strings.Join(actions, ",") != "project.create:1,project.profile.update:2,project.archive:3" {
		t.Fatalf("项目审计：%v，错误 %v", actions, err)
	}
}
