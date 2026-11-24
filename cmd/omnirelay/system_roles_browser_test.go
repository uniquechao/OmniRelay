package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

type acceptanceProjectAuthority struct{}

func (acceptanceProjectAuthority) ResolveProjectAccess(_ context.Context, p identity.Principal, projectID string) (access.ProjectAccess, error) {
	if projectID == "unavailable-project" {
		return access.ProjectAccess{}, errors.New("fixture authority unavailable")
	}
	if projectID != "acceptance-project" {
		return access.ProjectAccess{}, nil
	}
	switch p.OpenID {
	case "ou_project_operator":
		return access.ProjectAccess{Member: true, Manager: true, MembershipGeneration: "operator-generation"}, nil
	case "ou_historical":
		return access.ProjectAccess{Member: true, CanAssign: true, MembershipGeneration: "member-generation"}, nil
	default:
		return access.ProjectAccess{}, nil
	}
}

func (authority acceptanceProjectAuthority) HasProjectAccess(ctx context.Context, p identity.Principal) (bool, error) {
	project, err := authority.ResolveProjectAccess(ctx, p, "acceptance-project")
	return project.Member, err
}

func TestSystemRolesBrowser(t *testing.T) {
	runSystemBrowser(t, "system_roles_browser.test.mjs")
}

func TestCandidateRoleEntry(t *testing.T) {
	runSystemBrowser(t, "candidate_roles_browser.test.mjs")
}

func TestAccountGrantsBrowser(t *testing.T) {
	dir, err := filepath.Abs("../../internal/webui/dist")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OMNIRELAY_PREVIEW_DIR", dir)
	runSystemBrowser(t, "account_grants_browser.test.mjs")
}

func TestReactAuditBrowser(t *testing.T) {
	dir, err := filepath.Abs("../../internal/webui/dist")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OMNIRELAY_PREVIEW_DIR", dir)
	runSystemBrowser(t, "react_audit_browser.test.mjs")
}

