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
		explicit := false
		counts := map[string]int{}
		for _, p := range projects {
			counts[p.Name]++
		}
		instruction := strings.Join(routing.AuthorizedClauses(w.Address.Text, c.Name), "；")
		for _, p := range projects {
			if p.ID == target && (namesID(instruction, p.ID) || counts[p.Name] == 1 && strings.Contains(instruction, p.Name)) {
				if p.Status == "archived" {
					return nil, access.ErrForbidden
				}
				explicit = true
			}
		}
		if !explicit {
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
		ch, ok := s.Channels["workbench"]
		if !ok {
			return nil, errors.New("workbench channel unavailable")
		}
		id, err := Accept(ctx, tx, ch, Envelope{Tenant: s.Tenant, App: s.App, Account: w.Actor.AccountID, Mode: ProjectChat, Channel: "workbench", Chat: target, Project: target, Intent: "project", Message: "delegate:" + w.Work + ":" + target, Text: w.Address.Text, Parent: w.Work, Received: time.Now()})
		if err != nil {
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
	if len(calls) == 0 {
		var reason string
		if err := tx.QueryRow(ctx, "SELECT reason FROM agent_group_work WHERE id=$1", w.Work).Scan(&reason); err != nil {
			return "", err
		}
		if reason == "ambiguous_member" {
			return "本次没有已提交的业务操作。存在同名成员，请在原交办中注明成员编号。", nil
		}
		if len(failed) > 0 {
			return "本次没有已提交的业务操作。\n" + strings.Join(failed, "\n"), nil
		}
		if strings.HasSuffix(result.Answer, "？") && !strings.ContainsAny(result.Answer, "已成") {
			return "尚未提交操作。" + result.Answer, nil
		}
		return "本次没有已提交的业务操作。请核对目标、所需权限及办理内容。", nil
	}
	var lines []string
	for _, c := range calls {
		line := fmt.Sprintf("操作 %d · %s · 已提交", c.Slot, c.Tool)
		for _, key := range []string{"task_code", "task_id", "id", "work_id", "project_id", "chat_id", "version"} {
			if value, ok := c.Receipt[key]; ok && value != nil {
				line += fmt.Sprintf(" · %s=%v", key, value)
			}
		}
		lines = append(lines, line)
	}
	return strings.Join(append(lines, failed...), "\n"), nil
}
