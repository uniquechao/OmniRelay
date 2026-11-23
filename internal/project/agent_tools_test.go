package project

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/agent/pmtools"
	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

// One success with observable data, and denials for every catalog entry, against migrated PostgreSQL.
func TestProjectPMToolMatrix(t *testing.T) {
	pool := testProjectPool(t, "pm_tool_matrix")
	ctx := t.Context()
	sql := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	sql(`INSERT INTO accounts(id,tenant_key,app_id,open_id,name) SELECT x,'tenant','app','ou_'||x,x FROM unnest(ARRAY['reader','manager','member','editor','new','outside','admin']) x;
 INSERT INTO project_members(tenant_key,app_id,project_id,account_id,member_id,role) SELECT 'tenant','app','north',x,x,x FROM unnest(ARRAY['reader','manager','member','editor']) x;
 INSERT INTO project_profiles(tenant_key,app_id,project_id,name,status,version) VALUES('tenant','app','north','北辰','active',1);
 INSERT INTO project_sources(tenant_key,app_id,project_id,kind,id,title,body) VALUES('tenant','app','north','document','doc','私有文件','私有正文');
 INSERT INTO project_source_readers VALUES('tenant','app','north','document','doc','reader');
 INSERT INTO project_member_reports(tenant_key,app_id,project_id,account_id,work_date,note,version) VALUES('tenant','app','north','reader','2026-11-01','自己的日报',1),('tenant','app','north','member','2026-11-01','同事的日报',1);
 INSERT INTO feishu_bot_configs(tenant_key,app_id,credentials_generation,enabled) VALUES('tenant','app',1,true);
 INSERT INTO feishu_chat_conversations(tenant_key,app_id,chat_id,kind) VALUES('tenant','app','oc','group'),('tenant','app','else','group');
 INSERT INTO project_chat_links(tenant_key,app_id,chat_id,project_id) VALUES('tenant','app','else','south');
 INSERT INTO feishu_chat_messages(tenant_key,app_id,chat_id,message_id,body,search_text,sent_at,updated_at) VALUES('tenant','app','oc','msg','验收记录','验收记录',now(),now());
 INSERT INTO role_grants(tenant_key,app_id,account_id,role,source) VALUES('tenant','app','outside','system_admin','bootstrap');`)
	roles := access.New(pool, "tenant", "app")
	roles.ProjectAccess = access.LocalProjects{Pool: pool}
	source := &toolGroupSource{}
	svc := New(pool, roles, nil, nil, chat.New(pool, "tenant", "app", 1, source, nil, roles.Check, chat.ReceiptSources{}), false)
	var covered []string
	call := func(t *testing.T, who, name, input string, want int) string {
		t.Helper()
		var a pmtools.Args
		if err := json.Unmarshal([]byte(input), &a); err != nil {
			t.Fatal(err)
		}
		actor := identity.Principal{TenantKey: "tenant", AppID: "app", AccountID: who, OpenID: "ou_" + who}
		var v any
		err := roles.WithOrganizationMutation(ctx, func(tx pgx.Tx) error { var e error; v, e = svc.RunTool(ctx, tx, actor, "north", name, a); return e })
		if ToolStatus(err) != want {
			t.Fatalf("%s as %s: got %d (%v), want %d", name, who, ToolStatus(err), err, want)
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
	check := func(name, who, input, contains string) string {
		t.Helper()
		covered = append(covered, name)
		var got string
		t.Run(name, func(t *testing.T) {
			spec, _ := pmtools.Lookup(name)
			call(t, "outside", name, input, 403)
			if spec.Write {
				call(t, "reader", name, input, 403)
			}
			if spec.Action == "project.settings" {
				call(t, "member", name, input, 403)
				call(t, "editor", name, input, 403)
			}
			if !spec.Write {
				forged := strings.TrimSuffix(input, "}")
				if forged != "{" {
					forged += ","
				}
				call(t, who, name, forged+`"project_id":"south"}`, 400)
			}
			got = call(t, who, name, input, 200)
			if !strings.Contains(got, contains) {
				t.Fatalf("%s missing %q in %s", name, contains, got)
			}
		})
		return got
	}
	check("project_get", "reader", "{}", "北辰")
	check("project_update", "manager", `{"expected_version":1,"name":"北辰二期"}`, "2")
	call(t, "manager", "project_update", `{"expected_version":1,"name":"过期"}`, 409)
	check("member_list", "reader", `{"role":"editor"}`, "editor")
	check("member_candidates", "manager", `{"q":"new"}`, "new")
	check("member_add", "manager", `{"account_id":"new","role":"member"}`, "member")
	generation := func(id string) string {
		var g string
		if err := pool.QueryRow(ctx, "SELECT generation FROM project_members WHERE account_id=$1", id).Scan(&g); err != nil {
			t.Fatal(err)
		}
		return g
	}
	call(t, "manager", "member_add", `{"account_id":"new","role":"member"}`, 409)
	call(t, "manager", "member_set_role", `{"account_id":"new","role":"reader","generation":"stale"}`, 409)
	check("member_set_role", "manager", fmt.Sprintf(`{"account_id":"new","generation":%q,"role":"reader"}`, generation("new")), "reader")
	check("member_update", "manager", `{"account_id":"new","expected_version":2,"role_title":"测试负责人","responsibility":"接口验收"}`, "reader")
	call(t, "manager", "member_update", `{"account_id":"new","expected_version":2,"responsibility":"过期"}`, 409)
	check("member_remove", "manager", fmt.Sprintf(`{"account_id":"new","generation":%q}`, generation("new")), "revoked")
	call(t, "manager", "member_add", `{"account_id":"new","role":"project_admin"}`, 200)
	if got := call(t, "manager", "member_list", `{"role":"project_admin"}`, 200); !strings.Contains(got, `"project_role":"project_admin"`) || !strings.Contains(got, `"project_roles"`) {
		t.Fatal(got)
	}
	call(t, "new", "member_update", `{"account_id":"member","expected_version":1,"project_role":"project_admin","expected_project_role":"project_member"}`, 403)
	call(t, "manager", "member_set_role", fmt.Sprintf(`{"account_id":"new","generation":%q,"role":"project_member","expected_project_role":"project_admin"}`, generation("new")), 200)
	if ok, err := roles.Check(ctx, identity.Principal{TenantKey: "tenant", AppID: "app", AccountID: "new"}, "project.settings", "north"); err != nil || ok {
		t.Fatal("plugin demotion did not revoke administration", ok, err)
	}
	call(t, "manager", "member_remove", fmt.Sprintf(`{"account_id":"manager","generation":%q}`, generation("manager")), 409)
	call(t, "reader", "chat_list", `{"candidate_chat_id":"oc"}`, 403)
	if got := call(t, "manager", "chat_list", `{"candidate_chat_id":"oc"}`, 200); !strings.Contains(got, `"version":0`) {
		t.Fatal(got)
	}
	check("chat_bind", "manager", `{"chat_id":"oc","expected_version":0}`, "north")
	check("task_candidate_chat_sources", "manager", fmt.Sprintf(`{"chat_id":"oc","from_at":%q,"to_at":%q}`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), time.Now().Add(time.Hour).UTC().Format(time.RFC3339)), "source_revision")
	check("chat_list", "reader", "{}", "oc")
	check("chat_search_messages", "reader", `{"chat_id":"oc","q":"验收"}`, "验收记录")
	call(t, "member", "chat_search_messages", `{"chat_id":"oc","q":"验收"}`, 403)
	if got := call(t, "member", "chat_list", "{}", 200); strings.Contains(got, `"oc"`) {
		t.Fatal(got)
	}
	call(t, "manager", "chat_bind", `{"chat_id":"else","expected_version":0}`, 403)
	sql(`INSERT INTO project_relay_settings(tenant_key,app_id,project_id,target_chat_id,timezone,local_time,version,public_submission_count) VALUES('tenant','app','north','oc','UTC','18:00',1,true);
 INSERT INTO project_briefing_runs(tenant_key,app_id,project_id,business_date,id,account_id,target_chat_id,settings_version,source_revision,visible_submissions)
 VALUES('tenant','app','north','2026-11-01','published','manager','oc',1,'local:test',2);
 INSERT INTO feishu_send_operations(tenant_key,app_id,operation_key,account_id,chat_id,generation,fingerprint,business_receipt) VALUES('tenant','app','briefing:published','manager','oc',1,'x','{}');
 INSERT INTO feishu_send_parts(tenant_key,app_id,operation_key,position,kind,state,attempted,message_id) VALUES('tenant','app','briefing:published',0,'text','success',true,'msg')`)
	if got := call(t, "reader", "daily_report_query", `{"source":"published"}`, 200); !strings.Contains(got, "visible_submissions") {
		t.Fatal(got)
	}
	if got := call(t, "member", "daily_report_query", `{"source":"published"}`, 200); strings.Contains(got, "visible_submissions") {
		t.Fatal(got)
	}
	check("chat_set_primary", "manager", `{"chat_id":"oc","expected_version":1,"primary_version":0}`, "oc")
	call(t, "manager", "chat_set_primary", `{"chat_id":"oc","expected_version":2,"primary_version":0}`, 409)
	source.denied.Store(true)
	call(t, "manager", "chat_unbind", `{"chat_id":"oc","expected_version":2}`, 403)
	call(t, "manager", "chat_set_primary", `{"chat_id":"oc","expected_version":2,"primary_version":1}`, 403)
	source.denied.Store(false)
	check("chat_unbind", "manager", `{"chat_id":"oc","expected_version":2}`, `"project_id":""`)
	check("file_search", "reader", `{"q":"私有"}`, "document:doc")
	check("file_get", "reader", `{"file_ref":"document:doc"}`, "私有文件")
	call(t, "manager", "file_get", `{"file_ref":"document:doc"}`, 403)
	if got := call(t, "manager", "file_search", "{}", 200); strings.Contains(got, "私有") {
		t.Fatal(got)
	}
	result := check("task_create", "member", `{"title":"本人任务"}`, "todo")
	var task struct{ ID string }
	if json.Unmarshal([]byte(result), &task) != nil || task.ID == "" {
		t.Fatal("create returned no ID")
	}
	arg := func(extra string) string { return fmt.Sprintf(`{"task_id":%q%s}`, task.ID, extra) }
	check("task_get", "reader", arg(""), "本人任务")
	check("task_search", "reader", `{"q":"本人"}`, task.ID)
	check("task_update", "member", arg(`,"expected_version":1,"title":"修订"`), `"version":2`)
	call(t, "member", "task_update", arg(`,"expected_version":1,"title":"过期"`), 409)
	call(t, "member", "task_update", arg(`,"expected_version":2,"owner_member_id":"manager"`), 403)
	call(t, "member", "task_create", `{"title":"他人任务","owner_member_id":"manager"}`, 403)
	call(t, "member", "task_transition", arg(`,"expected_version":2,"expected_board_version":1,"status":"in_progress"`), 409)
	call(t, "member", "task_transition", arg(`,"expected_version":2,"expected_board_version":3,"status":"blocked"`), 400)
	check("task_transition", "member", arg(`,"expected_version":2,"expected_board_version":3,"status":"blocked","blocked_reason":"等待验收"`), "blocked")
	call(t, "member", "task_transition", arg(`,"expected_version":3,"expected_board_version":4,"status":"cancelled","change_reason":"取消"`), 403)
	check("task_add_progress", "editor", arg(`,"expected_version":3,"body":"补充进度"`), task.ID)
	got := check("daily_report_query", "reader", `{"date_from":"2026-11-01","date_to":"2026-11-01"}`, "自己的日报")
	if strings.Contains(got, "同事的日报") {
		t.Fatal(got)
	}
	if got = call(t, "manager", "daily_report_query", "{}", 200); !strings.Contains(got, "同事的日报") {
		t.Fatal(got)
	}
	// The owner/participant boundary is independent of being able to comment.
	foreign := call(t, "manager", "task_create", `{"title":"他人的任务"}`, 200)
	var other struct{ ID string }
	if err := json.Unmarshal([]byte(foreign), &other); err != nil {
		t.Fatal(err)
	}
	otherArgs := fmt.Sprintf(`{"task_id":%q,"expected_version":1,`, other.ID)
	call(t, "member", "task_update", otherArgs+`"title":"禁止"}`, 403)
	call(t, "member", "task_add_progress", otherArgs+`"body":"来自成员的进展"}`, 200)
	sql(`SELECT set_config('omnirelay.task_actor','manager',true); INSERT INTO project_task_boards(tenant_key,app_id,project_id) VALUES('tenant','app','south');
 INSERT INTO project_tasks(tenant_key,app_id,project_id,id,title,status,column_id,position) VALUES('tenant','app','south','foreign-task','跨项目','todo','todo',1)`)
	call(t, "manager", "task_get", `{"task_id":"foreign-task"}`, 403)
	call(t, "manager", "task_update", `{"task_id":"foreign-task","expected_version":1,"title":"禁止"}`, 403)
	removed := call(t, "manager", "task_create", `{"title":"待删除任务","due_at":"2026-12-01","acceptance":"验收","participant_ids":["member"]}`, 200)
	var removedTask struct{ ID string }
	if err := json.Unmarshal([]byte(removed), &removedTask); err != nil {
		t.Fatal(err)
	}
	deleteArgs := fmt.Sprintf(`{"task_id":%q,"expected_version":1,"change_reason":"重复任务"}`, removedTask.ID)
	call(t, "member", "task_delete", deleteArgs, 403)
	check("task_delete", "manager", deleteArgs, `"deleted":true`)
	call(t, "manager", "task_get", fmt.Sprintf(`{"task_id":%q}`, removedTask.ID), 403)
	if got := call(t, "manager", "task_search", `{"q":"待删除"}`, 200); strings.Contains(got, removedTask.ID) {
		t.Fatal(got)
	}

	encode := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	read := func(data string) map[string]any {
		t.Helper()
		var x map[string]any
		if err := json.Unmarshal([]byte(data), &x); err != nil {
			t.Fatal(err)
		}
		return x
	}
	check("task_board_get", "reader", "{}", "default_column_id")
	boardVersion := func() float64 { return read(call(t, "manager", "task_board_get", "{}", 200))["version"].(float64) }
	check("task_query", "reader", `{"q":"本人","limit":10}`, "items")
	activeTask := read(call(t, "member", "task_create", `{"title":"插件操作联验"}`, 200))
	id := activeTask["id"].(string)
	ver := func() float64 {
		return read(call(t, "manager", "task_get", encode(map[string]any{"task_id": id}), 200))["version"].(float64)
	}
	moved := map[string]any{"task_id": id, "expected_task_version": ver(), "expected_board_version": boardVersion(), "target_column_id": "in_progress"}
	check("task_move", "manager", encode(moved), "in_progress")
	check("task_bulk_update", "manager", encode(map[string]any{"items": []any{map[string]any{"id": id, "expected_version": ver(), "priority": "P2"}}}), id)
	check("task_activity_list", "reader", encode(map[string]any{"task_id": id, "kind": "change"}), "items")
	check("task_evidence_list", "reader", encode(map[string]any{"task_id": id}), "items")
	feedback := read(check("task_feedback_submit", "member", encode(map[string]any{"task_id": id, "task_version": ver(), "proposed_status": "done", "note": "完成检查"}), "id"))
	check("task_feedback_list", "member", encode(map[string]any{"task_id": id}), "完成检查")
	check("task_feedback_review", "manager", encode(map[string]any{"suggestion_id": feedback["id"], "expected_version": feedback["version"], "action": "accept", "reason": "已确认"}), "accepted")
	change := map[string]any{"task_id": id, "expected_version": ver(), "expected_board_version": boardVersion(), "status": "cancelled"}
	call(t, "manager", "task_transition", encode(change), 400)
	change["change_reason"] = "无需继续"
	call(t, "manager", "task_transition", encode(change), 200)
	check("task_board_save", "manager", encode(map[string]any{"expected_version": boardVersion(), "columns": defaultLanes, "default_column_id": "todo"}), "todo")
	tag := map[string]any{"name": "联验", "type": "分类", "color": "#123456", "expected_version": boardVersion()}
	tagged := read(check("task_tag_create", "manager", encode(tag), "联验"))
	added := tagged["tags"].([]any)[0].(map[string]any)["id"]
	tag["name"] = "改名"
	tag["expected_version"] = boardVersion()
	tag["tag_id"] = added
	check("task_tag_update", "manager", encode(tag), "改名")
	check("task_tag_delete", "manager", encode(map[string]any{"tag_id": added, "expected_version": boardVersion()}), "tags")
	sql("INSERT INTO project_source_readers VALUES('tenant','app','north','document','doc','manager')")
	check("task_actions", "reader", encode(map[string]any{"task_id": id}), "delete")
	check("task_business_time", "reader", "{}", "business_date")
	check("task_board_timezone", "manager", encode(map[string]any{"expected_board_version": boardVersion(), "business_timezone": "UTC"}), "UTC")
	check("task_milestone_create", "manager", encode(map[string]any{"title": "工具节点", "task_id": id}), "id")
	check("task_milestone_list", "reader", "{}", "工具节点")
	check("task_evidence_link", "manager", encode(map[string]any{"task_id": id, "source_kind": "document", "source_id": "doc"}), id)
	check("task_source_tasks", "reader", `{"source_kind":"document","source_id":"doc"}`, id)
	snapshot := read(check("task_candidate_sources", "manager", "{}", "source_revision"))
	sources := snapshot["sources"].([]any)
	if len(sources) == 0 {
		t.Fatal("missing source")
	}
	suggestion := map[string]any{"title": "候选验收", "related_project": true, "evidence_ids": []string{sources[0].(map[string]any)["id"].(string)}}
	submitted := read(check("task_candidate_submit", "manager", encode(map[string]any{"session_ref": "tool-matrix", "source_revision": snapshot["source_revision"], "candidates": []any{suggestion}}), "candidate_ids"))
	candidate := submitted["candidate_ids"].([]any)[0]
	check("task_candidate_list", "manager", `{"status":"pending_review"}`, "候选验收")
	check("task_candidate_review", "manager", encode(map[string]any{"candidate_id": candidate, "expected_version": 1, "action": "confirm", "share_ack": true, "no_deadline_ack": true, "fields": map[string]string{"title": "候选验收"}}), "task_id")
	actor := identity.Principal{TenantKey: "tenant", AppID: "app", AccountID: "manager", OpenID: "ou_manager"}
	recheck := func() error {
		return roles.WithOrganizationMutation(ctx, func(tx pgx.Tx) error {
			_, err := svc.RecheckToolRead(ctx, tx, actor, "north", candidateRead, pmtools.Args{"id": json.RawMessage(encode(candidate))})
			return err
		})
	}
	if err := recheck(); err != nil {
		t.Fatal("review must preserve access to original evidence", err)
	}
	sql("DELETE FROM project_source_readers WHERE account_id='manager' AND source_id='doc'")
	if err := recheck(); ToolStatus(err) != 403 {
		t.Fatal("revoked candidate source", err)
	}

	sql("INSERT INTO project_members(tenant_key,app_id,project_id,account_id,member_id,role) VALUES('tenant','app','north','outside','outside','reader')")
	call(t, "outside", "project_update", `{"expected_version":2,"background":"项目内系统管理员"}`, 200)
	// A delegated admin is authorized for settings only during that membership generation.
	sql(`INSERT INTO project_members(tenant_key,app_id,project_id,account_id,member_id,role) VALUES('tenant','app','north','admin','admin','member');
 INSERT INTO role_grants(tenant_key,app_id,account_id,project_id,role,source,granted_by,membership_generation)
 SELECT tenant_key,app_id,account_id,project_id,'project_admin','project_manager','manager',generation FROM project_members WHERE account_id='admin'`)
	call(t, "admin", "project_update", `{"expected_version":3,"background":"委派修改"}`, 200)
	call(t, "admin", "member_add", `{"account_id":"new","role":"manager"}`, 403)
	sql("UPDATE project_members SET generation=gen_random_uuid()::text WHERE account_id='admin'")
	call(t, "admin", "project_update", `{"expected_version":4,"background":"撤权"}`, 403)
	sql(`INSERT INTO feishu_chat_conversations(tenant_key,app_id,chat_id,kind) VALUES('tenant','app','oc_roster','group');
 INSERT INTO project_chat_links(tenant_key,app_id,chat_id,project_id) VALUES('tenant','app','oc_roster','north');
 INSERT INTO feishu_drive_group_memberships(tenant_key,app_id,chat_id,generation,verified_at,members,member_names)
 VALUES('tenant','app','oc_roster',1,now(),ARRAY['ou_group'], '{"ou_group":"王甲"}');`)
	if got := call(t, "manager", "member_list", `{"purpose":"assignment","q":"王"}`, 200); !strings.Contains(got, "feishu:ou_group") {
		t.Fatal(got)
	}
	call(t, "manager", "task_create", `{"title":"群内分派","owner_member_id":"feishu:ou_group"}`, 200)
	call(t, "manager", "task_create", `{"title":"无效分派","owner_member_id":"feishu:ou_someone"}`, 400)
	if got := call(t, "manager", "member_list", `{"q":"王甲"}`, 200); !strings.Contains(got, "feishu:ou_group") {
		t.Fatalf("bound group member missing from project: %s", got)
	}
	call(t, "manager", "member_add", `{"account_id":"feishu:ou_group","role":"member"}`, 409)
	slices.Sort(covered)
	if !slices.Equal(covered, pmtools.Names()) {
		t.Fatalf("catalog and tested tools differ: %v", covered)
	}
}

type toolGroupSource struct{ assignmentSource }

func (s *toolGroupSource) Members(context.Context, string, string) (chat.Page[chat.Member], error) {
	if s.denied.Load() {
		return chat.Page[chat.Member]{}, chat.ErrAccessLost
	}
	return chat.Page[chat.Member]{Items: []chat.Member{{OpenID: "ou_reader"}, {OpenID: "ou_manager"}}}, nil
}
