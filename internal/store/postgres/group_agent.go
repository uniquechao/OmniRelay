package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
	"github.com/uniquechao/OmniRelay/internal/orchestrator/group"
	"github.com/uniquechao/OmniRelay/internal/routing"
)

// EnableGroupAgent routes new group mentions and private identity queries from the live inbox.
func (s *FeishuInbox) EnableGroupAgent() { s.groupAgent = true }

func FeishuGroupScope(credentials int64) func(context.Context, pgx.Tx, group.Envelope) (group.Assignment, error) {
	return func(ctx context.Context, tx pgx.Tx, e group.Envelope) (group.Assignment, error) {
		var a group.Assignment
		var err error
		switch e.Mode {
		case group.PrivateReply:
			a.Project = e.Project
			err = tx.QueryRow(ctx, `SELECT b.bot_open_id,b.version FROM feishu_inbound_events f JOIN accounts u
                ON (u.tenant_key,u.app_id,u.open_id)=(f.tenant_id,f.app_id,f.sender_open_id)
                JOIN feishu_bot_configs b ON (b.tenant_key,b.app_id)=(f.tenant_id,f.app_id)
                WHERE f.tenant_id=$1 AND f.app_id=$2 AND f.chat_id=$3 AND f.message_id=$4 AND u.id=$5 AND u.active
                AND f.chat_kind='p2p' AND b.enabled AND b.bot_open_id<>'' AND b.credentials_generation=$6 AND b.version=$7`,
				e.Tenant, e.App, e.Chat, e.Message, e.Account, credentials, e.Config).Scan(&a.Bot, &a.Version)
		case group.PersonNotice:
			if e.Account != e.Chat {
				return a, access.ErrForbidden
			}
			a.Project = e.Project
			err = tx.QueryRow(ctx, `SELECT b.bot_open_id,b.version,u.open_id FROM feishu_bot_configs b JOIN accounts u USING(tenant_key,app_id)
               WHERE u.tenant_key=$1 AND u.app_id=$2 AND u.id=$3 AND u.active AND u.open_id IS NOT NULL AND b.enabled AND b.bot_open_id<>''
               AND b.credentials_generation=$4 AND b.version=$5`, e.Tenant, e.App, e.Account, credentials, e.Config).Scan(&a.Bot, &a.Version, &a.Recipient)
		default:
			a.Recipient = e.Chat
			err = tx.QueryRow(ctx, `SELECT g.project_id,g.version,b.bot_open_id FROM project_chat_assignments g JOIN feishu_bot_configs b USING(tenant_key,app_id)
      WHERE g.tenant_key=$1 AND g.app_id=$2 AND g.chat_id=$3 AND g.project_id IS NOT NULL AND b.enabled AND b.bot_open_id<>''
      AND b.credentials_generation=$4 AND b.version=$5`, e.Tenant, e.App, e.Chat, credentials, e.Config).Scan(&a.Project, &a.Version, &a.Bot)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return a, access.ErrForbidden
		}
		return a, err
	}
}
func (s *FeishuInbox) enrollGroup(ctx context.Context, tx pgx.Tx, id orchestrator.WorkID, account string, e orchestrator.InboundEvent) error {
	setState := func(state string) error {
		_, err := tx.Exec(ctx, "UPDATE feishu_inbound_work SET state=$2 WHERE id=$1", id, state)
		return err
	}
	mode := group.GroupReply
	if e.ChatKind == orchestrator.ChatPrivate {
		mode = group.PrivateReply
	}
	envelope := group.Envelope{
		Tenant: e.TenantID, App: e.AppID, Channel: "feishu", Account: account, Chat: e.ChatID, Thread: e.ThreadID, Message: e.MessageID, Text: e.Text,
		Config: e.Connection.ConfigVersion, Received: e.ReceivedAt, Mode: mode, Mention: e.ChatKind == orchestrator.ChatGroup}
	if mode == group.GroupReply {
		var linked bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT FROM project_chat_assignments WHERE (tenant_key,app_id,chat_id)=($1,$2,$3) AND project_id IS NOT NULL)", e.TenantID, e.AppID, e.ChatID).Scan(&linked); err != nil {
			return err
		}
		if !linked {
			return setState("unbound_group")
		}
	}
	if mode == group.PrivateReply && !routing.GroupIdentityQuery(e.Text) {
		original, err := group.ResolvePrivateProject(ctx, tx, &envelope)
		if err != nil {
			if errors.Is(err, access.ErrForbidden) {
				return setState("agent_denied")
			}
			return err
		}
		if original != "" {
			_, err = tx.Exec(ctx, "UPDATE feishu_inbound_work SET state='agent_routed',agent_work_id=$2 WHERE id=$1", id, original)
			return err
		}
	}
	work, err := group.Accept(ctx, tx, group.Channel{Resolve: FeishuGroupScope(s.bot.generation)}, envelope)
	state := "agent_routed"
	if errors.Is(err, access.ErrForbidden) {
		state = "agent_denied"
	} else if err != nil {
		return err
	}
	if state == "agent_routed" {
		var full bool
		if err := tx.QueryRow(ctx, "SELECT count(*)>$3 FROM agent_group_work WHERE tenant_key=$1 AND app_id=$2 AND channel='feishu' AND state IN ('route','running','ready','sending')", e.TenantID, e.AppID, s.capacity).Scan(&full); err != nil {
			return err
		}
		if full {
			return orchestrator.ErrInboxFull
		}
	}
	_, err = tx.Exec(ctx, "UPDATE feishu_inbound_work SET state=$2,agent_work_id=nullif($3,'') WHERE id=$1", id, state, work)
	return err
}
