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
	"github.com/uniquechao/OmniRelay/web"
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
	web.Register(mux)
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
			unnest(ARRAY['方宁','周言','林一帆','陈冬','顾清禾']) WITH ORDINALITY person(name,n)`)
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
	web.Register(mux)
	cmd := exec.CommandContext(t.Context(), "node", script)
	cmd.Dir = "../.."
	cmd.Env = append(os.Environ(), "H3_ORIGIN="+server.URL, "H3_PASSWORD="+password)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
