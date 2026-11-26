package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
)

const inboundTimeout = 2500 * time.Millisecond

type FeishuInbox struct {
	groupAgent bool
	bot        *FeishuBot
	capacity   int
}

var _ orchestrator.InboxAcceptor = (*FeishuInbox)(nil)

func NewFeishuInbox(bot *FeishuBot, capacity int) (*FeishuInbox, error) {
	if capacity <= 0 {
		return nil, errors.New("inbox capacity must be positive")
	}
	return &FeishuInbox{bot: bot, capacity: capacity}, nil
}

func (s *FeishuInbox) AcceptInbound(ctx context.Context, event orchestrator.InboundEvent) (orchestrator.AcceptResult, error) {
	if !s.validEvent(event) {
		return orchestrator.AcceptResult{}, orchestrator.ErrInvalidInbound
	}

	ctx, cancel := context.WithTimeout(ctx, inboundTimeout)
	defer cancel()
	var result orchestrator.AcceptResult
	// The configuration row serializes acceptance with capacity checks and
	// ownership changes. READ COMMITTED lets waiters see the winner's work.
	err := pgx.BeginTxFunc(ctx, s.bot.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if err := s.bot.lockGeneration(ctx, tx); err != nil {
			return connectionError(err)
		}
		if err := checkLiveConnection(ctx, tx, event); err != nil {
			return err
		}

		err := tx.QueryRow(ctx, `SELECT id FROM feishu_inbound_work
			WHERE app_id=$1 AND tenant_id=$2 AND message_id=$3`,
			event.AppID, event.TenantID, event.MessageID).Scan(&result.WorkID)
		switch {
		case err == nil:
			// Replays keep the original receipt and do not prove current-generation delivery.
			return checkLiveConnection(ctx, tx, event)
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		result.WorkID, err = s.insertInbound(ctx, tx, event)
		if err != nil {
			return err
		}
		// Account resolution or writes may have waited. Recheck the lease using
		// database time before recording receipt in the same transaction.
		return recordMessageReceived(ctx, tx, event)
	})
	switch err {
	case nil:
		return result, nil
	case orchestrator.ErrStaleConnection, orchestrator.ErrInboxFull:
		return orchestrator.AcceptResult{}, err
	default:
		// PostgreSQL details can contain message content or identifiers.
		return orchestrator.AcceptResult{}, orchestrator.ErrInboxUnavailable
	}
}

func (s *FeishuInbox) validEvent(event orchestrator.InboundEvent) bool {
	return event.AppID != "" && event.AppID == s.bot.config.AppID &&
		event.TenantID != "" && event.TenantID == s.bot.config.TenantKey &&
		event.EventID != "" && event.MessageID != "" && event.ChatID != "" &&
		event.SenderOpenID != "" && !event.ReceivedAt.IsZero() &&
		(event.ChatKind == orchestrator.ChatPrivate || event.ChatKind == orchestrator.ChatGroup) &&
		event.Connection.ConfigVersion >= 0 && event.Connection.ConnectionGeneration > 0
}

func checkLiveConnection(ctx context.Context, tx pgx.Tx, event orchestrator.InboundEvent) error {
	var held bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM feishu_bot_configs WHERE `+receivingConnection+`)`,
		event.TenantID, event.AppID, event.Connection.ConfigVersion, event.Connection.ConnectionGeneration).Scan(&held)
	if err == nil && !held {
		return orchestrator.ErrStaleConnection
	}
	return err
}

func recordMessageReceived(ctx context.Context, tx pgx.Tx, event orchestrator.InboundEvent) error {
	tag, err := tx.Exec(ctx, `UPDATE feishu_bot_configs
		SET last_message_at=GREATEST(last_message_at, clock_timestamp()) WHERE `+receivingConnection,
		event.TenantID, event.AppID, event.Connection.ConfigVersion, event.Connection.ConnectionGeneration)
	if err == nil && tag.RowsAffected() == 0 {
		return orchestrator.ErrStaleConnection
	}
	return err
}

func (s *FeishuInbox) insertInbound(ctx context.Context, tx pgx.Tx, event orchestrator.InboundEvent) (orchestrator.WorkID, error) {
	var account *string
	state := "admission_denied"
	id, err := identity.ResolveFeishuAccount(ctx, tx, event.TenantID, event.AppID, event.SenderOpenID)
	switch {
	case err == nil:
		account = &id
		state = "pending_route"
		if !s.groupAgent {
			state = "agent_disabled"
		}
	case !errors.Is(err, identity.ErrNotAdmitted):
		return "", err
	}

	workID, err := recordInbound(ctx, tx, event, account, state)
	if err == nil && s.groupAgent && account != nil {
		err = s.enrollGroup(ctx, tx, workID, *account, event)
	}
	return workID, err
}

func recordInbound(ctx context.Context, tx pgx.Tx, event orchestrator.InboundEvent, account *string, state string) (orchestrator.WorkID, error) {
	_, err := tx.Exec(ctx, `INSERT INTO feishu_inbound_events
		(app_id,tenant_id,message_id,event_id,chat_id,chat_kind,thread_id,reply_to_message_id,
		sender_open_id,received_at,body,config_version,connection_generation)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		event.AppID, event.TenantID, event.MessageID, event.EventID, event.ChatID, event.ChatKind,
		event.ThreadID, event.ReplyToMessageID, event.SenderOpenID, event.ReceivedAt.UTC(), event.Text,
		event.Connection.ConfigVersion, event.Connection.ConnectionGeneration)
	if err != nil {
		return "", err
	}
	var workID orchestrator.WorkID
	err = tx.QueryRow(ctx, `INSERT INTO feishu_inbound_work (app_id,tenant_id,message_id,account_id,state)
		VALUES ($1,$2,$3,$4,$5) RETURNING id`, event.AppID, event.TenantID, event.MessageID, account, state).Scan(&workID)
	return workID, err
}

// AcceptChatArchive commits independently of command admission and its capacity.
func (s *FeishuBot) AcceptChatArchive(ctx context.Context, event orchestrator.InboundEvent,
	accept func(context.Context, pgx.Tx, orchestrator.ArchiveEvent) error,
) error {
	if event.AppID != s.config.AppID || event.TenantID != s.config.TenantKey || event.Archive == nil {
		return orchestrator.ErrInvalidInbound
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.lockGeneration(ctx, tx); err != nil {
			return connectionError(err)
		}
		if err := checkLiveConnection(ctx, tx, event); err != nil {
			return err
		}
		source, chat, _, _, _, recall := candidateSource(event)
		if event.Archive.Message == nil && source != "" {
			if _, err := tx.Exec(ctx, `INSERT INTO feishu_candidate_event_owners
				(tenant_key,app_id,source_id,chat_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
				event.TenantID, event.AppID, source, chat); err != nil {
				return err
			}
		}
		if err := accept(ctx, tx, *event.Archive); err != nil {
			return err
		}
		if event.Archive.Message != nil && !recall {
			if _, err := tx.Exec(ctx, `UPDATE feishu_chat_messages SET sdk_received=true
				WHERE (tenant_key,app_id,message_id,chat_id)=($1,$2,$3,$4) AND NOT sdk_received`,
				event.TenantID, event.AppID, source, chat); err != nil {
				return err
			}
		}
		return checkLiveConnection(ctx, tx, event)
	})
	if err != nil {
		return orchestrator.ErrInboxUnavailable
	}
	return nil
}
