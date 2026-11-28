package group

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/routing"
)

const proposalMarker = "OMNI_PROPOSALS_JSON"

func singleBrief(p routing.Plan) bool {
	return len(p.Actions) == 1 && p.Actions[0].Tool == "briefing_save"
}
func draftInstructions(p routing.Plan) string {
	if singleBrief(p) {
		return "\n用户本轮要审阅的是一份新项目简报草稿。通过可用工具取齐依据，只返回用户将确认保存的简报本身。第一行用 \"# 标题\"，空一行后写完整 Markdown 正文。保留用户要求的来源标识。不要输出 JSON、操作说明、确认提示或写入成功声明。草稿目标：" + string(p.PromptJSON())
	}
	return "\n本轮是只读草稿，先用授权资料形成实际内容。回答末尾用一行 " + proposalMarker + " 后跟 JSON 数组，按下列候选操作顺序为每项提供可直接展示的字段键值字符串映射。完整列出 fields 及 generate 字段（不含版本），保持原指定目标。简报必须给出实际标题、正文；任务必须给出实际标题。绝不编造标识或报告已写入。操作：" + string(p.PromptJSON())
}
func (s *Service) publishDraft(ctx context.Context, tx pgx.Tx, w work, answer string) (string, error) {
	var supplied []map[string]string
	var prose string
	if singleBrief(w.Plan) {
		a := w.Plan.Actions[0]
		fields := map[string]string{}
		for k, v := range a.Fields {
			fields[k] = v
		}
		first, rest, split := strings.Cut(strings.TrimSpace(answer), "\n")
		title := strings.TrimSpace(strings.TrimLeft(first, "#"))
		body := strings.TrimSpace(rest)
		if !split || title == "" || len([]rune(title)) > 200 || body == "" {
			title = w.Scope.ProjectName + "项目简报"
			body = strings.TrimSpace(answer)
		}
		if t := fields["title"]; t != "" {
			title = t
		}
		fields["title"], fields["body"] = title, body
		supplied = []map[string]string{fields}
	} else {
		var tail string
		var ok bool
		prose, tail, ok = strings.Cut(answer, proposalMarker)
		prose = strings.TrimSpace(prose)
		tail = strings.TrimSpace(tail)
		if strings.HasPrefix(tail, "```json") && strings.HasSuffix(tail, "```") {
			tail = strings.TrimSpace(tail[7 : len(tail)-3])
		}
		if !ok || json.Unmarshal([]byte(tail), &supplied) != nil || len(supplied) != len(w.Plan.Actions) {
			return prose + "\n当前草稿尚缺少可确认的具体操作。", nil
		}
	}
	var items []routing.PlannedAction
	var lines []string
	for i, a := range w.Plan.Actions {
		fields := supplied[i]
		if len(fields) != len(a.Fields)+len(a.Generate) {
			return prose + "\n当前草稿尚缺少可确认的具体操作。", nil
		}
		for k, v := range a.Fields {
			if fields[k] != v {
				return prose + "\n当前草稿的对象或范围需要澄清。", nil
			}
		}
		for _, k := range a.Generate {
			if strings.TrimSpace(fields[k]) == "" || len(fields[k]) > 16000 {
				return prose + "\n当前草稿尚缺少可确认的具体内容。", nil
			}
		}
		if d := fields["business_date"]; d != "" {
			v, err := s.businessDate(ctx, tx, w, d)
			if err != nil {
				return "", err
			}
			fields["business_date"] = v
		}
		a.Fields = fields
		targets, err := s.proposalTargets(ctx, tx, w, a)
		if err != nil {
			if errors.Is(err, errProposalTarget) {
				return prose + "\n草稿需要唯一的业务对象，请先补充编号。", nil
			}
			return "", err
		}
		a.Targets = targets
		expected, err := s.proposalVersions(ctx, tx, w, a)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, errProposalTarget) {
				return prose + "\n草稿的业务对象尚未唯一确定，请补充对象后再确认。", nil
			}
			return "", err
		}
		a.Expected = expected
		a.Generate = nil
		items = append(items, a)
		// The exact values later eligible for confirmation appear in the user's rendered answer.
		if singleBrief(w.Plan) {
			lines = append(lines, "# "+fields["title"]+"\n\n"+fields["body"])
			continue
		}
		var parts []string
		for k, v := range fields {
			parts = append(parts, k+" = "+v)
		}
		slices.Sort(parts)
		lines = append(lines, fmt.Sprintf("%d. %s\n   %s", i+1, a.Tool, strings.Join(parts, "\n   ")))
	}
	_, err := tx.Exec(ctx, "UPDATE agent_group_work SET proposal_items=$2,proposal_parent=nullif($3,\x27\x27) WHERE id=$1", w.Work, items, w.ProposalParent)
	if singleBrief(w.Plan) {
		return lines[0], err
	}
	return prose + "\n可确认的操作\n" + strings.Join(lines, "\n"), err
}
func (s *Service) resolveConfirmation(ctx context.Context, tx pgx.Tx, w *work) error {
	type candidate struct {
		work  string
		index int
		item  routing.PlannedAction
	}
	var candidates []candidate
	var bound bool
	if err := tx.QueryRow(ctx, "SELECT confirmation_bound,coalesce(confirmation_origin,''),coalesce(confirmation_item,0) FROM agent_group_work WHERE id=$1 FOR UPDATE", w.Work).Scan(&bound, &w.OriginWork, &w.OriginItem); err != nil {
		return err
	}
	for _, prior := range w.PersonalContext {
		if prior.Committed || bound && prior.Work != w.OriginWork {
			continue
		}
		var items []routing.PlannedAction
		err := tx.QueryRow(ctx, proposalQuery, prior.Work).Scan(&items)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		for i, item := range items {
			if bound && w.OriginItem != i+1 || w.Plan.Selection == 0 && len(items) != 1 || w.Plan.Selection > 0 && w.Plan.Selection != i+1 {
				continue
			}
			var used bool
			if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT FROM agent_action_grants_used WHERE origin_work=$1 AND origin_item=$2 AND work_id<>$3)", prior.Work, i+1, w.Work).Scan(&used); err != nil {
				return err
			}
			if !used || bound {
				candidates = append(candidates, candidate{prior.Work, i + 1, item})
			}
		}
	}
	if !bound {
		var origin string
		var index int
		if len(candidates) == 1 {
			origin, index = candidates[0].work, candidates[0].index
		}
		if _, err := tx.Exec(ctx, "UPDATE agent_group_work SET confirmation_bound=true,confirmation_origin=nullif($2,''),confirmation_item=nullif($3,0) WHERE id=$1", w.Work, origin, index); err != nil {
			return err
		}
	}
	if len(candidates) != 1 {
		w.Plan.Mode = "clarify"
		w.Plan.Clarification = "请指明要确认的具体草稿和条目，已提交的条目无需再次确认。"
		w.Prompt += "\n" + w.Plan.Clarification
		return nil
	}
	c := candidates[0]
	if err := inheritEvidencePeriod(ctx, tx, w, c.work); err != nil {
		return err
	}
	w.OriginWork, w.OriginItem = c.work, c.index
	w.Plan.Actions = []routing.PlannedAction{c.item}
	return nil
}

