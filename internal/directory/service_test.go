package directory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
		People: map[string]contactPerson{
			person.ID: person,
			"ou_same": {ID: "ou_same", Name: person.Name},
		},
		Members: map[string]map[string]bool{person.ID: {"org": true, "sub": true}, "ou_same": {"sub": true}},
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
		{"/departments", 1},
		{"/departments?parent_id=0", 1},
		{"/departments?parent_id=sub", 0},
		{"/people?department_id=org", 1},
		{"/people?department_id=org&include_descendants=true", 2},
		{"/people?department_id=sub&q=example&account_status=unprovisioned", 1},
		{"/people?account_status=inactive", 0},
		{"/people?department_id=org&include_descendants=true&q=exam&account_status=active", 1},
		{"/people?offset=9", 2},
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
	versionQuery := "&snapshot_version=" + strconv.FormatInt(job.id, 10)
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, route+path, nil))
		return response
	}
	departments := get("/departments?limit=1" + versionQuery)
	firstPage := get("/people?limit=1" + versionQuery)
	secondPage := get("/people?limit=1&offset=1" + versionQuery)
	for _, item := range []struct {
		response *httptest.ResponseRecorder
		want     string
	}{
		{departments, `"id":"org"`},
		{firstPage, `"open_id":"ou_actor"`},
		{secondPage, `"open_id":"ou_same"`},
	} {
		var body struct {
			Version int64             `json:"snapshot_version"`
			Items   []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(item.response.Body.Bytes(), &body); err != nil || item.response.Code != http.StatusOK ||
			body.Version != job.id || len(body.Items) != 1 || !strings.Contains(string(body.Items[0]), item.want) {
			t.Fatalf("incorrect pinned page: HTTP %d %s", item.response.Code, item.response.Body.String())
		}
	}
	runHistory := get("/sync-runs?limit=1" + versionQuery)
	var historyTimes struct {
		LastSuccess string `json:"last_success_at"`
		NextRun     string `json:"next_run_at"`
		Items       []struct {
			StartedAt  string `json:"started_at"`
			FinishedAt string `json:"finished_at"`
		} `json:"items"`
	}
	if err := json.Unmarshal(runHistory.Body.Bytes(), &historyTimes); err != nil || runHistory.Code != http.StatusOK || len(historyTimes.Items) != 1 {
		t.Fatalf("invalid run history: %s", runHistory.Body.String())
	}
	for _, value := range []string{historyTimes.LastSuccess, historyTimes.NextRun, historyTimes.Items[0].StartedAt, historyTimes.Items[0].FinishedAt} {
		if _, err := time.Parse(time.RFC3339Nano, value); err != nil || !strings.HasSuffix(value, "Z") {
			t.Fatalf("run history timestamp is not UTC RFC 3339: %q", value)
		}
	}
	var people struct {
		Items []struct {
			Departments []department `json:"departments"`
			Status      string       `json:"directory_status"`
			Account     string       `json:"account_status"`
		} `json:"items"`
	}
	filtered := get("/people?department_id=org&limit=1" + versionQuery)
	if err := json.Unmarshal(filtered.Body.Bytes(), &people); err != nil || filtered.Code != http.StatusOK || len(people.Items) != 1 ||
		len(people.Items[0].Departments) != 2 || people.Items[0].Departments[1].ID != "sub" ||
		strings.Join(people.Items[0].Departments[1].Path, "/") != "Organization/Sub" {
		t.Fatalf("filter omitted a visible path: %s", filtered.Body.String())
	}
	if err := json.Unmarshal(secondPage.Body.Bytes(), &people); err != nil || people.Items[0].Status != "unknown" || people.Items[0].Account != "unprovisioned" {
		t.Fatalf("unknown status was not independent of provisioning: %s", secondPage.Body.String())
	}

	// The resource reader supplies the people it is allowed to expose. The
	// same person can arrive through its account mapping or direct Open ID.
	resourceAllowed, directoryAllowed := true, true
	resourceReader := func(_ context.Context, principal identity.Principal, resource string) ([]ResourcePersonRef, bool, error) {
		if principal != actor || resource != "test-resource" {
			t.Fatal("unexpected resource authorization input")
		}
		return []ResourcePersonRef{
			{AccountID: actor.AccountID, Name: "Source name"},
			{OpenID: person.ID, Name: "Source name"},
			{OpenID: "ou_same", Name: "Source name"},
			{Name: "Example"},
			{Name: "Example"},
		}, resourceAllowed, nil
	}
	directoryReader := func(_ context.Context, principal identity.Principal, action, resource string) (bool, error) {
		if principal != actor || action != "directory.read" || resource != "" {
			t.Fatal("unexpected directory authorization input")
		}
		return directoryAllowed, nil
	}
	projectPeople := func() ([]PersonProjection, error) {
		return agents[0].ReadResourcePeople(ctx, actor, "test-resource", resourceReader, directoryReader)
	}
	projected, err := projectPeople()
	if err != nil || len(projected) != 5 {
		t.Fatalf("could not project resource people: %+v %v", projected, err)
	}
	var pages [2]struct {
		Items []struct {
			PersonKey string `json:"person_key"`
		} `json:"items"`
	}
	for i, page := range []*httptest.ResponseRecorder{firstPage, secondPage} {
		if err := json.Unmarshal(page.Body.Bytes(), &pages[i]); err != nil || len(pages[i].Items) != 1 {
			t.Fatalf("invalid directory people page: %s", page.Body.String())
		}
	}
	if projected[0].PersonKey == "" || projected[0].PersonKey != pages[0].Items[0].PersonKey ||
		projected[0].PersonKey != projected[1].PersonKey || projected[2].PersonKey != pages[1].Items[0].PersonKey ||
		projected[0].PersonKey == projected[2].PersonKey || projected[0].Name != projected[2].Name ||
		projected[0].Source != "feishu" || len(projected[0].OrganizationPaths) != 2 ||
		strings.Join(projected[0].OrganizationPaths[1], "/") != "Organization/Sub" ||
		projected[3].PersonKey != "" || projected[4].PersonKey != "" || projected[4].Name != "Example" {
		t.Fatalf("identity or multi-department projection mismatch: %+v", projected)
	}
	assertMinimal := func() {
		t.Helper()
		minimal, err := projectPeople()
		if err != nil || len(minimal) != 5 {
			t.Fatalf("could not read minimal projection: %+v %v", minimal, err)
		}
		serialized, err := json.Marshal(minimal)
		if err != nil || strings.Contains(string(serialized), "organization_paths") ||
			strings.Contains(string(serialized), "ou_actor") || strings.Contains(string(serialized), "account_id") ||
			minimal[0].PersonKey != projected[0].PersonKey || minimal[0].Name != "Source name" {
			t.Fatalf("invalid minimal projection: %s %v", serialized, err)
		}
	}
	directoryAllowed = false
	assertMinimal()
	directoryAllowed, resourceAllowed = true, false
	if denied, err := projectPeople(); !errors.Is(err, ErrResourcePeopleForbidden) || denied != nil {
		t.Fatalf("directory authority granted resource access: %+v %v", denied, err)
	}
	resourceAllowed = true

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
	for _, path := range []string{"/departments?limit=1", "/people?limit=1"} {
		if response := get(path + versionQuery); response.Code != http.StatusConflict ||
			strings.TrimSpace(response.Body.String()) != `{"error":"directory_snapshot_changed"}` {
			t.Fatalf("old page version remained readable: %d %s", response.Code, response.Body.String())
		}
	}
	visibleVersion := "&snapshot_version=" + strconv.FormatInt(job.id, 10)
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
	if err := json.Unmarshal(response.Body.Bytes(), &previous); err != nil || response.Code != http.StatusOK || previous.Total != 2 || !previous.Visible {
		t.Fatalf("incomplete sync hid the prior verified directory: %d %s", response.Code, response.Body.String())
	}

	for _, failure := range []struct {
		name    string
		status  int
		code    int
		retries int32
	}{
		{"internal error", http.StatusBadRequest, 40003, 1},
		{"throttled", http.StatusForbidden, 99991400, 3},
		{"timeout", 0, 0, 3},
		{"permission revoked", http.StatusBadRequest, 40004, 1},
	} {
		t.Run(failure.name, func(t *testing.T) {
			manual, _, err := agents[0].enqueue(ctx, actor, allowTestSync)
			if err != nil {
				t.Fatal(err)
			}
			job, err := agents[0].claimNext(ctx)
			if err != nil || job.id != manual.ID {
				t.Fatalf("fault run was not claimed: %+v %v", job, err)
			}
			var attempts atomic.Int32
			feishu := fixtureService(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/oauth/v3/token":
					fmt.Fprint(w, `{"code":0,"access_token":"token"}`)
				case contacts + "/scopes":
					fmt.Fprint(w, `{"code":0,"data":{"department_ids":["org"],"has_more":false}}`)
				case contacts + "/departments/org":
					attempts.Add(1)
					if failure.status == 0 {
						select {
						case <-r.Context().Done():
						case <-time.After(time.Second):
						}
						return
					}
					w.WriteHeader(failure.status)
					fmt.Fprintf(w, `{"code":%d,"msg":"private source diagnostic"}`, failure.code)
				default:
					t.Errorf("unexpected request %s", r.URL)
					w.WriteHeader(http.StatusNotFound)
				}
			})
			if failure.status == 0 {
				feishu.client.Timeout = 120 * time.Millisecond
			}
			agents[0].cfg.BaseURL, agents[0].client = feishu.cfg.BaseURL, feishu.client
			agents[0].runClaim(ctx, job)
			item, err := scanRun(pool.QueryRow(ctx, "SELECT "+runFields+" FROM directory_sync_runs WHERE id=$1", job.id))
			if err != nil || item.State != "failed" || attempts.Load() != failure.retries || strings.Contains(item.FailureReason, "private source") {
				t.Fatalf("incorrect failure handling: %+v attempts=%d %v", item, attempts.Load(), err)
			}
			expected := http.StatusOK
			if failure.code == 40004 {
				expected = http.StatusConflict
			}
			if response := get("/people?limit=1" + visibleVersion); response.Code != expected {
				t.Fatalf("unexpected old snapshot availability: %d %s", response.Code, response.Body.String())
			}
		})
	}
	// A fully verified run can restore the same scope after access returns.
	if _, _, err := agents[0].enqueue(ctx, actor, allowTestSync); err != nil {
		t.Fatal(err)
	}
	job, err = agents[0].claimNext(ctx)
	if err != nil || job.id == 0 {
		t.Fatalf("restoration was not claimed: %+v %v", job, err)
	}
	if err := agents[0].publish(ctx, job, data); err != nil {
		t.Fatal(err)
	}
	visibleVersion = "&snapshot_version=" + strconv.FormatInt(job.id, 10)
	if response := get("/people?limit=1" + visibleVersion); response.Code != http.StatusOK {
		t.Fatalf("restored snapshot is unavailable: %d %s", response.Code, response.Body.String())
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
	for _, path := range []string{"/departments?limit=1", "/people?limit=1"} {
		if response := get(path + visibleVersion); response.Code != http.StatusConflict {
			t.Fatalf("hidden directory accepted old pagination: %d %s", response.Code, response.Body.String())
		}
	}
	assertMinimal()
	emptyScope := fixtureService(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/v3/token":
			fmt.Fprint(w, `{"code":0,"access_token":"token"}`)
		case contacts + "/scopes":
			fmt.Fprint(w, `{"code":0,"data":{"department_ids":[],"user_ids":[],"has_more":false}}`)
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	agents[0].cfg.BaseURL, agents[0].client = emptyScope.cfg.BaseURL, emptyScope.client
	agents[0].runClaim(ctx, job)
	newVersion := "&snapshot_version=" + strconv.FormatInt(job.id, 10)
	if response := get("/people?limit=1" + newVersion); response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"visible":true`) || !strings.Contains(response.Body.String(), `"items":[]`) {
		t.Fatalf("verified empty replacement did not publish: %d %s", response.Code, response.Body.String())
	}
	var retainedProfiles int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM directory_people").Scan(&retainedProfiles); err != nil || retainedProfiles != 0 {
		t.Fatalf("replacement retained revoked contact data: %d %v", retainedProfiles, err)
	}
	result, err := scanRun(pool.QueryRow(ctx, "SELECT "+runFields+" FROM directory_sync_runs WHERE id=$1", job.id))
	if err != nil || result.OutsideScopeCount != 2 || result.MissingPeopleCount != 0 {
		t.Fatalf("scope loss was not classified separately: %+v %v", result, err)
	}
	assertMinimal()
}

func allowTestSync(context.Context, pgx.Tx, identity.Principal) (bool, error) {
	return true, nil
}

func TestScheduledSynchronizationRecoversAfterRestart(t *testing.T) {
	pool := directoryTestPool(t)
	ctx := context.Background()
	cfg := Config{TenantKey: "tenant", AppID: "app", Interval: time.Minute}
	start := func() *Service {
		t.Helper()
		service, err := New(pool, cfg)
		if err != nil {
			t.Fatal(err)
		}
		return service
	}
	firstWorker := start()
	initial, err := firstWorker.claimNext(ctx)
	if err != nil || initial.id == 0 {
		t.Fatalf("initial claim: %+v %v", initial, err)
	}
	original := snapshot{
		Scope:  scope{Departments: []string{}, People: []string{"ou_gone", "ou_stays"}},
		People: map[string]contactPerson{"ou_gone": {ID: "ou_gone", Name: "Gone"}, "ou_stays": {ID: "ou_stays", Name: "Stays"}},
	}
	if err := firstWorker.publish(ctx, initial, original); err != nil {
		t.Fatal(err)
	}
	var before, next time.Time
	if err := pool.QueryRow(ctx, "SELECT last_success_at,next_run_at FROM directory_sources").Scan(&before, &next); err != nil ||
		before.IsZero() || next.Sub(before) != cfg.Interval {
		t.Fatalf("initial schedule: %v %v %v", before, next, err)
	}
	if job, err := firstWorker.claimNext(ctx); err != nil || job.id != 0 {
		t.Fatalf("claimed before due time: %+v %v", job, err)
	}
	makeDue := func() {
		t.Helper()
		if _, err := pool.Exec(ctx, "UPDATE directory_sources SET next_run_at=clock_timestamp()"); err != nil {
			t.Fatal(err)
		}
	}
	makeDue()
	old, err := firstWorker.claimNext(ctx)
	if err != nil || old.id == 0 {
		t.Fatalf("scheduled claim: %+v %v", old, err)
	}
	var actor identity.Principal
	actor.TenantKey, actor.AppID, actor.OpenID = cfg.TenantKey, cfg.AppID, "ou_admin"
	if err := pool.QueryRow(ctx, "INSERT INTO accounts (tenant_key,app_id,open_id) VALUES ($1,$2,$3) RETURNING id",
		actor.TenantKey, actor.AppID, actor.OpenID).Scan(&actor.AccountID); err != nil {
		t.Fatal(err)
	}
	restarted := start()
	for range 2 {
		item, coalesced, err := restarted.enqueue(ctx, actor, allowTestSync)
		if err != nil || !coalesced || item.ID != old.id || item.Trigger != "scheduled" {
			t.Fatalf("manual sync did not join scheduled run: %+v %t %v", item, coalesced, err)
		}
	}
	if job, err := restarted.claimNext(ctx); err != nil || job.id != 0 {
		t.Fatalf("restart took a live lease: %+v %v", job, err)
	}
	if _, err := pool.Exec(ctx, "UPDATE directory_sync_runs SET lease_until=clock_timestamp() WHERE id=$1", old.id); err != nil {
		t.Fatal(err)
	}
	if job, err := restarted.claimNext(ctx); err != nil || job.id != 0 {
		t.Fatalf("expiration did not honor backoff: %+v %v", job, err)
	}
	var lastSuccess time.Time
	var backedOff bool
	if err := pool.QueryRow(ctx, "SELECT last_success_at,next_run_at>clock_timestamp() FROM directory_sources").Scan(&lastSuccess, &backedOff); err != nil ||
		!lastSuccess.Equal(before) || !backedOff {
		t.Fatalf("failure changed last success or skipped backoff: %v %t %v", lastSuccess, backedOff, err)
	}
	makeDue()
	retry, err := restarted.claimNext(ctx)
	if err != nil || retry.id <= old.id {
		t.Fatalf("retry did not get a fresh run ID: %+v %v", retry, err)
	}
	if err := firstWorker.publish(ctx, old, original); !errors.Is(err, errLostClaim) {
		t.Fatalf("old claimant could publish into recovery: %v", err)
	}
	if err := firstWorker.failClaim(ctx, old, "late permission denial", true); !errors.Is(err, errLostClaim) {
		t.Fatalf("old claimant could hide the directory: %v", err)
	}
	resigned := original.People["ou_stays"]
	resigned.Status.Resigned = true
	replacement := snapshot{Scope: original.Scope, People: map[string]contactPerson{resigned.ID: resigned}}
	// Force expiration after staging and updating the pointer, without waiting
	// for the real lease duration or relying on scheduler timing.
	if _, err := pool.Exec(ctx, `CREATE FUNCTION expire_directory_test_lease() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			UPDATE directory_sync_runs SET lease_until=clock_timestamp() WHERE id=NEW.active_version;
			RETURN NEW;
		END $$;
		CREATE TRIGGER expire_directory_test_lease AFTER UPDATE OF active_version ON directory_sources
		FOR EACH ROW EXECUTE FUNCTION expire_directory_test_lease()`); err != nil {
		t.Fatal(err)
	}
	if err := restarted.publish(ctx, retry, replacement); !errors.Is(err, errLostClaim) {
		t.Fatalf("lease expiry after staging did not reject publication: %v", err)
	}
	var version int64
	var visible bool
	if err := pool.QueryRow(ctx, "SELECT active_version,visible FROM directory_sources").Scan(&version, &visible); err != nil ||
		version != initial.id || !visible {
		t.Fatalf("lease expiry did not roll back the directory pointer: %d %t %v", version, visible, err)
	}
	if _, err := pool.Exec(ctx, "DROP TRIGGER expire_directory_test_lease ON directory_sources"); err != nil {
		t.Fatal(err)
	}
	if err := restarted.publish(ctx, retry, replacement); err != nil {
		t.Fatal(err)
	}
	recovered, err := scanRun(pool.QueryRow(ctx, "SELECT "+runFields+" FROM directory_sync_runs WHERE id=$1", retry.id))
	if err != nil || recovered.State != "succeeded" || recovered.MissingPeopleCount != 1 || recovered.ChangedPeopleCount != 1 || recovered.OutsideScopeCount != 0 {
		t.Fatalf("incorrect recovery delta: %+v %v", recovered, err)
	}
	expired, err := scanRun(pool.QueryRow(ctx, "SELECT "+runFields+" FROM directory_sync_runs WHERE id=$1", old.id))
	if err != nil || expired.State != "failed" || expired.FailureReason != "worker lease expired" {
		t.Fatalf("lost attempt not preserved: %+v %v", expired, err)
	}
	var auditCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE action='directory.sync' AND resource_id=$1 AND result='coalesced'",
		strconv.FormatInt(old.id, 10)).Scan(&auditCount); err != nil || auditCount != 2 {
		t.Fatalf("manual joins were not independently audited: %d %v", auditCount, err)
	}
}
