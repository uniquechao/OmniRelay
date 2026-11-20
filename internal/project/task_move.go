package project

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"time"

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
	if !identifier.MatchString(id) || in.ExpectedTaskVersion < 1 || in.ExpectedBoardVersion < 1 || strings.TrimSpace(in.RequestKey) == "" || len(in.RequestKey) > 100 || columnNames[in.TargetColumnID] == "" || in.Sort != "" && in.Sort != "manual" {
		return nil, errInput
	}
	command, err := json.Marshal([]any{id, in})
	if err != nil {
		return nil, err
	}
	var result json.RawMessage
	err = k.write(ctx, s, actor, "project.assign", func(tx pgx.Tx, version int64) error {
		var same bool
		err := tx.QueryRow(ctx, "SELECT command=$6::jsonb,result FROM project_task_moves WHERE "+projectWhere+" AND account_id=$4 AND request_key=$5", k.args(actor.AccountID, in.RequestKey, command)...).Scan(&same, &result)
		if err == nil {
			if !same {
				return errMoveKey
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		now := time.Now()
		task, err := k.task(ctx, tx, id, now)
		if err != nil {
			return err
		}
		if version != in.ExpectedBoardVersion {
			return errBoardVersion
		}
		if task.Version != in.ExpectedTaskVersion {
			return errTaskVersion
		}
		if task.Status == "cancelled" {
			return errInput
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
				return err
			}
			position = after + 1
		}
		if in.BeforeID != nil {
			before, err := anchor(in.BeforeID)
			if err != nil {
				return err
			}
			if position > before {
				return errInput
			}
			position = before
		}
		if position == 0 {
			err := tx.QueryRow(ctx, "SELECT COALESCE(max(position),0)+1 FROM project_tasks WHERE "+projectWhere+" AND column_id=$4", k.args(in.TargetColumnID)...).Scan(&position)
			if err != nil {
				return err
			}
		} else {
			_, err := tx.Exec(ctx, "UPDATE project_tasks SET position=position+1 WHERE "+projectWhere+" AND column_id=$4 AND position>=$5", k.args(in.TargetColumnID, position)...)
			if err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, "UPDATE project_tasks SET column_id=$5,status=$5,position=$6,blocked_reason=$7,completion_note=COALESCE(NULLIF($8,''),completion_note),change_reason=$9,version=version+1 WHERE "+projectWhere+" AND id=$4", k.args(id, in.TargetColumnID, position, in.BlockedReason, in.CompletionNote, in.ChangeReason)...)
		if err != nil {
			return err
		}
		if err := k.bump(ctx, tx); err != nil {
			return err
		}
		task, err = k.task(ctx, tx, id, now)
		if err != nil {
			return err
		}
		result, err = json.Marshal(task)
		if err != nil {
			return err
		}
		// 两次均返回数据库规范化后的回执。
		return tx.QueryRow(ctx, "INSERT INTO project_task_moves(tenant_key,app_id,project_id,account_id,request_key,command,result) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING result", k.args(actor.AccountID, in.RequestKey, command, result)...).Scan(&result)
	})
	return result, err
}
