package group

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/agent/pmtools"
	"github.com/uniquechao/OmniRelay/internal/routing"
)

var errProposalTarget = errors.New("proposal target is not unique")

const scoped = " (tenant_key,app_id,project_id)=($1,$2,$3)"

// A later confirm must use the revision observed when these values were first
// shown. In particular, reading a newer revision cannot refresh an old proposal.
func (s *Service) proposalVersions(ctx context.Context, tx pgx.Tx, w work, a routing.PlannedAction) (map[string]string, error) {
	t, ok := pmtools.Lookup(a.Tool)
	if !ok {
		return nil, nil
	}
	expected := map[string]string{}
	params := []any{s.Tenant, s.App, w.Scope.ProjectID}
	taskVersion := func(ref string) (string, error) {
		var version string
		err := tx.QueryRow(ctx, `SELECT CASE WHEN count(*)=1 THEN min(t.version)::text ELSE '' END FROM project_tasks t JOIN project_short_codes p USING (tenant_key,app_id,project_id)
 WHERE (t.tenant_key,t.app_id,t.project_id)=($1,$2,$3) AND t.deleted_at IS NULL AND (t.id=$4 OR t.title=$4 OR project_task_code(p.short_code,t.display_number)=$4)`, append(params, ref)...).Scan(&version)
		if err == nil && version == "" {
			err = errProposalTarget
		}
		return version, err
	}
	if a.Tool == "task_bulk_update" {
		var rows []map[string]any
		if json.Unmarshal([]byte(a.Fields["items"]), &rows) != nil {
			return nil, errProposalTarget
		}
		for _, r := range rows {
			ref, ok := r["id"].(string)
			if !ok {
				return nil, errProposalTarget
			}
			v, err := taskVersion(ref)
			if err != nil {
				return nil, err
			}
			expected["items."+ref] = v
		}
	}
	for _, k := range t.RequiredFields() {
		if !routing.VersionField(k) {
			continue
		}
		var v string
		var err error
		query := ""
		args := params
		switch {
		case k == "target_version" || k == "expected_task_version" || k == "task_version" || k == "expected_version" && a.Fields["task_id"] != "":
			ref := a.Fields["task_id"]
			if k == "target_version" {
				ref = a.Fields["target_task_id"]
			}
			v, err = taskVersion(ref)
		case k == "source_revision":
			rows, e := tx.Query(ctx, "SELECT tool,args FROM agent_project_tool_reads WHERE work_id=$1 AND tool IN ('task_candidate_sources','task_candidate_chat_sources')", w.Work)
			if e != nil {
				return nil, e
			}
			type read struct {
				Name string
				Args pmtools.Args
			}
			reads, e := pgx.CollectRows(rows, pgx.RowToStructByPos[read])
			if e != nil {
				return nil, e
			}
			for _, r := range reads {
				val, e := s.Projects.RecheckToolRead(ctx, tx, w.Actor, w.Scope.ProjectID, r.Name, r.Args)
				if e != nil {
					return nil, e
				}
				data, e := json.Marshal(val)
				if e != nil {
					return nil, e
				}
				var snapshot struct {
					Revision string `json:"source_revision"`
				}
				if e = json.Unmarshal(data, &snapshot); e != nil {
					return nil, e
				}
				if v != "" && v != snapshot.Revision {
					return nil, errProposalTarget
				}
				v = snapshot.Revision
			}
		case a.Tool == "briefing_save":
			query = "SELECT coalesce(max(version),0)::text FROM project_daily_briefs WHERE " + scoped + " AND business_date=$4::text::date"
			args = append(args, a.Fields["business_date"])
		case a.Tool == "project_update":
			query = "SELECT version::text FROM project_profiles WHERE " + scoped
		case strings.HasPrefix(a.Tool, "member_"):
			col := "m.version::text"
			if k == "generation" {
				col = "m.generation"
			}
			query = "SELECT CASE WHEN count(*)=1 THEN min(" + col + ") ELSE '' END FROM project_members m JOIN accounts a ON (m.tenant_key,m.app_id,m.account_id)=(a.tenant_key,a.app_id,a.id) WHERE (m.tenant_key,m.app_id,m.project_id)=($1,$2,$3) AND m.role!='revoked' AND a.active AND (a.id=$4 OR a.name=$4)"
			args = append(args, a.Fields["account_id"])
		case k == "primary_version":
			query = "SELECT coalesce(max(version),0)::text FROM project_chat_primary WHERE " + scoped
		case strings.HasPrefix(a.Tool, "chat_"):
			ref := a.Fields["chat_id"]
			b, _ := json.Marshal(ref)
			raw, e := s.Projects.RunTool(ctx, tx, w.Actor, w.Scope.ProjectID, "chat_list", pmtools.Args{"candidate_chat_id": b})
			if e != nil {
				return nil, errProposalTarget
			}
			j, e := json.Marshal(raw)
			if e != nil {
				return nil, e
			}
			var item struct {
				Assignment struct {
					Version int64 `json:"version"`
				} `json:"assignment"`
			}
			if e = json.Unmarshal(j, &item); e != nil {
				return nil, e
			}
			v = strconv.FormatInt(item.Assignment.Version, 10)
		case a.Tool == "task_candidate_review":
			query = "SELECT version::text FROM project_task_candidates WHERE " + scoped + " AND id=$4 AND submitted_by=$5"
			args = append(args, a.Fields["candidate_id"], w.Actor.AccountID)
		case a.Tool == "task_feedback_review":
			query = "SELECT version::text FROM project_task_suggestions WHERE " + scoped + " AND id=$4"
			args = append(args, a.Fields["suggestion_id"])
		case strings.HasPrefix(a.Tool, "task_tag_") || a.Tool == "task_board_save" || k == "expected_board_version":
			query = "SELECT version::text FROM project_task_boards WHERE " + scoped
		default:
			return nil, errProposalTarget
		}
		if query != "" {
			err = tx.QueryRow(ctx, query, args...).Scan(&v)
		}
		if err != nil {
			return nil, err
		}
		if v == "" {
			return nil, errProposalTarget
		}
		expected[k] = v
	}
	return expected, nil
}

