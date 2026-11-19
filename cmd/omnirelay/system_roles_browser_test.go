package main

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
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
	if os.Getenv("OMNIRELAY_PLAYWRIGHT_MODULE") == "" {
		t.Skip("未配置浏览器联调依赖")
	}
	dsn := os.Getenv("OMNIRELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("浏览器联调需要隔离 PostgreSQL")
	}
	ctx, stop := context.WithTimeout(t.Context(), 100*time.Second)
	defer stop()
	pool := backendTestPool(t, ctx, dsn)
	server := httptest.NewUnstartedServer(nil)
	defer server.Close()
	origin := "https://" + server.Listener.Addr().String()
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
	handler, workers, err := newBackendWithAccess(ctx, pool, cfg, &feishuFixture{}, roles)
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

	command := exec.CommandContext(ctx, "node", "--test", "tests/integration/system_roles_browser.test.mjs")
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
