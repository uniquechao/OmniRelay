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

// EnableGroupAgent enrolls new mentions on the live inbox only. Historical or
// candidate events never acquire authority from a later group assignment.
func (s *FeishuInbox) EnableGroupAgent() { s.groupAgent = true }

func FeishuGroupScope(credentials int64) func(context.Context, pgx.Tx, group.Envelope) (group.Assignment, error) {
	return func(ctx context.Context, tx pgx.Tx, e group.Envelope) (group.Assignment, error) {
		var a group.Assignment
		err := tx.QueryRow(ctx, `SELECT g.project_id,g.version,b.bot_open_id FROM project_chat_assignments g JOIN feishu_bot_configs b USING(tenant_key,app_id)
      WHERE g.tenant_key=$1 AND g.app_id=$2 AND g.chat_id=$3 AND g.project_id IS NOT NULL AND b.enabled AND b.bot_open_id<>''
      AND b.credentials_generation=$4 AND b.version=$5`, e.Tenant, e.App, e.Chat, credentials, e.Config).Scan(&a.Project, &a.Version, &a.Bot)
		if errors.Is(err, pgx.ErrNoRows) {
			return a, access.ErrForbidden
		}
		return a, err
	}
}
func (s *FeishuInbox) enrollGroup(ctx context.Context, tx pgx.Tx, id orchestrator.WorkID, account string, e orchestrator.InboundEvent) error {
	if e.ChatKind != orchestrator.ChatGroup || !routing.GroupIdentityQuery(e.Text) {
		return nil
	}
	work, err := group.Accept(ctx, tx, group.Channel{Resolve: FeishuGroupScope(s.bot.generation)}, group.Envelope{
		Tenant: e.TenantID, App: e.AppID, Channel: "feishu", Account: account, Chat: e.ChatID, Thread: e.ThreadID, Message: e.MessageID, Text: e.Text,
		Config: e.Connection.ConfigVersion, Received: e.ReceivedAt, Mention: true})
	state := "agent_routed"
	if errors.Is(err, access.ErrForbidden) {
		state = "agent_denied"
	} else if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE feishu_inbound_work SET state=$2,agent_work_id=nullif($3,'') WHERE id=$1", id, state, work)
	return err
}
