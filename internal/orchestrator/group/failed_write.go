package group

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/agent/dsh"
	"github.com/uniquechao/OmniRelay/internal/agent/pmtools"
	"github.com/uniquechao/OmniRelay/internal/project"
	"slices"
	"strings"
)

func callbackStatus(err error) int {
	if errors.Is(err, access.ErrForbidden) {
		return 403
	}
	if errors.Is(err, errSlot) || errors.Is(err, errToolReadStale) {
		return 409
	}
	return project.ToolStatus(err)
}

func recordRefusal(ctx context.Context, tx pgx.Tx, w work, c projectCall, status int) error {
	if c.Slot < 1 || c.Slot > 128 {
		return nil
	}
	if !slices.ContainsFunc(pmtools.RoleTools(w.Role), func(t pmtools.Tool) bool { return t.Name == c.Name && t.Write }) {
		return nil
	}
	hash, err := toolDigest(c.Args)
	if err != nil {
		return err
	}
	var target string
	for _, key := range []string{"title", "task_id", "project_id", "account_id", "chat_id"} {
		if value := c.Args.String(key); value != "" && len(value) <= 200 && strings.Contains(w.Address.Text, value) {
			target = value
			break
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO agent_project_tool_failures(work_id,slot,tool,fingerprint,status,target_hint)
			SELECT id,$2,$3,$4,$5,$6 FROM agent_group_work WHERE id=$1 AND bound_generation=$7 AND bound_turn=$8
			ON CONFLICT(work_id,slot) DO NOTHING`, w.Work, c.Slot, c.Name, hash, status, target, c.Generation, c.Turn)
	return err
}

func failureLines(ctx context.Context, tx pgx.Tx, id string, result dsh.Result, reconcile bool) ([]string, error) {
	type refusal struct {
		Slot   int
		Tool   string
		Status int
		Target string
	}
	rows, err := tx.Query(ctx, `SELECT f.slot,f.tool,f.status,f.target_hint FROM agent_project_tool_failures f
		WHERE f.work_id=$1 AND NOT EXISTS(SELECT FROM agent_project_tool_calls committed WHERE (committed.work_id,committed.slot)=(f.work_id,f.slot))
		ORDER BY f.slot`, id)
	if err != nil {
		return nil, err
	}
	failed, err := pgx.CollectRows(rows, pgx.RowToStructByPos[refusal])
	if err != nil {
		return nil, err
	}
	confirmed := make([]dsh.FailedWrite, 0, len(failed))
	var lines []string
	for _, f := range failed {
		confirmed = append(confirmed, dsh.FailedWrite{Slot: f.Slot, Tool: f.Tool, Status: f.Status})
		reason := map[int]string{400: "参数不符合要求", 403: "动作、权限或目标不匹配", 409: "对象版本或回执冲突"}[f.Status]
		lines = append(lines, fmt.Sprintf("操作 %d · %s %s · 未提交（%d，%s）", f.Slot, f.Tool, f.Target, f.Status, reason))
	}
	if result.Kind != "" && !reconcile && !slices.Equal(confirmed, result.FailedWrites) {
		return nil, errSlot
	}
	return lines, nil
}
