package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/drive"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
	"github.com/uniquechao/OmniRelay/internal/store/postgres"
	"github.com/uniquechao/OmniRelay/internal/system"
)

type candidatePlatform struct {
	chat.Source
	chat.DeliverySource
	targets []string
	checks  []drive.Ref
}

func (*candidatePlatform) Group(_ context.Context, target string) (chat.Group, error) {
	return chat.Group{ID: target}, nil
}
func (*candidatePlatform) Members(context.Context, string, string) (chat.Page[chat.Member], error) {
	return chat.Page[chat.Member]{Items: []chat.Member{{OpenID: "ou_tester"}}}, nil
}
func (*candidatePlatform) History(context.Context, chat.HistoryQuery) (chat.Page[orchestrator.ChatMessage], error) {
	return chat.Page[orchestrator.ChatMessage]{}, nil
}
func (*candidatePlatform) Bot(context.Context) (chat.Member, error) {
	return chat.Member{OpenID: "ou_bot", Name: "Bot"}, nil
}
func (*candidatePlatform) BotInGroup(context.Context, string) error { return nil }
func (f *candidatePlatform) Deliver(_ context.Context, target, bot, kind, content, nonce string) chat.DeliveryResult {
	f.targets = append(f.targets, target)
	return chat.DeliveryResult{State: "unknown", Reason: "receipt_unknown", MessageID: "om_platform_" + target}
}
func (*candidatePlatform) ConfirmDelivery(_ context.Context, target, kind, bot, id string) bool {
	return id == "om_platform_"+target && bot == "ou_bot" && kind == "text"
}
func (f *candidatePlatform) CheckShare(_ context.Context, reference drive.Ref, chatID string) error {
	f.checks = append(f.checks, reference)
	return nil
}

