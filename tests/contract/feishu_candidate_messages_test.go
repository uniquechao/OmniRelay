package contract_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
	"github.com/uniquechao/OmniRelay/internal/store/postgres"
	"github.com/uniquechao/OmniRelay/internal/system"
)

func messageStore(f inboxFixture, armed bool) (*postgres.CandidateMessages, *chat.Service) {
	archive := chat.New(f.pool, f.event.TenantID, f.event.AppID, 1, nil, nil, nil, chat.ReceiptSources{})
	return postgres.NewCandidateMessages(f.inbox.(*postgres.FeishuInbox), archive.AcceptEvent, armed), archive
}

func candidateEvent(f inboxFixture, id, target string, kind orchestrator.ChatKind) orchestrator.InboundEvent {
	e := f.event
	e.MessageID, e.EventID, e.ChatID, e.ChatKind = id, "event-"+id, target, kind
	e.ReceivedAt = time.Now().UTC().Truncate(time.Millisecond)
	e.ProviderAt = e.ReceivedAt
	e.Archive = &orchestrator.ArchiveEvent{
		ChatID: target, Kind: kind, EventID: e.EventID, VerifiedSender: e.SenderOpenID, ProviderAt: e.ProviderAt,
		Message: &orchestrator.ChatMessage{ID: id, ChatID: target, SenderID: e.SenderOpenID, SenderKind: "user",
			Type: "text", Body: `{"text":"候选消息"}`, SentAt: e.ProviderAt},
	}
	if kind == orchestrator.ChatPrivate {
		e.Archive.PeerOpenID = e.SenderOpenID
	}
	return e
}

func recallCandidateEvent(e orchestrator.InboundEvent) orchestrator.InboundEvent {
	a, m := *e.Archive, *e.Archive.Message
	m.Body, m.Deleted = "", true
	a.Kind, a.EventID, a.Message = "", "recall-"+e.EventID, &m
	e.MessageID, e.EventID, e.Text, e.Archive = "", "", "", &a
	return e
}

