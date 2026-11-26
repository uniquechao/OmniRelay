package chat

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

// AgentThread reads a bounded window of public messages from the requested topic.
// The caller fixes the cutoff at admission, including on replay and historical reads.
func (s *Service) AgentThread(ctx context.Context, tx pgx.Tx, actor identity.Principal, project, group, thread, message string, cutoff time.Time) (json.RawMessage, error) {
	if _, err := s.ProjectGroupForMutation(ctx, tx, actor, group); err != nil {
		return nil, err
	}
	var bound, deleted bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM project_chat_assignments WHERE
		(tenant_key,app_id,project_id,chat_id)=($1,$2,$3,$4)),
		EXISTS(SELECT FROM feishu_chat_messages WHERE
		(tenant_key,app_id,chat_id,message_id)=($1,$2,$4,$5) AND deleted)`,
		s.tenant, s.app, project, group, message).Scan(&bound, &deleted)
	if err != nil {
		return nil, err
	}
	if !bound || deleted {
		return nil, ErrAccessLost
	}
	var history json.RawMessage
	err = tx.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(h) ORDER BY h.sent_at,h.message_id),'[]')
	FROM (SELECT m.message_id,m.sender_id,m.sent_at,m.updated_at,left(m.search_text,800) AS excerpt
	FROM feishu_chat_messages m WHERE `+visibleMessageRows+`
	AND m.chat_id=$3 AND (m.thread_id=$4 OR m.message_id=$4) AND NOT m.deleted AND m.sent_at<=$5
	AND ((m.sender_kind IN ('user','person')) OR EXISTS (
	    SELECT FROM agent_group_work delivered WHERE delivered.tenant_key=m.tenant_key
	    AND delivered.app_id=m.app_id AND delivered.chat_id=m.chat_id AND delivered.state='sent'
	    AND delivered.platform_message_id=m.message_id))
	ORDER BY m.sent_at DESC,m.message_id DESC LIMIT 24) h`, s.tenant, s.app, group, thread, cutoff).Scan(&history)
	return history, err
}
