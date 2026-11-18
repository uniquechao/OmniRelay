package contract_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
	"github.com/uniquechao/OmniRelay/internal/store/postgres"
	"github.com/uniquechao/OmniRelay/internal/system"
	"github.com/uniquechao/OmniRelay/migrations"
)

type inboxFixture struct {
	pool  *pgxpool.Pool
	bot   *postgres.FeishuBot
	inbox orchestrator.InboxAcceptor
	event orchestrator.InboundEvent
}

func newInboxFixture(t *testing.T, capacity int) inboxFixture {
	t.Helper()
	dsn := os.Getenv("OMNIRELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL")
	}
	ctx := t.Context()
	control, err := pgxpool.New(ctx, dsn)
	must(t, err)
	t.Cleanup(control.Close)
	schema := fmt.Sprintf("inbox_%d", time.Now().UnixNano())
	_, err = control.Exec(ctx, "CREATE SCHEMA "+schema)
	must(t, err)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := control.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE")
		must(t, err)
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	must(t, err)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	must(t, err)
	t.Cleanup(pool.Close)
	must(t, migrations.Apply(ctx, pool))

	bot := postgres.NewFeishuBot(pool, system.Config{AppID: "cli_fixture", TenantKey: "tenant_fixture",
		CredentialsConfigured: true}, 1, 30*time.Second,
		func(context.Context, pgx.Tx, identity.Principal, string) error { return nil },
		func(context.Context, pgx.Tx, identity.Principal, int64) error { return nil })
	must(t, bot.Activate(ctx))
	_, err = bot.Configure(ctx, identity.Principal{}, `"bot-0"`, true)
	must(t, err)
	snapshot, err := bot.ConnectionFence(ctx)
	must(t, err)
	fence, err := bot.StartConnection(ctx, snapshot)
	must(t, err)
	must(t, bot.RenewConnection(ctx, fence))

	inbox, err := postgres.NewFeishuInbox(bot, capacity)
	must(t, err)
	return inboxFixture{pool: pool, bot: bot, inbox: inbox, event: orchestrator.InboundEvent{
		AppID: "cli_fixture", TenantID: "tenant_fixture", EventID: "evt_fixture", MessageID: "om_fixture",
		ChatID: "oc_fixture", ChatKind: orchestrator.ChatGroup, ThreadID: "omt_fixture",
		ReplyToMessageID: "om_parent", SenderOpenID: "ou_member",
		ReceivedAt: time.Now().UTC().Truncate(time.Microsecond), Text: "查询项目进度", Connection: fence,
	}}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (f inboxFixture) account(t *testing.T, tenant, openID string, active bool) string {
	t.Helper()
	var id string
	err := f.pool.QueryRow(t.Context(), `INSERT INTO accounts (tenant_key,app_id,open_id,active)
		VALUES ($1,$2,$3,$4) RETURNING id`, tenant, f.event.AppID, openID, active).Scan(&id)
	must(t, err)
	return id
}

func (f inboxFixture) lastMessage(t *testing.T) *time.Time {
	t.Helper()
	status, err := f.bot.Status(t.Context())
	must(t, err)
	return status.Connection.LastMessageAt
}

