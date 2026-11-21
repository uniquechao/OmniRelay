package postgres

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
)

type CandidateMessages struct {
	inbox   *FeishuInbox
	archive func(context.Context, pgx.Tx, orchestrator.ArchiveEvent) error
	armed   bool
}

func NewCandidateMessages(inbox *FeishuInbox, archive func(context.Context, pgx.Tx, orchestrator.ArchiveEvent) error, armed bool) *CandidateMessages {
	return &CandidateMessages{inbox: inbox, archive: archive, armed: armed}
}

func candidateSource(e orchestrator.InboundEvent) (source, chat, sender string, kind orchestrator.ChatKind, provider time.Time, recall bool) {
	if e.MessageID != "" {
		return e.MessageID, e.ChatID, e.SenderOpenID, e.ChatKind, e.ProviderAt, false
	}
	if a := e.Archive; a != nil && !a.GroupMembersChanged {
		if a.Message != nil {
			return a.Message.ID, a.ChatID, a.VerifiedSender, a.Kind, a.ProviderAt, a.Message.Deleted
		}
		if a.EventID != "" {
			return "entry:" + a.EventID, a.ChatID, a.VerifiedSender, a.Kind, a.ProviderAt, false
		}
	}
	return "", "", "", "", time.Time{}, false
}

// Intercept shares the connection-generation lock with the ordinary inbox and
// archive. Classification and placement are local to the production database.
func (s *CandidateMessages) Intercept(ctx context.Context, e orchestrator.InboundEvent) (bool, error) {
	bot := s.inbox.bot
	source, chat, sender, kind, provider, recall := candidateSource(e)
	if source == "" || e.AppID != bot.config.AppID || e.TenantID != bot.config.TenantKey {
		return false, nil
	}
	var run *string
	err := bot.pool.QueryRow(ctx, `SELECT run_id FROM feishu_candidate_event_owners
		WHERE (tenant_key,app_id,source_id)=($1,$2,$3)`, e.TenantID, e.AppID, source).Scan(&run)
	if err == nil && run == nil {
		return false, nil
	}
	if errors.Is(err, pgx.ErrNoRows) && !s.armed {
		return false, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return true, orchestrator.ErrInboxUnavailable
	}
	if run == nil && (recall || provider.IsZero() || sender == "") {
		return false, nil
	}
	var handled bool
	err = pgx.BeginFunc(ctx, bot.pool, func(tx pgx.Tx) error {
		if err := bot.lockGeneration(ctx, tx); err != nil {
			return connectionError(err)
		}
		if err := checkLiveConnection(ctx, tx, e); err != nil {
			return err
		}
		var storedChat string
		err := tx.QueryRow(ctx, `SELECT run_id,chat_id FROM feishu_candidate_event_owners
			WHERE (tenant_key,app_id,source_id)=($1,$2,$3)`, e.TenantID, e.AppID, source).Scan(&run, &storedChat)
		switch {
		case err == nil:
			if run == nil {
				return nil
			}
			handled = true
			if storedChat != chat {
				return orchestrator.ErrInvalidInbound
			}
			if !recall {
				return nil
			}
			return s.recall(ctx, tx, e, *run, source)
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		case recall || !s.armed:
			return nil
		}

		// A previously admitted production command keeps its original owner.
		// Match provider time against the rule window even if delivery arrives
		// late. Such a row stays traceable under the expired run.
		err = tx.QueryRow(ctx, `SELECT r.id FROM feishu_candidate_message_runs r
			WHERE (r.tenant_key,r.app_id,r.sender_open_id)=($1,$2,$3)
			AND (($4='group' AND r.group_chat=$5) OR ($4='p2p' AND r.private_chat=$5))
			AND $6>=r.starts_at AND $6<LEAST(r.expires_at,coalesce(r.transfer_stopped_at,r.expires_at))
			AND NOT EXISTS (SELECT FROM feishu_inbound_events i WHERE (i.tenant_id,i.app_id,i.message_id)=($1,$2,$7))
			AND NOT EXISTS (SELECT FROM feishu_chat_messages m WHERE (m.tenant_key,m.app_id,m.message_id)=($1,$2,$7) AND m.sdk_received)
			ORDER BY r.starts_at DESC LIMIT 1`, e.TenantID, e.AppID, sender, kind, chat, provider, source).Scan(&run)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if run == nil {
			return nil
		}
		if _, err := tx.Exec(ctx, `INSERT INTO feishu_candidate_event_owners
			(tenant_key,app_id,source_id,chat_id,run_id) VALUES($1,$2,$3,$4,$5)`, e.TenantID, e.AppID, source, chat, run); err != nil {
			return err
		}
		handled = true
		var account *string
		id, err := identity.ResolveFeishuAccount(ctx, tx, e.TenantID, e.AppID, sender)
		switch {
		case err == nil:
			account = &id
		case !errors.Is(err, identity.ErrNotAdmitted):
			return err
		}
		if e.Archive != nil {
			if err := s.archive(ctx, tx, *e.Archive); err != nil {
				return err
			}
		}
		var deleted bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM feishu_chat_messages
			WHERE (tenant_key,app_id,message_id,chat_id)=($1,$2,$3,$4) AND deleted)`,
			e.TenantID, e.AppID, source, chat).Scan(&deleted); err != nil {
			return err
		}
		command := e.MessageID != "" && account != nil && !deleted
		if e.MessageID != "" {
			state := "candidate_cancelled"
			if command {
				state = "candidate"
			} else {
				e.Text = ""
			}
			if _, err := recordInbound(ctx, tx, e, account, state); err != nil {
				return err
			}
			if err := recordMessageReceived(ctx, tx, e); err != nil {
				return err
			}
		}
		if account == nil || deleted {
			if e.MessageID == "" && (e.Archive == nil || e.Archive.Message == nil) {
				return checkLiveConnection(ctx, tx, e)
			}
			e = candidateTombstone(e, source, chat)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO feishu_candidate_message_queue
			(run_id,source_id,event,command,withdrawn) VALUES($1,$2,$3,$4,$5)`,
			run, source, e, command, account == nil || deleted); err != nil {
			return err
		}
		return checkLiveConnection(ctx, tx, e)
	})
	if err != nil && !errors.Is(err, orchestrator.ErrStaleConnection) && !errors.Is(err, orchestrator.ErrInvalidInbound) {
		return true, orchestrator.ErrInboxUnavailable
	}
	if handled && err == nil && run != nil && s.armed {
		slog.InfoContext(ctx, "候选事件归属已提交", "run", *run, "event_ref", orchestrator.InboundReference(e))
	}
	return handled, err
}

