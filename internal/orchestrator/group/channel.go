package group

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/routing"
	"time"
)

// Envelope is assembled by an authenticated adapter. Sender and conversation
// never come from the text. The same interface is used by the internal gate.
type Envelope struct {
	Tenant, App, Channel, Account, Chat, Thread, Message, Text string
	Config                                                     int64
	Received                                                   time.Time
	Mention                                                    bool
}
type Assignment struct {
	Project, Bot string
	Version      int64
}
type Receipt struct{ State, MessageID, Reason string }
type Channel struct {
	Resolve func(context.Context, pgx.Tx, Envelope) (Assignment, error)
	Reply   func(context.Context, Envelope, string, string, string) Receipt
}

func Accept(ctx context.Context, tx pgx.Tx, adapter Channel, e Envelope) (string, error) {
	if !e.Mention || !routing.GroupIdentityQuery(e.Text) || e.Tenant == "" || e.App == "" || e.Channel == "" || e.Account == "" || e.Chat == "" || e.Message == "" || e.Received.IsZero() {
		return "", access.ErrForbidden
	}
	a, err := adapter.Resolve(ctx, tx, e)
	if err != nil {
		return "", err
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO agent_group_work(tenant_key,app_id,channel,account_id,chat_id,thread_id,message_id,config_version,project_id,
          membership_generation,assignment_version,permission_version,deadline)
        SELECT $1,$2,$3,$4,$5,$6,$7,$8,p.project_id,m.generation,$10,a.permission_version,least(now(),$11::timestamptz)+interval '10 minutes'
        FROM project_members m JOIN project_profiles p USING(tenant_key,app_id,project_id)
        JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(m.tenant_key,m.app_id,m.account_id)
        WHERE m.tenant_key=$1 AND m.app_id=$2 AND m.account_id=$4 AND m.project_id=$9 AND m.role!='revoked' AND a.active
        ON CONFLICT (tenant_key,app_id,channel,message_id) DO UPDATE SET message_id=excluded.message_id
        WHERE (agent_group_work.account_id,agent_group_work.chat_id,agent_group_work.thread_id,agent_group_work.project_id)=
              (excluded.account_id,excluded.chat_id,excluded.thread_id,excluded.project_id)
        RETURNING id`, e.Tenant, e.App, e.Channel, e.Account, e.Chat, e.Thread, e.Message, e.Config, a.Project, a.Version, e.Received).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", access.ErrForbidden
	}
	return id, err
}