const proposalQuery = `SELECT proposal_items FROM agent_group_work p WHERE id=$1 AND state='sent' AND proposal_items IS NOT NULL
 AND NOT EXISTS(SELECT FROM agent_group_work revision WHERE revision.proposal_parent=p.id AND revision.state='sent' AND revision.proposal_items IS NOT NULL)`

// Editing a draft can change only proposed content; object IDs and permission
// remain fixed. Publishing the revised preview supersedes its predecessor.
func (s *Service) resolveRevision(ctx context.Context, tx pgx.Tx, w *work) error {
	var drafts [][]routing.PlannedAction
	var parents []string
	for _, turn := range w.PersonalContext {
		var items []routing.PlannedAction
		err := tx.QueryRow(ctx, proposalQuery+" AND NOT EXISTS(SELECT FROM agent_action_grants_used WHERE origin_work=p.id)", turn.Work).Scan(&items)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if len(items) > 0 && len(items) >= w.Plan.Selection {
			drafts = append(drafts, items)
			parents = append(parents, turn.Work)
		}
	}
	if len(drafts) != 1 {
		w.Plan.Mode = "clarify"
		w.Plan.Clarification = "请指明需要修订的草稿及条目。"
		return nil
	}
	if err := inheritEvidencePeriod(ctx, tx, w, parents[0]); err != nil {
		return err
	}
	w.ProposalParent = parents[0]
	w.Plan.Mode = "draft"
	w.Plan.Actions = drafts[0]
	for i := range w.Plan.Actions {
		a := &w.Plan.Actions[i]
		a.Expected = nil
		if w.Plan.Selection != 0 && w.Plan.Selection != i+1 {
			continue
		}
		for k := range a.Fields {
			if routing.GeneratedField(a.Tool, k) {
				delete(a.Fields, k)
				a.Generate = append(a.Generate, k)
			}
		}
		slices.Sort(a.Generate)
	}
	return nil
}

func inheritEvidencePeriod(ctx context.Context, tx pgx.Tx, w *work, origin string) error {
	if w.Plan.EvidencePeriod != nil {
		return nil
	}
	return tx.QueryRow(ctx, "SELECT semantic_plan->'evidence_period' FROM agent_group_work WHERE id=$1", origin).Scan(&w.Plan.EvidencePeriod)
}