func TestFeishuCandidateReceiverScopeAndReceipts(t *testing.T) {
	dsn := os.Getenv("OMNIRELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("需要隔离数据库")
	}
	ctx := t.Context()
	pool := backendTestPool(t, ctx, dsn)
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	const tenant, app, sender, project = "tenant_fixture", "cli_fixture", "ou_tester", "fixture-project"
	_, err := pool.Exec(ctx, `INSERT INTO accounts(id,tenant_key,app_id,open_id) VALUES('account',$1,$2,$3)`, tenant, app, sender)
	check(err)
	_, err = pool.Exec(ctx, `INSERT INTO project_members(tenant_key,app_id,project_id,account_id,member_id,role)
		VALUES($1,$2,$3,'account','member','editor')`, tenant, app, project)
	check(err)
	roles := access.New(pool, tenant, app)
	roles.ProjectAccess = access.LocalProjects{Pool: pool}
	bot := postgres.NewFeishuBot(pool, system.Config{AppID: app, TenantKey: tenant, CredentialsConfigured: true}, 1, 30*time.Second,
		func(context.Context, pgx.Tx, identity.Principal, string) error { return nil },
		func(context.Context, pgx.Tx, identity.Principal, int64) error { return nil })
	check(bot.Activate(ctx))
	_, err = bot.Configure(ctx, identity.Principal{}, `"bot-0"`, true)
	check(err)
	fence, err := bot.ConnectionFence(ctx)
	check(err)
	fence, err = bot.StartConnection(ctx, fence)
	check(err)
	check(bot.RenewConnection(ctx, fence))
	inbox, err := postgres.NewFeishuInbox(bot, 10)
	check(err)
	platform := &candidatePlatform{}
	chats := chat.New(pool, tenant, app, 1, platform, nil, roles.Check, chat.ReceiptSources{})
	chats.ConfigureDelivery(platform, false)
	store := postgres.NewCandidateMessages(inbox, chats.AcceptEvent, true)
	previous := receiverSourceSHA
	receiverSourceSHA = strings.Repeat("a", 40)
	t.Cleanup(func() { receiverSourceSHA = previous })
	rollback := "registry.example.test/omnirelay@sha256:" + strings.Repeat("d", 64)
	server := httptest.NewTLSServer(candidateReceiver{store: store, roles: roles, chat: chats, source: platform,
		token: "only-manager", rollback: rollback})
	defer server.Close()
	key := postgres.CandidateRunKey{Run: strings.Repeat("b", 32), SHA: strings.Repeat("c", 40), Pod: "pod"}
	call := func(request any, token, revision string, status int) receiverReply {
		t.Helper()
		body, err := json.Marshal(request)
		check(err)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, bytes.NewReader(body))
		check(err)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Omnirelay-Receiver-SHA", revision)
		req.Header.Set("X-Omnirelay-Receiver-Rollback", rollback)
		res, err := server.Client().Do(req)
		check(err)
		defer res.Body.Close()
		if res.StatusCode != status {
			data, _ := io.ReadAll(res.Body)
			t.Fatalf("received HTTP %d, want %d: %s", res.StatusCode, status, data)
		}
		var reply receiverReply
		if status == http.StatusOK {
			check(json.NewDecoder(res.Body).Decode(&reply))
			if reply.ReceiverSHA != receiverSourceSHA || reply.ReceiverRollback != rollback {
				t.Fatal("receiver revision missing")
			}
		}
		return reply
	}
	do := func(req receiverRequest, status int) receiverReply {
		t.Helper()
		req.CandidateRunKey = key
		return call(req, "only-manager", receiverSourceSHA, status)
	}
	open := receiverRequest{Operation: "open", CandidateRunKey: key, Binding: &postgres.CandidateRun{
		Tenant: tenant, App: app, Sender: sender, Project: project, GroupChat: "oc_group", PrivateChat: "oc_private",
		FileToken: "file_ok", Expires: time.Now().Add(time.Minute), Budget: 12, MayReply: true, MayReconcile: true,
	}}
	call(open, "candidate-token", receiverSourceSHA, http.StatusNotFound)
	call(open, "only-manager", strings.Repeat("d", 40), http.StatusNotFound)
	do(open, http.StatusOK)
	_, err = pool.Exec(ctx, `UPDATE feishu_candidate_message_runs SET starts_at=clock_timestamp()-interval '1 second'`)
	check(err)
	ingest := func(source, target, typ, body string) {
		t.Helper()
		at := time.Now().UTC()
		kind, peer := orchestrator.ChatGroup, ""
		if target == "oc_private" {
			kind, peer = orchestrator.ChatPrivate, sender
		}
		e := orchestrator.InboundEvent{AppID: app, TenantID: tenant, SenderOpenID: sender, ChatID: target, ChatKind: kind,
			EventID: "event-" + source, MessageID: source, Text: body, ReceivedAt: at, ProviderAt: at, Connection: fence,
			Archive: &orchestrator.ArchiveEvent{ChatID: target, Kind: kind, PeerOpenID: peer, VerifiedSender: sender,
				Message: &orchestrator.ChatMessage{ID: source, ChatID: target, SenderID: sender, Type: typ, Body: body, SentAt: at}}}
		handled, err := store.Intercept(ctx, e)
		check(err)
		if !handled {
			t.Fatal("scoped message missed the candidate queue")
		}
	}
	claim := func(id string) *orchestrator.CandidateMessage {
		t.Helper()
		item := do(receiverRequest{Operation: orchestrator.CandidateClaim}, http.StatusOK).Message
		if item == nil || item.Source != id {
			t.Fatalf("wrong queue receipt for %s", id)
		}
		return item
	}
	ack := func(item *orchestrator.CandidateMessage) {
		t.Helper()
		do(receiverRequest{Operation: orchestrator.CandidateAck, Source: item.Source, Lease: item.Lease, Revision: item.Revision}, http.StatusOK)
	}
	ingest("om_group", "oc_group", "text", `{"text":"群聊"}`)
	item := claim("om_group")
	send := receiverRequest{Operation: orchestrator.CandidateReply, Source: item.Source, Text: "候选回复"}
	do(send, http.StatusForbidden)
	ack(item)
	call(map[string]any{"Operation": orchestrator.CandidateReply, "Run": key.Run, "SHA": key.SHA, "Pod": key.Pod,
		"Source": item.Source, "Text": "越界", "Target": "oc_other"}, "only-manager", receiverSourceSHA, http.StatusBadRequest)
	if got := do(send, http.StatusOK).Delivery; got == nil || got.State != "unknown" {
		t.Fatalf("expected unknown receipt: %+v", got)
	}
	if got := do(receiverRequest{Operation: orchestrator.CandidateReceipt, Source: item.Source}, http.StatusOK).Delivery; got == nil || got.State != "success" {
		t.Fatalf("expected confirmed ledger receipt: %+v", got)
	}
	do(send, http.StatusOK)
	if len(platform.targets) != 1 || platform.targets[0] != "oc_group" {
		t.Fatalf("unexpected upstream sends %v", platform.targets)
	}

	ingest("om_private", "oc_private", "text", `{"text":"私聊"}`)
	item = claim("om_private")
	ack(item)
	_, err = pool.Exec(ctx, `UPDATE project_members SET role='reader'`)
	check(err)
	send.Source = item.Source
	do(send, http.StatusForbidden)
	_, err = pool.Exec(ctx, `UPDATE project_members SET role='editor'`)
	check(err)
	do(send, http.StatusOK)
	if len(platform.targets) != 2 || platform.targets[1] != "oc_private" {
		t.Fatalf("unexpected private delivery %v", platform.targets)
	}

	ingest("om_bad_file", "oc_group", "file", `{"file_key":"file_blocked"}`)
	item = claim("om_bad_file")
	if item.Event.MessageID != "" || item.Event.Text != "" || item.Event.Archive.Message.Body != "" {
		t.Fatal("unapproved source reached candidate")
	}
	ack(item)
	send.Source = item.Source
	do(send, http.StatusForbidden)
	if len(platform.checks) != 0 {
		t.Fatal("platform was called for an unapproved file")
	}
	do(receiverRequest{Operation: "stop", RevokeOutbound: true}, http.StatusOK)
	ingest("om_valid_file", "oc_group", "file", `{"file_key":"file_ok"}`)
	item = claim("om_valid_file")
	if len(platform.checks) != 1 || platform.checks[0].MessageID != item.Source ||
		platform.checks[0].ChatKind != "group" || item.Event.Archive.Message.Body == "" {
		t.Fatal("approved file did not inherit its source authorization")
	}
	ack(item)
	send.Source = item.Source
	do(send, http.StatusForbidden)
}
