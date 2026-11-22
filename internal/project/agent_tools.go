package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/agent/pmtools"
	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

func ToolStatus(err error) int {
	switch {
	case err == nil:
		return 200
	case errors.Is(err, errDenied), errors.Is(err, chat.ErrAccessLost), errors.Is(err, pgx.ErrNoRows):
		return 403
	case errors.Is(err, errInput), errors.Is(err, pmtools.ErrArguments):
		return 400
	case errors.Is(err, errBoardVersion), errors.Is(err, errConflict), errors.Is(err, errTaskVersion), errors.Is(err, errRequestKey):
		return 409
	default:
		return 503
	}
}

// RunTool is called inside the Gateway's authorized work transaction. No actor or project
// selector is accepted in the model arguments.
func (s *Service) RunTool(ctx context.Context, tx pgx.Tx, actor identity.Principal, projectID, name string, a pmtools.Args) (any, error) {
	t, ok := pmtools.Lookup(name)
	if !ok {
		return nil, errInput
	}
	allowed, err := s.roles.CheckInOrganizationMutation(ctx, tx, actor, t.Action, projectID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, errDenied
	}
	if err = t.Validate(a); err != nil {
		return nil, err
	}
	bound := *s
	bound.mutation = tx
	return bound.executeTool(ctx, scope(actor, projectID), actor, name, a)
}

// Replaying a committed write may return only its original receipt and still requires
// the actor to have authority over the particular resource.
func (s *Service) AuthorizeToolReceipt(ctx context.Context, tx pgx.Tx, actor identity.Principal, projectID, name string, a pmtools.Args) error {
	switch name {
	case "task_update", "task_transition":
		k := scope(actor, projectID)
		task, err := k.task(ctx, tx, a.String("task_id"), time.Now())
		if err != nil {
			return err
		}
		g, err := s.roles.ProjectGrant(ctx, tx, actor, projectID)
		if err != nil {
			return err
		}
		if !g.CanAssign && (!g.CanUpdateOwn || !ownTask(task, g.MemberID)) {
			return errDenied
		}
	case "member_add", "member_set_role", "member_remove":
		var manager bool
		err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT FROM project_members WHERE "+projectWhere+" AND account_id=$4 AND role='manager')", scope(actor, projectID).args(a.String("account_id"))...).Scan(&manager)
		if err != nil {
			return err
		}
		if manager || a.String("role") == "manager" {
			ok, err := s.roles.CheckInOrganizationMutation(ctx, tx, actor, "project.delegate", projectID)
			if err != nil {
				return err
			}
			if !ok {
				return errDenied
			}
		}
	case "chat_bind", "chat_unbind", "chat_set_primary":
		id := a.String("chat_id")
		if _, err := s.chat.ProjectGroupForMutation(ctx, tx, actor, id); err != nil {
			return err
		}
		link, err := assignment(ctx, tx, actor, id)
		if err != nil {
			return err
		}
		if link.ProjectID != "" && link.ProjectID != projectID {
			return errDenied
		}
	}
	return nil
}

