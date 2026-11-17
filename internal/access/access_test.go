package access

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

func TestAuthorizationUsesCurrentAccountAndProjectMembership(t *testing.T) {
	ctx, pool := testPool(t)
	service := New(pool, "tenant", "app")
	if err := service.Bootstrap(ctx, "ou_admin"); err != nil {
		t.Fatal(err)
	}
	admin := principal(t, ctx, pool, "tenant", "app", "ou_admin")
	member := addAccount(t, ctx, pool, "tenant", "app", "ou_member")
	foreign := addAccount(t, ctx, pool, "foreign", "app", "ou_foreign")
	service.ProjectAccess = testProjects{
		admin.AccountID:  {Member: true},
		member.AccountID: {Member: true, Manager: true},
	}

	cases := []struct {
		name      string
		actor     identity.Principal
		action    string
		projectID string
		want      bool
	}{
		{"admin can manage accounts", admin, "account.manage", "", true},
		{"member cannot manage accounts", member, "account.manage", "", false},
		{"foreign tenant cannot read directory", foreign, "directory.read", "", false},
		{"admin role alone cannot manage project", admin, "project.settings", "project", false},
		{"trusted project member can read assigned project", admin, "project.read", "project", true},
		{"trusted manager can manage assigned project", member, "project.settings", "project", true},
		{"member cannot read different project", member, "project.read", "other", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			allowed, err := service.Check(ctx, tc.actor, tc.action, tc.projectID)
			if err != nil || allowed != tc.want {
				t.Fatalf("allowed = %v, error = %v; want allowed = %v", allowed, err, tc.want)
			}
		})
	}
	service.ProjectAccess = nil
	allowed, err := service.Check(ctx, admin, "project.read", "project")
	if err != nil || allowed {
		t.Fatalf("missing provider: allowed = %v, error = %v", allowed, err)
	}
}

