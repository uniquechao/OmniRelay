package group

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
)

// Project tasks have no chat owner. A sole collaboration group or the explicitly
// designated primary group may publish the minimal board fields. Other groups
// may only see tasks corroborated by a currently public message in that chat.
const openGroupTaskQuery = `WITH binding AS (
  SELECT c.primary_group OR NOT EXISTS (SELECT FROM project_chat_assignments other
    WHERE (other.tenant_key,other.app_id,other.project_id)=($1,$2,$3) AND other.chat_id<>$4) AS board,
    g.members,g.member_names
  FROM project_chat_assignments c JOIN feishu_bot_configs b USING(tenant_key,app_id)
  LEFT JOIN feishu_drive_group_memberships g ON (g.tenant_key,g.app_id,g.chat_id)=(c.tenant_key,c.app_id,c.chat_id)
    AND g.generation=b.credentials_generation AND g.verified_at>now()-interval '26 hours'
  WHERE (c.tenant_key,c.app_id,c.project_id,c.chat_id)=($1,$2,$3,$4) AND b.enabled
    AND NOT EXISTS (SELECT FROM feishu_chat_messages msg
      WHERE (msg.tenant_key,msg.app_id,msg.chat_id,msg.message_id)=($1,$2,$4,$6) AND msg.deleted)
), owners AS (
  SELECT person.id AS open_id,
    coalesce(nullif(nullif(r.member_names->>person.id,person.id),''),nullif(a.name,person.id),'') AS name
  FROM binding r CROSS JOIN LATERAL unnest(r.members) person(id)
  LEFT JOIN accounts a ON (a.tenant_key,a.app_id,a.open_id)=($1,$2,person.id)
  LEFT JOIN project_members m ON (m.tenant_key,m.app_id,m.project_id,m.account_id)=($1,$2,$3,a.id)
  LEFT JOIN directory_roster dr ON (dr.tenant_key,dr.app_id,dr.open_id)=($1,$2,person.id)
  LEFT JOIN directory_sources ds ON (ds.tenant_key,ds.app_id)=($1,$2) AND ds.visible
  LEFT JOIN directory_people dp ON (dp.tenant_key,dp.app_id,dp.snapshot_version,dp.open_id)=($1,$2,ds.active_version,person.id)
  WHERE person.id ~ '^ou_[A-Za-z0-9_-]+$' AND coalesce(a.active,true) AND m.role IS DISTINCT FROM 'revoked'
    AND dr.resigned_at IS NULL AND dp.directory_status IS DISTINCT FROM 'resigned'
), available AS (
  SELECT t.display_number, CASE WHEN r.board THEN project_task_code(p.short_code,t.display_number) END AS code,
    t.title,CASE WHEN r.board THEN t.status ELSE 'open' END AS status,r.board AND t.owner_member_id IS NOT NULL AS assigned,
    CASE WHEN r.board OR proof.owner_seen THEN o.open_id END AS open_id,
    CASE WHEN r.board OR proof.owner_seen THEN o.name END AS name,count(*) OVER () AS total
  FROM binding r CROSS JOIN project_tasks t JOIN project_short_codes p USING(tenant_key,app_id,project_id)
  LEFT JOIN project_members owner ON (owner.tenant_key,owner.app_id,owner.project_id,owner.member_id)=
    (t.tenant_key,t.app_id,t.project_id,t.owner_member_id)
  LEFT JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(owner.tenant_key,owner.app_id,owner.account_id) AND a.active
  LEFT JOIN owners o ON CASE WHEN owner.member_id IS NULL THEN t.owner_member_id='feishu:'||o.open_id
    ELSE owner.role<>'revoked' AND a.open_id=o.open_id END
  LEFT JOIN LATERAL (
      SELECT true AS seen,msg.mention_names ? o.open_id AS owner_seen FROM project_task_sources ts JOIN project_sources src ON
        (src.tenant_key,src.app_id,src.project_id,src.kind,src.id)=(ts.tenant_key,ts.app_id,ts.project_id,ts.kind,ts.source_id)
      JOIN feishu_chat_messages msg ON (msg.tenant_key,msg.app_id,msg.chat_id,msg.message_id)=(src.tenant_key,src.app_id,src.chat_id,src.id)
      JOIN project_source_readers sr ON (sr.tenant_key,sr.app_id,sr.project_id,sr.kind,sr.source_id,sr.account_id)=
        (ts.tenant_key,ts.app_id,ts.project_id,ts.kind,ts.source_id,$5)
      WHERE NOT r.board AND (ts.tenant_key,ts.app_id,ts.project_id,ts.task_id,ts.kind)=($1,$2,$3,t.id,'message')
        AND msg.chat_id=$4 AND msg.visible_in_chat AND NOT msg.deleted AND length(trim(t.title))>0
        AND strpos(msg.search_text,t.title)>0
        AND NOT EXISTS(SELECT FROM feishu_chat_sync sync WHERE (sync.tenant_key,sync.app_id,sync.chat_id,sync.thread_id)=($1,$2,$4,'') AND sync.source_blocked)
        AND NOT EXISTS(SELECT FROM feishu_candidate_event_owners e WHERE (e.tenant_key,e.app_id,e.source_id)=($1,$2,msg.message_id) AND e.run_id IS NOT NULL)
      ORDER BY owner_seen DESC NULLS LAST LIMIT 1
  ) proof ON true
  WHERE (t.tenant_key,t.app_id,t.project_id)=($1,$2,$3) AND t.deleted_at IS NULL
    AND t.status IN ('todo','in_progress','blocked') AND (r.board OR proof.seen)
)
SELECT r.board,coalesce(t.total,0),coalesce(t.code,''),coalesce(t.title,''),coalesce(t.status,''),
  coalesce(t.assigned,false),coalesce(t.open_id,''),coalesce(t.name,'')
FROM binding r LEFT JOIN available t ON true
ORDER BY CASE t.status WHEN 'blocked' THEN 0 WHEN 'in_progress' THEN 1 ELSE 2 END,
  CASE WHEN NOT r.board THEN t.title END,t.display_number
LIMIT 150`

