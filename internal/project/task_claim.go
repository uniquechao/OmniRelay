package project

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

func (s *Service) claimTask(w http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
	var in struct {
		Version int64  `json:"expected_version"`
		Key     string `json:"request_key"`
	}
	id := r.PathValue("taskID")
	if !decodeLimit(w, r, &in, 4096) || !identifier.MatchString(id) || in.Version < 1 || !validKey(in.Key) {
		return nil, errInput
	}
	k, ctx := scope(actor, r.PathValue("projectID")), r.Context()
	var result json.RawMessage
	err := k.write(ctx, s, actor, "project.read", func(tx pgx.Tx, _ int64) error {
		grant, err := s.roles.ProjectGrant(ctx, tx, actor, k.project)
		if err != nil {
			return err
		}
		if !grant.Member || grant.MemberID == "" || !(grant.CanAssign || grant.CanUpdateOwn) {
			return errDenied
		}
		result, err = k.command(ctx, tx, actor, in.Key, []any{"claim", id, in.Version}, func() (any, error) {
			old, err := k.task(ctx, tx, id, time.Now())
			if err != nil {
				return nil, err
			}
			if old.Version != in.Version || old.Owner != nil || old.Status == "done" || old.Status == "cancelled" {
				return nil, errTaskVersion
			}
			names, err := k.relationNames(ctx, tx, taskFields{Owner: &grant.MemberID})
			if err != nil {
				return nil, err
			}
			_, err = tx.Exec(ctx, "UPDATE project_tasks SET owner_member_id=$5,relation_names=relation_names||$6::jsonb,version=version+1 WHERE "+projectWhere+" AND id=$4", k.args(id, grant.MemberID, names)...)
			if err != nil {
				return nil, err
			}
			if err := k.bump(ctx, tx); err != nil {
				return nil, err
			}
			task, err := k.task(ctx, tx, id, time.Now())
			if err != nil {
				return nil, err
			}
			return task, k.history(ctx, tx, actor, "task.claimed", old, task)
		})
		return err
	})
	return result, err
}