// Freeze commonly name-selected mutable resources to their canonical identifier.
// A later rename, deletion or replacement by another same-named object cannot
// redirect a confirmation. Exact IDs for other resource kinds pass through.
func (s *Service) proposalTargets(ctx context.Context, tx pgx.Tx, w work, a routing.PlannedAction) (map[string]string, error) {
	targets := map[string]string{}
	for k, ref := range a.Fields {
		var query string
		switch k {
		case "task_id", "target_task_id", "before_id", "after_id":
			query = `SELECT t.id FROM project_tasks t JOIN project_short_codes p USING(tenant_key,app_id,project_id) WHERE (t.tenant_key,t.app_id,t.project_id)=($1,$2,$3) AND t.deleted_at IS NULL AND (t.id=$4 OR t.title=$4 OR project_task_code(p.short_code,t.display_number)=$4)`
		case "owner_member_id":
			query = `SELECT m.member_id FROM project_members m JOIN accounts a ON (m.tenant_key,m.app_id,m.account_id)=(a.tenant_key,a.app_id,a.id) WHERE (m.tenant_key,m.app_id,m.project_id)=($1,$2,$3) AND a.active AND m.role!='revoked' AND (m.member_id=$4 OR a.name=$4)`
		case "account_id":
			query = `SELECT a.id FROM accounts a WHERE (a.tenant_key,a.app_id)=($1,$2) AND $3::text<>'' AND a.active AND (a.id=$4 OR a.name=$4)`
		case "tag_id":
			query = "SELECT id FROM project_task_tags WHERE " + scoped + " AND (name=$4 OR id=$4)"
		case "milestone_id":
			query = "SELECT id FROM project_milestones WHERE " + scoped + " AND (title=$4 OR id=$4)"
		case "column_id", "default_column_id", "target_column_id":
			query = "SELECT c->>'id' FROM project_task_boards, jsonb_array_elements(columns) c WHERE " + scoped + " AND (c->>'name'=$4 OR c->>'id'=$4)"
		default:
			continue
		}
		var id string
		err := tx.QueryRow(ctx, "SELECT CASE WHEN count(*)=1 THEN min(id) ELSE '' END FROM ("+query+") t(id)", s.Tenant, s.App, w.Scope.ProjectID, ref).Scan(&id)
		if err != nil {
			return nil, err
		}
		if id == "" {
			return nil, errProposalTarget
		}
		targets[k] = id
	}
	return targets, nil
}