type groupTaskRow struct {
	board                  bool
	total                  int
	code, title, status    string
	assigned               bool
	ownerOpenID, ownerName string
}

func (s *Service) openGroupTasks(ctx context.Context, tx pgx.Tx, w work, mentionOwners bool) (GroupReplyBody, error) {
	e := w.Address
	if e.Mode != GroupReply || e.Tenant != s.Tenant || e.App != s.App || e.Project == "" || e.Chat == "" || e.Project != w.Scope.ProjectID || w.Actor.AccountID == "" {
		return GroupReplyBody{}, access.ErrForbidden
	}
	rows, err := tx.Query(ctx, openGroupTaskQuery, e.Tenant, e.App, e.Project, e.Chat, w.Actor.AccountID, e.Message)
	if err != nil {
		return GroupReplyBody{}, err
	}
	tasks, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (groupTaskRow, error) {
		var t groupTaskRow
		err := row.Scan(&t.board, &t.total, &t.code, &t.title, &t.status, &t.assigned, &t.ownerOpenID, &t.ownerName)
		return t, err
	})
	if err != nil {
		return GroupReplyBody{}, err
	}
	if len(tasks) == 0 {
		return GroupReplyBody{}, access.ErrForbidden
	}
	return renderGroupTasks(tasks, mentionOwners)
}

func groupTaskLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

func renderGroupTasks(tasks []groupTaskRow, mentionOwners bool) (GroupReplyBody, error) {
	first := tasks[0]
	scope := "本群绑定项目"
	if !first.board {
		scope = "本群公开消息可核实"
	}
	if first.total == 0 {
		return GroupReplyBody{Lines: []ReplyLine{{Text: scope + "的未完成任务共 0 项。"}}}, nil
	}
	body := GroupReplyBody{Lines: []ReplyLine{{Text: fmt.Sprintf("%s的未完成任务共 %d 项", scope, first.total)}}}
	used := 0
	for _, t := range tasks {
		status := ""
		switch t.status {
		case "blocked":
			status = "受阻"
		case "in_progress":
			status = "进行中"
		case "todo":
			status = "待办"
		case "open":
			status = "未完成"
		default:
			return GroupReplyBody{}, errors.New("invalid open task status")
		}
		line := ReplyLine{Text: fmt.Sprintf("　%s · %s · %s", t.code, status, groupTaskLabel(t.title))}
		if !t.board {
			line.Text = "　" + groupTaskLabel(t.title)
		}
		name := groupTaskLabel(t.ownerName)
		switch {
		case !t.board && t.ownerOpenID == "":
			line.Text = strings.TrimSpace(line.Text)
			if mentionOwners {
				line.Text += "（本群依据未确认负责人）"
			}
		case t.board && !t.assigned:
			line.Text = "未指派负责人" + line.Text
		case t.ownerOpenID == "":
			line.Text = "负责人群身份未核实" + line.Text
		case mentionOwners:
			line.MentionName, line.MentionOpenID = name, t.ownerOpenID
			if line.MentionName == "" {
				line.MentionName = "负责人"
			}
		case name != "":
			line.Text = name + line.Text
		default:
			line.Text = "已指派负责人" + line.Text
		}
		// Leave room for the post wrapper, native tags and any undelivered-mention notice.
		encoded, err := json.Marshal(line)
		if err != nil {
			return GroupReplyBody{}, err
		}
		used += len(encoded) + 100
		if used > 18*1024 {
			break
		}
		body.Lines = append(body.Lines, line)
	}
	if shown := len(body.Lines) - 1; shown < first.total {
		body.Lines = append(body.Lines, ReplyLine{Text: fmt.Sprintf("本条已列 %d 项，另有 %d 项未展示。", shown, first.total-shown)})
	}
	return body, nil
}
