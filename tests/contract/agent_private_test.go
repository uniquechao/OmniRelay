package contract_test

import (
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
	"github.com/uniquechao/OmniRelay/internal/orchestrator/group"
	"github.com/uniquechao/OmniRelay/internal/store/postgres"
	"testing"
)

func TestPrivateAgentInboxAdapter(t *testing.T) {
	f := newInboxFixture(t, 4)
	ctx := t.Context()
	f.inbox.(*postgres.FeishuInbox).EnableGroupAgent()
	person := f.account(t, f.event.TenantID, f.event.SenderOpenID, true)
	other := f.account(t, f.event.TenantID, "ou_other", true)
	_, err := f.pool.Exec(ctx, "UPDATE feishu_bot_configs SET bot_open_id='ou_bot'")
	must(t, err)
	f.event.Text = "我是谁"
	f.event.ChatKind = orchestrator.ChatPrivate
	f.event.ChatID = "oc_dm"
	f.event.ThreadID = ""
	work, err := f.inbox.AcceptInbound(ctx, f.event)
	must(t, err)
	var mode, project string
	err = f.pool.QueryRow(ctx, `SELECT w.mode,w.project_id FROM feishu_inbound_work i JOIN agent_group_work w ON w.id=i.agent_work_id WHERE i.id=$1`, work.WorkID).Scan(&mode, &project)
	must(t, err)
	if mode != "private_reply" || project != "" {
		t.Fatal("wrong private Agent")
	}
	tx, err := f.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	must(t, err)
	defer tx.Rollback(ctx)
	adapter := postgres.FeishuGroupScope(1)
	e := group.Envelope{Tenant: f.event.TenantID, App: f.event.AppID, Chat: "oc_dm", Account: other, Mode: group.PrivateReply, Message: f.event.MessageID, Config: f.event.Connection.ConfigVersion}
	if _, err = adapter(ctx, tx, e); !errors.Is(err, access.ErrForbidden) {
		t.Fatal("claimed another user's private source", err)
	}
	e.Chat = person
	e.Account = person
	e.Mode = group.PersonNotice
	e.Project = "north"
	a, err := adapter(ctx, tx, e)
	must(t, err)
	if a.Recipient != f.event.SenderOpenID || a.Bot != "ou_bot" {
		t.Fatal("incorrect app-scoped recipient")
	}
}
