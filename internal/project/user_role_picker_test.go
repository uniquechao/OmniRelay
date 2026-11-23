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

func TestSystemProjectRolesAndUserChoices(t *testing.T) {
	pool := testProjectPool(t, "project_user_role_choices")
	ctx := t.Context()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO accounts(id,tenant_key,app_id,open_id,name,active) VALUES
 ('manager','tenant','app','manager','负责人',true),('candidate','tenant','app','candidate','同名用户',true),
 ('member','tenant','app','member','同名用户',true),('reader','tenant','app','reader','只读用户',true),
 ('disabled','tenant','app','disabled','停用用户',false),('outsider','else','app','outside','其他租户',true);
 INSERT INTO project_members(tenant_key,app_id,project_id,account_id,member_id,role) VALUES
 ('tenant','app','north','manager','manager','manager'),('tenant','app','north','member','member','member'),('tenant','app','north','reader','reader','reader')`)
	if err := identity.ProvisionPassword(ctx, pool, "tenant", "app", "candidate", "alice.login", "test-password-strong-enough"); err != nil {
		t.Fatal(err)
	}
	roles := access.New(pool, "tenant", "app")
	roles.ProjectAccess = access.LocalProjects{Pool: pool}
	if err := roles.Bootstrap(ctx, "manager"); err != nil {
		t.Fatal(err)
	}
	svc := Service{pool: pool, roles: roles}
	actor := identity.Principal{TenantKey: "tenant", AppID: "app", AccountID: "manager", OpenID: "manager"}
	mux := http.NewServeMux()
	auth := func(_ http.ResponseWriter, _ *http.Request, _ identity.SessionPolicy) (identity.Principal, bool) {
		return actor, true
	}
	svc.Register(mux, auth)
	roles.Register(mux, auth)
	call := func(method, path, body string, want int) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s %d %s", method, path, w.Code, w.Body.String())
		}
		var value map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	root := "/api/v1/projects/north/"
	data := call("GET", root+"members", "", 200)["data"].(map[string]any)
	definitions := data["project_roles"].([]any)
	if len(definitions) != 2 {
		t.Fatal(definitions)
	}
	system := call("GET", "/api/v1/system/roles", "", 200)["items"].([]any)
	for _, item := range definitions {
		role := item.(map[string]any)
		if role["scope"] != "project" {
			t.Fatal(role)
		}
		found := false
		for _, value := range system {
			other := value.(map[string]any)
			if other["id"] == role["id"] {
				found = other["name"] == role["name"]
			}
		}
		if !found {
			t.Fatal(role)
		}
	}
	candidates := call("GET", root+"member-candidates", "", 200)["items"].([]any)
	if len(candidates) != 1 {
		t.Fatal(candidates)
	}
	choice := candidates[0].(map[string]any)
	if choice["account_id"] != "candidate" || choice["username"] != "alice.login" {
		t.Fatal(choice)
	}
	raw, _ := json.Marshal(choice)
	if strings.Contains(string(raw), "password") || strings.Contains(string(raw), "permission_version") {
		t.Fatal("non-public user fields returned")
	}
	byLogin := call("GET", root+"member-candidates?q=alice.login", "", 200)["items"].([]any)
	if len(byLogin) != 1 {
		t.Fatal(byLogin)
	}
	// A binding does not narrow system-wide search. Directory-only people
	// resolve through their existing Feishu identity when added to a project.
	exec(`INSERT INTO feishu_bot_configs(tenant_key,app_id,credentials_generation,enabled) VALUES ('tenant','app',1,true);
		INSERT INTO feishu_chat_conversations(tenant_key,app_id,chat_id,kind) VALUES ('tenant','app','oc_picker','group');
		INSERT INTO project_chat_links(tenant_key,app_id,chat_id,project_id) VALUES ('tenant','app','oc_picker','north');
		INSERT INTO directory_sources(tenant_key,app_id) VALUES ('tenant','app');
		INSERT INTO directory_sync_runs(tenant_key,app_id,trigger,state) VALUES ('tenant','app','initial','succeeded');
		UPDATE directory_sources SET visible=true,active_version=(SELECT id FROM directory_sync_runs);
		INSERT INTO directory_people(tenant_key,app_id,snapshot_version,open_id,name,avatar_url,directory_status)
		SELECT 'tenant','app',id,'ou_directory','通讯录用户','','active' FROM directory_sync_runs`)
	if all := call("GET", root+"member-candidates", "", 200)["items"].([]any); len(all) != 2 {
		t.Fatalf("system directory for linked project: %v", all)
	}
	if outside := call("GET", root+"member-candidates?q=alice.login", "", 200)["items"].([]any); len(outside) != 1 {
		t.Fatalf("outside-group system account: %v", outside)
	}
	directory := call("GET", root+"member-candidates?q=通讯录", "", 200)["items"].([]any)
	if len(directory) != 1 || directory[0].(map[string]any)["account_id"] != "feishu:ou_directory" {
		t.Fatalf("directory-only candidate: %v", directory)
	}
	call("PUT", root+"members/feishu:ou_directory", `{"role":"member","expected_version":0}`, 200)
	if repeated := call("GET", root+"member-candidates?q=通讯录", "", 200)["items"].([]any); len(repeated) != 0 {
		t.Fatal(repeated)
	}
	exec(`INSERT INTO feishu_drive_group_memberships(tenant_key,app_id,chat_id,generation,verified_at,members,member_names)
		VALUES ('tenant','app','oc_picker',1,now(),ARRAY['ou_default'],'{"ou_default":"群内人员"}')`)
	defaultMember := identity.Principal{TenantKey: "tenant", AppID: "app", OpenID: "ou_default"}
	if err := pool.QueryRow(ctx, "SELECT id FROM accounts WHERE tenant_key='tenant' AND app_id='app' AND open_id=$1", defaultMember.OpenID).Scan(&defaultMember.AccountID); err != nil {
		t.Fatal(err)
	}
	if ok, err := roles.Check(ctx, defaultMember, "project.read", "north"); err != nil || !ok {
		t.Fatalf("default member access: %v %v", ok, err)
	}
	if ok, err := roles.Check(ctx, defaultMember, "project.settings", "north"); err != nil || ok {
		t.Fatalf("default member settings: %v %v", ok, err)
	}
	if repeated := call("GET", root+"member-candidates?q=群内人员", "", 200)["items"].([]any); len(repeated) != 0 {
		t.Fatal(repeated)
	}
	call("PUT", root+"members/candidate", `{"project_role":"system_admin","expected_version":0,"expected_project_role":"project_member"}`, 400)
	call("PUT", root+"members/candidate", `{"project_role":"project_admin","expected_version":0,"expected_project_role":"project_member","responsibility":"验收管理"}`, 200)
	target := identity.Principal{TenantKey: "tenant", AppID: "app", AccountID: "candidate", OpenID: "candidate"}
	if ok, err := roles.Check(ctx, target, "project.settings", "north"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := roles.Check(ctx, target, "project.delegate", "north"); err != nil || ok {
		t.Fatal("delegated admin gained delegation", ok, err)
	}
	actor = target
	call("PUT", root+"members/member", `{"project_role":"project_admin","expected_version":1,"expected_project_role":"project_member"}`, 404)
	actor = identity.Principal{TenantKey: "tenant", AppID: "app", AccountID: "manager", OpenID: "manager"}
	var grants int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM role_grants WHERE project_id='north' AND account_id='candidate' AND role='project_admin' AND revoked_at IS NULL").Scan(&grants); err != nil || grants != 1 {
		t.Fatal(grants, err)
	}
	call("PUT", root+"members/candidate", `{"project_role":"project_member","expected_version":1,"expected_project_role":"project_admin"}`, 200)
	if ok, err := roles.Check(ctx, target, "project.settings", "north"); err != nil || ok {
		t.Fatal("revoked admin retained settings", ok, err)
	}
	// System role management changes grants without bumping member profile version.
	call("POST", "/api/v1/system/role-grants", `{"accountId":"candidate","role":"project_admin","projectId":"north"}`, 201)
	call("PUT", root+"members/candidate", `{"project_role":"project_member","expected_version":2,"expected_project_role":"project_member","responsibility":"不能覆盖新授权"}`, 409)
	// Ordinary profile edits of a legacy read-only member keep its original access restriction.
	call("PUT", root+"members/reader", `{"project_role":"project_member","expected_project_role":"project_member","expected_version":1,"responsibility":"资料查阅"}`, 200)
	reader := identity.Principal{TenantKey: "tenant", AppID: "app", AccountID: "reader", OpenID: "reader"}
	if ok, err := roles.Check(ctx, reader, "project.comment", "north"); err != nil || ok {
		t.Fatal("reader upgraded through profile edit", ok, err)
	}
	call("PUT", root+"members/manager", `{"project_role":"project_member","expected_project_role":"project_admin","expected_version":1}`, 409)
	// A rejected member audit must also roll back the role grant and account permission change.
	exec(`ALTER TABLE audit_events ADD CONSTRAINT reject_member_change CHECK(action<>'project.member.update') NOT VALID`)
	var before int64
	if err := pool.QueryRow(ctx, "SELECT permission_version FROM accounts WHERE id='member'").Scan(&before); err != nil {
		t.Fatal(err)
	}
	call("PUT", root+"members/member", `{"project_role":"project_admin","expected_project_role":"project_member","expected_version":1}`, 503)
	var after int64
	if err := pool.QueryRow(ctx, "SELECT permission_version FROM accounts WHERE id='member'").Scan(&after); err != nil || before != after {
		t.Fatal(before, after, err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM role_grants WHERE account_id='member' AND project_id='north' AND revoked_at IS NULL").Scan(&grants); err != nil || grants != 0 {
		t.Fatal("grant leaked after rollback", grants, err)
	}
}
