package group

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/agent/dsh"

	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/project"
	"github.com/uniquechao/OmniRelay/internal/routing"
)

type Service struct {
	DB                 *pgxpool.Pool
	Roles              *access.Service
	Projects           *project.Service
	Tenant, App, Owner string
	Channels           map[string]Channel
	Execute            func(context.Context, dsh.Binding) (dsh.Result, error)
}
type Who struct {
	UserID      string `json:"user_id,omitempty"`
	UserName    string `json:"user_name,omitempty"`
	ProjectID   string `json:"project_id,omitempty"`
	ProjectName string `json:"project_name,omitempty"`
}
type work struct {
	dsh.Binding
	State, Bot, Delivery, Text string
	Address                    Envelope
	Actor                      identity.Principal
	Scope                      Who
}

// The same current account, group assignment, membership lifetime and Bot
// configuration are required for admission, tool reads and publication.
func (s *Service) authorize(ctx context.Context, tx pgx.Tx, id string) (work, error) {
	var w work
	var p identity.Principal
	var scope, member, plan string
	var pv, av int64
	err := tx.QueryRow(ctx, `SELECT a.id,a.open_id,a.name,w.project_id,coalesce(p.name,''),w.chat_id,w.thread_id,w.message_id,
            w.channel,w.config_version,w.membership_generation,w.assignment_version,w.permission_version,w.delivery_id::text,w.mode,coalesce(w.plan_id::text,''),coalesce(w.question,w.group_question,'')
        FROM agent_group_work w
        JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(w.tenant_key,w.app_id,w.account_id)
        LEFT JOIN project_profiles p ON (p.tenant_key,p.app_id,p.project_id)=(w.tenant_key,w.app_id,w.project_id)
        WHERE w.id=$1 AND w.tenant_key=$2 AND w.app_id=$3 AND a.active AND a.permission_version=w.permission_version
        AND (w.mode='private_reply' OR p.project_id IS NOT NULL) AND w.deadline>clock_timestamp()`, id, s.Tenant, s.App).Scan(&p.AccountID, &p.OpenID, &p.Name, &w.Scope.ProjectID, &w.Scope.ProjectName,
		&w.Address.Chat, &w.Address.Thread, &w.Address.Message, &w.Address.Channel, &w.Address.Config, &member, &av, &pv, &w.Delivery, &w.Address.Mode, &plan, &w.Address.Text)
	if errors.Is(err, pgx.ErrNoRows) {
		return w, access.ErrForbidden
	}
	if err != nil {
		return w, err
	}
	p.TenantKey, p.AppID = s.Tenant, s.App
	w.Actor = p
	w.Address.Account, w.Address.Tenant, w.Address.App = p.AccountID, s.Tenant, s.App
	w.Address.Project = w.Scope.ProjectID
	ch, ok := s.Channels[w.Address.Channel]
	if !ok {
		return w, access.ErrForbidden
	}
	assignment, err := ch.Resolve(ctx, tx, w.Address)
	if err != nil {
		return w, err
	}
	if assignment.Project != w.Scope.ProjectID || assignment.Version != av {
		return w, access.ErrForbidden
	}
	w.Bot = assignment.Bot
	w.Address.Recipient = assignment.Recipient
	if w.Address.Mode.notice() {
		if err = s.checkNotice(ctx, tx, plan, w.Address, assignment); err != nil {
			return w, err
		}
	}
	gateway := routing.Gateway{Check: func(ctx context.Context, p identity.Principal, action, id string) (bool, error) {
		return s.Roles.CheckInOrganizationMutation(ctx, tx, p, action, id)
	}}
	intent := "project"
	if w.Address.Mode == PrivateReply {
		intent = "knowledge"
	}
	target, err := gateway.Resolve(ctx, p, intent, w.Scope.ProjectID)
	if err != nil {
		return w, err
	}
	if w.Address.Mode != PrivateReply {
		grant, err := s.Roles.ProjectGrant(ctx, tx, p, w.Scope.ProjectID)
		if err != nil {
			return w, err
		}
		if !grant.Member || grant.MembershipGeneration != member {
			return w, access.ErrForbidden
		}
	}
	w.Work, w.Agent, w.Scope.UserID, w.Scope.UserName = id, target.AgentID, p.AccountID, p.Name
	scope = fmt.Sprintf("%s/%d/%d", member, av, pv)
	fields, _ := json.Marshal([]string{w.Agent, p.AccountID, w.Address.Channel, w.Address.Chat, w.Address.Thread, scope})
	if w.Address.Mode != GroupReply {
		fields, _ = json.Marshal([]string{string(w.Address.Mode), plan, w.Agent, p.AccountID, w.Address.Channel, w.Address.Chat, w.Address.Thread, scope})
		w.Kind = string(w.Address.Mode)
	}
	if w.Address.Mode == GroupReply && w.Address.Text != "" {
		w.Kind = "project_group_chat"
		// Tool outputs belong to a private workbench result for this person, never to the original group.
		fields, _ = json.Marshal([]string{w.Kind, id, w.Agent, p.AccountID, scope})
		w.Prompt = w.Address.Text
	}
	if w.Address.Mode == GroupNotice {
		w.Scope.UserID = ""
		w.Scope.UserName = ""
	}
	if w.Address.Mode == ProjectChat {
		// Never resume a model transcript carrying source-restricted results into a later work.
		var boundary string
		err := tx.QueryRow(ctx, `SELECT coalesce((SELECT old.id FROM agent_group_work old JOIN agent_group_work cur ON cur.id=$1
   WHERE (old.tenant_key,old.app_id,old.account_id,old.project_id,old.mode)=(cur.tenant_key,cur.app_id,cur.account_id,cur.project_id,cur.mode)
   AND (old.asked_at,old.id)<(cur.asked_at,cur.id) AND EXISTS(SELECT FROM agent_project_tool_reads r WHERE r.work_id=old.id)
   ORDER BY old.asked_at DESC,old.id DESC LIMIT 1),'')`, id).Scan(&boundary)
		if err != nil {
			return w, err
		}
		if boundary != "" {
			fields = append(fields, []byte(boundary)...)
		}
		verified, _ := json.Marshal(w.Scope)
		w.Prompt = fmt.Sprintf("当前登录账号和项目由工作台验证：%s\n用户消息：%s", verified, w.Address.Text)
	}
	digest := sha256.Sum256(fields)
	w.Session = hex.EncodeToString(digest[:])
	return w, nil
}
func (s *Service) claim(ctx context.Context) (work, error) {
	var w work
	err := pgx.BeginTxFunc(ctx, s.DB, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		var id, state string
		err := tx.QueryRow(ctx, `SELECT w.id,w.state FROM agent_group_work w
          LEFT JOIN agent_instances a ON a.agent_id=w.agent_id
          WHERE w.tenant_key=$1 AND w.app_id=$2 AND (a.owner_id IS NULL OR a.owner_id=$3)
          AND w.state IN ('route','running','ready','sending') AND w.available_at<=now()
          AND (w.lease_until IS NULL OR w.lease_until<=now())
          ORDER BY w.available_at,w.id LIMIT 1 FOR UPDATE OF w SKIP LOCKED`, s.Tenant, s.App, s.Owner).Scan(&id, &state)
		if err != nil {
			return err
		}
		if state == "sending" {
			_, err = tx.Exec(ctx, "UPDATE agent_group_work SET state='unknown',reason='missing_platform_receipt' WHERE id=$1", id)
			return err
		}
		w, err = s.authorize(ctx, tx, id)
		if errors.Is(err, access.ErrForbidden) {
			_, err = tx.Exec(ctx, "UPDATE agent_group_work SET state=CASE WHEN deadline<=now() THEN 'expired' ELSE 'suppressed' END,reason='scope_or_deadline' WHERE id=$1", id)
			return err
		}
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO agent_instances(agent_id,tenant_key,app_id,project_id,owner_id)
            VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, w.Agent, s.Tenant, s.App, w.Scope.ProjectID, s.Owner); err != nil {
			return err
		}
		if err = tx.QueryRow(ctx, "SELECT owner_id,owner_epoch FROM agent_instances WHERE agent_id=$1 FOR SHARE", w.Agent).Scan(&w.Owner, &w.Epoch); err != nil {
			return err
		}
		if w.Owner != s.Owner {
			_, err = tx.Exec(ctx, "UPDATE agent_group_work SET agent_id=$2 WHERE id=$1", id, w.Agent)
			w = work{}
			return err
		}
		w.State = "running"
		if state == "ready" {
			w.State = "sending"
		}
		return tx.QueryRow(ctx, `UPDATE agent_group_work SET state=$2,agent_id=$3,session_id=$4,generation=generation+1,
          lease_until=clock_timestamp()+interval '100 seconds' WHERE id=$1 RETURNING generation,coalesce(response_text,'')`,
			id, w.State, w.Agent, w.Session).Scan(&w.Generation, &w.Text)
	})
	return w, err
}
func (s *Service) Step(ctx context.Context) error {
	w, err := s.claim(ctx)
	if err != nil || w.State == "" {
		return err
	}
	if w.State == "sending" {
		// A changed display name also invalidates a previously frozen receipt.
		expected := w.publicReply()
		if w.Text != expected {
			_, err = s.DB.Exec(ctx, "UPDATE agent_group_work SET state='suppressed',reason='source_changed' WHERE id=$1 AND generation=$2", w.Work, w.Generation)
			return err
		}
		result := s.Channels[w.Address.Channel].Reply(ctx, w.Address, w.Bot, w.Text, w.Delivery)
		state := "unknown"
		switch result.State {
		case "success":
			if result.MessageID != "" {
				state = "sent"
			}
		case "failed":
			state = "failed"
		}
		_, err = s.DB.Exec(ctx, `UPDATE agent_group_work SET state=$3,platform_message_id=nullif($4,''),reason=$5,lease_until=NULL
           WHERE id=$1 AND generation=$2 AND state IN ('sending','unknown')`, w.Work, w.Generation, state, result.MessageID, result.Reason)
		return err
	}
	result, err := s.Execute(ctx, w.Binding)
	if err != nil {
		_, dbErr := s.DB.Exec(ctx, `UPDATE agent_group_work SET available_at=now()+interval '3 seconds',lease_until=NULL
              WHERE id=$1 AND generation=$2 AND state='running'`, w.Work, w.Generation)
		return errors.Join(err, dbErr)
	}
	if result.Session != w.Session {
		return errors.New("unbound runtime session")
	}
	return s.Roles.WithOrganizationMutation(ctx, func(tx pgx.Tx) error {
		var receipt Who
		err := tx.QueryRow(ctx, `SELECT tool_receipt FROM agent_group_work w JOIN agent_instances a USING(agent_id)
         WHERE id=$1 AND generation=$2 AND state='running' AND tool_turn=$3 AND owner_id=$4 AND owner_epoch=$5
         AND deadline>now() AND lease_until>now() FOR UPDATE OF w`, w.Work, w.Generation, result.Turn, s.Owner, w.Epoch).Scan(&receipt)
		if err != nil {
			return err
		}
		if w.Address.Mode == ProjectChat || w.Kind == "project_group_chat" {
			if result.Answer == "" || len(result.Answer) > 48000 {
				return errors.New("missing or oversized project chat response")
			}
			current, err := s.authorize(ctx, tx, w.Work)
			if err != nil && !errors.Is(err, access.ErrForbidden) {
				return err
			}
			if err == nil {
				err = s.checkToolReads(ctx, tx, current.Work, current.Actor, current.Scope.ProjectID)
			}
			if err != nil && !errors.Is(err, access.ErrForbidden) && !errors.Is(err, errToolReadStale) {
				return err
			}
			if err != nil || current.Scope != receipt {
				reason := ""
				if errors.Is(err, errToolReadStale) {
					reason = "tool_source_changed"
				}
				_, err = tx.Exec(ctx, "UPDATE agent_group_work SET state='suppressed',reason=$2,lease_until=NULL WHERE id=$1", w.Work, reason)
				return err
			}
			if w.Kind == "project_group_chat" {
				_, err = tx.Exec(ctx, "UPDATE agent_group_work SET state='ready',group_answer=$2,response_text=$3,lease_until=NULL WHERE id=$1", w.Work, "项目 PM · "+current.Scope.ProjectName+"\n"+result.Answer, current.publicReply())
				return err
			}
			// Publication into the user's workbench and its receipt are the same database write.
			_, err = tx.Exec(ctx, "UPDATE agent_group_work SET state='sent',response_text=$2,platform_message_id=delivery_id::text,lease_until=NULL WHERE id=$1", w.Work, "项目 PM · "+current.Scope.ProjectName+"\n"+result.Answer)
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE agent_group_work SET state='ready',response_text=$2,lease_until=NULL WHERE id=$1", w.Work, render(w.Address.Mode, receipt))
		return err
	})
}
func (w work) publicReply() string {
	if w.Kind == "project_group_chat" {
		return "项目 PM · " + w.Scope.ProjectName + "\n请在本项目工作台的 Bot 对话中查看本次请求的结果。"
	}
	return render(w.Address.Mode, w.Scope)
}
func render(mode Mode, w Who) string {
	switch mode {
	case PrivateReply:
		return fmt.Sprintf("全局查询助手 · 全局服务空间\n你的账号是%s（%s）。", w.UserName, w.UserID)
	case GroupNotice:
		return fmt.Sprintf("项目 PM · %s\n项目提醒：请关注%s的项目进展。", w.ProjectName, w.ProjectName)
	case PersonNotice:
		return fmt.Sprintf("项目 PM · %s\n%s，这是发给你本人的项目提醒，请查看%s的进展。", w.ProjectName, w.UserName, w.ProjectName)
	}
	return fmt.Sprintf("项目 PM · %s\n%s（%s），你当前所在的项目是%s（%s）。", w.ProjectName, w.UserName, w.UserID, w.ProjectName, w.ProjectID)
}
func (s *Service) Run(ctx context.Context) {
	for ctx.Err() == nil {
		err := s.Step(ctx)
		if err == nil {
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) && ctx.Err() == nil {
			slog.Warn("Group Agent work deferred")
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}