func (s *Service) executeTool(ctx context.Context, k taskScope, actor identity.Principal, name string, a pmtools.Args) (any, error) {
	tx := s.mutation
	version := a.Int("expected_version")
	q := url.Values{}
	for _, key := range []string{"q", "role", "date_from", "date_to", "status", "owner_member_id"} {
		if _, ok := a[key]; ok {
			q.Set(key, a.String(key))
		}
	}
	page := max(int64(1), a.Int("page"))
	q.Set("page", strconv.FormatInt(page, 10))
	q.Set("page_size", "25")
	switch name {
	case "project_get", "project_update":
		p, err := k.profile(ctx, tx)
		if err != nil || name == "project_get" {
			return p, err
		}
		if len(a) < 2 {
			return nil, errInput
		}
		in := profileInput{Name: p.Name, Status: p.Status, Background: p.Background}
		raw, _ := json.Marshal(a)
		if err = json.Unmarshal(raw, &in); err != nil {
			return nil, errInput
		}
		if in.Status == "archived" {
			return nil, errInput
		}
		return s.updateProfile(ctx, k, actor, version, &in, nil)
	case "member_list":
		rows, err := tx.Query(ctx, `SELECT jsonb_build_object('member_id',m.member_id,'account_id',a.id,'name',a.name,'role',m.role,'generation',m.generation)
   FROM project_members m JOIN accounts a ON (m.tenant_key,m.app_id,m.account_id)=(a.tenant_key,a.app_id,a.id)
   WHERE (m.tenant_key,m.app_id,m.project_id)=($1,$2,$3) AND a.active AND m.role!='revoked'
   AND ($4='' OR m.role=$4) AND strpos(lower(a.name||' '||m.member_id),lower($5))>0 ORDER BY m.member_id LIMIT 25 OFFSET $6`, k.args(a.String("role"), a.String("q"), (page-1)*25)...)
		if err != nil {
			return nil, err
		}
		return collectToolItems(rows)
	case "member_add", "member_set_role", "member_remove":
		target := a.String("account_id")
		role := a.String("role")
		if role == "revoked" {
			return nil, errInput
		}
		if name == "member_remove" {
			role = "revoked"
		}
		var current, generation string
		err := tx.QueryRow(ctx, "SELECT role,generation FROM project_members WHERE "+projectWhere+" AND account_id=$4", k.args(target)...).Scan(&current, &generation)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if role == "manager" || current == "manager" {
			manager, err := s.roles.CheckInOrganizationMutation(ctx, tx, actor, "project.delegate", k.project)
			if err != nil {
				return nil, err
			}
			if !manager {
				return nil, errDenied
			}
		}
		if name == "member_add" {
			if current != "" && current != "revoked" {
				return nil, errConflict
			}
		} else {
			if current == "" || current == "revoked" {
				return nil, errDenied
			}
			if a.String("generation") != generation {
				return nil, errConflict
			}
		}
		return s.changeMember(ctx, k, actor, target, memberChange{Role: role})
	case "chat_list":
		if id := a.String("candidate_chat_id"); id != "" {
			if err := s.requireSettingsMutation(ctx, tx, actor, k.project); err != nil {
				return nil, err
			}
			if _, err := s.chat.ProjectGroupForMutation(ctx, tx, actor, id); err != nil {
				return nil, err
			}
			link, err := assignment(ctx, tx, actor, id)
			if err != nil {
				return nil, err
			}
			if link.ProjectID != "" && link.ProjectID != k.project {
				return nil, errDenied
			}
			return chatListCandidate{Assignment: link, ID: id}, nil
		}
		return s.visibleChats(ctx, actor, k.project)
	case "chat_search_messages":
		reader, ok := s.chat.(interface {
			SearchProjectMessages(context.Context, pgx.Tx, identity.Principal, string, string, string) (any, error)
		})
		if !ok {
			return nil, errUnavailable
		}
		return reader.SearchProjectMessages(ctx, tx, actor, k.project, a.String("chat_id"), a.String("q"))
	case "chat_bind", "chat_unbind", "chat_set_primary":
		id := a.String("chat_id")
		link, err := assignment(ctx, tx, actor, id)
		if err != nil {
			return nil, err
		}
		if link.ProjectID != k.project && (name != "chat_bind" || link.ProjectID != "") {
			return nil, errDenied
		}
		if name == "chat_set_primary" {
			return s.setPrimary(ctx, actor, k.project, id, version, a.Int("primary_version"))
		}
		next := k.project
		if name == "chat_unbind" {
			next = ""
		}
		return s.setAssignment(ctx, actor, id, next, version)
	case "file_search", "file_get":
		if name == "file_get" {
			q.Set("file_ref", a.String("file_ref"))
		}
		view, err := s.sourcePanel(ctx, actor, k.project, "agent_files", q)
		if err != nil {
			return nil, err
		}
		if name == "file_get" {
			items := view.Data["items"].([]sourceSummary)
			if len(items) != 1 {
				return nil, errDenied
			}
			item := items[0]
			if item.FileID != 0 {
				return map[string]any{"file": item, "authorized_path": fmt.Sprintf("/api/v1/feishu/files/%d", item.FileID)}, nil
			}
			var excerpt string
			err = tx.QueryRow(ctx, `SELECT left(s.body,1500) FROM project_sources s JOIN project_source_readers r
   ON (r.tenant_key,r.app_id,r.project_id,r.kind,r.source_id)=(s.tenant_key,s.app_id,s.project_id,s.kind,s.id)
   WHERE (s.tenant_key,s.app_id,s.project_id,s.kind,s.id,r.account_id)=($1,$2,$3,$4,$5,$6)`, k.args(item.Kind, item.RefID, actor.AccountID)...).Scan(&excerpt)
			return map[string]any{"file": item, "excerpt": excerpt}, err
		}
		return view, nil
	case "daily_report_query":
		switch a.String("source") {
		case "", "reports":
		case "published":
			return s.publishedToolReports(ctx, k, actor, q)
		default:
			return nil, errInput
		}
		return s.reports(ctx, actor, k.project, q)
	case "task_search":
		rows, err := tx.Query(ctx, `SELECT jsonb_build_object('id',id,'title',title,'status',status,'version',version,'owner_member_id',owner_member_id)
   FROM project_tasks WHERE `+projectWhere+` AND strpos(lower(title||' '||description),lower($4))>0 AND ($5='' OR status=$5) AND ($6='' OR owner_member_id=$6)
   ORDER BY id LIMIT 25 OFFSET $7`, k.args(q.Get("q"), q.Get("status"), q.Get("owner_member_id"), (page-1)*25)...)
		if err != nil {
			return nil, err
		}
		return collectToolItems(rows)
	case "task_get":
		return k.task(ctx, tx, a.String("task_id"), time.Now())
	case "task_create", "task_update", "task_transition":
		id := a.String("task_id")
		in := saveTask{Expected: version}
		if id != "" {
			old, err := k.task(ctx, tx, id, time.Now())
			if err != nil {
				return nil, err
			}
			in.taskFields = old.taskFields
		}
		if name == "task_update" && len(a) < 3 {
			return nil, errInput
		}
		raw, _ := json.Marshal(a)
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, errInput
		}
		if name == "task_create" && in.Owner == nil {
			grant, err := s.roles.ProjectGrant(ctx, tx, actor, k.project)
			if err != nil {
				return nil, err
			}
			in.Owner = &grant.MemberID
		}
		result, err := s.saveTaskInput(ctx, k, id, actor, in)
		if err != nil {
			return nil, err
		}
		var saved storedTask
		if err = json.Unmarshal(result.(json.RawMessage), &saved); err != nil {
			return nil, err
		}
		return map[string]any{"id": saved.ID, "version": saved.Version, "status": saved.Status}, nil
	case "task_add_progress":
		// The outer tool ledger deduplicates this command. The task domain also requires its own key.
		var key string
		if err := tx.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&key); err != nil {
			return nil, err
		}
		result, err := s.addProgress(ctx, k, a.String("task_id"), actor, progressInput{version, key, a.String("body")})
		if err != nil {
			return nil, err
		}
		var event activityEvent
		if err = json.Unmarshal(result.(json.RawMessage), &event); err != nil {
			return nil, err
		}
		return map[string]any{"id": event.ID, "task_id": a.String("task_id")}, nil
	}
	return nil, errInput
}

