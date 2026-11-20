package project

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

var (
	errTaskVersion  = errors.New("任务版本已变化")
	errBoardVersion = errors.New("看板版本已变化")
	errMoveKey      = errors.New("请求键已用于另一条移动命令")
)

type moveCommand struct {
	ExpectedTaskVersion  int64   `json:"expected_task_version"`
	ExpectedBoardVersion int64   `json:"expected_board_version"`
	TargetColumnID       string  `json:"target_column_id"`
	BeforeID             *string `json:"before_id,omitempty"`
	AfterID              *string `json:"after_id,omitempty"`
	Sort                 string  `json:"sort,omitempty"`
	BlockedReason        string  `json:"blocked_reason,omitempty"`
	CompletionNote       string  `json:"completion_note,omitempty"`
	ChangeReason         string  `json:"change_reason,omitempty"`
	RequestKey           string  `json:"request_key"`
}

func (s *Service) moveTask(w http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
	var in moveCommand
	if !decodeLimit(w, r, &in, 96<<10) {
		return nil, errInput
	}
	return s.applyMove(r.Context(), scope(actor, r.PathValue("projectID")), actor, r.PathValue("taskID"), in)
}

func (s *Service) applyMove(ctx context.Context, k taskScope, actor identity.Principal, id string, in moveCommand) (json.RawMessage, error) {
	if !identifier.MatchString(id) || in.ExpectedTaskVersion < 1 || in.ExpectedBoardVersion < 1 || strings.TrimSpace(in.RequestKey) == "" || len(in.RequestKey) > 100 || !textLimit(in.BlockedReason, 4000) || !textLimit(in.CompletionNote, 4000) || !textLimit(in.ChangeReason, 4000) || columnNames[in.TargetColumnID] == "" || in.Sort != "" && in.Sort != "manual" {
		return nil, errInput
	}
	var result json.RawMessage
	err := k.write(ctx, s, actor, "project.read", func(tx pgx.Tx, version int64) error {
		now := time.Now()
		task, err := k.task(ctx, tx, id, now)
		if err != nil {
			return err
		}
		grant, err := s.roles.ProjectGrant(ctx, tx, actor, k.project)
		if err != nil {
			return err
		}
		if !grant.Member || !grant.CanAssign && !(grant.CanUpdateOwn && ownTask(task, grant.MemberID)) {
			return errDenied
		}
		result, err = k.command(ctx, tx, actor, in.RequestKey, []any{id, in}, func() (any, error) {
			if !canUpdateProgress(task, grant) {
				return nil, errDenied
			}
			if version != in.ExpectedBoardVersion {
				return nil, errBoardVersion
			}
			if task.Version != in.ExpectedTaskVersion {
				return nil, errTaskVersion
			}
			if err := transition(task.Status, in.TargetColumnID, in.BlockedReason, in.ChangeReason, grant.CanAssign); err != nil {
				return nil, err
			}
			old := task
			if in.TargetColumnID != "done" && in.CompletionNote != "" && old.Completion == "" {
				return nil, errInput
			}
			if in.TargetColumnID != "blocked" && strings.TrimSpace(in.BlockedReason) != "" {
				return nil, errInput
			}
			if task.Status == "cancelled" {
				return nil, errInput
			}
			anchor := func(key *string) (int64, error) {
				var position int64
				err := tx.QueryRow(ctx, "SELECT position FROM project_tasks WHERE "+projectWhere+" AND id=$4 AND column_id=$5 AND id!=$6 AND status!='cancelled'", k.args(*key, in.TargetColumnID, id)...).Scan(&position)
				if errors.Is(err, pgx.ErrNoRows) {
					return 0, errInput
				}
				return position, err
			}
			var position int64
			if in.AfterID != nil {
				after, err := anchor(in.AfterID)
				if err != nil {
					return nil, err
				}
				position = after + 1
			}
			if in.BeforeID != nil {
				before, err := anchor(in.BeforeID)
				if err != nil {
					return nil, err
				}
				if position > before {
					return nil, errInput
				}
				position = before
			}
			if position == 0 {
				err := tx.QueryRow(ctx, "SELECT COALESCE(max(position),0)+1 FROM project_tasks WHERE "+projectWhere+" AND column_id=$4", k.args(in.TargetColumnID)...).Scan(&position)
				if err != nil {
					return nil, err
				}
			} else {
				_, err := tx.Exec(ctx, "UPDATE project_tasks SET position=position+1 WHERE "+projectWhere+" AND column_id=$4 AND position>=$5", k.args(in.TargetColumnID, position)...)
				if err != nil {
					return nil, err
				}
			}
			_, err = tx.Exec(ctx, "UPDATE project_tasks SET column_id=$5,status=$5,position=$6,blocked_reason=$7,completion_note=COALESCE(NULLIF($8,''),completion_note),change_reason=$9,version=version+1 WHERE "+projectWhere+" AND id=$4", k.args(id, in.TargetColumnID, position, in.BlockedReason, in.CompletionNote, in.ChangeReason)...)
			if err != nil {
				return nil, err
			}
			if err := k.bump(ctx, tx); err != nil {
				return nil, err
			}
			task, err = k.task(ctx, tx, id, now)
			if err != nil {
				return nil, err
			}
			if err := k.history(ctx, tx, actor, "task.moved", old, task); err != nil {
				return nil, err
			}
			return task, nil
		})
		if errors.Is(err, errRequestKey) {
			return errMoveKey
		}
		return err
	})
	return result, err
}