func (f inboxFixture) counts(t *testing.T, expected int) {
	t.Helper()
	var events, works int
	must(t, f.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM feishu_inbound_events),
		(SELECT count(*) FROM feishu_inbound_work)`).Scan(&events, &works))
	if events != expected || works != expected {
		t.Fatalf("expected %d events and work records, got %d and %d", expected, events, works)
	}
}

func (f inboxFixture) reject(t *testing.T, event orchestrator.InboundEvent, want error) {
	t.Helper()
	result, err := f.inbox.AcceptInbound(t.Context(), event)
	if result.WorkID != "" || !errors.Is(err, want) {
		t.Fatalf("received %+v, %v; expected empty receipt and %v", result, err, want)
	}
}

func TestFeishuInboxConcurrentDuplicateBeforeCapacity(t *testing.T) {
	f := newInboxFixture(t, 1)
	account := f.account(t, f.event.TenantID, f.event.SenderOpenID, true)
	const attempts = 16
	type reply struct {
		result orchestrator.AcceptResult
		err    error
	}
	start := make(chan struct{})
	replies := make(chan reply, attempts)
	var workers sync.WaitGroup
	for range attempts {
		workers.Go(func() {
			<-start
			result, err := f.inbox.AcceptInbound(t.Context(), f.event)
			replies <- reply{result, err}
		})
	}
	close(start)
	workers.Wait()
	close(replies)

	var first orchestrator.WorkID
	for response := range replies {
		must(t, response.err)
		if first == "" {
			first = response.result.WorkID
		}
		if first == "" || response.result.WorkID != first {
			t.Fatalf("inconsistent receipt: %v and %v", first, response.result.WorkID)
		}
	}
	f.counts(t, 1)
	var id, actor, state, eventID, chatID, kind, thread, parent, sender, body string
	var received time.Time
	must(t, f.pool.QueryRow(t.Context(), `SELECT w.id,w.account_id,w.state,e.event_id,e.chat_id,e.chat_kind,
		e.thread_id,e.reply_to_message_id,e.sender_open_id,e.received_at,e.body
		FROM feishu_inbound_work w JOIN feishu_inbound_events e USING (app_id,tenant_id,message_id)`).
		Scan(&id, &actor, &state, &eventID, &chatID, &kind, &thread, &parent, &sender, &received, &body))
	e := f.event
	if id != string(first) || actor != account || state != "pending_route" || eventID != e.EventID ||
		chatID != e.ChatID || kind != string(e.ChatKind) || thread != e.ThreadID ||
		parent != e.ReplyToMessageID || sender != e.SenderOpenID || !received.Equal(e.ReceivedAt) || body != e.Text {
		t.Fatal("persisted fields differ from the initial event and enabled account")
	}
	last := f.lastMessage(t)
	if last == nil || last.Before(e.ReceivedAt) {
		t.Fatal("successful receipt must record a receive time")
	}
	e.EventID, e.Text = "evt_duplicate", "changed duplicate"
	repeated, err := f.inbox.AcceptInbound(t.Context(), e)
	must(t, err)
	if repeated.WorkID != first {
		t.Fatal("new event ID must not change the original work ID")
	}
	e.MessageID = "om_over_capacity"
	f.reject(t, e, orchestrator.ErrInboxFull)
	f.counts(t, 1)
	if current := f.lastMessage(t); current == nil || !current.Equal(*last) {
		t.Fatal("replay or capacity rejection changed receive time")
	}
}

func TestFeishuInboxAdmissionBoundary(t *testing.T) {
	f := newInboxFixture(t, 1)
	ctx := t.Context()
	f.account(t, f.event.TenantID, "ou_disabled", false)
	f.account(t, "other_tenant", "ou_foreign", true)
	for _, sender := range []string{"ou_unprovisioned", "ou_disabled", "ou_foreign"} {
		e := f.event
		e.MessageID, e.SenderOpenID = sender, sender
		receipt, err := f.inbox.AcceptInbound(ctx, e)
		must(t, err)
		var account *string
		var state string
		must(t, f.pool.QueryRow(ctx, `SELECT account_id,state FROM feishu_inbound_work WHERE id=$1`,
			receipt.WorkID).Scan(&account, &state))
		if account != nil || state != "admission_denied" {
			t.Fatalf("unexpected admission for %s", sender)
		}
	}
	f.counts(t, 3)
	last := f.lastMessage(t)
	if last == nil {
		t.Fatal("a committed trusted event proves receipt even when admission is denied")
	}

	e := f.event
	e.TenantID = "other_tenant"
	f.reject(t, e, orchestrator.ErrInvalidInbound)
	e = f.event
	e.AppID = "other_app"
	f.reject(t, e, orchestrator.ErrInvalidInbound)
	e = f.event
	e.ChatKind = "unsupported"
	f.reject(t, e, orchestrator.ErrInvalidInbound)
	f.counts(t, 3)
	var accounts int
	must(t, f.pool.QueryRow(ctx, "SELECT count(*) FROM accounts").Scan(&accounts))
	if accounts != 2 {
		t.Fatal("inbound acceptance changed the account inventory")
	}
	if current := f.lastMessage(t); current == nil || !current.Equal(*last) {
		t.Fatal("rejected source or invalid event changed receive time")
	}

	expected := f.account(t, f.event.TenantID, f.event.SenderOpenID, true)
	webAccount := feishuWebAccount(t, f)
	receipt, err := f.inbox.AcceptInbound(ctx, f.event)
	must(t, err)
	var botAccount string
	must(t, f.pool.QueryRow(ctx, "SELECT account_id FROM feishu_inbound_work WHERE id=$1", receipt.WorkID).Scan(&botAccount))
	if botAccount != expected || webAccount != expected {
		t.Fatal("OAuth session and Bot work must use the same preprovisioned account")
	}
	f.counts(t, 4)
}

type oauthFixtureTransport struct {
	event orchestrator.InboundEvent
}

func (f oauthFixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body string
	switch req.Method {
	case http.MethodPost:
		body = `{"code":0,"access_token":"fixture-token","token_type":"Bearer"}`
	case http.MethodGet:
		body = fmt.Sprintf(`{"code":0,"data":{"tenant_key":%q,"open_id":%q,"name":"Fixture member"}}`,
			f.event.TenantID, f.event.SenderOpenID)
	default:
		return nil, errors.New("unexpected OAuth request method")
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func feishuWebAccount(t *testing.T, f inboxFixture) string {
	t.Helper()
	svc, err := identity.New(f.pool, identity.Config{
		AppID: f.event.AppID, AppSecret: "fixture-secret", TenantKey: f.event.TenantID,
		PublicOrigin: "https://relay.example.com", Transport: oauthFixtureTransport{event: f.event},
	})
	must(t, err)
	mux := http.NewServeMux()
	svc.Register(mux)
	request := func(path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
		for _, cookie := range cookies {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	start := request("/auth/feishu/start?return_to=" + url.QueryEscape("/#/workbench"))
	auth, err := url.Parse(start.Header().Get("Location"))
	must(t, err)
	callback := request("/auth/feishu/callback?code=fixture-code&state="+url.QueryEscape(auth.Query().Get("state")),
		start.Result().Cookies()...)
	if callback.Code != http.StatusSeeOther || callback.Header().Get("Location") != "/#/workbench" {
		t.Fatalf("OAuth callback did not start a session: status %d", callback.Code)
	}
	me := request("/api/v1/me", callback.Result().Cookies()...)
	if me.Code != http.StatusOK {
		t.Fatalf("OAuth session lookup failed: status %d", me.Code)
	}
	var profile struct {
		Principal identity.Principal `json:"principal"`
	}
	must(t, json.Unmarshal(me.Body.Bytes(), &profile))
	return profile.Principal.AccountID
}

func TestFeishuInboxCommitFailureRollsBack(t *testing.T) {
	f := newInboxFixture(t, 1)
	ctx := t.Context()
	f.account(t, f.event.TenantID, f.event.SenderOpenID, true)
	_, err := f.pool.Exec(ctx, `CREATE FUNCTION fail_inbox_commit() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'fixture commit failure'; END $$;
		CREATE CONSTRAINT TRIGGER fail_inbox_commit AFTER INSERT ON feishu_inbound_work
		DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_inbox_commit();`)
	must(t, err)
	f.reject(t, f.event, orchestrator.ErrInboxUnavailable)
	f.counts(t, 0)
	if f.lastMessage(t) != nil {
		t.Fatal("transaction failure published a receive time")
	}
	_, err = f.pool.Exec(ctx, "DROP TRIGGER fail_inbox_commit ON feishu_inbound_work")
	must(t, err)
	_, err = f.inbox.AcceptInbound(ctx, f.event)
	must(t, err)
	f.counts(t, 1)
}

func TestFeishuInboxConnectionFences(t *testing.T) {
	f := newInboxFixture(t, 2)
	ctx := t.Context()
	f.account(t, f.event.TenantID, f.event.SenderOpenID, true)
	first, err := f.inbox.AcceptInbound(ctx, f.event)
	must(t, err)

	// Hold the transaction that installs a new generation open until the old
	// callback is demonstrably waiting for the row lock.
	tx, pid := lockTransaction(t, f.pool)
	_, err = tx.Exec(ctx, `UPDATE feishu_bot_configs SET connection_generation=connection_generation+1,
		last_message_at=NULL WHERE tenant_key=$1 AND app_id=$2`, f.event.TenantID, f.event.AppID)
	must(t, err)
	result := acceptWhileBlocked(t, f, f.event, pid)
	must(t, tx.Commit(ctx))
	if err := <-result; !errors.Is(err, orchestrator.ErrStaleConnection) {
		t.Fatalf("late callback against replaced generation: %v", err)
	}
	if f.lastMessage(t) != nil {
		t.Fatal("old-generation callback populated new receive time")
	}

	snapshot, err := f.bot.ConnectionFence(ctx)
	must(t, err)
	current, err := f.bot.StartConnection(ctx, snapshot)
	must(t, err)
	if _, err := f.bot.StartConnection(ctx, snapshot); !errors.Is(err, orchestrator.ErrStaleConnection) {
		t.Fatalf("stale activation reused its snapshot: %v", err)
	}
	if err := f.bot.EndConnection(ctx, f.event.Connection); !errors.Is(err, orchestrator.ErrStaleConnection) {
		t.Fatalf("stale holder ended the current connection: %v", err)
	}

	e := f.event
	e.Connection = current
	duplicate, err := f.inbox.AcceptInbound(ctx, e)
	must(t, err)
	if duplicate != first || f.lastMessage(t) != nil {
		t.Fatal("historical replay must keep its receipt and leave receive time empty")
	}
	e.MessageID = "om_current"
	staleVersion := e
	staleVersion.Connection.ConfigVersion--
	f.reject(t, staleVersion, orchestrator.ErrStaleConnection)
	_, err = f.inbox.AcceptInbound(ctx, e)
	must(t, err)
	if f.lastMessage(t) == nil {
		t.Fatal("new valid event did not populate current generation receive time")
	}
	next, err := f.bot.StartConnection(ctx, current)
	must(t, err)
	if f.lastMessage(t) != nil {
		t.Fatal("new activation retained the previous generation's receive time")
	}
	e.Connection = next
	e.MessageID = "om_expired"
	_, err = f.pool.Exec(ctx, "UPDATE feishu_bot_configs SET connection_lease_until=clock_timestamp()")
	must(t, err)
	f.reject(t, e, orchestrator.ErrStaleConnection)
	if err := f.bot.RenewConnection(ctx, next); !errors.Is(err, orchestrator.ErrStaleConnection) {
		t.Fatalf("expired lease was renewed: %v", err)
	}
	must(t, f.bot.EndConnection(ctx, next))
	f.counts(t, 2)
}

func TestFeishuInboxConcurrentDeactivationAndDisable(t *testing.T) {
	f := newInboxFixture(t, 2)
	ctx := t.Context()
	account := f.account(t, f.event.TenantID, f.event.SenderOpenID, true)

	tx, pid := lockTransaction(t, f.pool)
	_, err := tx.Exec(ctx, "UPDATE accounts SET active=false WHERE id=$1", account)
	must(t, err)
	result := acceptWhileBlocked(t, f, f.event, pid)
	must(t, tx.Commit(ctx))
	must(t, <-result)
	var state string
	var boundAccount *string
	must(t, f.pool.QueryRow(ctx, "SELECT state,account_id FROM feishu_inbound_work").Scan(&state, &boundAccount))
	if state != "admission_denied" || boundAccount != nil {
		t.Fatal("concurrent deactivation granted an executable identity")
	}

	tx, pid = lockTransaction(t, f.pool)
	_, err = tx.Exec(ctx, `UPDATE feishu_bot_configs SET enabled=false,version=version+1,
		connection_generation=connection_generation+1,connection_lease_until=NULL,last_message_at=NULL`)
	must(t, err)
	e := f.event
	e.MessageID = "om_late"
	result = acceptWhileBlocked(t, f, e, pid)
	must(t, tx.Commit(ctx))
	if err := <-result; !errors.Is(err, orchestrator.ErrStaleConnection) {
		t.Fatalf("late callback against disabled Bot: %v", err)
	}
	f.reject(t, f.event, orchestrator.ErrStaleConnection)
	f.counts(t, 1)
	if f.lastMessage(t) != nil {
		t.Fatal("disabled Bot published a receive time")
	}
}

func TestFeishuInboxLeaseExpiresWhileResolvingAccount(t *testing.T) {
	f := newInboxFixture(t, 1)
	ctx := t.Context()
	account := f.account(t, f.event.TenantID, f.event.SenderOpenID, true)
	var expiry time.Time
	must(t, f.pool.QueryRow(ctx, `UPDATE feishu_bot_configs
		SET connection_lease_until=clock_timestamp()+interval '1 second'
		RETURNING connection_lease_until`).Scan(&expiry))
	tx, pid := lockTransaction(t, f.pool)
	_, err := tx.Exec(ctx, "UPDATE accounts SET updated_at=clock_timestamp() WHERE id=$1", account)
	must(t, err)
	result := acceptWhileBlocked(t, f, f.event, pid)
	_, err = f.pool.Exec(ctx, "SELECT pg_sleep_until($1)", expiry)
	must(t, err)
	must(t, tx.Commit(ctx))
	if err := <-result; !errors.Is(err, orchestrator.ErrStaleConnection) {
		t.Fatalf("callback accepted with a lease that expired during admission: %v", err)
	}
	f.counts(t, 0)
	if f.lastMessage(t) != nil {
		t.Fatal("expired holder populated receive time")
	}
}

func lockTransaction(t *testing.T, pool *pgxpool.Pool) (pgx.Tx, int32) {
	t.Helper()
	tx, err := pool.Begin(t.Context())
	must(t, err)
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	var pid int32
	must(t, tx.QueryRow(t.Context(), "SELECT pg_backend_pid()").Scan(&pid))
	return tx, pid
}

func acceptWhileBlocked(t *testing.T, f inboxFixture, event orchestrator.InboundEvent, pid int32) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := f.inbox.AcceptInbound(t.Context(), event)
		done <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var blocked bool
		must(t, f.pool.QueryRow(t.Context(), `SELECT EXISTS(
			SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&blocked))
		if blocked {
			return done
		}
		select {
		case err := <-done:
			t.Fatalf("callback completed without waiting on PostgreSQL: %v", err)
		case <-time.After(5 * time.Millisecond):
		}
	}
	t.Fatal("callback did not wait on the expected PostgreSQL transaction")
	return nil
}

