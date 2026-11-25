package access

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

func TestLocalAdministratorLogin(t *testing.T) {
	ctx, pool := testPool(t)
	roles := New(pool, "tenant", "app")
	const password = "isolated-fixture-password"
	id, err := roles.CreateLocalAdmin(ctx, password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := roles.CreateLocalAdmin(ctx, "different-fixture-password"); err == nil {
		t.Fatal("second create must not replace the account or its password")
	}
	p := identity.Principal{AccountID: id, TenantKey: "tenant", AppID: "app"}
	if ok, err := roles.Check(ctx, p, "account.manage", ""); err != nil || !ok {
		t.Fatalf("admin access %v %v", ok, err)
	}
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		for _, oid := range []string{"", "admin", id} {
			if _, err := identity.ResolveFeishuAccount(ctx, tx, "tenant", "app", oid); !errors.Is(err, identity.ErrNotAdmitted) {
				t.Fatalf("unexpected Feishu resolution for %q: %v", oid, err)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	const origin = "https://relay.example.test"
	auth, err := identity.New(pool, identity.Config{TenantKey: "tenant", AppID: "app", PublicOrigin: origin, Access: roles.WorkbenchAccess, Audit: roles.AuditIdentity})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	auth.Register(mux)
	auth.RegisterPasswords(mux)
	req := httptest.NewRequest("POST", origin+"/auth/login", strings.NewReader(`{"username":"admin","password":"`+password+`"}`))
	req.Header.Set("Origin", origin)
	req.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	mux.ServeHTTP(out, req)
	assertStatus(t, out, 204)
	req = httptest.NewRequest("GET", origin+"/api/v1/me", nil)
	cookie := out.Result().Cookies()[0]
	req.AddCookie(cookie)
	out = httptest.NewRecorder()
	mux.ServeHTTP(out, req)
	assertStatus(t, out, 200)
	if !strings.Contains(out.Body.String(), `"system_admin"`) || !strings.Contains(out.Body.String(), `"auth_method":"password"`) {
		t.Fatal("local session missing role or login method")
	}
	if err := roles.Bootstrap(ctx, "ou_existing_admin"); err != nil {
		t.Fatal(err)
	}
	operator := principal(t, ctx, pool, "tenant", "app", "ou_existing_admin")
	adminMux := testMux(roles, operator, p)
	page := request(adminMux, "GET", "/api/v1/system/user-page?limit=5&offset=0&q=本地", p, "")
	assertStatus(t, page, 200)
	if !strings.Contains(page.Body.String(), `"source":"本地"`) && !strings.Contains(page.Body.String(), `"source": "本地"`) {
		t.Fatal("missing local account source", page.Body.String())
	}
	if err := identity.ProvisionPassword(ctx, pool, "tenant", "app", id, "admin", "replacement-fixture-password"); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest("GET", origin+"/api/v1/me", nil)
	req.AddCookie(cookie)
	out = httptest.NewRecorder()
	mux.ServeHTTP(out, req)
	assertStatus(t, out, 401)
	if _, err := roles.setAccountActive(ctx, operator, id, false); err != nil {
		t.Fatal(err)
	}
	if ok, err := roles.Check(ctx, p, "account.manage", ""); err != nil || ok {
		t.Fatalf("disabled account access %v %v", ok, err)
	}
	if _, err := roles.setAccountActive(ctx, operator, id, true); err != nil {
		t.Fatal(err)
	}
}

func TestLocalAdminNameCollision(t *testing.T) {
	ctx, pool := testPool(t)
	user := addAccount(t, ctx, pool, "tenant", "app", "ou_other")
	if err := identity.ProvisionPassword(ctx, pool, "tenant", "app", user.AccountID, "admin", "existing-fixture-password"); err != nil {
		t.Fatal(err)
	}
	roles := New(pool, "tenant", "app")
	if _, err := roles.CreateLocalAdmin(ctx, "different-fixture-password"); err == nil {
		t.Fatal("overwrote existing username")
	}
	var accounts, grants int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM accounts WHERE open_id IS NULL),(SELECT count(*) FROM role_grants)`).Scan(&accounts, &grants); err != nil || accounts != 0 || grants != 0 {
		t.Fatalf("partial provisioning: %d accounts, %d grants: %v", accounts, grants, err)
	}
}