func runSystemBrowser(t *testing.T, suite string) {
	t.Helper()
	if os.Getenv("OMNIRELAY_PLAYWRIGHT_MODULE") == "" {
		t.Skip("未配置浏览器联调依赖")
	}
	dsn := os.Getenv("OMNIRELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("浏览器联调需要隔离 PostgreSQL")
	}
	ctx, stop := context.WithTimeout(t.Context(), 3*time.Minute)
	defer stop()
	pool := backendTestPool(t, ctx, dsn)
	server := httptest.NewUnstartedServer(nil)
	defer server.Close()
	origin := "https://" + server.Listener.Addr().String()
	if suite == "dshbot_browser.test.mjs" {
		startNativeDebugFixture(t, origin)
	}
	cfg := configuration{
		publicOrigin: origin, appID: "cli_fixture", appSecret: "fixture-only",
		tenantKey: "tenant_fixture", bootstrap: "ou_admin", syncInterval: time.Hour,
		credentialsGeneration: 1, botHeartbeatTTL: 30 * time.Second,
	}
	roles := access.New(pool, cfg.tenantKey, cfg.appID)
	roles.ProjectAccess = acceptanceProjectAuthority{}
	if err := roles.Bootstrap(ctx, cfg.bootstrap); err != nil {
		t.Fatal(err)
	}
	for _, account := range []struct{ openID, username, role string }{
		{"ou_admin", "admin", ""},
		{"ou_operator", "operator", access.RoleSystemOperator},
		{"ou_auditor", "auditor", access.RoleAuditViewer},
		{"ou_historical", "historical", ""},
		{"ou_project_operator", "project_operator", access.RoleSystemOperator},
	} {
		var id string
		err := pool.QueryRow(ctx, `INSERT INTO accounts (tenant_key,app_id,open_id,name)
			VALUES ($1,$2,$3,$4) ON CONFLICT (tenant_key,app_id,open_id)
			DO UPDATE SET name=EXCLUDED.name RETURNING id`, cfg.tenantKey, cfg.appID, account.openID, account.username).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		if account.role != "" {
			_, err = pool.Exec(ctx, `INSERT INTO role_grants (tenant_key,app_id,account_id,role,source,granted_by)
				SELECT $1,$2,$3,$4,'platform_admin',id FROM accounts WHERE open_id=$5`,
				cfg.tenantKey, cfg.appID, id, account.role, cfg.bootstrap)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := identity.ProvisionPassword(ctx, pool, cfg.tenantKey, cfg.appID, id, account.username, "fixture-password-only"); err != nil {
			t.Fatal(err)
		}
	}
	if suite == "account_grants_browser.test.mjs" || suite == "candidate_roles_browser.test.mjs" {
		roles.ProjectAccess = access.LocalProjects{Pool: pool}
		_, err := pool.Exec(ctx, `INSERT INTO directory_sources(tenant_key,app_id) VALUES ('tenant_fixture','cli_fixture');
        INSERT INTO directory_sync_runs(id,tenant_key,app_id,trigger,state) OVERRIDING SYSTEM VALUE VALUES(17,'tenant_fixture','cli_fixture','initial','succeeded');
        UPDATE directory_sources SET visible=true,active_version=17,next_run_at=now()+interval '1 day' WHERE tenant_key='tenant_fixture' AND app_id='cli_fixture';
        INSERT INTO directory_people (tenant_key,app_id,snapshot_version,open_id,name,avatar_url,directory_status,unactivated)
        VALUES ('tenant_fixture','cli_fixture',17,'ou_new','待开通成员','','inactive',true);`)
		if err != nil {
			t.Fatal(err)
		}
		_, err = pool.Exec(ctx, `INSERT INTO project_members(tenant_key,app_id,project_id,account_id,member_id,role)
            SELECT tenant_key,app_id,'acceptance-project',id,id,CASE WHEN open_id='ou_project_operator' THEN 'manager' ELSE 'member' END
            FROM accounts WHERE open_id IN ('ou_project_operator','ou_historical')`)
		if err != nil {
			t.Fatal(err)
		}
	}
	if suite == "react_audit_browser.test.mjs" {
		// 登录本身还会生成一条审计记录。
		_, err := pool.Exec(ctx, `INSERT INTO audit_events
            (tenant_key,app_id,actor_account_id,action,resource_type,resource_id,result,change_version,created_at)
            SELECT tenant_key,app_id,id,'account.enable','account','样例-'||i,'success',1,
                '2025-01-01'::timestamptz FROM accounts CROSS JOIN
                generate_series(1,10000-(SELECT count(*) FROM audit_events)) i WHERE open_id=$1`, cfg.bootstrap)
		if err != nil {
			t.Fatal(err)
		}
	}
	fixture := &feishuFixture{}
	var botInfoCalls atomic.Int32
	transport := botTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/open-apis/bot/v3/info" {
			return fixture.RoundTrip(r)
		}
		body := `{"code":99991672}`
		if botInfoCalls.Add(1) == 1 {
			body = `{"code":0,"bot":{"app_name":"Fixture Bot","open_id":"ou_bot","activate_status":2}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	handler, workers, err := newBackendWithAccess(ctx, pool, cfg, transport, roles)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.StartTLS()
	done := make(chan struct{})
	go func() {
		defer close(done)
		workers.Run(ctx)
	}()
	defer func() {
		stop()
		<-done
	}()

	command := exec.CommandContext(ctx, "node", "--test", "tests/integration/"+suite)
	command.Dir = "../.."
	command.Env = append(os.Environ(), "OMNIRELAY_BROWSER_ORIGIN="+origin)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("系统角色浏览器联调失败 %v\n%s", err, output)
	}
	t.Logf("%s", output)

	var automaticGrants int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM role_grants WHERE role='system_user'").Scan(&automaticGrants); err != nil || automaticGrants != 0 {
		t.Fatalf("自动角色写入了授权记录，数量 %d，错误 %v", automaticGrants, err)
	}
}