func TestFeishuCandidateQueueRecoveryAndOwnership(t *testing.T) {
	prod, candidate := newInboxFixture(t, 20), newInboxFixture(t, 20)
	ctx := t.Context()
	prod.account(t, prod.event.TenantID, prod.event.SenderOpenID, true)
	candidateID := candidate.account(t, candidate.event.TenantID, candidate.event.SenderOpenID, true)
	store, archive := messageStore(prod, true)
	local, _ := messageStore(candidate, false)
	key := postgres.CandidateRunKey{Run: strings.Repeat("a", 32), SHA: strings.Repeat("c", 40), Pod: "candidate-pod"}
	run := postgres.CandidateRun{CandidateRunKey: key, App: prod.event.AppID, Tenant: prod.event.TenantID,
		Sender: prod.event.SenderOpenID, Project: "project", GroupChat: prod.event.ChatID, PrivateChat: "oc_private",
		Expires: time.Now().Add(time.Hour), Budget: 2, MayReply: true, MayReconcile: true}
	accept := func(e orchestrator.InboundEvent, wantCandidate bool) {
		t.Helper()
		handled, err := store.Intercept(ctx, e)
		must(t, err)
		if handled != wantCandidate {
			t.Fatalf("ownership of %s: got %t; want %t", e.EventID, handled, wantCandidate)
		}
		if !handled && e.MessageID != "" {
			_, err = prod.inbox.AcceptInbound(ctx, e)
			must(t, err)
		}
		if !handled && e.Archive != nil {
			must(t, prod.bot.AcceptChatArchive(ctx, e, archive.AcceptEvent))
		}
	}
	ordinary := candidateEvent(prod, "om_before", run.GroupChat, orchestrator.ChatGroup)
	accept(ordinary, false)
	must(t, store.Open(ctx, run))
	_, err := prod.pool.Exec(ctx, `UPDATE feishu_candidate_message_runs SET starts_at=clock_timestamp()-interval '10 seconds'`)
	must(t, err)
	accept(ordinary, false)
	accept(candidateEvent(prod, "om_unrelated", "oc_unrelated", orchestrator.ChatGroup), false)
	wrongSender := candidateEvent(prod, "om_wrong_sender", run.GroupChat, orchestrator.ChatGroup)
	wrongSender.SenderOpenID = "ou_other"
	accept(wrongSender, false)
	e := candidateEvent(prod, "om_group", run.GroupChat, orchestrator.ChatGroup)

	_, err = prod.pool.Exec(ctx, `ALTER TABLE feishu_candidate_message_queue ADD CONSTRAINT crash CHECK (false) NOT VALID`)
	must(t, err)
	if _, err := store.Intercept(ctx, e); !errors.Is(err, orchestrator.ErrInboxUnavailable) {
		t.Fatalf("failed queue persist should fail the callback: %v", err)
	}
	prod.counts(t, 3)
	_, err = prod.pool.Exec(ctx, `ALTER TABLE feishu_candidate_message_queue DROP CONSTRAINT crash`)
	must(t, err)
	// No candidate is running when the receiver commits.
	accept(e, true)
	accept(e, true)
	accept(candidateEvent(prod, "om_private", run.PrivateChat, orchestrator.ChatPrivate), true)
	archiveOnly := candidateEvent(prod, "om_archive", run.GroupChat, orchestrator.ChatGroup)
	archiveOnly.MessageID = ""
	accept(archiveOnly, true)
	prod.counts(t, 5)
	var productionReady int
	must(t, prod.pool.QueryRow(ctx, `SELECT count(*) FROM feishu_inbound_work WHERE state='agent_disabled'`).Scan(&productionReady))
	if productionReady != 2 {
		t.Fatalf("candidate work entered the production route: %d", productionReady)
	}

	disallowed := key
	disallowed.Pod = "other-pod"
	item, _, err := store.Claim(ctx, disallowed)
	must(t, err)
	if item != nil {
		t.Fatal("wrong candidate received a message")
	}
	claim := func() *orchestrator.CandidateMessage {
		t.Helper()
		item, _, err := store.Claim(ctx, key)
		must(t, err)
		if item == nil {
			t.Fatal("expected a queued message")
		}
		return item
	}
	first := claim()
	if first.Source != e.MessageID {
		t.Fatalf("unexpected first message: %s", first.Source)
	}
	accept(candidateEvent(prod, "om_during_claim", "oc_unrelated", orchestrator.ChatGroup), false)
	deny := func(context.Context, pgx.Tx, identity.Principal, string) (bool, error) { return false, nil }
	if err := local.AcceptCandidateMessage(ctx, *first, deny); !errors.Is(err, postgres.ErrCandidateScope) {
		t.Fatalf("local role check returned %v", err)
	}
	allow := func(_ context.Context, _ pgx.Tx, actor identity.Principal, project string) (bool, error) {
		return actor.AccountID == candidateID && project == run.Project, nil
	}
	expiredLease := *first
	expiredLease.LeaseExpires = time.Now().Add(-time.Second)
	if err := local.AcceptCandidateMessage(ctx, expiredLease, allow); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired lease did not fence local acceptance: %v", err)
	}
	must(t, local.AcceptCandidateMessage(ctx, *first, allow))
	// A replacement worker reclaims after the first committed locally and
	// stopped before acknowledgement.
	_, err = prod.pool.Exec(ctx, `UPDATE feishu_candidate_message_queue SET lease_until=clock_timestamp()-interval '1 second' WHERE source_id=$1`, first.Source)
	must(t, err)
	store, _ = messageStore(prod, true)
	local, _ = messageStore(candidate, false)
	reclaimed := claim()
	must(t, local.AcceptCandidateMessage(ctx, *reclaimed, allow))
	candidate.counts(t, 1)
	if reclaimed.Lease == first.Lease || !errors.Is(store.Ack(ctx, key, first.Source, first.Lease, first.Revision), postgres.ErrCandidateScope) {
		t.Fatal("obsolete lease retained acknowledgement authority")
	}
	must(t, store.Ack(ctx, key, reclaimed.Source, reclaimed.Lease, reclaimed.Revision))
	must(t, store.Ack(ctx, key, reclaimed.Source, reclaimed.Lease, reclaimed.Revision))
	must(t, store.ReserveOutbound(ctx, key, first.Source, true))
	must(t, store.WithOutbound(ctx, key, first.Source, true, func(out postgres.CandidateOutbound) error {
		if out.Chat != run.GroupChat || out.Actor.OpenID != run.Sender {
			t.Fatal("outbound target or actor differs from the admitted source")
		}
		return nil
	}))
	if !errors.Is(store.ReserveOutbound(ctx, key, "om_unrelated", true), postgres.ErrCandidateScope) {
		t.Fatal("unrelated work was granted outbound authority")
	}
	must(t, store.ReserveOutbound(ctx, key, first.Source, false))
	if !errors.Is(store.ReserveOutbound(ctx, key, first.Source, true), postgres.ErrCandidateScope) {
		t.Fatal("retry did not consume the outbound budget")
	}
	for _, id := range []string{"om_private", "om_archive"} {
		item := claim()
		if item.Source != id {
			t.Fatalf("unexpected source %s, want %s", item.Source, id)
		}
		must(t, local.AcceptCandidateMessage(ctx, *item, allow))
		must(t, store.Ack(ctx, key, item.Source, item.Lease, item.Revision))
	}
	candidate.counts(t, 2)
	// The archive has both commands and the archive-only message.
	var archived int
	must(t, candidate.pool.QueryRow(ctx, `SELECT count(*) FROM feishu_chat_messages`).Scan(&archived))
	if archived != 3 {
		t.Fatalf("archive count = %d", archived)
	}
	accept(recallCandidateEvent(e), true)
	recalled := claim()
	must(t, local.AcceptCandidateMessage(ctx, *recalled, deny))
	must(t, store.Ack(ctx, key, recalled.Source, recalled.Lease, recalled.Revision))
	var state, body string
	must(t, candidate.pool.QueryRow(ctx, `SELECT state,body FROM feishu_inbound_work JOIN feishu_inbound_events USING(app_id,tenant_id,message_id)
		WHERE message_id=$1`, first.Source).Scan(&state, &body))
	if state != "candidate_cancelled" || body != "" {
		t.Fatal("recalled work retains a usable body")
	}
	reordered := candidateEvent(prod, "om_reordered", run.GroupChat, orchestrator.ChatGroup)
	accept(recallCandidateEvent(reordered), false)
	accept(reordered, true)
	tombstone := claim()
	if tombstone.Event.MessageID != "" || tombstone.Event.Text != "" || !tombstone.Event.Archive.Message.Deleted {
		t.Fatal("late original message superseded its recall")
	}
	must(t, local.AcceptCandidateMessage(ctx, *tombstone, deny))
	must(t, store.Ack(ctx, key, tombstone.Source, tombstone.Lease, tombstone.Revision))
	candidate.counts(t, 2)
	accept(candidateEvent(prod, "om_pending", run.GroupChat, orchestrator.ChatGroup), true)
	must(t, store.Stop(ctx, key, true, false))
	item, _, err = store.Claim(ctx, key)
	must(t, err)
	if item != nil {
		t.Fatal("closed transfer released pending data")
	}
	must(t, store.Stop(ctx, key, false, true))
	store, _ = messageStore(prod, false)
	accept(e, true)
	accept(candidateEvent(prod, "om_after", run.GroupChat, orchestrator.ChatGroup), false)
}