func TestFeishuConnectionTransitionStateAndFencing(t *testing.T) {
	f := newInboxFixture(t, 2)
	ctx := t.Context()
	state := func(want string) {
		t.Helper()
		status, err := f.bot.Status(ctx)
		must(t, err)
		if status.Connection.State != want {
			t.Fatalf("got %s, want %s", status.Connection.State, want)
		}
	}
	state("connected")
	old := f.event.Connection
	must(t, f.bot.MarkReconnecting(ctx, old))
	state("reconnecting")
	f.reject(t, f.event, orchestrator.ErrStaleConnection)
	if err := f.bot.RenewConnection(ctx, old); !errors.Is(err, orchestrator.ErrStaleConnection) {
		t.Fatalf("disconnected session renewed its heartbeat: %v", err)
	}

	current, err := f.bot.StartConnection(ctx, old)
	must(t, err)
	state("connecting")
	must(t, f.bot.KeepConnecting(ctx, current))
	if err := f.bot.MarkReconnecting(ctx, old); !errors.Is(err, orchestrator.ErrStaleConnection) {
		t.Fatalf("old holder overwrote current generation: %v", err)
	}
	must(t, f.bot.RenewConnection(ctx, current))
	state("connected")
	_, err = f.pool.Exec(ctx, `UPDATE feishu_bot_configs
		SET last_heartbeat_at=clock_timestamp()-interval '31 seconds', connection_lease_until=clock_timestamp()`)
	must(t, err)
	state("unavailable")
	_, err = f.bot.Configure(ctx, identity.Principal{}, `"bot-1"`, false)
	must(t, err)
	state("disabled")
	var persisted string
	must(t, f.pool.QueryRow(ctx, "SELECT connection_state FROM feishu_bot_configs").Scan(&persisted))
	if persisted != "disabled" {
		t.Fatalf("persisted state = %s", persisted)
	}
}
