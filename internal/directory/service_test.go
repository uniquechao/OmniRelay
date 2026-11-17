package directory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/migrations"
)

func directoryTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("OMNIRELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set OMNIRELAY_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("directory_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestSynchronizationClaimsPublicationAndAccess(t *testing.T) {
	pool := directoryTestPool(t)
	ctx := context.Background()
	cfg := Config{TenantKey: "tenant", AppID: "app", AppSecret: "secret", Interval: time.Minute}
	var agents []*Service
	for range 2 {
		service, err := New(pool, cfg)
		if err != nil {
			t.Fatal(err)
		}
		agents = append(agents, service)
	}
	var claims [2]claim
	var errs [2]error
	var workers sync.WaitGroup
	for i, agent := range agents {
		workers.Go(func() { claims[i], errs[i] = agent.claimNext(ctx) })
	}
	workers.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	job := claims[0]
	if job.id == 0 {
		job = claims[1]
	} else if claims[1].id != 0 {
		t.Fatalf("two workers claimed the same source: %+v", claims)
	}
	if job.id == 0 {
		t.Fatal("no initial sync claimed")
	}

	if _, err := pool.Exec(ctx, "UPDATE directory_sync_runs SET lease_until=clock_timestamp() WHERE id=$1", job.id); err != nil {
		t.Fatal(err)
	}
	stale := job
	if err := agents[0].publish(ctx, stale, snapshot{}); !errors.Is(err, errLostClaim) {
		t.Fatalf("stale worker could publish: %v", err)
	}
	if next, err := agents[1].claimNext(ctx); err != nil || next.id != 0 {
		t.Fatalf("interrupted attempt must be recorded and backed off: %+v %v", next, err)
	}

	var actor identity.Principal
	actor.TenantKey, actor.AppID, actor.OpenID = "tenant", "app", "ou_actor"
	if err := pool.QueryRow(ctx, "INSERT INTO accounts (tenant_key,app_id,open_id) VALUES ($1,$2,$3) RETURNING id",
		actor.TenantKey, actor.AppID, actor.OpenID).Scan(&actor.AccountID); err != nil {
		t.Fatal(err)
	}
	manual, duplicate, err := agents[0].enqueue(ctx, actor, allowTestSync)
	if err != nil || duplicate || manual.State != "queued" {
		t.Fatalf("manual request failed: %+v %t %v", manual, duplicate, err)
	}
	coalesced, duplicate, err := agents[1].enqueue(ctx, actor, allowTestSync)
	if err != nil || !duplicate || coalesced.ID != manual.ID {
		t.Fatalf("request was not coalesced: %+v %t %v", coalesced, duplicate, err)
	}
	job, err = agents[1].claimNext(ctx)
	if err != nil || job.id != manual.ID {
		t.Fatalf("manual job was not claimed: %+v %v", job, err)
	}
	var auditCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE action='directory.sync' AND resource_id=$1",
		fmt.Sprint(manual.ID)).Scan(&auditCount); err != nil || auditCount != 2 {
		t.Fatalf("manual requests were not audited: %d %v", auditCount, err)
	}

	person := contactPerson{ID: actor.OpenID, Name: "Example"}
	activated := true
	person.Status.Activated = &activated
	authorization := scope{Departments: []string{"org"}, People: []string{}}
	data := snapshot{Scope: authorization,
		Departments: map[string]department{
			"org": {ID: "org", Name: "Organization", Path: []string{"Organization"}, HasChildren: true},
			"sub": {ID: "sub", ParentID: "org", Name: "Sub", Path: []string{"Organization", "Sub"}},
		},
		People:  map[string]contactPerson{person.ID: person},
		Members: map[string]map[string]bool{person.ID: {"org": true, "sub": true}},
	}
	if err := agents[1].publish(ctx, job, data); err != nil {
		t.Fatal(err)
	}
	otherTenant := actor
	otherTenant.TenantKey = "someone_else"
	if agents[0].sameSource(otherTenant) {
		t.Fatal("foreign tenant matches directory source")
	}

	mux := http.NewServeMux()
	agents[0].Register(mux, func(http.ResponseWriter, *http.Request, bool) (identity.Principal, bool) { return actor, true },
		func(_ context.Context, _ identity.Principal, action, resource string) (bool, error) {
			return action == "directory.read" && resource == "", nil
		}, nil)
	for _, tc := range []struct {
		path  string
		total int
	}{
		{"/departments?parent_id=org", 1},
		{"/people?department_id=org&include_descendants=true&q=exam&account_status=active", 1},
		{"/people?offset=9", 1},
		{"/sync-runs", 2},
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, route+tc.path, nil))
		var body struct {
			Total int `json:"total"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != http.StatusOK || body.Total != tc.total {
			t.Fatalf("%s returned %d %s", tc.path, response.Code, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, route+"/sync-runs", nil))
	if response.Code != http.StatusForbidden {
		t.Fatalf("directory.read alone permitted a manual mutation: %d", response.Code)
	}

	if _, _, err := agents[0].enqueue(ctx, actor, allowTestSync); err != nil {
		t.Fatal(err)
	}
	job, err = agents[0].claimNext(ctx)
	if err != nil || job.id == 0 {
		t.Fatalf("department move run was not claimed: %+v %v", job, err)
	}
	delete(data.Members[person.ID], "org")
	if err := agents[0].publish(ctx, job, data); err != nil {
		t.Fatal(err)
	}
	moved, err := scanRun(pool.QueryRow(ctx, "SELECT "+runFields+" FROM directory_sync_runs WHERE id=$1", job.id))
	if err != nil || moved.ChangedPeopleCount != 1 || moved.NewPeopleCount != 0 || moved.MissingPeopleCount != 0 {
		t.Fatalf("department membership change was not counted: %+v %v", moved, err)
	}

	if _, _, err := agents[0].enqueue(ctx, actor, allowTestSync); err != nil {
		t.Fatal(err)
	}
	job, err = agents[0].claimNext(ctx)
	if err != nil || job.id == 0 {
		t.Fatalf("follow-up was not claimed: %+v %v", job, err)
	}
	feishu := fixtureService(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/v3/token":
			fmt.Fprint(w, `{"code":0,"access_token":"token"}`)
		case contacts + "/scopes":
			fmt.Fprint(w, `{"code":0,"data":{"department_ids":["org"],"has_more":false}}`)
		case contacts + "/departments/org":
			fmt.Fprint(w, `{"code":0,"data":{"department":{"open_department_id":"org","parent_department_id":"0","name":"Organization"}}}`)
		case contacts + "/departments/org/children":
			fmt.Fprint(w, `{"code":0,"data":{"items":null,"has_more":false}}`)
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	agents[0].cfg.BaseURL, agents[0].client = feishu.cfg.BaseURL, feishu.client
	agents[0].runClaim(ctx, job)
	failed, err := scanRun(pool.QueryRow(ctx, "SELECT "+runFields+" FROM directory_sync_runs WHERE id=$1", job.id))
	if err != nil || failed.State != "failed" {
		t.Fatalf("incomplete sync was not marked as failed: %+v %v", failed, err)
	}
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, route+"/people", nil))
	var previous struct {
		Total   int  `json:"total"`
		Visible bool `json:"visible"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &previous); err != nil || response.Code != http.StatusOK || previous.Total != 1 || !previous.Visible {
		t.Fatalf("incomplete sync hid the prior verified directory: %d %s", response.Code, response.Body.String())
	}

	if _, _, err := agents[0].enqueue(ctx, actor, allowTestSync); err != nil {
		t.Fatal(err)
	}
	job, err = agents[0].claimNext(ctx)
	if err != nil || job.id == 0 {
		t.Fatalf("scope recheck was not claimed: %+v %v", job, err)
	}
	if err := agents[0].recordProgress(ctx, job, scope{Departments: []string{}, People: []string{}}, 0, 0); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, route+"/people", nil))
	if response.Code != http.StatusOK || !json.Valid(response.Body.Bytes()) {
		t.Fatalf("directory read failed after revocation: %d %s", response.Code, response.Body.String())
	}
	var hidden struct {
		Total   int  `json:"total"`
		Visible bool `json:"visible"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &hidden); err != nil || hidden.Total != 0 || hidden.Visible {
		t.Fatalf("directory data remains visible after revocation: %s", response.Body.String())
	}
	replacement := snapshot{Scope: scope{Departments: []string{}, People: []string{}}}
	if err := agents[0].publish(ctx, job, replacement); err != nil {
		t.Fatal(err)
	}
	var retainedProfiles int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM directory_people").Scan(&retainedProfiles); err != nil || retainedProfiles != 0 {
		t.Fatalf("replacement retained revoked contact data: %d %v", retainedProfiles, err)
	}
	result, err := scanRun(pool.QueryRow(ctx, "SELECT "+runFields+" FROM directory_sync_runs WHERE id=$1", job.id))
	if err != nil || result.OutsideScopeCount != 1 || result.MissingPeopleCount != 0 {
		t.Fatalf("scope loss was not classified separately: %+v %v", result, err)
	}
}

func allowTestSync(context.Context, pgx.Tx, identity.Principal) (bool, error) {
	return true, nil
}