func TestFeishuCandidateQueueExpiredRunKeepsLateOwnership(t *testing.T) {
	f := newInboxFixture(t, 5)
	ctx := t.Context()
	f.account(t, f.event.TenantID, f.event.SenderOpenID, true)
	store, _ := messageStore(f, true)
	key := postgres.CandidateRunKey{Run: strings.Repeat("e", 32), SHA: strings.Repeat("a", 40), Pod: "candidate-pod"}
	run := postgres.CandidateRun{CandidateRunKey: key, App: f.event.AppID, Tenant: f.event.TenantID,
		Sender: f.event.SenderOpenID, Project: "project", GroupChat: f.event.ChatID,
		Expires: time.Now().Add(time.Hour), Budget: 1, MayReply: true}
	must(t, store.Open(ctx, run))
	_, err := f.pool.Exec(ctx, `UPDATE feishu_candidate_message_runs SET starts_at=clock_timestamp()-interval '20 minutes',
		expires_at=clock_timestamp()-interval '1 minute'`)
	must(t, err)
	e := candidateEvent(f, "om_late", run.GroupChat, orchestrator.ChatGroup)
	e.ProviderAt = time.Now().Add(-5 * time.Minute)
	handled, err := store.Intercept(ctx, e)
	must(t, err)
	if !handled {
		t.Fatal("late test message reached ordinary processing")
	}
	item, _, err := store.Claim(ctx, key)
	must(t, err)
	if item != nil || !errors.Is(store.ReserveOutbound(ctx, key, e.MessageID, true), postgres.ErrCandidateScope) {
		t.Fatal("expired run retained receive or send authority")
	}
	var frozen bool
	must(t, f.pool.QueryRow(ctx, `SELECT r.expires_at<=clock_timestamp() AND q.ack_revision<q.revision
		FROM feishu_candidate_message_queue q JOIN feishu_candidate_message_runs r ON q.run_id=r.id WHERE q.source_id=$1`, e.MessageID).Scan(&frozen))
	if !frozen {
		t.Fatal("late test message left no frozen queue record")
	}
}

