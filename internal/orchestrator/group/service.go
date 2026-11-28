package group

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/agent/dsh"
	"github.com/uniquechao/OmniRelay/internal/agentadmin"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/project"
	"github.com/uniquechao/OmniRelay/internal/routing"
)

type Service struct {
	PlanIntent         func(context.Context, routing.PlanRequest) (routing.Plan, error)
	ReadThread         func(context.Context, pgx.Tx, identity.Principal, string, string, string, string, time.Time) (json.RawMessage, error)
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
	Plan           routing.Plan
	PlanRequest    routing.PlanRequest
	planDeadline   time.Time
	ProposalParent string
	OriginWork     string
	OriginItem     int
	dsh.Binding
	State, Bot, Delivery, Text, Reason string
	Address                            Envelope
	Actor                              identity.Principal
	Scope                              Who
	Config                             agentadmin.Config
	LiveTools                          []string
	PersonalContext                    []PriorTurn
	liveConfigVersion                  int64
	WriteRequested                     bool
	LegacyOutput                       bool
	Frozen                             json.RawMessage
}

// The same current account, group assignment, membership lifetime and Bot
// configuration are required for admission, tool reads and publication.
func (s *Service) authorize(ctx context.Context, tx pgx.Tx, id string) (work, error) {
	var w work
	var p identity.Principal
	var scope, member, plan string
	var pv, av int64
	err := tx.QueryRow(ctx, `SELECT a.id,coalesce(a.open_id,''),a.name,w.project_id,coalesce(p.name,''),w.chat_id,w.thread_id,w.message_id,
            w.channel,w.config_version,w.membership_generation,w.assignment_version,w.permission_version,w.delivery_id::text,w.mode,coalesce(w.plan_id::text,''),coalesce(w.question,w.group_question,w.direct_question,''),w.intent,w.write_requested,coalesce(w.choice_prompt,''),w.config_snapshot,w.legacy_output,coalesce(w.semantic_plan,'{}'),w.reason
        FROM agent_group_work w
        JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(w.tenant_key,w.app_id,w.account_id)
        LEFT JOIN project_profiles p ON (p.tenant_key,p.app_id,p.project_id)=(w.tenant_key,w.app_id,w.project_id)
        WHERE w.id=$1 AND w.tenant_key=$2 AND w.app_id=$3 AND a.active AND a.permission_version=w.permission_version
        AND (w.mode='private_reply' OR p.project_id IS NOT NULL) AND w.deadline>clock_timestamp()`, id, s.Tenant, s.App).Scan(&p.AccountID, &p.OpenID, &p.Name, &w.Scope.ProjectID, &w.Scope.ProjectName,
		&w.Address.Chat, &w.Address.Thread, &w.Address.Message, &w.Address.Channel, &w.Address.Config, &member, &av, &pv, &w.Delivery, &w.Address.Mode, &plan, &w.Address.Text, &w.Address.Intent, &w.WriteRequested, &w.Address.ChoicePrompt, &w.Frozen, &w.LegacyOutput, &w.Plan, &w.Reason)
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
	intent := w.Address.Intent
	if w.Address.ChoicePrompt != "" {
		intent = "knowledge"
	}
	target, err := gateway.Resolve(ctx, p, intent, w.Scope.ProjectID)
	if err != nil {
		return w, err
	}
	if w.Scope.ProjectID != "" {
		grant, err := s.Roles.ProjectGrant(ctx, tx, p, w.Scope.ProjectID)
		if err != nil {
			return w, err
		}
		if !grant.Member || grant.MembershipGeneration != member {
			return w, access.ErrForbidden
		}
	}
	w.Role = target.Role
	cfg, err := agentadmin.Load(ctx, tx, s.Tenant, s.App, w.configScope(), w.Role)
	if err != nil {
		return w, err
	}
	if !cfg.Enabled {
		return w, access.ErrForbidden
	}
	w.LiveTools = cfg.Tools
	w.liveConfigVersion = cfg.Version
	if len(w.Frozen) > 0 {
		if err = json.Unmarshal(w.Frozen, &w.Config); err != nil {
			return w, err
		}
	} else {
		w.Config = cfg
	}
	w.ConfigVersion, w.Persona = w.Config.Version, w.Config.Persona
	w.Work, w.Agent, w.Scope.UserID, w.Scope.UserName = id, target.AgentID, p.AccountID, p.Name
	if w.Address.ChoicePrompt == "" && w.Plan.EvidencePeriod != nil {
		if err := s.bindEvidencePeriod(ctx, tx, &w); err != nil {
			return w, err
		}
	}
	if w.Plan.PriorResult != "" && w.Address.Mode != ProjectChat && w.Address.Mode != PrivateReply {
		w.clarifyPriorResult()
	}
	scope = fmt.Sprintf("%s/%d/%d", member, av, pv)
	fields, _ := json.Marshal([]string{w.Agent, p.AccountID, w.Address.Channel, w.Address.Chat, w.Address.Thread, scope})
	if w.Address.Mode != GroupReply {
		fields, _ = json.Marshal([]string{string(w.Address.Mode), plan, w.Agent, p.AccountID, w.Address.Channel, w.Address.Chat, w.Address.Thread, scope})
		w.Kind = string(w.Address.Mode)
	}
	if w.Address.Mode == GroupReply && w.Address.Text != "" {
		w.Kind = "project_group_chat"
		// Bind each group request to its own session and track the audience of its sources.
		fields, _ = json.Marshal([]string{w.Kind, id, w.Agent, p.AccountID, scope})
		w.Prompt = w.Address.Text
		// An unrestricted thread transcript may contain facts outside an explicitly
		// requested reporting period. The scoped message search supplies dated evidence.
		if w.Plan.EvidencePeriod == nil && !w.openTaskReply() {
			if err := s.attachThreadContext(ctx, tx, &w); err != nil {
				return w, err
			}
		}
	}
	if w.Kind == "project_group_chat" && w.Plan.GroupReply != nil && !w.openTaskReply() {
		w.Prompt += "\n通过飞书回复当前群。此群的 chat_id 为 " + w.Address.Chat + "。查询本群消息时直接使用此群 ID。按实际取得的资料回答，说明查询范围。"
		if w.groupSources() {
			w.Prompt += "\n本轮向全群展示答复。使用本群可见的工具目录，按实际取得的资料陈述事实。查询时间范围时先核对项目业务日期，再按日期检索本群消息。"
			if w.Plan.Mode == "draft" && singleBrief(w.Plan) {
				w.Prompt += "简报说明实际覆盖的日期、资料和已查证的进展、风险、待跟进事项。"
			}
		}
	}
	if w.Address.Mode == GroupNotice || w.groupSources() {
		w.Scope.UserID = ""
		w.Scope.UserName = ""
	}
	if w.Address.Mode == ProjectChat || w.Address.Mode == PrivateReply && w.Address.Text != "" {
		if w.Address.Mode == PrivateReply {
			w.Kind = "private_chat"
		}
		// Retries of one work reopen its own native session. Later requests
		// receive only the Gateway's reauthorized, frozen personal context.
		fields, _ = json.Marshal([]string{w.Kind, id, w.Agent, p.AccountID, scope})
		if w.Address.ChoicePrompt == "" {
			if err := s.attachPersonalContext(ctx, tx, &w, gateway); err != nil {
				return w, err
			}
		}
	}
	for i := range w.Plan.Actions {
		fields := w.Plan.Actions[i].Fields
		if d := fields["business_date"]; strings.HasPrefix(d, "@") {
			day, err := s.businessDate(ctx, tx, w, d)
			if err != nil {
				return w, err
			}
			fields["business_date"] = day
		}
	}
	if w.Plan.Mode == "revise" && w.Address.ChoicePrompt == "" {
		if err := s.resolveRevision(ctx, tx, &w); err != nil {
			return w, err
		}
	}
	if w.Plan.Mode == "confirm" && w.Address.ChoicePrompt == "" {
		if err := s.resolveConfirmation(ctx, tx, &w); err != nil {
			return w, err
		}
	}
	if p := w.Plan.EvidencePeriod; w.Address.ChoicePrompt == "" && p != nil {
		w.Prompt += fmt.Sprintf("\n资料取证范围（项目时区 %s）：%s 到 %s，包含首尾两日。属于范围外的群消息、日报、会议事件和简报不得作为本次报告依据。各日期检索均须限定在此区间；缺少可核对资料时说明缺口。保存简报的业务日期另行确定。", p.Timezone, p.From, p.To)
	}
	if w.Plan.Mode == "clarify" {
		w.Prompt += "\n请就此点向用户澄清：" + w.Plan.Clarification
	}
	if w.Plan.Mode == "draft" {
		w.Prompt += draftInstructions(w.Plan)
	}
	if w.Plan.Mode == "execute" || w.Plan.Mode == "confirm" {
		w.Prompt += "\n本次用户交办的操作范围（授权详情以 Gateway 为准）：" + string(w.Plan.PromptJSON())
	}
	digest := sha256.Sum256(fields)
	w.Session = hex.EncodeToString(digest[:])
	return w, nil
}
func (s *Service) claimOne(ctx context.Context) (work, error) {
	var w work
	err := pgx.BeginTxFunc(ctx, s.DB, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		var id, state string
		var needsPlan bool
		err := tx.QueryRow(ctx, `SELECT w.id,w.state,w.needs_plan AND w.deadline>now() FROM agent_group_work w
          LEFT JOIN agent_instances a ON a.agent_id=w.agent_id
          WHERE w.tenant_key=$1 AND w.app_id=$2 AND (a.owner_id IS NULL OR a.owner_id=$3)
          AND (w.state IN ('route','running','ready','sending') OR (w.state='awaiting_project' AND w.deadline<=now())) AND w.available_at<=now()
          AND (w.lease_until IS NULL OR w.lease_until<=now())
          AND NOT EXISTS (SELECT FROM agent_group_work prior
            WHERE w.mode='group_reply' AND w.thread_id<>'' AND prior.mode='group_reply'
            AND prior.state IN ('route','running','ready','sending')
            AND (prior.tenant_key,prior.app_id,prior.channel,prior.chat_id,prior.thread_id,prior.assignment_version)=
                (w.tenant_key,w.app_id,w.channel,w.chat_id,w.thread_id,w.assignment_version)
            AND (prior.group_asked_at,prior.id)<(w.group_asked_at,w.id))
          AND NOT EXISTS (SELECT FROM agent_group_work prior
             WHERE w.mode='private_reply' AND prior.mode=w.mode AND prior.direct_question IS NOT NULL AND prior.deadline>now()
             AND prior.state IN ('route','running','ready','sending')
             AND (prior.tenant_key,prior.app_id,prior.channel,prior.chat_id,prior.thread_id,prior.account_id)=
                 (w.tenant_key,w.app_id,w.channel,w.chat_id,w.thread_id,w.account_id)
             AND (prior.personal_at,prior.id)<(w.personal_at,w.id))
          ORDER BY w.available_at,w.id LIMIT 1 FOR UPDATE OF w SKIP LOCKED`, s.Tenant, s.App, s.Owner).Scan(&id, &state, &needsPlan)
		if err != nil {
			return err
		}
		if needsPlan && state == "route" {
			if err = s.planningAccess(ctx, tx, id); err != nil {
				if errors.Is(err, access.ErrForbidden) {
					_, err = tx.Exec(ctx, "UPDATE agent_group_work SET state='suppressed',reason='planning_access_changed',lease_until=NULL WHERE id=$1", id)
				}
				return err
			}
			w.Work, w.State = id, "planning"
			if err = tx.QueryRow(ctx, displayedProposalQuery, id).Scan(&w.PlanRequest.DisplayedProposal); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `UPDATE agent_group_work SET lease_until=least(deadline,now()+interval '100 seconds'),generation=generation+1 WHERE id=$1
   RETURNING coalesce(question,group_question,direct_question),requested_intent,mode='private_reply',project_id<>'',mode='group_reply',generation,lease_until`, id).Scan(&w.PlanRequest.Text, &w.PlanRequest.Intent, &w.PlanRequest.Private, &w.PlanRequest.ProjectSelected, &w.PlanRequest.Group, &w.Generation, &w.planDeadline)
		}
		if state == "sending" {
			_, err = tx.Exec(ctx, "UPDATE agent_group_work SET state='unknown',reason='missing_platform_receipt' WHERE id=$1", id)
			return err
		}
		w, err = s.authorize(ctx, tx, id)
		if errors.Is(err, access.ErrForbidden) || errors.Is(err, errToolReadStale) {
			reason := "scope_or_deadline"
			if errors.Is(err, errToolReadStale) {
				reason = "tool_source_changed"
			}
			_, err = tx.Exec(ctx, "UPDATE agent_group_work SET state=CASE WHEN deadline<=now() THEN 'expired' ELSE 'suppressed' END,reason=$2 WHERE id=$1", id, reason)
			return err
		}
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO agent_instances(agent_id,tenant_key,app_id,project_id,owner_id)
            VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, w.Agent, s.Tenant, s.App, w.configScope(), s.Owner); err != nil {
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
			if w.Address.ChoicePrompt != "" {
				options, err := project.VisiblePortfolio(ctx, tx, w.Actor.TenantKey, w.Actor.AppID, w.Actor.AccountID)
				if err != nil {
					return err
				}
				w.Address.ChoicePrompt = projectChoice(options)
			}
			// A generic failure notice contains no retrieved material.
			if w.Reason != workBindingConflict {
				err = s.checkToolReads(ctx, tx, w.Work, w.Actor, w.Scope.ProjectID)
			}
			if err != nil {
				if !errors.Is(err, access.ErrForbidden) && !errors.Is(err, errToolReadStale) {
					return err
				}
				_, err = tx.Exec(ctx, "UPDATE agent_group_work SET state='suppressed',reason='source_changed',lease_until=NULL WHERE id=$1", id)
				w = work{}
				return err
			}
			if w.Kind == "project_group_chat" && w.Plan.GroupReply != nil {
				if err := s.refreshGroupDelivery(ctx, tx, &w); err != nil {
					if !errors.Is(err, access.ErrForbidden) && !errors.Is(err, errToolReadStale) {
						return err
					}
					_, err = tx.Exec(ctx, "UPDATE agent_group_work SET state='suppressed',reason='source_changed',lease_until=NULL WHERE id=$1", id)
					w = work{}
					return err
				}
			}
			w.State = "sending"
		}
		var leaseUntil time.Time
		err = tx.QueryRow(ctx, `UPDATE agent_group_work SET state=$2,agent_id=$3,session_id=$4,generation=generation+1,config_snapshot=coalesce(config_snapshot,$5::jsonb),
          lease_until=least(deadline,clock_timestamp()+CASE WHEN $2='running' THEN make_interval(secs=>$6) ELSE interval '100 seconds' END)
          WHERE id=$1 RETURNING generation,coalesce(response_text,''),lease_until`,
			id, w.State, w.Agent, w.Session, w.Config, dsh.ExecutionLease.Seconds()).Scan(&w.Generation, &w.Text, &leaseUntil)
		w.ExecuteBeforeMS = leaseUntil.Add(-dsh.ReceiptReserve).UnixMilli()
		return err
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
		if w.Kind == "project_group_chat" && w.Plan.GroupReply != nil || w.Kind == "private_chat" && w.Address.ChoicePrompt == "" {
			expected = w.Text
		}
		if w.Text != expected {
			_, err = s.DB.Exec(ctx, "UPDATE agent_group_work SET state='suppressed',reason='source_changed' WHERE id=$1 AND generation=$2", w.Work, w.Generation)
			return err
		}
		if w.Address.ReplyBody != nil {
			w.Text = w.Address.ReplyBody.PlainText()
		}
		result := s.Channels[w.Address.Channel].Reply(ctx, w.Address, w.Bot, w.Text, w.Delivery)
		state := "unknown"
		switch result.State {
		case "success":
			if result.MessageID != "" {
				state = "sent"
				if w.Address.ChoicePrompt != "" && w.Address.ChoicePrompt != projectChoice(nil) {
					state = "awaiting_project"
				}
			}
		case "failed":
			state = "failed"
		}
		_, err = s.DB.Exec(ctx, `UPDATE agent_group_work SET state=$3,platform_message_id=nullif($4,''),reason=CASE WHEN reason=$6 AND $3='sent' THEN reason ELSE $5 END,lease_until=NULL,
              personal_displayed_at=CASE WHEN mode='private_reply' AND $3='sent' THEN clock_timestamp() END
           WHERE id=$1 AND generation=$2 AND state IN ('sending','unknown')`, w.Work, w.Generation, state, result.MessageID, result.Reason, workBindingConflict)
		return err
	}
	if w.Address.ChoicePrompt != "" {
		_, err = s.DB.Exec(ctx, "UPDATE agent_group_work SET state='ready',response_text=$3,lease_until=NULL WHERE id=$1 AND generation=$2", w.Work, w.Generation, w.publicReply())
		return err
	}
	if w.openTaskReply() {
		return s.prepareOpenGroupTasks(ctx, w)
	}
	result, err := s.Execute(ctx, w.Binding)
	if w.settlesPersonalWrite() {
		// A confirmed single action and its user-visible receipt commit
		// together. A lost Runtime acknowledgement never reopens that action.
		var settled bool
		if dbErr := s.DB.QueryRow(ctx, "SELECT EXISTS(SELECT FROM agent_project_tool_calls WHERE work_id=$1 AND confirmation_args IS NOT NULL)", w.Work).Scan(&settled); dbErr != nil {
			return dbErr
		}
		if settled {
			return nil
		}
	}
	if errors.Is(err, dsh.ErrWorkBindingConflict) {
		return s.finishBindingConflict(ctx, w)
	}
	if errors.Is(err, dsh.ErrConfiguration) {
		return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, "UPDATE agent_group_work SET state='failed',reason='configuration_rejected',lease_until=NULL WHERE id=$1 AND generation=$2 AND state='running'", w.Work, w.Generation)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
			return agentadmin.ApplyFailed(ctx, tx, s.Tenant, s.App, w.configScope(), w.Role, w.ConfigVersion)
		})
	}
	if err != nil {
		_, dbErr := s.DB.Exec(ctx, `UPDATE agent_group_work SET available_at=now()+interval '3 seconds',lease_until=NULL
              WHERE id=$1 AND generation=$2 AND state='running'`, w.Work, w.Generation)
		return errors.Join(err, dbErr)
	}
	if result.Session != w.Session || result.AppliedVersion != w.ConfigVersion {
		return errors.New("unbound runtime session")
	}
	return s.Roles.WithOrganizationMutation(ctx, func(tx pgx.Tx) error {
		if err := agentadmin.Applied(ctx, tx, s.Tenant, s.App, w.configScope(), w.Role, result.AppliedVersion); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE agent_group_work SET applied_config_version=$3 WHERE id=$1 AND generation=$2", w.Work, w.Generation, result.AppliedVersion); err != nil {
			return err
		}
		var receipt Who
		err := tx.QueryRow(ctx, `SELECT tool_receipt FROM agent_group_work w JOIN agent_instances a USING(agent_id)
         WHERE id=$1 AND generation=$2 AND state='running' AND tool_turn=$3 AND owner_id=$4 AND owner_epoch=$5
         AND deadline>now() AND lease_until>now() FOR UPDATE OF w`, w.Work, w.Generation, result.Turn, s.Owner, w.Epoch).Scan(&receipt)
		if err != nil {
			return err
		}
		if w.Address.Mode == ProjectChat || w.Kind == "project_group_chat" || w.Kind == "private_chat" {
			if result.Answer == "" || len(result.Answer) > 48000 {
				return errors.New("missing or oversized project chat response")
			}
			current, err := s.authorize(ctx, tx, w.Work)
			if err != nil && !errors.Is(err, access.ErrForbidden) && !errors.Is(err, errToolReadStale) {
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
			answer, err := s.verifiedAnswer(ctx, tx, w, result)
			if err != nil {
				return err
			}
			if w.Plan.Mode == "draft" {
				answer, err = s.publishDraft(ctx, tx, w, result.Answer)
				if err != nil {
					return err
				}
			}
			answer = current.signature() + "\n" + answer
			if w.Kind == "private_chat" {
				_, err = tx.Exec(ctx, "UPDATE agent_group_work SET state='ready',response_text=$2,lease_until=NULL WHERE id=$1", w.Work, answer)
				return err
			}
			if w.Kind == "project_group_chat" {
				public, err := s.groupReplyText(ctx, tx, current, answer)
				if err != nil {
					return err
				}
				_, err = tx.Exec(ctx, "UPDATE agent_group_work SET state='ready',group_answer=$2,response_text=$3,lease_until=NULL WHERE id=$1", w.Work, answer, public)
				return err
			}
			// Publication into the user's workbench and its receipt are the same database write.
			_, err = tx.Exec(ctx, "UPDATE agent_group_work SET state='sent',response_text=$2,personal_displayed_at=clock_timestamp(),platform_message_id=NULL,lease_until=NULL WHERE id=$1", w.Work, answer)
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE agent_group_work SET state='ready',response_text=$2,lease_until=NULL WHERE id=$1", w.Work, render(w.Address.Mode, receipt))
		return err
	})
}
func (w work) configScope() string {
	if w.Role == "project-pm" {
		return w.Scope.ProjectID
	}
	return ""
}
func (w work) signature() string {
	if w.Scope.ProjectName != "" {
		return w.Config.Name + " · " + w.Scope.ProjectName
	}
	return w.Config.Name + " · 全局服务空间"
}
func (w work) publicReply() string {
	if w.Address.ChoicePrompt != "" {
		if w.Address.ChoicePrompt == projectChoice(nil) {
			return w.Address.ChoicePrompt
		}
		return w.Address.ChoicePrompt + "\n工作编号 " + w.Work
	}
	if w.Kind == "project_group_chat" {
		return w.signature() + "\n请在本项目工作台的 Bot 对话中查看本次请求的结果。"
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
