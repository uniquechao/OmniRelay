package group

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/agent/dsh"
	"github.com/uniquechao/OmniRelay/internal/agent/pmtools"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/project"
	"github.com/uniquechao/OmniRelay/internal/routing"
)

type delegatedWork struct {
	Parent  string `json:"parent_work"`
	ID      string `json:"work_id"`
	Project string `json:"project_id"`
	State   string `json:"state"`
}

func delegationStatus(ctx context.Context, tx pgx.Tx, actor identity.Principal, parent string) ([]delegatedWork, error) {
	rows, err := tx.Query(ctx, `SELECT w.parent_work,w.id,w.project_id,w.state FROM agent_group_work w
	 JOIN project_members m ON (m.tenant_key,m.app_id,m.project_id,m.account_id)=(w.tenant_key,w.app_id,w.project_id,w.account_id)
	 JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(w.tenant_key,w.app_id,w.account_id)
	 WHERE w.tenant_key=$1 AND w.app_id=$2 AND w.account_id=$3 AND m.role!='revoked' AND a.active
	 AND w.permission_version=a.permission_version AND w.membership_generation=m.generation AND w.parent_work IS NOT NULL AND ($4='' OR w.parent_work=$4)
	 ORDER BY w.asked_at DESC,w.id LIMIT 100`, actor.TenantKey, actor.AppID, actor.AccountID, parent)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[delegatedWork])
}
func (s *Service) managementTool(ctx context.Context, tx pgx.Tx, w work, c projectCall, fingerprint string) (json.RawMessage, error) {
	switch c.Name {
	case "visible_projects":
		if c.Slot != 0 {
			return nil, errSlot
		}
		summary, err := project.PortfolioSummary(ctx, tx, w.Actor)
		if err != nil {
			return nil, err
		}
		digest, err := toolDigest(summary)
		if err != nil {
			return nil, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO agent_project_tool_reads(work_id,tool,fingerprint,args,digest) VALUES($1,$2,$3,'{}',$4) ON CONFLICT DO NOTHING`, w.Work, c.Name, fingerprint, digest)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"items": summary})
	case "delegation_status":
		if c.Slot != 0 {
			return nil, errSlot
		}
		progress, err := delegationStatus(ctx, tx, w.Actor, c.Args.String("parent_work"))
		if err != nil {
			return nil, err
		}
		digest, err := toolDigest(progress)
		if err != nil {
			return nil, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO agent_project_tool_reads(work_id,tool,fingerprint,args,digest) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, w.Work, c.Name, fingerprint, c.Args, digest)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"items": progress})
	case "delegate_project":
		target := c.Args.String("project_id")
		if c.Slot < 1 || c.Slot > 128 {
			return nil, errSlot
		}
		projects, err := project.VisiblePortfolio(ctx, tx, w.Actor.TenantKey, w.Actor.AppID, w.Actor.AccountID)
		if err != nil {
			return nil, err
		}
		if !slices.ContainsFunc(projects, func(p project.PortfolioProject) bool { return p.ID == target && p.Status != "archived" }) {
			return nil, access.ErrForbidden
		}
		var fp, name string
		var receipt json.RawMessage
		err = tx.QueryRow(ctx, "SELECT tool,fingerprint,receipt FROM agent_project_tool_calls WHERE work_id=$1 AND slot=$2", w.Work, c.Slot).Scan(&name, &fp, &receipt)
		if err == nil {
			if fp != fingerprint || name != c.Name {
				return nil, errSlot
			}
			return receipt, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if err = s.checkWriteTargets(ctx, tx, w, c); err != nil {
			return nil, err
		}
		ch, ok := s.Channels["workbench"]
		if !ok {
			return nil, errors.New("workbench channel unavailable")
		}
		var item int
		if err = tx.QueryRow(ctx, "SELECT item FROM agent_action_grants_used WHERE work_id=$1 AND slot=$2", w.Work, c.Slot).Scan(&item); err != nil {
			return nil, err
		}
		handoff := w.Plan.Actions[item-1]
		childPlan := routing.Plan{Mode: "read", Intent: "project"}
		if period := w.Plan.EvidencePeriod; period != nil {
			scoped := *period
			scoped.Timezone = ""
			if scoped.Kind != "range" {
				scoped.From, scoped.To = "", ""
			}
			childPlan.EvidencePeriod = &scoped
		}
		if len(handoff.ChildActions) > 0 {
			childPlan.Mode = "execute"
			childPlan.Actions = handoff.ChildActions
		}
		id, err := Accept(ctx, tx, ch, Envelope{Tenant: s.Tenant, App: s.App, Account: w.Actor.AccountID, Mode: ProjectChat, Channel: "workbench", Chat: target, Project: target, Intent: "project", Message: "delegate:" + w.Work + ":" + target, Text: handoff.Evidence, Parent: w.Work, Received: time.Now()})
		if err != nil {
			return nil, err
		}
		// The parent delegates only this target's grounded sub-plan; the child
		// cannot reinterpret another project's part of the authenticated request.
		if _, err = tx.Exec(ctx, "UPDATE agent_group_work SET needs_plan=false,semantic_plan=$2,write_requested=$3 WHERE id=$1 AND needs_plan", id, childPlan.JSON(), childPlan.Mode == "execute"); err != nil {
			return nil, err
		}
		receipt, err = json.Marshal(map[string]string{"work_id": id, "project_id": target})
		if err != nil {
			return nil, err
		}
		_, err = tx.Exec(ctx, "INSERT INTO agent_project_tool_calls(work_id,slot,tool,fingerprint,receipt) VALUES($1,$2,$3,$4,$5)", w.Work, c.Slot, c.Name, fingerprint, receipt)
		return receipt, err
	}
	return nil, access.ErrForbidden
}

func (s *Service) verifiedAnswer(ctx context.Context, tx pgx.Tx, w work, result dsh.Result) (string, error) {
	reconcile := result.Kind == "reconcile" && w.LegacyOutput
	if result.Kind == "reconcile" && !reconcile {
		return "", errSlot
	}
	if w.Plan.Mode == "clarify" && w.Plan.Clarification != "" && !reconcile {
		if len(result.WriteSlots) > 0 || result.Kind == "writes" {
			return "", errSlot
		}
		return w.Plan.Clarification, nil
	}
	if !w.WriteRequested && !reconcile {
		if len(result.WriteSlots) > 0 || result.Kind == "writes" {
			return "", errSlot
		}
		if w.Role == "query" && !routing.GroupIdentityQuery(w.Address.Text) {
			return citedAnswer(ctx, tx, w, result.Answer)
		}
		return result.Answer, nil
	}
	rows, err := tx.Query(ctx, "SELECT slot,tool,receipt FROM agent_project_tool_calls WHERE work_id=$1 ORDER BY slot", w.Work)
	if err != nil {
		return "", err
	}
	type saved struct {
		Slot    int
		Tool    string
		Receipt map[string]any
	}
	calls, err := pgx.CollectRows(rows, pgx.RowToStructByPos[saved])
	if err != nil {
		return "", err
	}
	serverSlots := make([]int, 0, len(calls))
	for _, c := range calls {
		serverSlots = append(serverSlots, c.Slot)
	}
	if !reconcile && result.Kind != "" && (!slices.Equal(serverSlots, result.WriteSlots) || (result.Kind == "writes") != (len(serverSlots) > 0)) {
		return "", errSlot
	}
	failed, err := failureLines(ctx, tx, w.Work, result, reconcile)
	if err != nil {
		return "", err
	}
	// A reply is a delivery receipt. Report each missing business operation
	// separately, even when the model stopped after a successful first step.
	accounted := map[string]int{}
	for _, call := range calls {
		accounted[call.Tool]++
	}
	for _, failure := range result.FailedWrites {
		accounted[failure.Tool]++
	}
	var missing []string
	for _, action := range w.Plan.Actions {
		if accounted[action.Tool] > 0 {
			accounted[action.Tool]--
			continue
		}
		status := action.Tool + " 尚未提交。"
		if action.Tool == "briefing_save" {
			status = "简报尚未保存。"
		}
		switch {
		case !w.toolConfigured(action.Tool):
			status += "本轮 Agent 配置未启用该工具。"
		default:
			tool, ok := pmtools.Lookup(action.Tool)
			if ok && tool.Action != "" {
				allowed, err := s.Roles.CheckInOrganizationMutation(ctx, tx, w.Actor, tool.Action, w.Scope.ProjectID)
				if err != nil {
					return "", err
				}
				if !allowed {
					status += "当前账号缺少办理这项操作的权限。"
				}
			}
		}
		missing = append(missing, status)
	}
	if len(calls) == 0 {
		lines := []string{"本次没有已提交的业务操作。"}
		lines = append(lines, failed...)
		lines = append(lines, missing...)
		var reason string
		if err := tx.QueryRow(ctx, "SELECT reason FROM agent_group_work WHERE id=$1", w.Work).Scan(&reason); err != nil {
			return "", err
		}
		if reason == "ambiguous_member" {
			lines = append(lines, "存在同名成员，请指明要分派给哪一位。")
		}
		return strings.Join(lines, "\n"), nil
	}
	var lines []string
	for _, c := range calls {
		if c.Tool == "briefing_save" {
			date, dateOK := c.Receipt["business_date"].(string)
			version, versionOK := c.Receipt["version"].(float64)
			if _, err := time.Parse(time.DateOnly, date); err != nil || !dateOK || !versionOK || version < 1 || version != float64(int64(version)) {
				return "", errSlot
			}
			lines = append(lines, fmt.Sprintf("已保存 %s 项目简报（版本 %d）。", date, int64(version)))
			continue
		}
		line := fmt.Sprintf("操作 %d · %s · 已提交", c.Slot, c.Tool)
		for _, key := range []string{"task_code", "task_id", "id", "work_id", "project_id", "chat_id", "version"} {
			if value, ok := c.Receipt[key]; ok && value != nil {
				line += fmt.Sprintf(" · %s=%v", key, value)
			}
		}
		lines = append(lines, line)
	}
	lines = append(lines, failed...)
	lines = append(lines, missing...)
	return strings.Join(lines, "\n"), nil
}