func TestFeishuCandidateQueueCredentialRotationKeepsLateOwnership(t *testing.T) {
	f := newInboxFixture(t, 5)
	ctx := t.Context()
	f.account(t, f.event.TenantID, f.event.SenderOpenID, true)
	old, archive := messageStore(f, true)
	key := postgres.CandidateRunKey{Run: strings.Repeat("d", 32), SHA: strings.Repeat("a", 40), Pod: "candidate-pod"}
	run := postgres.CandidateRun{CandidateRunKey: key, App: f.event.AppID, Tenant: f.event.TenantID,
		Sender: f.event.SenderOpenID, Project: "project", GroupChat: f.event.ChatID,
		Expires: time.Now().Add(time.Hour), Budget: 1, MayReply: true}
	must(t, old.Open(ctx, run))
	original := candidateEvent(f, "om_before_rotation", run.GroupChat, orchestrator.ChatGroup)
	original.ProviderAt = time.Now()
	handled, err := old.Intercept(ctx, original)
	must(t, err)
	if !handled {
		t.Fatal("test message missed the old run")
	}
	claim, _, err := old.Claim(ctx, key)
	must(t, err)
	if claim == nil {
		t.Fatal("expected claim before rotation")
	}
	bot := postgres.NewFeishuBot(f.pool, system.Config{AppID: run.App, TenantKey: run.Tenant}, 2, 30*time.Second, nil, nil)
	must(t, bot.Activate(ctx))
	if err := old.Ack(ctx, key, claim.Source, claim.Lease, claim.Revision); !errors.Is(err, postgres.ErrCandidateScope) {
		t.Fatalf("old receiver confirmed after credential rotation: %v", err)
	}
	fence, err := bot.ConnectionFence(ctx)
	must(t, err)
	fence, err = bot.StartConnection(ctx, fence)
	must(t, err)
	must(t, bot.RenewConnection(ctx, fence))
	inbox, err := postgres.NewFeishuInbox(bot, 5)
	must(t, err)
	current := postgres.NewCandidateMessages(inbox, archive.AcceptEvent, true)
	late := candidateEvent(f, "om_late_after_rotation", run.GroupChat, orchestrator.ChatGroup)
	late.ProviderAt, late.Connection = original.ProviderAt, fence
	handled, err = current.Intercept(ctx, late)
	must(t, err)
	if !handled {
		t.Fatal("delayed message from the old window fell through to production")
	}
	fresh := candidateEvent(f, "om_after_rotation", run.GroupChat, orchestrator.ChatGroup)
	fresh.ProviderAt, fresh.Connection = time.Now(), fence
	handled, err = current.Intercept(ctx, fresh)
	must(t, err)
	if handled {
		t.Fatal("old run intercepted a message sent after rotation")
	}
}
