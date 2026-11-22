package project

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

func TestMemberTaskCRUDAtomicity(t *testing.T) {
	pool := testProjectPool(t, "member_task_atomicity")
	ctx := t.Context()
	sql := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	sql(`INSERT INTO accounts(id,tenant_key,app_id,open_id,name) SELECT x,'tenant','app',x,x FROM unnest(ARRAY['manager','candidate','reader','outside']) x;
 INSERT INTO project_members(tenant_key,app_id,project_id,account_id,member_id,role) VALUES('tenant','app','north','manager','manager','manager'),('tenant','app','north','reader','reader','reader')`)
	roles := access.New(pool, "tenant", "app")
	roles.ProjectAccess = access.LocalProjects{Pool: pool}
	svc := Service{pool: pool, roles: roles}
	actor := identity.Principal{TenantKey: "tenant", AppID: "app", AccountID: "manager", OpenID: "manager"}
	mux := http.NewServeMux()
	svc.Register(mux, func(_ http.ResponseWriter, r *http.Request, policy identity.SessionPolicy) (identity.Principal, bool) {
		if r.Method != "GET" && policy != identity.RefreshSessionWithCSRF {
			t.Error("write omitted CSRF policy")
		}
		return actor, true
	})
	call := func(method, path, body string, status int) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1/projects/north/"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		var v map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	call("PUT", "members/candidate", `{"role":"editor","expected_version":0,"role_title":"开发负责人","responsibility":"接口交付"}`, 200)
	call("PUT", "members/candidate", `{"role":"reader","expected_version":0}`, 409)
	var generation string
	if err := pool.QueryRow(ctx, "SELECT generation FROM project_members WHERE account_id='candidate'").Scan(&generation); err != nil {
		t.Fatal(err)
	}
	call("PUT", "members/candidate", `{"expected_version":1,"responsibility":""}`, 200)
	var next string
	var version int64
	if err := pool.QueryRow(ctx, "SELECT generation,version FROM project_members WHERE account_id='candidate'").Scan(&next, &version); err != nil || generation != next || version != 2 {
		t.Fatalf("metadata update changed authorization generation: %v %s %d", err, next, version)
	}
	actor.AccountID, actor.OpenID = "reader", "reader"
	call("PUT", "members/candidate/access", `{"expected_version":99,"role":"editor"}`, 404)
	actor.AccountID, actor.OpenID = "manager", "manager"
	task := call("POST", "tasks", `{"title":"删除样本","owner_member_id":"candidate"}`, 200)
	id := task["id"].(string)
	milestone := call("POST", "milestones", `{"title":"独立节点","task_id":"`+id+`"}`, 200)
	sql(`INSERT INTO project_sources(tenant_key,app_id,project_id,kind,id,title,body) VALUES('tenant','app','north','document','source','来源','正文');
 INSERT INTO project_source_readers VALUES('tenant','app','north','document','source','manager')`)
	call("PUT", "tasks/"+id+"/evidence/document/source", "", 200)
	call("PUT", "members/candidate", `{"role":"revoked","expected_version":2}`, 409)
	path := "tasks/" + id
	call("DELETE", path, `{"expected_version":2,"change_reason":"过期"}`, 409)
	payload := `{"expected_version":1,"request_key":"remove-once","change_reason":"重复任务"}`
	first := call("DELETE", path, payload, 200)
	again := call("DELETE", path, payload, 200)
	if first["version"] != float64(2) || again["version"] != first["version"] {
		t.Fatal(first, again)
	}
	call("DELETE", path, strings.Replace(payload, "重复任务", "其他原因", 1), 409)
	for _, suffix := range []string{"", "/actions", "/activity", "/evidence", "/update-suggestions"} {
		call("GET", path+suffix, "", 404)
	}
	sources := call("GET", "sources/document/source/tasks", "", 200)
	if sources["total"] != float64(0) {
		t.Fatal(sources)
	}
	board := call("GET", "tasks", "", 200)["data"].(map[string]any)
	if board["total"] != float64(0) {
		t.Fatal(board)
	}
	var linked *string
	if err := pool.QueryRow(ctx, "SELECT task_id FROM project_milestones WHERE id=$1", milestone["id"]).Scan(&linked); err != nil || linked != nil {
		t.Fatal(err, linked)
	}
	var events int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM project_task_activity WHERE task_id=$1 AND action='task.deleted'", id).Scan(&events); err != nil || events != 1 {
		t.Fatal(err, events)
	}
	call("PUT", "members/candidate", `{"role":"revoked","expected_version":2}`, 200)
	// Failure to append the audit must roll back the deletion, activity, and board version.
	fresh := call("POST", "tasks", `{"title":"审计失败回滚"}`, 200)
	fid := fresh["id"].(string)
	sql(`ALTER TABLE audit_events ADD CONSTRAINT reject_task_delete CHECK(action<>'task.deleted') NOT VALID`)
	call("DELETE", "tasks/"+fid, `{"expected_version":1,"request_key":"rollback-delete","change_reason":"验证回滚"}`, 400)
	restored := call("GET", "tasks/"+fid, "", 200)
	if restored["version"] != float64(1) || restored["board_version"] != fresh["board_version"] {
		t.Fatal(restored, fresh)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM project_task_activity WHERE task_id=$1 AND action='task.deleted'", fid).Scan(&events); err != nil || events != 0 {
		t.Fatal(err, events)
	}
}
