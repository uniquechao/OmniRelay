package project

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

type deleteTaskInput struct {
	Expected int64  `json:"expected_version"`
	Key      string `json:"request_key"`
	Reason   string `json:"change_reason"`
}

func (s *Service) deleteTask(w http.ResponseWriter, r *http.Request, a identity.Principal) (any, error) {
	var in deleteTaskInput
	if !decodeLimit(w, r, &in, 16<<10) {
		return nil, errInput
	}
	return s.removeTask(r.Context(), scope(a, r.PathValue("projectID")), r.PathValue("taskID"), a, in)
}

// Logical deletion preserves immutable activity and source references. Normal task reads
// exclude removed rows. The receipt commits atomically and is safe to replay after deletion.
func (s *Service) removeTask(ctx context.Context, k taskScope, id string, a identity.Principal, in deleteTaskInput) (any, error) {
	if !identifier.MatchString(id) || in.Expected < 1 || strings.TrimSpace(in.Reason) == "" || !textLimit(in.Reason, 4000) || in.Key != "" && !validKey(in.Key) {
		return nil, errInput
	}
	var result json.RawMessage
	err := k.write(ctx, s, a, "project.assign", func(tx pgx.Tx, _ int64) error {
		var err error
		result, err = k.command(ctx, tx, a, in.Key, []any{"delete", id, in}, func() (any, error) {
			old, err := k.task(ctx, tx, id, time.Now())
			if err != nil {
				return nil, err
			}
			if old.Version != in.Expected {
				return nil, errTaskVersion
			}
			if _, err = tx.Exec(ctx, "UPDATE project_tasks SET deleted_at=clock_timestamp(),version=version+1 WHERE "+projectWhere+" AND id=$4", k.args(id)...); err != nil {
				return nil, err
			}
			if _, err = k.event(ctx, tx, a, id, "change", "task.deleted", strings.TrimSpace(in.Reason), taskValues(old), map[string]any{"已删除": true}); err != nil {
				return nil, err
			}
			// A milestone remains meaningful when its former task is removed.
			if _, err = tx.Exec(ctx, "UPDATE project_milestones SET task_id=NULL WHERE "+projectWhere+" AND task_id=$4", k.args(id)...); err != nil {
				return nil, err
			}
			if err = k.bump(ctx, tx); err != nil {
				return nil, err
			}
			_, err = tx.Exec(ctx, `INSERT INTO audit_events(tenant_key,app_id,actor_account_id,action,resource_type,resource_id,result,change_version,details)
   VALUES($1,$2,$3,'task.deleted','project_task',$4,'success',$5,$6)`, a.TenantKey, a.AppID, a.AccountID, id, old.Version+1, map[string]any{"project_id": k.project, "change_reason": in.Reason})
			return map[string]any{"id": id, "version": old.Version + 1, "deleted": true}, err
		})
		return err
	})
	return result, err
}
