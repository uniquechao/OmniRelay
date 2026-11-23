package project

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"

	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/webui"
)

func TestTaskTwoAccountBrowser(t *testing.T) {
	if os.Getenv("OMNIRELAY_PLAYWRIGHT_MODULE") == "" {
		t.Skip("双账号浏览器验收需要 Playwright")
	}
	pool := testProjectPool(t, "h3_task_browser")
	roles := access.New(pool, "tenant", "app")
	roles.ProjectAccess = access.LocalProjects{Pool: pool}
	var secret [24]byte
	rand.Read(secret[:])
	password := hex.EncodeToString(secret[:])
	for id, name := range map[string]string{"manager": "项目负责人", "second": "项目成员"} {
		if _, err := pool.Exec(t.Context(), "INSERT INTO accounts(id, tenant_key, app_id, open_id, name, active) VALUES($1,'tenant','app',$1,$2,true)", id, name); err != nil {
			t.Fatal(err)
		}
		if err := identity.ProvisionPassword(t.Context(), pool, "tenant", "app", id, id, password); err != nil {
			t.Fatal(err)
		}
	}
	if err := roles.Bootstrap(t.Context(), "manager"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	server := httptest.NewUnstartedServer(mux)
	server.StartTLS()
	defer server.Close()
	auth, err := identity.New(pool, identity.Config{PublicOrigin: server.URL, TenantKey: "tenant", AppID: "app", Access: roles.WorkbenchAccess})
	if err != nil {
		t.Fatal(err)
	}
	auth.Register(mux)
	auth.RegisterPasswords(mux)
	service := Service{pool: pool, roles: roles}
	service.Register(mux, auth.Authenticate)
	webui.Register(mux)
	cmd := exec.CommandContext(t.Context(), "node", "tests/integration/task_two_accounts.mjs")
	cmd.Dir = "../.."
	cmd.Env = append(os.Environ(), "H3_ORIGIN="+server.URL, "H3_PASSWORD="+password)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestProjectMemberTaskCRUDBrowser(t *testing.T) {
	runProjectBrowser(t, "member_task_crud_browser", "tests/integration/project_member_task_crud.mjs")
}
func TestTaskBoardReplicaBrowser(t *testing.T) {
	runProjectBrowser(t, "task_board_replica_browser", "tests/integration/task_board_replica.mjs")
}
func runProjectBrowser(t *testing.T, schema, script string) {
	t.Helper()
	if os.Getenv("OMNIRELAY_PLAYWRIGHT_MODULE") == "" {
		t.Skip("双账号浏览器验收需要 Playwright")
	}
	pool := testProjectPool(t, schema)
	if schema == "member_task_crud_browser" {
		_, err := pool.Exec(t.Context(), `INSERT INTO accounts(id,tenant_key,app_id,open_id,name)
			SELECT 'sample-'||n,'tenant','app','ou_sample_'||n,name FROM
			unnest(ARRAY['方宁','周言','林一帆','陈冬','顾清禾']) WITH ORDINALITY person(name,n);
			INSERT INTO directory_sources(tenant_key,app_id) VALUES ('tenant','app');
			INSERT INTO directory_sync_runs(tenant_key,app_id,trigger,state) VALUES ('tenant','app','initial','succeeded');
			UPDATE directory_sources SET visible=true,active_version=(SELECT id FROM directory_sync_runs);
			INSERT INTO departments(tenant_key,app_id,snapshot_version,id,name,path,has_children)
				SELECT 'tenant','app',active_version,d.id,d.name,ARRAY['研发中心',d.name],false FROM directory_sources
				CROSS JOIN (VALUES ('qa','质量组'),('platform','平台组')) AS d(id,name);
			INSERT INTO directory_people(tenant_key,app_id,snapshot_version,open_id,name,avatar_url,directory_status)
				SELECT 'tenant','app',active_version,'second','项目成员','','active' FROM directory_sources;
			INSERT INTO department_memberships(tenant_key,app_id,snapshot_version,department_id,open_id)
				SELECT tenant_key,app_id,snapshot_version,id,'second' FROM departments;
			INSERT INTO feishu_bot_configs(tenant_key,app_id,credentials_generation,enabled) VALUES('tenant','app',1,true);
			INSERT INTO feishu_chat_conversations(tenant_key,app_id,chat_id,kind)
				SELECT 'tenant','app',id,'group' FROM unnest(ARRAY['alpha','beta','hidden','elsewhere']) id;
			INSERT INTO feishu_chat_conversations(tenant_key,app_id,chat_id,kind,peer_open_id)
				VALUES ('tenant','app','private','p2p','manager');
			INSERT INTO project_chat_links(tenant_key,app_id,chat_id,project_id,members_initialized_at)
				SELECT 'tenant','app',chat_id,CASE WHEN chat_id='elsewhere' THEN 'other' ELSE 'h3' END,now()
				FROM feishu_chat_conversations WHERE kind='group';
			INSERT INTO feishu_drive_group_memberships(tenant_key,app_id,chat_id,generation,verified_at,members)
				SELECT 'tenant','app',chat_id,1,now(),CASE WHEN chat_id='hidden' THEN ARRAY['second'] ELSE ARRAY['manager'] END
				FROM feishu_chat_conversations WHERE kind='group';
			INSERT INTO feishu_chat_sync(tenant_key,app_id,chat_id,thread_id,source_blocked)
				VALUES ('tenant','app','alpha','hidden-thread',true);
			INSERT INTO feishu_chat_messages(tenant_key,app_id,message_id,chat_id,sender_id,sender_kind,sent_at,updated_at,deleted,thread_id)
				SELECT 'tenant','app',id,chat,sender,kind,'2026-01-01 12:00Z'::timestamptz + n * interval '1 hour',now(),deleted,thread_id
				FROM (VALUES ('older','alpha','ou_sample_1','user',0,false,''),('second-msg','alpha','second','user',1,false,''),
					('latest','beta','ou_sample_1','user',2,false,''),('gone','alpha','manager','user',3,true,''),
					('app-msg','alpha','manager','app',3,false,''),('thread-msg','alpha','manager','user',3,false,'hidden-thread'),
					('hidden-msg','hidden','manager','user',3,false,''),('other','elsewhere','manager','user',3,false,''),
					('private-msg','private','manager','user',3,false,'')) AS sample(id,chat,sender,kind,n,deleted,thread_id)`)
		if err != nil {
			t.Fatal(err)
		}
	}
	if schema == "task_board_replica_browser" {
		_, err := pool.Exec(t.Context(), `INSERT INTO feishu_bot_configs(tenant_key,app_id,credentials_generation,enabled) VALUES('tenant','app',1,true);
          INSERT INTO feishu_chat_conversations(tenant_key,app_id,chat_id,kind) SELECT 'tenant','app',x,'group' FROM unnest(ARRAY['oc_a','oc_b','oc_other']) x;
          INSERT INTO project_chat_links(tenant_key,app_id,chat_id,project_id) VALUES ('tenant','app','oc_a','board-test'),('tenant','app','oc_b','board-test'),('tenant','app','oc_other','elsewhere');
          INSERT INTO feishu_drive_group_memberships(tenant_key,app_id,chat_id,generation,verified_at,members,member_names) VALUES
          ('tenant','app','oc_a',1,now(),ARRAY['ou_wang'],'{"ou_wang":"王甲"}'),
          ('tenant','app','oc_b',1,now(),ARRAY['ou_wang','ou_wang2'],'{"ou_wang":"王甲","ou_wang2":"王乙"}'),
          ('tenant','app','oc_other',1,now(),ARRAY['ou_out'],'{"ou_out":"王外"}');`)
		if err != nil {
			t.Fatal(err)
		}
	}
	roles := access.New(pool, "tenant", "app")
	roles.ProjectAccess = access.LocalProjects{Pool: pool}
	var secret [24]byte
	rand.Read(secret[:])
	password := hex.EncodeToString(secret[:])
	for id, name := range map[string]string{"manager": "项目负责人", "second": "项目成员"} {
		if _, err := pool.Exec(t.Context(), "INSERT INTO accounts(id, tenant_key, app_id, open_id, name, active) VALUES($1,'tenant','app',$1,$2,true)", id, name); err != nil {
			t.Fatal(err)
		}
		if err := identity.ProvisionPassword(t.Context(), pool, "tenant", "app", id, id, password); err != nil {
			t.Fatal(err)
		}
	}
	if err := roles.Bootstrap(t.Context(), "manager"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	server := httptest.NewUnstartedServer(mux)
	server.StartTLS()
	defer server.Close()
	auth, err := identity.New(pool, identity.Config{PublicOrigin: server.URL, TenantKey: "tenant", AppID: "app", Access: roles.WorkbenchAccess})
	if err != nil {
		t.Fatal(err)
	}
	auth.Register(mux)
	auth.RegisterPasswords(mux)
	service := Service{pool: pool, roles: roles}
	service.Register(mux, auth.Authenticate)
	webui.Register(mux)
	cmd := exec.CommandContext(t.Context(), "node", script)
	cmd.Dir = "../.."
	cmd.Env = append(os.Environ(), "H3_ORIGIN="+server.URL, "H3_PASSWORD="+password)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