// Published briefs expose the aggregate the channel actually delivered. A draft, a
// private note, or a receipt whose source has been withdrawn is never sufficient.
func (s *Service) publishedToolReports(ctx context.Context, k taskScope, actor identity.Principal, q url.Values) (any, error) {
	from, to := q.Get("date_from"), q.Get("date_to")
	if from != "" && !validReportDate(from) || to != "" && !validReportDate(to) || from != "" && to != "" && from > to {
		return nil, errInput
	}
	empty := map[string]any{"items": []any{}}
	var target string
	err := s.mutation.QueryRow(ctx, `SELECT l.chat_id FROM project_relay_settings c JOIN project_chat_links l
 ON (l.tenant_key,l.app_id,l.project_id,l.chat_id)=(c.tenant_key,c.app_id,c.project_id,c.target_chat_id)
 WHERE (c.tenant_key,c.app_id,c.project_id)=($1,$2,$3) AND c.public_submission_count`, k.args()...).Scan(&target)
	if errors.Is(err, pgx.ErrNoRows) {
		return empty, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err = s.chat.ProjectGroupForMutation(ctx, s.mutation, actor, target); errors.Is(err, chat.ErrAccessLost) {
		return empty, nil
	} else if err != nil {
		return nil, err
	}
	page, _ := strconv.Atoi(q.Get("page"))
	rows, err := s.mutation.Query(ctx, `SELECT jsonb_build_object('source','published','business_date',r.business_date,'visible_submissions',r.visible_submissions,'chat_id',r.target_chat_id)
 FROM project_briefing_runs r JOIN feishu_send_operations o ON (o.tenant_key,o.app_id,o.account_id,o.chat_id,o.operation_key)=(r.tenant_key,r.app_id,r.account_id,r.target_chat_id,'briefing:'||r.id)
 JOIN feishu_send_parts p ON (p.tenant_key,p.app_id,p.operation_key)=(o.tenant_key,o.app_id,o.operation_key)
 JOIN feishu_chat_messages m ON (m.tenant_key,m.app_id,m.chat_id,m.message_id)=(o.tenant_key,o.app_id,o.chat_id,p.message_id)
 WHERE (r.tenant_key,r.app_id,r.project_id,r.target_chat_id)=($1,$2,$3,$4) AND p.position=0 AND p.state='success' AND o.business_receipt IS NOT NULL
 AND NOT m.deleted AND (m.thread_id='' OR m.visible_in_chat) AND (nullif($5,'') IS NULL OR r.business_date>=nullif($5,'')::date)
 AND (nullif($6,'') IS NULL OR r.business_date<=nullif($6,'')::date) AND strpos(lower(m.search_text),lower($7))>0
 ORDER BY r.business_date DESC,r.id LIMIT 25 OFFSET $8`, k.args(target, from, to, q.Get("q"), (page-1)*25)...)
	if err != nil {
		return nil, err
	}
	return collectToolItems(rows)
}

func collectToolItems(rows pgx.Rows) (any, error) {
	items, err := pgx.CollectRows(rows, pgx.RowTo[json.RawMessage])
	return map[string]any{"items": items}, err
}
func fileRef(item sourceSummary) string {
	if item.FileID != 0 {
		return fmt.Sprintf("drive:%d", item.FileID)
	}
	return item.Kind + ":" + strings.TrimSpace(item.RefID)
}
