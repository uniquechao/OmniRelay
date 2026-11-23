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
	if !identifier.MatchString(id) || in.ExpectedTaskVersion < 1 || in.ExpectedBoardVersion < 1 || strings.TrimSpace(in.RequestKey) == "" || len(in.RequestKey) > 100 || !textLimit(in.BlockedReason, 4000) || !textLimit(in.CompletionNote, 4000) || !textLimit(in.ChangeReason, 4000) || !identifier.MatchString(in.TargetColumnID) || in.Sort != "" && in.Sort != "manual" {
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
			cfg, err := k.config(ctx, tx)
			if err != nil {
				return nil, err
			}
			target, err := cfg.resolve(in.TargetColumnID, "")
			if err != nil {
				return nil, err
			}
			if err := transition(task.Status, target.Stage, in.BlockedReason, in.ChangeReason, grant.CanAssign); err != nil {
				return nil, err
			}
			old := task
			if target.Stage != "done" && in.CompletionNote != "" && old.Completion == "" {
				return nil, errInput
			}
			if target.Stage != "blocked" && strings.TrimSpace(in.BlockedReason) != "" {
				return nil, errInput
			}
			if task.Status == "cancelled" {
				return nil, errInput
			}
			position, err := k.movePosition(ctx, tx, actor, id, in)
			if err != nil {
				return nil, err
			}
			_, err = tx.Exec(ctx, "UPDATE project_tasks SET column_id=$5,status=$10,position=$6,blocked_reason=$7,completion_note=COALESCE(NULLIF($8,''),completion_note),change_reason=$9,version=version+1 WHERE "+projectWhere+" AND id=$4", k.args(id, in.TargetColumnID, position, in.BlockedReason, in.CompletionNote, in.ChangeReason, target.Stage)...)
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

func (k taskScope) movePosition(ctx context.Context, tx pgx.Tx, a identity.Principal, id string, in moveCommand) (int64, error) {
	// Read the full destination, including filtered-out cards, before placing a stable anchor.
	type ranked struct {
		ID       string
		Position int64
		Status   string
	}
	rows, err := tx.Query(ctx, "SELECT id,position,status FROM project_tasks WHERE "+projectWhere+" AND column_id=$4 AND id!=$5 AND deleted_at IS NULL ORDER BY position,id", k.args(in.TargetColumnID, id)...)
	if err != nil {
		return 0, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[ranked])
	if err != nil {
		return 0, err
	}
	anchor := func(key *string) (int, error) {
		for i, t := range items {
			if t.ID == *key && t.Status != "cancelled" {
				return i, nil
			}
		}
		return 0, errInput
	}
	insert := len(items)
	if in.AfterID != nil {
		idx, err := anchor(in.AfterID)
		if err != nil {
			return 0, err
		}
		insert = idx + 1
	}
	if in.BeforeID != nil {
		idx, err := anchor(in.BeforeID)
		if err != nil || in.AfterID != nil && idx < insert {
			return 0, errInput
		}
		insert = idx
	}
	left := int64(0)
	if insert > 0 {
		left = items[insert-1].Position
	}
	if insert == len(items) {
		return left + 1024, nil
	}
	right := items[insert].Position
	if right-left > 1 {
		return left + (right-left)/2, nil
	}
	// Integer ranks exhausted. Re-space changed rows, version and record them in the move transaction.
	for i, t := range items {
		rank := int64(i+1) * 1024
		if t.Position == rank {
			continue
		}
		old, err := k.task(ctx, tx, t.ID, time.Now())
		if err != nil {
			return 0, err
		}
		if _, err = tx.Exec(ctx, "UPDATE project_tasks SET position=$5,version=version+1 WHERE "+projectWhere+" AND id=$4", k.args(t.ID, rank)...); err != nil {
			return 0, err
		}
		next, err := k.task(ctx, tx, t.ID, time.Now())
		if err != nil {
			return 0, err
		}
		if err = k.history(ctx, tx, a, "task.reordered", old, next); err != nil {
			return 0, err
		}
	}
	return int64(insert)*1024 + 512, nil
}
