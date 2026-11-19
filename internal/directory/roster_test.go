package directory

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

type rosterReply struct {
	root, members string
	lookup        func(http.ResponseWriter, *http.Request)
}

const rosterMembers = `[{"open_id":"ou_admin","name":"Admin","status":{"is_activated":true}},
	{"open_id":"ou_member","name":"Member","status":{"is_activated":true}},
	{"open_id":"ou_unprovisioned","name":"Unprovisioned","status":{"is_activated":true}}]`

func setupRosterSync(t *testing.T, pool *pgxpool.Pool, first rosterReply) (*Service, *atomic.Pointer[rosterReply]) {
	t.Helper()
	current := &atomic.Pointer[rosterReply]{}
	current.Store(&first)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := current.Load()
		switch r.URL.Path {
		case "/oauth/v3/token":
			fmt.Fprint(w, `{"code":0,"access_token":"token"}`)
			return
		case contacts + "/scopes":
			fmt.Fprintf(w, `{"code":0,"data":{"department_ids":[%q],"has_more":false}}`, reply.root)
		case contacts + "/departments/team":
			fmt.Fprint(w, `{"code":0,"data":{"department":{"open_department_id":"team","parent_department_id":"0","name":"Team"}}}`)
		case contacts + "/departments/team/children":
			fmt.Fprint(w, `{"code":0,"data":{"items":[],"has_more":false}}`)
		case contacts + "/departments/0/children":
			fmt.Fprint(w, `{"code":0,"data":{"items":[{"open_department_id":"team","parent_department_id":"0","name":"Team"}],"has_more":false}}`)
		case contacts + "/users/find_by_department":
			if r.URL.Query().Get("department_id") == "0" {
				fmt.Fprint(w, `{"code":0,"data":{"items":[],"has_more":false}}`)
			} else if reply.members == "" {
				fmt.Fprint(w, `{"code":0,"data":{"has_more":true}}`)
			} else {
				fmt.Fprintf(w, `{"code":0,"data":{"items":%s,"has_more":false}}`, reply.members)
			}
		default:
			if !strings.HasPrefix(r.URL.Path, contacts+"/users/ou_") || r.URL.Query().Get("user_id_type") != "open_id" || reply.lookup == nil {
				t.Errorf("unexpected provider request %s", r.URL)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			reply.lookup(w, r)
		}
	}))
	t.Cleanup(server.Close)
	service, err := New(pool, Config{TenantKey: "tenant", AppID: "app", AppSecret: "secret",
		BaseURL: server.URL, HTTPClient: &http.Client{Timeout: 125 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	return service, current
}

func missingRosterUser(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusBadRequest)
	fmt.Fprintf(w, `{"code":40007,"msg":"user %s does not exist"}`, strings.TrimPrefix(r.URL.Path, contacts+"/users/"))
}

func claimRosterTestRun(t *testing.T, service *Service) claim {
	t.Helper()
	ctx := t.Context()
	if _, err := service.db.Exec(ctx, `UPDATE directory_sources SET next_run_at=clock_timestamp()`); err != nil {
		t.Fatal(err)
	}
	job, err := service.claimNext(ctx)
	if err != nil || job.id == 0 {
		t.Fatalf("claim = %+v, %v", job, err)
	}
	return job
}

func runRosterTestSync(t *testing.T, service *Service, expected string) int64 {
	t.Helper()
	job := claimRosterTestRun(t, service)
	service.runClaim(t.Context(), job)
	var state, reason string
	err := service.db.QueryRow(t.Context(), `SELECT state,failure_reason FROM directory_sync_runs WHERE id=$1`, job.id).Scan(&state, &reason)
	if err != nil || state != expected {
		t.Fatalf("batch %d = %s, %s, %v; expected %s", job.id, state, reason, err, expected)
	}
	return job.id
}

func rosterExec(t *testing.T, pool *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func assertRosterAccount(t *testing.T, pool *pgxpool.Pool, openID string, active, departed bool, version, sessions, disables int) {
	t.Helper()
	var gotActive, gotDeparted bool
	var gotVersion, gotSessions, gotDisables int
	err := pool.QueryRow(t.Context(), `SELECT a.active, r.resigned_at IS NOT NULL, a.permission_version,
		(SELECT count(*) FROM web_sessions WHERE account_id=a.id),
		(SELECT count(*) FROM audit_events WHERE resource_id=a.id AND action='account.disable')
		FROM accounts a LEFT JOIN directory_roster r USING(tenant_key,app_id,open_id)
		WHERE a.tenant_key='tenant' AND a.app_id='app' AND a.open_id=$1`, openID).
		Scan(&gotActive, &gotDeparted, &gotVersion, &gotSessions, &gotDisables)
	if err != nil || gotActive != active || gotDeparted != departed || gotVersion != version || gotSessions != sessions || gotDisables != disables {
		t.Fatalf("%s active=%v departed=%v version=%d sessions=%d disables=%d, %v", openID, gotActive, gotDeparted, gotVersion, gotSessions, gotDisables, err)
	}
}

func TestRosterFullSyncReclaimsAccountsAndProtectsLastAdmin(t *testing.T) {
	pool := directoryTestPool(t)
	ctx := t.Context()
	service, provider := setupRosterSync(t, pool, rosterReply{root: "0", members: rosterMembers})
	if err := service.access.Bootstrap(ctx, "ou_admin"); err != nil {
		t.Fatal(err)
	}
	rosterExec(t, pool, `INSERT INTO accounts(id,tenant_key,app_id,open_id) VALUES
		('member','tenant','app','ou_member'),('other_tenant','other','app','ou_member'),
		('other_app','tenant','other','ou_member'),('successor','tenant','app','ou_successor')`)
	rosterExec(t, pool, `INSERT INTO web_sessions (account_id,token_digest,expires_at,idle_expires_at)
		SELECT a.id,sha256(convert_to(a.id || n::text,'UTF8')), now()+interval '1 hour', now()+interval '1 hour'
		FROM accounts a CROSS JOIN generate_series(1,2) n WHERE a.id='member' OR a.open_id='ou_admin'`)
	rosterExec(t, pool, `INSERT INTO role_grants(tenant_key,app_id,account_id,role,source,granted_by)
		SELECT tenant_key,app_id,'member','audit_viewer','platform_admin',id FROM accounts WHERE open_id='ou_admin'`)
	initial := runRosterTestSync(t, service, "succeeded")
	assertRosterAccount(t, pool, "ou_member", true, false, 0, 2, 0)

	provider.Store(&rosterReply{root: "0", members: `[]`, lookup: func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/ou_unprovisioned") {
			fmt.Fprint(w, `{"code":0,"data":{"user":{"open_id":"ou_unprovisioned","status":{"is_resigned":true}}}}`)
			return
		}
		missingRosterUser(w, r)
	}})
	stale := claimRosterTestRun(t, service)
	snapshot, err := service.collect(ctx, func(scope, int, int) error { return nil }, service.verifyDepartures)
	if err != nil || len(snapshot.Departures) != 3 {
		t.Fatalf("collect departures = %+v, %v", snapshot.Departures, err)
	}
	rosterExec(t, pool, `UPDATE directory_sync_runs SET lease_until=clock_timestamp() WHERE id=$1`, stale.id)
	if err := service.publish(ctx, stale, snapshot); !errors.Is(err, errLostClaim) {
		t.Fatalf("expired publish = %v", err)
	}
	assertRosterAccount(t, pool, "ou_member", true, false, 0, 2, 0)
	if _, err := service.claimNext(ctx); err != nil {
		t.Fatal(err)
	}

	// Fail after the account and its sessions have been updated.
	rosterExec(t, pool, `CREATE FUNCTION reject_disable_audit() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.action='account.disable' THEN RAISE EXCEPTION 'fixture audit failure'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER reject_disable_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_disable_audit()`)
	runRosterTestSync(t, service, "failed")
	assertRosterAccount(t, pool, "ou_member", true, false, 0, 2, 0)
	var version int64
	if err := pool.QueryRow(ctx, `SELECT active_version FROM directory_sources`).Scan(&version); err != nil || version != initial {
		t.Fatalf("rollback snapshot = %d, %v", version, err)
	}
	rosterExec(t, pool, `DROP TRIGGER reject_disable_audit ON audit_events`)
	confirmed := runRosterTestSync(t, service, "succeeded")
	assertRosterAccount(t, pool, "ou_member", false, true, 1, 0, 1)
	assertRosterAccount(t, pool, "ou_admin", true, true, 1, 2, 0)

	runRosterTestSync(t, service, "succeeded")
	assertRosterAccount(t, pool, "ou_member", false, true, 1, 0, 1)
	var resigns, protected, foreign, unprovisioned int
	err = pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM audit_events WHERE directory_sync_run_id=$1 AND action='directory.resign' AND actor_account_id IS NULL),
		(SELECT count(*) FROM audit_events WHERE action='account.disable_protected' AND result='protected'),
		(SELECT count(*) FROM accounts WHERE id IN ('other_tenant','other_app') AND active AND permission_version=0),
		(SELECT count(*) FROM accounts WHERE open_id='ou_unprovisioned')`, confirmed).Scan(&resigns, &protected, &foreign, &unprovisioned)
	if err != nil || resigns != 3 || protected != 1 || foreign != 2 || unprovisioned != 0 {
		t.Fatalf("resigns=%d protected=%d foreign=%d unprovisioned=%d, %v", resigns, protected, foreign, unprovisioned, err)
	}

	rosterExec(t, pool, `INSERT INTO role_grants(tenant_key,app_id,account_id,role,source,granted_by)
		SELECT tenant_key,app_id,'successor','system_admin','platform_admin',id FROM accounts WHERE open_id='ou_admin'`)
	transferred := runRosterTestSync(t, service, "succeeded")
	assertRosterAccount(t, pool, "ou_admin", false, true, 2, 0, 1)
	var recoveredBy int64
	if err := pool.QueryRow(ctx, `SELECT e.directory_sync_run_id FROM audit_events e JOIN accounts a ON a.id=e.resource_id
		WHERE a.open_id='ou_admin' AND e.action='account.disable'`).Scan(&recoveredBy); err != nil || recoveredBy != transferred {
		t.Fatalf("transferred protection audit run = %d, %v", recoveredBy, err)
	}
	provider.Store(&rosterReply{root: "0", members: rosterMembers})
	runRosterTestSync(t, service, "succeeded")
	assertRosterAccount(t, pool, "ou_member", false, false, 1, 0, 1)

	adminAPI := http.NewServeMux()
	service.access.Register(adminAPI, func(http.ResponseWriter, *http.Request, bool) (identity.Principal, bool) {
		return identity.Principal{AccountID: "successor", OpenID: "ou_successor", TenantKey: "tenant", AppID: "app"}, true
	})
	changeMember := func(body string) *httptest.ResponseRecorder {
		out := httptest.NewRecorder()
		adminAPI.ServeHTTP(out, httptest.NewRequest(http.MethodPatch, "/api/v1/system/accounts/member", strings.NewReader(body)))
		return out
	}
	if out := changeMember(`{"active":true}`); out.Code != http.StatusOK {
		t.Fatalf("enable returned member: %d %s", out.Code, out.Body)
	}
	provider.Store(&rosterReply{root: "0", members: `[]`, lookup: missingRosterUser})
	racingRun := claimRosterTestRun(t, service)
	published := make(chan struct{})
	go func() {
		defer close(published)
		service.runClaim(ctx, racingRun)
	}()
	out := changeMember(`{"active":false}`)
	<-published
	if out.Code != http.StatusOK {
		t.Fatalf("concurrent manual disable: %d %s", out.Code, out.Body)
	}
	assertRosterAccount(t, pool, "ou_member", false, true, 3, 0, 2)
}

func TestRosterPartialScopeRequiresAuthoritativeAbsence(t *testing.T) {
	pool := directoryTestPool(t)
	service, provider := setupRosterSync(t, pool, rosterReply{root: "0", members: rosterMembers})
	rosterExec(t, pool, `INSERT INTO accounts(id,tenant_key,app_id,open_id) VALUES ('member','tenant','app','ou_member')`)
	runRosterTestSync(t, service, "succeeded")

	var probes atomic.Int32
	provider.Store(&rosterReply{root: "team", members: `[]`, lookup: func(w http.ResponseWriter, r *http.Request) {
		probes.Add(1)
		missingRosterUser(w, r)
	}})
	runRosterTestSync(t, service, "succeeded")
	if probes.Load() != 0 {
		t.Fatal("scope change probed people in the old authorization")
	}
	assertRosterAccount(t, pool, "ou_member", true, false, 0, 0, 0)
	provider.Store(&rosterReply{root: "team", members: rosterMembers})
	runRosterTestSync(t, service, "succeeded")

	provider.Store(&rosterReply{root: "team", members: `[]`, lookup: func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, contacts+"/users/")
		if id == "ou_member" {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"code":41050,"msg":"no user authority"}`)
			return
		}
		// A successful lookup outside the visible team must not add an old path.
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"user": map[string]any{"open_id": id, "department_ids": []string{"other"}}}})
	}})
	runRosterTestSync(t, service, "succeeded")
	assertRosterAccount(t, pool, "ou_member", true, false, 0, 0, 0)

	for _, tc := range []struct {
		name, members string
		failure       func(http.ResponseWriter, *http.Request)
	}{
		{"missing page", "", nil},
		{"permission denied", `[]`, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"code":99991672}`)
		}},
		{"ambiguous error", `[]`, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"code":40008}`)
		}},
		{"rate limited", `[]`, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"code":40007}`)
		}},
		{"timeout", `[]`, func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider.Store(&rosterReply{root: "team", members: tc.members, lookup: tc.failure})
			runRosterTestSync(t, service, "failed")
			assertRosterAccount(t, pool, "ou_member", true, false, 0, 0, 0)
		})
	}
	provider.Store(&rosterReply{root: "team", members: rosterMembers})
	runRosterTestSync(t, service, "succeeded")
	provider.Store(&rosterReply{root: "team", members: `[]`, lookup: func(w http.ResponseWriter, r *http.Request) {
		provider.Store(&rosterReply{root: "0", members: `[]`, lookup: missingRosterUser})
		missingRosterUser(w, r)
	}})
	runRosterTestSync(t, service, "failed")
	assertRosterAccount(t, pool, "ou_member", true, false, 0, 0, 0)
	var visible bool
	if err := pool.QueryRow(t.Context(), `SELECT visible FROM directory_sources`).Scan(&visible); err != nil || visible {
		t.Fatalf("old scope visibility = %v, %v", visible, err)
	}
	provider.Store(&rosterReply{root: "team", members: `[]`, lookup: missingRosterUser})
	runRosterTestSync(t, service, "succeeded")
	assertRosterAccount(t, pool, "ou_member", false, true, 1, 0, 1)
}