func TestRoleRevocationAndDisableProtectLastAdmin(t *testing.T) {
	ctx, pool := testPool(t)
	service := New(pool, "tenant", "app")
	if err := service.Bootstrap(ctx, "ou_admin"); err != nil {
		t.Fatal(err)
	}
	admin := principal(t, ctx, pool, "tenant", "app", "ou_admin")
	second := addAccount(t, ctx, pool, "tenant", "app", "ou_second")
	foreign := addAccount(t, ctx, pool, "tenant", "foreign-app", "ou_foreign")
	mux := testMux(service, admin, second)
	accountPath := "/api/v1/system/accounts/"
	grantPath := "/api/v1/system/role-grants"
	var bootstrapGrant string
	if err := pool.QueryRow(ctx, "SELECT id FROM role_grants WHERE account_id=$1", admin.AccountID).Scan(&bootstrapGrant); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, request(mux, http.MethodDelete, grantPath+"/"+bootstrapGrant, admin, ""), 409)

	assertStatus(t, request(mux, http.MethodPatch, accountPath+admin.AccountID, admin, `{"active":false}`), 409)
	assertStatus(t, request(mux, http.MethodPatch, accountPath+foreign.AccountID, admin, `{"active":false}`), 404)
	grantBody := fmt.Sprintf(`{"accountId":%q,"role":"system_admin"}`, second.AccountID)
	assertStatus(t, request(mux, http.MethodPost, grantPath, second, grantBody), 403)
	assertStatus(t, request(mux, http.MethodPost, grantPath, admin, grantBody), 201)
	if _, err := pool.Exec(ctx, `INSERT INTO web_sessions (token_digest,account_id,expires_at,idle_expires_at)
		VALUES (decode(repeat('00',32),'hex'), $1,now()+interval '1 hour',now()+interval '5 minutes'),
		       (decode(repeat('01',32),'hex'), $2,now()+interval '1 hour',now()+interval '5 minutes')`, admin.AccountID, second.AccountID); err != nil {
		t.Fatal(err)
	}

	statuses := make(chan int, 2)
	for _, actor := range []identity.Principal{admin, second} {
		go func() {
			statuses <- request(mux, http.MethodPatch, accountPath+actor.AccountID, actor, `{"active":false}`).Code
		}()
	}
	first, next := <-statuses, <-statuses
	if (first != 200 || next != 409) && (first != 409 || next != 200) {
		t.Fatalf("competing disables = %d, %d; want 200 and 409", first, next)
	}

	var enabled, events int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM accounts WHERE active AND tenant_key='tenant' AND app_id='app'").Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE tenant_key='tenant' AND app_id='app'").Scan(&events); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 || events != 3 {
		t.Fatalf("enabled = %d, audit events = %d; want 1 and 3", enabled, events)
	}
	var inactiveSessions, sessions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE NOT a.active), count(*)
		FROM web_sessions s JOIN accounts a ON a.id=s.account_id`).Scan(&inactiveSessions, &sessions); err != nil {
		t.Fatal(err)
	}
	if inactiveSessions != 0 || sessions != 1 {
		t.Fatalf("inactive sessions = %d, total sessions = %d; want 0 and 1", inactiveSessions, sessions)
	}
	var remainingAdmin identity.Principal
	for _, actor := range []identity.Principal{admin, second} {
		var active bool
		if err := pool.QueryRow(ctx, "SELECT active FROM accounts WHERE id=$1", actor.AccountID).Scan(&active); err != nil {
			t.Fatal(err)
		}
		if active {
			remainingAdmin = actor
			continue
		}
		allowed, err := service.Check(ctx, actor, "account.read", "")
		if err != nil || allowed {
			t.Fatalf("disabled account: allowed = %v, error = %v", allowed, err)
		}
	}
	log := "/api/v1/system/audit-events?limit=1"
	firstPage := request(mux, http.MethodGet, log, remainingAdmin, "")
	assertStatus(t, firstPage, 200)
	var page struct {
		Items      []AuditEvent
		NextCursor string
	}
	if err := json.Unmarshal(firstPage.Body.Bytes(), &page); err != nil || len(page.Items) != 1 || page.NextCursor == "" || page.Items[0].Action != "account.disable" {
		t.Fatalf("unexpected audit first page: %+v, error = %v", page, err)
	}
	assertStatus(t, request(mux, http.MethodGet, log+"&cursor="+page.NextCursor, remainingAdmin, ""), 200)
	assertStatus(t, request(mux, http.MethodGet, "/api/v1/system/accounts?limit=1", remainingAdmin, ""), 200)
	assertStatus(t, request(mux, http.MethodGet, grantPath+"?limit=1", remainingAdmin, ""), 200)

	if err := service.Bootstrap(ctx, "ou_never_created"); err != nil {
		t.Fatal(err)
	}
	var created int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM accounts WHERE open_id='ou_never_created'").Scan(&created); err != nil || created != 0 {
		t.Fatalf("repeat bootstrap: created = %d, error = %v", created, err)
	}
}

func TestProjectDelegationStopsWhenMembershipEnds(t *testing.T) {
	ctx, pool := testPool(t)
	service := New(pool, "tenant", "app")
	manager := addAccount(t, ctx, pool, "tenant", "app", "ou_manager")
	member := addAccount(t, ctx, pool, "tenant", "app", "ou_member")
	nonmember := addAccount(t, ctx, pool, "tenant", "app", "ou_nonmember")
	projects := testProjects{
		manager.AccountID: {Member: true, Manager: true},
		member.AccountID:  {Member: true, MembershipGeneration: "membership-1"},
	}
	service.ProjectAccess = projects
	mux := testMux(service, manager, member)
	path := "/api/v1/system/role-grants"
	payload := func(accountID string) string {
		return fmt.Sprintf(`{"accountId":%q,"role":"project_admin","projectId":"project"}`, accountID)
	}
	assertStatus(t, request(mux, http.MethodPost, path, manager, payload(nonmember.AccountID)), 404)
	assertStatus(t, request(mux, http.MethodPost, path, member, payload(member.AccountID)), 403)
	grant := request(mux, http.MethodPost, path, manager, payload(member.AccountID))
	assertStatus(t, grant, 201)

	allowed, err := service.Check(ctx, member, "project.settings", "project")
	if err != nil || !allowed {
		t.Fatalf("current member: allowed = %v, error = %v", allowed, err)
	}
	delete(projects, member.AccountID)
	allowed, err = service.Check(ctx, member, "project.settings", "project")
	if err != nil || allowed {
		t.Fatalf("former member: allowed = %v, error = %v", allowed, err)
	}
	var response struct{ ID string }
	if err := json.Unmarshal(grant.Body.Bytes(), &response); err != nil || response.ID == "" {
		t.Fatalf("missing created grant ID: %v", err)
	}
	projects[member.AccountID] = ProjectAccess{Member: true, MembershipGeneration: "membership-2"}
	allowed, err = service.Check(ctx, member, "project.settings", "project")
	if err != nil || allowed {
		t.Fatalf("rejoined member before delegation: allowed = %v, error = %v", allowed, err)
	}
	assertStatus(t, request(mux, http.MethodPost, path, manager, payload(member.AccountID)), 201)
	assertStatus(t, request(mux, http.MethodDelete, path+"/"+response.ID, manager, ""), 404)
	allowed, err = service.Check(ctx, member, "project.settings", "project")
	if err != nil || !allowed {
		t.Fatalf("rejoined member after new delegation: allowed = %v, error = %v", allowed, err)
	}
}

type testProjects map[string]ProjectAccess

func (p testProjects) ResolveProjectAccess(_ context.Context, account identity.Principal, projectID string) (ProjectAccess, error) {
	if projectID != "project" {
		return ProjectAccess{}, nil
	}
	return p[account.AccountID], nil
}

func testPool(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("OMNIRELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("OMNIRELAY_TEST_DATABASE_URL is required for PostgreSQL access tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	control, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(control.Close)
	schema := fmt.Sprintf("access_test_%d", time.Now().UnixNano())
	if _, err := control.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := control.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, name := range []string{"0001_identity.sql", "0002_access.sql"} {
		migration, err := os.ReadFile("../../migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	return ctx, pool
}

func addAccount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenant, app, openID string) identity.Principal {
	t.Helper()
	if _, err := pool.Exec(ctx, "INSERT INTO accounts (tenant_key,app_id,open_id) VALUES ($1,$2,$3)", tenant, app, openID); err != nil {
		t.Fatal(err)
	}
	return principal(t, ctx, pool, tenant, app, openID)
}

func principal(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenant, app, openID string) identity.Principal {
	t.Helper()
	result := identity.Principal{TenantKey: tenant, AppID: app, OpenID: openID}
	if err := pool.QueryRow(ctx, "SELECT id FROM accounts WHERE tenant_key=$1 AND app_id=$2 AND open_id=$3", tenant, app, openID).Scan(&result.AccountID); err != nil {
		t.Fatal(err)
	}
	return result
}

func testMux(service *Service, actors ...identity.Principal) *http.ServeMux {
	mux := http.NewServeMux()
	service.Register(mux, func(w http.ResponseWriter, r *http.Request, _ bool) (identity.Principal, bool) {
		for _, actor := range actors {
			if actor.AccountID == r.Header.Get("Test-Account") {
				return actor, true
			}
		}
		w.WriteHeader(http.StatusUnauthorized)
		return identity.Principal{}, false
	})
	return mux
}

func request(mux http.Handler, method, path string, actor identity.Principal, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Test-Account", actor.AccountID)
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)
	return response
}

func assertStatus(t *testing.T, response *httptest.ResponseRecorder, status int) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d; want %d, body %s", response.Code, status, response.Body)
	}
}

func TestPlatformRolesStayInTheirScope(t *testing.T) {
	tests := []struct {
		role   string
		action string
		allow  bool
	}{
		{"system_admin", "role.manage", true},
		{"system_admin", "audit.read", true},
		{"system_admin", "system.operate", false},
		{"system_operator", "system.operate", true},
		{"system_operator", "directory.sync", false},
		{"audit_viewer", "audit.read", true},
		{"audit_viewer", "account.read", false},
		{"project_admin", "role.manage", false},
		{"system_admin", "project.settings", false},
	}
	for _, tc := range tests {
		t.Run(tc.role+"/"+tc.action, func(t *testing.T) {
			if got := platformAllowed([]string{tc.role}, tc.action); got != tc.allow {
				t.Errorf("allowed = %v; want %v", got, tc.allow)
			}
		})
	}
}

func TestHTTPWriteRejectsUnknownFieldsAndOversizedBody(t *testing.T) {
	mux := testMux(New(nil, "tenant", "app"), identity.Principal{AccountID: "admin"})
	admin := identity.Principal{AccountID: "admin"}
	path := "/api/v1/system/accounts/target"
	assertStatus(t, request(mux, http.MethodPatch, path, admin, `{"active":true,"role":"system_admin"}`), 400)
	assertStatus(t, request(mux, http.MethodPatch, path, admin, `{"active":true} {"active":false}`), 400)
	assertStatus(t, request(mux, http.MethodPatch, path, admin, `{"active":null}`), 400)
	assertStatus(t, request(mux, http.MethodPatch, path, admin, strings.Repeat(" ", 5000)), 413)
}

func TestAccountPreauthorizationRequiresAdminAndPreservesDisabledAccount(t *testing.T) {
	ctx, pool := testPool(t)
	service := New(pool, "tenant", "app")
	if err := service.Bootstrap(ctx, "ou_admin"); err != nil {
		t.Fatal(err)
	}
	admin := principal(t, ctx, pool, "tenant", "app", "ou_admin")
	user := addAccount(t, ctx, pool, "tenant", "app", "ou_user")
	foreign := addAccount(t, ctx, pool, "tenant", "other-app", "ou_candidate")
	mux := testMux(service, admin, user, foreign)
	path := "/api/v1/system/accounts"
	body := `{"openId":"ou_candidate","name":"Candidate"}`
	assertStatus(t, request(mux, http.MethodPost, path, user, body), 403)
	assertStatus(t, request(mux, http.MethodPost, path, foreign, body), 403)
	created := request(mux, http.MethodPost, path, admin, body)
	assertStatus(t, created, 201)
	var account Account
	if err := json.Unmarshal(created.Body.Bytes(), &account); err != nil || !account.Active || account.Name != "Candidate" || account.ID == foreign.AccountID {
		t.Fatalf("invalid preauthorized account: %+v, error = %v", account, err)
	}
	candidate := principal(t, ctx, pool, "tenant", "app", "ou_candidate")
	if candidate.AccountID != account.ID {
		t.Fatalf("candidate account = %s; want %s", candidate.AccountID, account.ID)
	}
	allowed, err := service.Check(ctx, candidate, "account.manage", "")
	if err != nil || allowed {
		t.Fatalf("preauthorization grants no role: allowed = %v, error = %v", allowed, err)
	}

	assertStatus(t, request(mux, http.MethodPatch, path+"/"+account.ID, admin, `{"active":false}`), 200)
	assertStatus(t, request(mux, http.MethodPost, path, admin, `{"openId":"ou_candidate","name":"Changed"}`), 409)
	var active bool
	var name string
	if err := pool.QueryRow(ctx, "SELECT active,name FROM accounts WHERE id=$1", account.ID).Scan(&active, &name); err != nil {
		t.Fatal(err)
	}
	if active || name != "Candidate" {
		t.Fatalf("duplicate preauthorization changed account: active=%v, name=%q", active, name)
	}
	var events int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE tenant_key='tenant' AND app_id='app'
		AND actor_account_id=$1 AND action='account.preauthorize' AND resource_id=$2`, admin.AccountID, account.ID).Scan(&events); err != nil || events != 1 {
		t.Fatalf("preauthorization audit count = %d, error = %v", events, err)
	}
}

func TestDirectorySyncAuthorizesAgainstSerializedRoleChanges(t *testing.T) {
	ctx, pool := testPool(t)
	service := New(pool, "tenant", "app")
	if err := service.Bootstrap(ctx, "ou_admin"); err != nil {
		t.Fatal(err)
	}
	admin := principal(t, ctx, pool, "tenant", "app", "ou_admin")
	operator := addAccount(t, ctx, pool, "tenant", "app", "ou_operator")
	grant, err := service.grantRole(ctx, admin, grantRequest{AccountID: operator.AccountID, Role: "system_admin"})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := service.AuthorizeDirectorySync(ctx, tx, operator); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	err = service.revokeRole(short, admin, grant.ID)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("role revoke while directory authorization is locked: %v; want timeout", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.revokeRole(ctx, admin, grant.ID); err != nil {
		t.Fatal(err)
	}
	tx, err = pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := service.AuthorizeDirectorySync(ctx, tx, operator); !errors.Is(err, ErrForbidden) {
		t.Fatalf("directory authorization after revoke: %v; want forbidden", err)
	}
}
