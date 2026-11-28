package group

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/routing"
)

type Mode string

const (
	GroupReply   Mode = "group_reply"
	PrivateReply Mode = "private_reply"
	GroupNotice  Mode = "group_notice"
	PersonNotice Mode = "person_notice"
	ProjectChat  Mode = "project_chat"
)

func (m Mode) notice() bool { return m == GroupNotice || m == PersonNotice }

// Envelope is assembled by an authenticated adapter. Sender and conversation
// never come from the text. The same interface is used by the internal gate.
type Envelope struct {
	Tenant, App, Channel, Account, Chat, Thread, Message, Text string
	Mode                                                       Mode
	Project, Recipient, Intent, Parent, ChoicePrompt           string
	Config                                                     int64
	Received                                                   time.Time
	Mention                                                    bool

	// ReplyBody is set only when delivering a frozen, server-generated group result.
	ReplyBody *GroupReplyBody
}
type Assignment struct {
	Project, Bot, Recipient string
	Version                 int64
}
type Receipt struct{ State, MessageID, Reason string }
type Channel struct {
	Resolve func(context.Context, pgx.Tx, Envelope) (Assignment, error)
	Reply   func(context.Context, Envelope, string, string, string) Receipt
}

func Accept(ctx context.Context, tx pgx.Tx, adapter Channel, e Envelope) (string, error) {
	if e.Mode == "" {
		e.Mode = GroupReply
	}
	if e.Mode == ProjectChat {
		if e.Channel != "workbench" || !validQuestion(e.Text) {
			return "", access.ErrForbidden
		}
	} else if (e.Mode != GroupReply && e.Mode != PrivateReply) || (e.Mode == GroupReply && !e.Mention) || !validQuestion(e.Text) {
		return "", access.ErrForbidden
	}
	if e.Tenant == "" || e.App == "" || e.Channel == "" || e.Account == "" || e.Chat == "" || e.Message == "" || e.Received.IsZero() {
		return "", access.ErrForbidden
	}
	a, err := adapter.Resolve(ctx, tx, e)
	if err != nil {
		return "", err
	}
	if (e.Mode != PrivateReply && a.Project == "") || a.Bot == "" {
		return "", access.ErrForbidden
	}
	groupBusiness := e.Mode == GroupReply && !routing.GroupIdentityQuery(e.Text)
	directBusiness := e.Mode == PrivateReply && !routing.GroupIdentityQuery(e.Text)
	requested := e.Intent
	if e.Intent == "" {
		e.Intent = "project"
		if e.Mode == PrivateReply {
			e.Intent = "knowledge"
		}
	}
	if !groupBusiness && !directBusiness && e.Mode != ProjectChat {
		if e.Mode == PrivateReply {
			e.Intent = "knowledge"
		} else {
			e.Intent = "project"
		}
	}
	if e.Intent != "project" && e.Intent != "knowledge" && e.Intent != "admin" {
		return "", access.ErrForbidden
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO agent_group_work(tenant_key,app_id,channel,account_id,chat_id,thread_id,message_id,config_version,project_id,
          membership_generation,assignment_version,permission_version,deadline,mode,question,asked_at,group_question,group_asked_at,intent,direct_question,write_requested,parent_work,choice_prompt,requested_intent,needs_plan,choice_grants,prior_displayed_work)
        SELECT $1,$2,$3,$4,$5,$6,$7,$8, $9::text,coalesce(m.generation,''),$10,a.permission_version,least(now(),$11::timestamptz)+interval '10 minutes',$12,
        CASE WHEN $12='project_chat' THEN $13::text END,CASE WHEN $12='project_chat' THEN now() END, CASE WHEN $14 THEN $13::text END, CASE WHEN $14 THEN now() END,$15, CASE WHEN $16 THEN $13::text END,$17,nullif($18,''),nullif($19,''),$20,$21, CASE WHEN $19<>'' OR $12='private_reply' AND $16 THEN (SELECT jsonb_object_agg(choices.project_id,choices.generation) FROM project_members choices JOIN project_profiles pp USING(tenant_key,app_id,project_id) WHERE
         (choices.tenant_key,choices.app_id,choices.account_id)=(a.tenant_key,a.app_id,a.id) AND choices.role!='revoked') END,
        CASE WHEN $12 IN ('project_chat','private_reply') AND $18='' THEN
          (SELECT old.id FROM agent_group_work old WHERE
          (old.tenant_key,old.app_id,old.account_id,old.mode,old.channel,old.chat_id,old.thread_id)=($1,$2,$4,$12,$3,$5,$6)
          AND old.state='sent' AND (old.question IS NOT NULL OR old.direct_question IS NOT NULL)
          AND old.personal_displayed_at<=least(clock_timestamp(),$11::timestamptz)
          ORDER BY old.personal_displayed_at DESC,old.personal_at DESC,old.id DESC LIMIT 1) END
        FROM accounts a LEFT JOIN project_members m ON (m.tenant_key,m.app_id,m.account_id,m.project_id)=(a.tenant_key,a.app_id,a.id,$9)
        WHERE a.tenant_key=$1 AND a.app_id=$2 AND a.id=$4 AND a.active
        AND ($9='' OR (m.role!='revoked' AND EXISTS(SELECT 1 FROM project_profiles p WHERE
            (p.tenant_key,p.app_id,p.project_id)=(a.tenant_key,a.app_id,$9))))
        ON CONFLICT (tenant_key,app_id,channel,message_id) WHERE plan_id IS NULL DO UPDATE SET message_id=excluded.message_id
        WHERE agent_group_work.mode=excluded.mode AND (agent_group_work.account_id,agent_group_work.chat_id,agent_group_work.thread_id)=
              (excluded.account_id,excluded.chat_id,excluded.thread_id) AND (agent_group_work.mode='private_reply' OR agent_group_work.project_id=excluded.project_id)
        AND agent_group_work.requested_intent=excluded.requested_intent AND agent_group_work.direct_question IS NOT DISTINCT FROM excluded.direct_question AND agent_group_work.question IS NOT DISTINCT FROM excluded.question AND agent_group_work.group_question IS NOT DISTINCT FROM excluded.group_question
        RETURNING id`, e.Tenant, e.App, e.Channel, e.Account, e.Chat, e.Thread, e.Message, e.Config, a.Project, a.Version, e.Received, e.Mode, e.Text, groupBusiness, e.Intent, directBusiness, false, e.Parent, e.ChoicePrompt, requested, groupBusiness || directBusiness || e.Mode == ProjectChat && !routing.GroupIdentityQuery(e.Text)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", access.ErrForbidden
	}
	return id, err
}