func candidateTombstone(e orchestrator.InboundEvent, source, chat string) orchestrator.InboundEvent {
	at := e.ReceivedAt
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if e.Archive != nil && e.Archive.Message != nil && e.Archive.Message.Deleted {
		at = e.Archive.Message.UpdatedAt
	}
	e.MessageID, e.Text = "", ""
	e.Archive = &orchestrator.ArchiveEvent{ChatID: chat, Message: &orchestrator.ChatMessage{
		ID: source, ChatID: chat, SentAt: at, UpdatedAt: at, Deleted: true,
	}}
	return e
}

func (s *CandidateMessages) recall(ctx context.Context, tx pgx.Tx, e orchestrator.InboundEvent, run, source string) error {
	if err := s.archive(ctx, tx, *e.Archive); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE feishu_candidate_message_queue
		SET event=$3,withdrawn=true,revision=revision+1,lease_id=NULL,lease_until=NULL
		WHERE (run_id,source_id)=($1,$2) AND NOT withdrawn`, run, source, e); err != nil {
		return err
	}
	if err := cancelCandidateWork(ctx, tx, e.TenantID, e.AppID, source); err != nil {
		return err
	}
	return checkLiveConnection(ctx, tx, e)
}

func cancelCandidateWork(ctx context.Context, tx pgx.Tx, tenant, app, source string) error {
	if _, err := tx.Exec(ctx, `UPDATE feishu_inbound_work SET state='candidate_cancelled'
		WHERE (tenant_id,app_id,message_id)=($1,$2,$3)`, tenant, app, source); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE feishu_inbound_events SET body=''
		WHERE (tenant_id,app_id,message_id)=($1,$2,$3)`, tenant, app, source)
	return err
}
