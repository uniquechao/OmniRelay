package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/store/postgres"
	"github.com/uniquechao/OmniRelay/internal/system"
)

const botPath = "/api/v1/system/feishu-bot"

type botTransport func(*http.Request) (*http.Response, error)

func (fn botTransport) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestFeishuBotManagement(t *testing.T) {
	ctx, pool := botTestPool(t)
	const secret = "private-fixture-secret"
	const token = "private-fixture-token"
	var calls, providerMode atomic.Int32
	botEntered := make(chan struct{})
	botRelease := make(chan struct{})
	transport := botTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		var body string
		status := http.StatusOK
		switch {
		case r.Method == "POST" && r.URL.String() == "https://accounts.feishu.cn/oauth/v3/token":
			if r.ParseForm() != nil || r.Form.Get("client_id") != "cli_fixture" || r.Form.Get("client_secret") != secret ||
				r.Form.Get("grant_type") != "client_credentials" {
				return nil, errors.New("unexpected token request")
			}
			body = fmt.Sprintf(`{"code":0,"access_token":%q}`, token)
			if providerMode.Load() == 1 {
				status = http.StatusBadRequest
				body = fmt.Sprintf(`{"code":20002,"error_description":%q}`, secret)
			}
		case r.Method == "GET" && r.URL.String() == "https://open.feishu.cn/open-apis/bot/v3/info":
			if r.Header.Get("Authorization") != "Bearer "+token {
				return nil, errors.New("unexpected Bot request")
			}
			if providerMode.Load() == 2 {
				body = fmt.Sprintf(`{"code":99991672,"msg":%q}`, token)
				break
			}
			close(botEntered)
			select {
			case <-botRelease:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
			body = `{"code":0,"bot":{"app_name":"Fixture Bot","open_id":"ou_bot","activate_status":2}}`
		default:
			return nil, errors.New("unexpected provider request")
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	cfg := configuration{
		publicOrigin: fixtureOrigin, appID: "cli_fixture", appSecret: secret,
		tenantKey: "tenant_fixture", bootstrap: "ou_admin", credentialsGeneration: 1, botHeartbeatTTL: 30 * time.Second,
	}
	handler, workers, err := newBackend(ctx, pool, cfg, transport)
	if err != nil {
		t.Fatal(err)
	}
	admin := seedBotSession(t, ctx, pool, "ou_admin", "")
	operator := seedBotSession(t, ctx, pool, "ou_operator", access.RoleSystemOperator)
	member := seedBotSession(t, ctx, pool, "ou_member", "")
	auditor := seedBotSession(t, ctx, pool, "ou_auditor", access.RoleAuditViewer)
	for _, session := range []*botSession{&admin, &operator, &member, &auditor} {
		req := httptest.NewRequest("GET", fixtureOrigin+"/api/v1/me", nil)
		req.AddCookie(session.cookie)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		session.csrf = decodeBot[struct {
			CSRF string `json:"csrf_token"`
		}](t, res).CSRF
		if res.Code != 200 || session.csrf == "" {
			t.Fatal("could not obtain CSRF token for test session")
		}
	}
	request := func(method, path, body string, session botSession, headers map[string]string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, fixtureOrigin+path, strings.NewReader(body))
		if session.cookie != nil {
			req.AddCookie(session.cookie)
		}
		req.Header.Set("Origin", fixtureOrigin)
		req.Header.Set("X-CSRF-Token", session.csrf)
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want || strings.Contains(res.Body.String(), secret) || strings.Contains(res.Body.String(), token) {
			t.Fatalf("HTTP %s %s: got %d, want %d; sanitized=%t", method, path, res.Code, want,
				!strings.Contains(res.Body.String(), secret) && !strings.Contains(res.Body.String(), token))
		}
		return res
	}
	for _, test := range []struct{ method, path string }{
		{"GET", botPath}, {"PATCH", botPath}, {"POST", botPath + "/checks"}, {"GET", botPath + "/checks/unknown"},
	} {
		request(test.method, test.path, "", botSession{}, nil, 401)
		request(test.method, test.path, "", member, nil, 403)
	}
	const systemPath = "/api/v1/system"
	const directoryPath = systemPath + "/directory"
	for _, path := range []string{
		systemPath + "/accounts", systemPath + "/roles", systemPath + "/role-grants",
		directoryPath + "/people", directoryPath + "/departments", directoryPath + "/sync-runs", botPath,
	} {
		request("GET", path, "", operator, nil, 200)
		request("GET", path, "", auditor, nil, 403)
		request("GET", path, "", member, nil, 403)
	}
	request("GET", systemPath+"/audit-events", "", operator, nil, 200)
	request("GET", systemPath+"/audit-events", "", auditor, nil, 200)
	request("GET", systemPath+"/audit-events", "", member, nil, 403)
	request("POST", directoryPath+"/sync-runs", "", operator, nil, 202)
	request("POST", directoryPath+"/sync-runs", "", auditor, nil, 403)

	me := request("GET", "/api/v1/me", "", operator, nil, 200)
	view := decodeBot[struct{ Access identity.WorkbenchAccess }](t, me).Access
	if !slices.Equal(view.Roles, []string{access.RoleSystemOperator, access.RoleSystemUser}) ||
		view.DefaultRoute != "/#/system/users" || view.PermissionVersion != "0" ||
		view.Navigation["workbench"] || !view.Navigation["system"] ||
		len(view.Admin) != 4 || !view.Admin["users"] || !view.Admin["roles"] || !view.Admin["audit"] || !view.Admin["feishu_bot"] ||
		!view.Permissions["directory.sync"] || !view.Permissions["system.operate"] || view.Permissions["system.configure"] {
		t.Fatalf("invalid operator access: %+v", view)
	}
	request("POST", systemPath+"/accounts", `{"openId":"ou_candidate"}`, operator, nil, 403)
	request("PATCH", systemPath+"/accounts/unknown", `{"active":false}`, operator, nil, 403)
	request("POST", systemPath+"/role-grants", `{"accountId":"unknown","role":"audit_viewer"}`, operator, nil, 403)
	var administratorGrant string
	if err := pool.QueryRow(ctx, "SELECT id FROM role_grants WHERE role='system_admin'").Scan(&administratorGrant); err != nil {
		t.Fatal(err)
	}
	request("DELETE", systemPath+"/role-grants/"+administratorGrant, "", operator, nil, 403)
	request("DELETE", systemPath+"/role-grants/"+administratorGrant, "", member, nil, 404)
	for range 3 {
		res := request("GET", botPath, "", operator, nil, 200)
		initial := decodeBot[system.Status](t, res)
		if initial.Enabled || initial.Version != 0 || initial.Connection.State != "disabled" ||
			!initial.CredentialsConfigured || initial.CallbackURL != fixtureOrigin+"/auth/feishu/callback" ||
			res.Header().Get("ETag") != `"bot-0"` {
			t.Fatalf("unexpected initial status: %+v", initial)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("status polling called Feishu")
	}
	match0 := map[string]string{"If-Match": `"bot-0"`}
	request("PATCH", botPath, `{"enabled":true}`, operator, match0, 403)
	request("PATCH", botPath, `{"enabled":true}`, admin, nil, 428)
	for _, invalid := range []string{`{}`, `{"enabled":null}`, `{"enabled":"true"}`, `{"enabled":true,"other":1}`, `{"enabled":true,"enabled":false}`} {
		request("PATCH", botPath, invalid, admin, match0, 400)
	}
	for _, invalid := range []string{"*", `W/"bot-0"`, `"bot-0","bot-1"`, `"bot-01"`, `"bot-3"`} {
		request("PATCH", botPath, `{"enabled":true}`, admin, map[string]string{"If-Match": invalid}, 412)
	}
	for _, method := range []string{"PATCH", "POST"} {
		path, body, headers, session := botPath, `{"enabled":true}`, match0, admin
		if method == "POST" {
			path, body, headers, session = botPath+"/checks", "{}", map[string]string{"Idempotency-Key": "first"}, operator
		}
		for name, value := range map[string]string{"Origin": "https://foreign.example", "X-CSRF-Token": ""} {
			forbidden := map[string]string{name: value}
			for key, value := range headers {
				forbidden[key] = value
			}
			request(method, path, body, session, forbidden, 403)
		}
	}
	if res := request("PATCH", botPath, `{"enabled":true}`, admin, match0, 200); res.Header().Get("ETag") != `"bot-1"` {
		t.Fatal("configuration version did not advance")
	}
	request("PATCH", botPath, `{"enabled":true}`, admin, match0, 412)
	if res := request("PATCH", botPath, `{"enabled":true}`, admin, map[string]string{"If-Match": `"bot-1"`}, 200); res.Header().Get("ETag") != `"bot-1"` {
		t.Fatal("configuration no-op advanced version")
	}
	audit := request("GET", "/api/v1/system/audit-events", "", admin, nil, 200)
	if strings.Count(audit.Body.String(), "feishu_bot.configure") != 1 {
		t.Fatal("expected exactly one Bot configuration audit")
	}
	unknown := request("GET", botPath+"/checks/unknown", "", operator, nil, 404)
	if strings.TrimSpace(unknown.Body.String()) != `{"error":"check_not_found"}` {
		t.Fatal("check error did not use the contract")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO feishu_bot_configs (tenant_key,app_id,credentials_generation)
		VALUES ('foreign','foreign',1)`); err != nil {
		t.Fatal(err)
	}
	var foreignID string
	if err := pool.QueryRow(ctx, `INSERT INTO feishu_bot_checks (tenant_key,app_id,credentials_generation)
		VALUES ('foreign','foreign',1) RETURNING id`).Scan(&foreignID); err != nil {
		t.Fatal(err)
	}
	foreign := request("GET", botPath+"/checks/"+foreignID, "", operator, nil, 404)
	if foreign.Body.String() != unknown.Body.String() {
		t.Fatal("cross-scope check differed from a missing check")
	}
	request("POST", botPath+"/checks", `null`, operator, nil, 400)
	request("POST", botPath+"/checks", `{"enabled":true}`, operator, nil, 400)
	request("POST", botPath+"/checks", "{}", operator, nil, 400)
	request("POST", botPath+"/checks", "{}", operator, map[string]string{"Idempotency-Key": "bad key"}, 400)
	headers := map[string]string{"Idempotency-Key": "first"}
	first := decodeBot[system.Submission](t, request("POST", botPath+"/checks", "{}", operator, headers, 202))
	if first.ID == "" || first.State != "queued" || first.Reused {
		t.Fatalf("unexpected first task: %+v", first)
	}
	path := botPath + "/checks/" + first.ID
	queued := decodeBot[system.Check](t, request("GET", path, "", operator, nil, 200))
	if queued.State != "queued" || queued.StartedAt != nil || queued.FinishedAt != nil || queued.Results == nil || len(queued.Results) != 0 {
		t.Fatal("unexpected queued state")
	}
	if calls.Load() != 0 {
		t.Fatal("submission or reads performed synchronous provider calls")
	}
	startBotWorker(t, ctx, workers.bot)
	select {
	case <-botEntered:
	case <-ctx.Done():
		t.Fatal("worker did not reach Bot info")
	}
	running := decodeBot[system.Check](t, request("GET", path, "", operator, nil, 200))
	if running.State != "running" || running.StartedAt == nil || running.FinishedAt != nil ||
		len(running.Results) != 1 || running.Results[0].Name != "tenant_token" || running.Results[0].State != "passed" {
		t.Fatalf("unexpected running state: %+v", running)
	}
	merged := decodeBot[system.Submission](t, request("POST", botPath+"/checks", "{}", admin, map[string]string{"Idempotency-Key": "second"}, 202))
	if merged.ID != first.ID || !merged.Reused || merged.State != "running" {
		t.Fatalf("unexpected coalesced task: %+v", merged)
	}
	close(botRelease)
	fetchCheck := func() system.Check { return decodeBot[system.Check](t, request("GET", path, "", operator, nil, 200)) }
	passed := waitForBotCheck(t, ctx, fetchCheck)
	if passed.State != "succeeded" || passed.Generation != "1" || passed.FinishedAt == nil || len(passed.Results) != 2 ||
		passed.Results[1].Name != "bot_info" || passed.Results[1].State != "passed" {
		t.Fatalf("unexpected successful task: %+v", passed)
	}
	if reused := decodeBot[system.Submission](t, request("POST", botPath+"/checks", "{}", admin, headers, 202)); reused.ID != first.ID || !reused.Reused || reused.State != "succeeded" {
		t.Fatalf("terminal task was not reused: %+v", reused)
	}
	status := decodeBot[system.Status](t, request("GET", botPath, "", operator, nil, 200))
	if status.BotName != "Fixture Bot" || status.Connection.State != "unavailable" ||
		status.Connection.LastHeartbeatAt != nil || status.Connection.LastMessageAt != nil || calls.Load() != 2 {
		t.Fatalf("diagnostics affected connection or invoked extra APIs: %+v, calls=%d", status, calls.Load())
	}
	for _, test := range []struct {
		mode          int32
		states        [2]string
		failures      [2]system.CheckError
		providerCalls int32
	}{
		{1, [2]string{"failed", "skipped"}, [2]system.CheckError{system.CredentialsRejected, ""}, 1},
		{2, [2]string{"passed", "failed"}, [2]system.CheckError{"", system.BotUnavailable}, 2},
	} {
		providerMode.Store(test.mode)
		before := calls.Load()
		task := decodeBot[system.Submission](t, request("POST", botPath+"/checks", "{}", operator,
			map[string]string{"Idempotency-Key": fmt.Sprintf("failure-%d", test.mode)}, 202))
		failed := waitForBotCheck(t, ctx, func() system.Check {
			return decodeBot[system.Check](t, request("GET", botPath+"/checks/"+task.ID, "", operator, nil, 200))
		})
		if failed.State != "failed" || failed.FinishedAt == nil || len(failed.Results) != 2 || calls.Load()-before != test.providerCalls {
			t.Fatalf("unexpected provider failure: %+v", failed)
		}
		for i, stage := range failed.Results {
			if stage.State != test.states[i] || stage.Error != test.failures[i] || stage.CheckedAt.IsZero() {
				t.Fatalf("unexpected provider stage: %+v", stage)
			}
		}
	}
	prevCalls := calls.Load()
	if _, err := pool.Exec(ctx, "UPDATE role_grants SET revoked_at=now() WHERE role='system_operator'"); err != nil {
		t.Fatal(err)
	}
	request("GET", path, "", operator, nil, 403)
	request("POST", botPath+"/checks", "{}", operator, headers, 403)
	expiredDigest := sha256.Sum256([]byte(member.cookie.Value))
	if _, err := pool.Exec(ctx, `UPDATE web_sessions SET expires_at=now(), idle_expires_at=now()
		WHERE token_digest=$1`, expiredDigest[:]); err != nil {
		t.Fatal(err)
	}
	expired := request("POST", botPath+"/checks", "{}", member, headers, 401)
	if strings.TrimSpace(expired.Body.String()) != `{"error":"unauthorized"}` {
		t.Fatal("expired session error did not use the contract")
	}

	cfg.appSecret = ""
	cfg.credentialsGeneration++
	handler, workers, err = newBackend(ctx, pool, cfg, transport)
	if err != nil {
		t.Fatal(err)
	}
	status = decodeBot[system.Status](t, request("GET", botPath, "", admin, nil, 200))
	if status.CredentialsConfigured || status.BotName != "" {
		t.Fatal("missing secret or old Bot name incorrectly reported")
	}
	if reused := decodeBot[system.Submission](t, request("POST", botPath+"/checks", "{}", admin, headers, 202)); reused.ID != first.ID || !reused.Reused {
		t.Fatal("key did not survive credential rotation")
	}
	missing := decodeBot[system.Submission](t, request("POST", botPath+"/checks", "{}", admin, map[string]string{"Idempotency-Key": "missing"}, 202))
	startBotWorker(t, ctx, workers.bot)
	failed := waitForBotCheck(t, ctx, func() system.Check {
		return decodeBot[system.Check](t, request("GET", botPath+"/checks/"+missing.ID, "", admin, nil, 200))
	})
	if failed.State != "failed" || failed.Generation != "2" || len(failed.Results) != 2 ||
		failed.Results[0].Error != system.CredentialsMissing || failed.Results[1].State != "skipped" || calls.Load() != prevCalls {
		t.Fatalf("unexpected missing credential check: %+v", failed)
	}
}

func TestFeishuBotConcurrentChecksAndFencing(t *testing.T) {
	ctx, pool := botTestPool(t)
	config := system.Config{TenantKey: "tenant", AppID: "app"}
	roles := access.New(pool, config.TenantKey, config.AppID)
	if err := roles.Bootstrap(ctx, "ou_admin"); err != nil {
		t.Fatal(err)
	}
	actor := identity.Principal{TenantKey: config.TenantKey, AppID: config.AppID, OpenID: "ou_admin"}
	if err := pool.QueryRow(ctx, "SELECT id FROM accounts WHERE open_id=$1", actor.OpenID).Scan(&actor.AccountID); err != nil {
		t.Fatal(err)
	}
	newStore := func(cfg system.Config, generation int64, audit system.AuditChange) *postgres.FeishuBot {
		return postgres.NewFeishuBot(pool, cfg, generation, 30*time.Second, roles.AuthorizeBotWrite, audit)
	}
	store := newStore(config, 1, roles.AuditBotConfig)
	if err := store.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	brokenAudit := errors.New("audit failure")
	atomicStore := newStore(config, 1, func(context.Context, pgx.Tx, identity.Principal, int64) error { return brokenAudit })
	if _, err := atomicStore.Configure(ctx, actor, `"bot-0"`, true); !errors.Is(err, brokenAudit) {
		t.Fatalf("expected audit failure, got %v", err)
	}
	if status, err := store.Status(ctx); err != nil || status.Enabled || status.Version != 0 {
		t.Fatalf("audit failure did not roll back config: %+v %v", status, err)
	}
	if _, err := store.Configure(ctx, actor, `"bot-0"`, true); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct{ age, want string }{{"5 seconds", "connected"}, {"30 seconds", "unavailable"}, {"-5 seconds", "unavailable"}} {
		exec(`UPDATE feishu_bot_configs SET connection_state='connected', last_heartbeat_at=clock_timestamp()-$1::text::interval`, test.age)
		if status, err := store.Status(ctx); err != nil || status.Connection.State != test.want {
			t.Fatalf("wrong heartbeat state at age %s: %+v %v", test.age, status, err)
		}
	}
	replica := newStore(config, 1, roles.AuditBotConfig)
	type outcome struct {
		task system.Submission
		err  error
	}
	const clients = 20
	outcomes := make(chan outcome, clients)
	var group sync.WaitGroup
	for i := range clients {
		group.Go(func() {
			backend := store
			if i%2 == 1 {
				backend = replica
			}
			task, err := backend.Submit(ctx, actor, fmt.Sprintf("concurrent-%d", i%5))
			outcomes <- outcome{task, err}
		})
	}
	group.Wait()
	close(outcomes)
	var id string
	newCount := 0
	for result := range outcomes {
		if id == "" {
			id = result.task.ID
		}
		if result.err != nil || result.task.ID == "" || result.task.ID != id {
			t.Fatalf("concurrent submission diverged: %+v %v", result.task, result.err)
		}
		if !result.task.Reused {
			newCount++
		}
	}
	if newCount != 1 {
		t.Fatalf("expected one new task, got %d", newCount)
	}
	_, err := pool.Exec(ctx, `INSERT INTO feishu_bot_checks (tenant_key,app_id,credentials_generation) VALUES ('tenant','app',1)`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" || pgErr.ConstraintName != "feishu_bot_checks_active_idx" {
		t.Fatalf("database accepted duplicate active check: %v", err)
	}
	old, err := store.Claim(ctx)
	if err != nil || old.ID != id {
		t.Fatalf("unexpected initial claim: %+v %v", old, err)
	}
	empty, err := replica.Claim(ctx)
	if err != nil || empty.ID != "" {
		t.Fatalf("valid lease was reclaimed: %+v %v", empty, err)
	}
	exec("UPDATE feishu_bot_checks SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", id)
	reclaimed, err := replica.Claim(ctx)
	if err != nil || reclaimed.ID != id || reclaimed.Version != old.Version+1 {
		t.Fatalf("lease was not recovered: %+v %v", reclaimed, err)
	}
	tokenStage := system.Result{Name: "tenant_token", State: "passed"}
	results := []system.Result{tokenStage, {Name: "bot_info", State: "passed"}}
	info := system.BotInfo{Name: "Stale", OpenID: "ou_stale"}
	if _, err := store.Renew(ctx, old, tokenStage); !errors.Is(err, system.ErrCheckNotFound) {
		t.Fatalf("old claim renewed: %v", err)
	}
	if err := store.Finish(ctx, old, results, info); !errors.Is(err, system.ErrCheckNotFound) {
		t.Fatalf("old claim finished: %v", err)
	}
	if _, err := replica.Renew(ctx, reclaimed, tokenStage); err != nil {
		t.Fatal(err)
	}
	current := newStore(config, 2, roles.AuditBotConfig)
	if err := current.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Activate(ctx); !errors.Is(err, system.ErrStaleGeneration) {
		t.Fatalf("old generation activated: %v", err)
	}
	if _, err := store.Renew(ctx, reclaimed, tokenStage); !errors.Is(err, system.ErrStaleGeneration) {
		t.Fatalf("old generation renewed: %v", err)
	}
	if err := store.Finish(ctx, reclaimed, results, info); !errors.Is(err, system.ErrStaleGeneration) {
		t.Fatalf("old generation finished: %v", err)
	}
	if oldTask, err := current.Check(ctx, id); err != nil || oldTask.State != "failed" || len(oldTask.Results) != 2 ||
		oldTask.Results[0].State != "passed" || oldTask.Results[1].Error != system.ProviderUnavailable {
		t.Fatalf("old task not correctly finalized: %+v %v", oldTask, err)
	}
	if status, err := current.Status(ctx); err != nil || status.BotName != "" || status.Connection.LastHeartbeatAt != nil {
		t.Fatalf("rotation kept stale status: %+v %v", status, err)
	}
	if replay, err := current.Submit(ctx, actor, "concurrent-0"); err != nil || !replay.Reused || replay.ID != id {
		t.Fatalf("old key did not replay: %+v %v", replay, err)
	}
	exec("UPDATE feishu_bot_checks SET finished_at=clock_timestamp()-interval '24 hours' WHERE id=$1", id)
	if fresh, err := current.Submit(ctx, actor, "concurrent-0"); err != nil || fresh.Reused || fresh.ID == id {
		t.Fatalf("expired key did not create a new check: %+v %v", fresh, err)
	}
	otherApp := newStore(system.Config{TenantKey: "tenant", AppID: "other"}, 2, roles.AuditBotConfig)
	if _, err := otherApp.Check(ctx, id); !errors.Is(err, system.ErrCheckNotFound) {
		t.Fatalf("cross-app check was visible: %v", err)
	}
}

func botTestPool(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("OMNIRELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx, backendTestPool(t, ctx, dsn)
}

type botSession struct {
	cookie *http.Cookie
	csrf   string
}

func seedBotSession(t *testing.T, ctx context.Context, pool *pgxpool.Pool, openID, role string) botSession {
	t.Helper()
	var id string
	err := pool.QueryRow(ctx, `INSERT INTO accounts (tenant_key,app_id,open_id)
		VALUES ('tenant_fixture','cli_fixture',$1) ON CONFLICT (tenant_key,app_id,open_id)
		DO UPDATE SET open_id=EXCLUDED.open_id RETURNING id`, openID).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	if role != "" {
		_, err := pool.Exec(ctx, `INSERT INTO role_grants (tenant_key,app_id,account_id,role,source,granted_by)
			SELECT tenant_key,app_id,$1,$2,'platform_admin',id FROM accounts WHERE open_id='ou_admin'`, id, role)
		if err != nil {
			t.Fatal(err)
		}
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	hash := sha256.Sum256([]byte(token))
	_, err = pool.Exec(ctx, `INSERT INTO web_sessions (token_digest,account_id,expires_at,idle_expires_at,auth_method)
		VALUES ($1,$2,now()+interval '1 hour',now()+interval '1 hour','password')`, hash[:], id)
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: "__Host-omnirelay_session", Value: token}
	return botSession{cookie: cookie}
}

func decodeBot[T any](t *testing.T, response *httptest.ResponseRecorder) T {
	t.Helper()
	var body T
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func startBotWorker(t *testing.T, ctx context.Context, worker *system.Service) {
	t.Helper()
	ctx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(ctx)
	}()
	t.Cleanup(func() {
		stop()
		<-done
	})
}

func waitForBotCheck(t *testing.T, ctx context.Context, fetch func() system.Check) system.Check {
	t.Helper()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		check := fetch()
		if check.State == "succeeded" || check.State == "failed" {
			return check
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal("check did not finish")
		}
	}
}
