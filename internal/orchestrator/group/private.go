package group

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/project"
)

func projectChoice(options []project.PortfolioProject) string {
	if len(options) == 0 {
		return "当前账号没有可访问的项目。"
	}
	var b strings.Builder
	b.WriteString("请指定要处理的项目，回复「选择项目 工作编号 项目编号」。")
	for _, p := range options {
		fmt.Fprintf(&b, "\n%s · %s", p.ID, p.Name)
	}
	return b.String()
}

// ResolvePrivateProject runs inside intake's transaction. Choice replies can
// only continue an outstanding work from the same account and conversation.
func ResolvePrivateProject(ctx context.Context, tx pgx.Tx, e *Envelope) (string, error) {
	if args := strings.Fields(e.Text); len(args) > 0 && args[0] == "选择项目" {
		if len(args) != 3 {
			return "", access.ErrForbidden
		}
		var id string
		err := tx.QueryRow(ctx, `UPDATE agent_group_work w SET project_id=m.project_id,membership_generation=m.generation,choice_prompt=NULL,state='route',lease_until=NULL,
		response_text=NULL,config_snapshot=NULL,agent_id=NULL,session_id=NULL,available_at=now(),platform_message_id=NULL,delivery_id=gen_random_uuid()
		FROM project_members m,accounts a WHERE w.id=$1 AND (w.tenant_key,w.app_id,w.chat_id,w.account_id,w.channel)=($2,$3,$4,$5,$6)
		AND w.mode='private_reply' AND w.state='awaiting_project' AND w.deadline>now() AND w.config_version=$8 AND w.thread_id=$9
		AND (a.tenant_key,a.app_id,a.id)=(w.tenant_key,w.app_id,w.account_id) AND a.active AND a.permission_version=w.permission_version
		AND (m.tenant_key,m.app_id,m.account_id)=(a.tenant_key,a.app_id,a.id) AND m.project_id=$7 AND m.role!='revoked' AND w.choice_grants->>m.project_id=m.generation RETURNING w.id`,
			args[1], e.Tenant, e.App, e.Chat, e.Account, e.Channel, args[2], e.Config, e.Thread).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			err = access.ErrForbidden
		}
		return id, err
	}
	// Project name/ID matching is a resource selector. Role selection happens in the worker.
	if e.Intent == "admin" {
		return "", nil
	}
	options, err := project.VisiblePortfolio(ctx, tx, e.Tenant, e.App, e.Account)
	if err != nil {
		return "", err
	}
	var matches []string
	for _, p := range options {
		if len(options) == 1 || strings.Contains(e.Text, p.Name) || strings.Contains(" "+e.Text+" ", " "+p.ID+" ") {
			matches = append(matches, p.ID)
		}
	}
	if len(matches) == 1 {
		e.Project = matches[0]
	} else {
		e.ChoicePrompt = projectChoice(options)
	}
	return "", nil
}
