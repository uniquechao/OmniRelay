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
