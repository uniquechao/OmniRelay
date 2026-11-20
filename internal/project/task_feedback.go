package project

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

func (s *Service) suggestions(_ http.ResponseWriter, r *http.Request, a identity.Principal) (any, error) {
	ctx, k, id := r.Context(), scope(a, r.PathValue("projectID")), r.PathValue("taskID")
	task, err := k.task(ctx, s.pool, id, time.Now())
	if err != nil {
		return nil, err
	}
	grant, err := s.roles.ProjectGrant(ctx, s.pool, a, k.project)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, "SELECT to_jsonb(f)-'tenant_key'-'app_id' FROM project_task_suggestions f WHERE "+projectWhere+" AND task_id=$4 AND (actor_id=$5 OR $6) ORDER BY created_at DESC,id DESC LIMIT 100", k.args(id, a.AccountID, grant.CanAssign)...)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowTo[json.RawMessage])
	return map[string]any{"items": items, "can_submit": grant.CanComment && ownTask(task, grant.MemberID) && task.Status != "done" && task.Status != "cancelled", "can_review": grant.CanAssign}, err
}
func (s *Service) suggest(w http.ResponseWriter, r *http.Request, a identity.Principal) (any, error) {
	var in struct {
		Version int64  `json:"task_version"`
		Status  string `json:"proposed_status"`
		Note    string `json:"note"`
		Key     string `json:"request_key"`
	}
	if !decodeLimit(w, r, &in, 32<<10) || in.Version < 1 || !validKey(in.Key) || strings.TrimSpace(in.Note) == "" || !textLimit(in.Note, 4000) || in.Status != "in_progress" && in.Status != "done" && in.Status != "blocked" {
		return nil, errInput
	}
	ctx, k, id := r.Context(), scope(a, r.PathValue("projectID")), r.PathValue("taskID")
	var result json.RawMessage
	err := k.write(ctx, s, a, "project.comment", func(tx pgx.Tx, _ int64) error {
		task, err := k.task(ctx, tx, id, time.Now())
		if err != nil {
			return err
		}
		grant, err := s.roles.ProjectGrant(ctx, tx, a, k.project)
		if err != nil {
			return err
		}
		if !ownTask(task, grant.MemberID) {
			return errDenied
		}
		result, err = k.command(ctx, tx, a, in.Key, []any{"feedback", id, in}, func() (any, error) {
			if task.Version != in.Version {
				return nil, errTaskVersion
			}
			if task.Status == "done" || task.Status == "cancelled" {
				return nil, errInput
			}
			var value json.RawMessage
			err := tx.QueryRow(ctx, `INSERT INTO project_task_suggestions AS f(tenant_key,app_id,project_id,task_id,actor_id,task_version,proposed_status,note) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING to_jsonb(f)-'tenant_key'-'app_id'`, k.args(id, a.AccountID, in.Version, in.Status, in.Note)...).Scan(&value)
			return value, err
		})
		return err
	})
	return result, err
}
func (s *Service) review(w http.ResponseWriter, r *http.Request, a identity.Principal) (any, error) {
	var in struct {
		Version int64  `json:"expected_version"`
		Action  string `json:"action"`
		Reason  string `json:"reason"`
	}
	if !decodeLimit(w, r, &in, 16<<10) || in.Version < 1 || in.Action != "accept" && in.Action != "reject" || strings.TrimSpace(in.Reason) == "" || !textLimit(in.Reason, 2000) {
		return nil, errInput
	}
	ctx, k, id := r.Context(), scope(a, r.PathValue("projectID")), r.PathValue("suggestionID")
	var result json.RawMessage
	err := k.write(ctx, s, a, "project.assign", func(tx pgx.Tx, _ int64) error {
		var taskID, status, proposed, note string
		var reviewer, reason *string
		var version, taskVersion int64
		if err := tx.QueryRow(ctx, "SELECT task_id,status,proposed_status,note,version,task_version,reviewed_by,reason,to_jsonb(f)-'tenant_key'-'app_id' FROM project_task_suggestions f WHERE "+projectWhere+" AND id=$4", k.args(id)...).Scan(&taskID, &status, &proposed, &note, &version, &taskVersion, &reviewer, &reason, &result); err != nil {
			return err
		}
		final := "rejected"
		if in.Action == "accept" {
			final = "accepted"
		}
		if status == final && version-1 == in.Version && reviewer != nil && *reviewer == a.AccountID && reason != nil && *reason == in.Reason {
			return nil
		}
		if status != "pending" || version != in.Version {
			return errConflict
		}
		if in.Action == "accept" {
			old, err := k.task(ctx, tx, taskID, time.Now())
			if err != nil {
				return err
			}
			if old.Version != taskVersion {
				return errTaskVersion
			}
			_, err = tx.Exec(ctx, "UPDATE project_tasks SET status=$5,column_id=$5,blocked_reason=CASE WHEN $5='blocked' THEN $6 ELSE '' END,completion_note=CASE WHEN $5='done' THEN $6 ELSE completion_note END,change_reason=$7,version=version+1,position=CASE WHEN column_id=$5 THEN position ELSE (SELECT COALESCE(max(position),0)+1 FROM project_tasks WHERE "+projectWhere+" AND column_id=$5) END WHERE "+projectWhere+" AND id=$4", k.args(taskID, proposed, note, in.Reason)...)
			if err != nil {
				return err
			}
			if err = k.bump(ctx, tx); err != nil {
				return err
			}
			task, err := k.task(ctx, tx, taskID, time.Now())
			if err != nil {
				return err
			}
			if err = k.history(ctx, tx, a, "task.updated", old, task); err != nil {
				return err
			}
		}
		_, err := k.event(ctx, tx, a, taskID, "change", "task.feedback_reviewed", in.Reason, map[string]any{"审核状态": "待审核"}, map[string]any{"审核状态": map[string]string{"accepted": "已采纳", "rejected": "已拒绝"}[final]})
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, "UPDATE project_task_suggestions AS f SET status=$5,reviewed_by=$6,reason=$7,reviewed_at=clock_timestamp(),version=version+1 WHERE "+projectWhere+" AND id=$4 RETURNING to_jsonb(f)-'tenant_key'-'app_id'", k.args(id, final, a.AccountID, in.Reason)...).Scan(&result)
	})
	return result, err
}
