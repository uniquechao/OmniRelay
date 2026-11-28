package group

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
)

const workBindingConflict = "runtime_binding_conflict"

func (s *Service) finishBindingConflict(ctx context.Context, w work) error {
	// A retry with the identical immutable request will receive the same
	// permanent conflict. No raw model output is used in this receipt.
	if w.Address.Mode == GroupReply && w.Plan.GroupReply != nil {
		w.Reason = workBindingConflict
		answer := w.bindingConflictNotice()
		_, err := s.DB.Exec(ctx, "UPDATE agent_group_work SET state='ready',reason=$3,group_answer=$4,response_text=$4,lease_until=NULL WHERE id=$1 AND generation=$2 AND state='running'", w.Work, w.Generation, w.Reason, answer)
		return err
	}
	_, err := s.DB.Exec(ctx, "UPDATE agent_group_work SET state='failed',reason=$3,lease_until=NULL WHERE id=$1 AND generation=$2 AND state='running'", w.Work, w.Generation, workBindingConflict)
	return err
}

func (w work) bindingConflictNotice() string {
	if w.WriteRequested {
		return w.signature() + "\n本次请求执行中断。请先在工作台核对已提交的操作，再决定是否重试。"
	}
	return w.signature() + "\n本次请求执行中断，未能生成答复。请重新发起。"
}

func (w work) openTaskReply() bool {
	return w.Address.Mode == GroupReply && w.Plan.GroupReply != nil && w.Plan.GroupReply.Kind == "open_tasks"
}

// The selected capability returns a group projection. Its recipient identifiers
// are resolved again while claiming delivery and never come from model text.
func (s *Service) currentGroupTasks(ctx context.Context, tx pgx.Tx, w work) (GroupReplyBody, error) {
	allowed, err := s.Roles.CheckInOrganizationMutation(ctx, tx, w.Actor, "project.read", w.Scope.ProjectID)
	if err != nil {
		return GroupReplyBody{}, err
	}
	body := GroupReplyBody{}
	if allowed && (w.Role == "project-pm" || w.Role == "query") &&
		(w.toolConfigured("task_board_get") || w.toolConfigured("task_query") || w.toolConfigured("task_search")) {
		body, err = s.openGroupTasks(ctx, tx, w, w.Plan.GroupReply.MentionEvidence != "")
		if err != nil {
			return body, err
		}
	} else {
		body.Lines = []ReplyLine{{Text: "当前 Agent 无权读取本项目任务看板，无法核实未完成任务。"}}
	}
	body.Lines = append([]ReplyLine{{Text: w.signature()}}, body.Lines...)
	return body, nil
}

func (s *Service) prepareOpenGroupTasks(ctx context.Context, claim work) error {
	return s.Roles.WithOrganizationMutation(ctx, func(tx pgx.Tx) error {
		w, err := s.boundWork(ctx, tx, ToolCall{Binding: claim.Binding})
		if err == nil {
			var body GroupReplyBody
			body, err = s.currentGroupTasks(ctx, tx, w)
			if err == nil {
				_, err = tx.Exec(ctx, "UPDATE agent_group_work SET state='ready',response_text=$2,group_answer=$2,lease_until=NULL WHERE id=$1", w.Work, body.PlainText())
				return err
			}
		}
		if errors.Is(err, access.ErrForbidden) || errors.Is(err, errToolReadStale) {
			_, err = tx.Exec(ctx, "UPDATE agent_group_work SET state='suppressed',reason='scope_or_deadline',lease_until=NULL WHERE id=$1 AND generation=$2 AND state='running'", claim.Work, claim.Generation)
		}
		return err
	})
}

// At ready -> sending, project the current board once more so a closed task
// or a former owner never receives a stale reminder. Persist the exact public
// text for history; only the channel adapter gets the typed mention nodes.
func (s *Service) refreshGroupDelivery(ctx context.Context, tx pgx.Tx, w *work) error {
	if w.openTaskReply() {
		body, err := s.currentGroupTasks(ctx, tx, *w)
		if err != nil {
			return err
		}
		w.Address.ReplyBody = &body
		_, err = tx.Exec(ctx, "UPDATE agent_group_work SET response_text=$2,group_answer=$2 WHERE id=$1", w.Work, body.PlainText())
		return err
	}
	var answer, frozen string
	if err := tx.QueryRow(ctx, "SELECT coalesce(group_answer,''),coalesce(response_text,'') FROM agent_group_work WHERE id=$1", w.Work).Scan(&answer, &frozen); err != nil {
		return err
	}
	expected, err := s.groupReplyText(ctx, tx, *w, answer)
	if err == nil && (expected != frozen || !strings.HasPrefix(answer, w.signature()+"\n")) {
		return errToolReadStale
	}
	return err
}

func (s *Service) groupReplyText(ctx context.Context, tx pgx.Tx, w work, answer string) (string, error) {
	if w.Reason == workBindingConflict {
		return w.bindingConflictNotice(), nil
	}
	g := w.Plan.GroupReply
	if g == nil {
		return w.publicReply(), nil
	}
	if w.WriteRequested {
		var n int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM agent_project_tool_calls WHERE work_id=$1", w.Work).Scan(&n); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s\n本次已提交 %d 项业务操作。", w.signature(), n), nil
	}
	if w.Role != "management-pm" && g.Scope != "personal" && !g.PrivateSources && (w.Plan.Mode == "read" || w.Plan.Mode == "clarify" || w.Plan.Mode == "draft") {
		return answer, nil
	}
	return w.signature() + "\n这次答复涉及本群可见范围以外的资料。你可以在项目工作台的 Bot 对话中查看详情。", nil
}
