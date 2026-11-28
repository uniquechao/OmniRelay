package group

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/agent/pmtools"
	"github.com/uniquechao/OmniRelay/internal/routing"
)

func TestSemanticBindingsKeepGeneratedContentInsideRequestedOperation(t *testing.T) {
	g := personalGate(t)
	const user = "请把纪要里的行动项凝练成一张工作卡，归我跟进。"
	g.PlanIntent = func(_ context.Context, r routing.PlanRequest) (routing.Plan, error) {
		if r.Text != user {
			t.Fatalf("unexpected planner input %s", r.Text)
		}
		return routing.Plan{Intent: "project", Mode: "execute", Actions: []routing.PlannedAction{{Tool: "task_create", Evidence: r.Text, Generate: []string{"title"}}}}, nil
	}
	personalRequest(t, g, "alice", "workbench", "", "", "generate-one", user)
	w, err := g.claim(g.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if w.Role != "project-pm" || !w.Plan.Allows("task_create") || w.Plan.Allows("task_delete") {
		t.Fatalf("wrong route or permissions: %+v", w.Plan)
	}
	call := ToolCall{Binding: w.Binding, Turn: 1}
	if _, err = g.useTurn(g.ctx, call, true); err != nil {
		t.Fatal(err)
	}
	send := func(slot int, name string, args pmtools.Args) error {
		_, err := g.projectTool(g.ctx, projectCall{ToolCall: call, Name: name, Slot: slot, Args: args})
		return err
	}
	a := pmtools.Args{"title": json.RawMessage(`"冻结接口格式"`)}
	if err = send(1, "task_create", a); err != nil {
		t.Fatal(err)
	}
	if err = send(1, "task_create", a); err != nil {
		t.Fatal(err)
	}
	if err = send(2, "task_create", a); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("second grant use %v", err)
	}
	a["owner_member_id"] = json.RawMessage(`"b"`)
	if err = send(3, "task_create", a); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("foreign assignee %v", err)
	}
	if err = send(4, "task_delete", pmtools.Args{}); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("unrequested delete %v", err)
	}
}

func TestOnlyShownProposalMayBeConfirmedOnce(t *testing.T) {
	g := personalGate(t)
	const draft = "从纪要拟两条行动建议，先给我过目"
	const confirm = "就按第二条办吧"
	g.PlanIntent = func(_ context.Context, r routing.PlanRequest) (routing.Plan, error) {
		p := routing.Plan{Intent: "project", Mode: "read"}
		if !r.ProjectSelected {
			t.Fatal("selected workspace was lost before planning")
		}
		if r.Text == draft {
			if r.DisplayedProposal {
				t.Fatal("unshown proposal hint")
			}
			a := routing.PlannedAction{Tool: "task_create", Evidence: draft, Generate: []string{"title"}}
			p.Mode = "draft"
			p.Actions = []routing.PlannedAction{a, a}
		}
		if r.Text == confirm {
			if !r.DisplayedProposal {
				t.Fatal("same-scope displayed proposal absent from classification")
			}
			p.Mode = "confirm"
			p.Selection = 2
		}
		return p, nil
	}
	post := func(id, text string) string {
		return personalRequest(t, g, "alice", "workbench", "", "project", id, text)
	}
	prior := post("proposal-original", draft)
	completedPersonalTurn(t, g, func(c ToolCall) string {
		err := pgx.BeginFunc(g.ctx, g.DB, func(tx pgx.Tx) error {
			w, err := g.authorize(g.ctx, tx, c.Work)
			if err != nil {
				return err
			}
			ts, err := g.allowedTools(g.ctx, tx, w)
			if slices.ContainsFunc(ts, func(t pmtools.Tool) bool { return t.Write }) {
				t.Fatal("draft exposed a write")
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return "供审核。\n" + proposalMarker + `[{"title":"整理任务单"},{"title":"冻结接口格式"}]`
	})
	post("proposal-other", "顺便查下项目档案")
	completedPersonalTurn(t, g, func(ToolCall) string { return "已查询" })
	apply := post("proposal-confirm", confirm)
	w, err := g.claim(g.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if w.OriginWork != prior || w.OriginItem != 2 || len(w.Plan.Actions) != 1 {
		t.Fatalf("wrong proposal %s: %+v", w.OriginWork, w.Plan)
	}
	c := ToolCall{Binding: w.Binding, Turn: 1}
	if _, err = g.useTurn(g.ctx, c, true); err != nil {
		t.Fatal(err)
	}
	save := func(slot int, title string) error {
		b, _ := json.Marshal(title)
		_, err := g.projectTool(g.ctx, projectCall{ToolCall: c, Name: "task_create", Slot: slot, Args: pmtools.Args{"title": b}})
		return err
	}
	if err = save(1, "整理任务单"); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("unselected item accepted: %v", err)
	}
	if err = save(2, "冻结接口格式"); err != nil {
		t.Fatal(err)
	}
	// Simulate completion of the original work independently of the native transport.
	g.sql("UPDATE agent_group_work SET state='sent',response_text='已提交',lease_until=NULL WHERE id=$1", apply)
	post("proposal-repeated", confirm)
	w, err = g.claim(g.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if w.Plan.Allows("task_create") {
		t.Fatal("consumed proposal granted a second write")
	}
}

func TestHistoricalWorkCannotGainSemanticWrites(t *testing.T) {
	g := newGate(t)
	id := g.projectMention("historical-write", "创建任务 校验交付")
	g.sql("UPDATE agent_group_work SET needs_plan=false,semantic_plan=NULL,write_requested=true WHERE id=$1", id)
	w, err := g.claim(g.ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := ToolCall{Binding: w.Binding, Turn: 1}
	if _, err = g.useTurn(g.ctx, c, true); err != nil {
		t.Fatal(err)
	}
	_, err = g.projectTool(g.ctx, projectCall{ToolCall: c, Slot: 1, Name: "task_create", Args: pmtools.Args{"title": json.RawMessage(`"校验交付"`)}})
	if !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("legacy write granted: %v", err)
	}
}

func TestBriefPreviewPinsShownBodyAndVersion(t *testing.T) {
	g := personalGate(t)
	g.sql("UPDATE project_members SET role='manager' WHERE account_id='alice'")
	const date = "2026-11-27"
	const body = "验收编号 2817，设计评审通过。"
	g.PlanIntent = func(_ context.Context, r routing.PlanRequest) (routing.Plan, error) {
		if r.Text == "执行刚才这份" {
			return routing.Plan{Intent: "project", Mode: "confirm"}, nil
		}
		return routing.Plan{Intent: "project", Mode: "draft", Actions: []routing.PlannedAction{{Tool: "briefing_save", Evidence: r.Text, Fields: map[string]string{"business_date": date}, Generate: []string{"title", "body"}}}}, nil
	}
	original := personalRequest(t, g, "alice", "workbench", "", "project", "version-original", "请先起草 "+date+" 项目周报")
	completedPersonalTurn(t, g, func(ToolCall) string { return "# 联调周报\n\n" + body })
	var visible string
	var items []routing.PlannedAction
	if err := g.DB.QueryRow(g.ctx, "SELECT response_text,proposal_items FROM agent_group_work WHERE id=$1", original).Scan(&visible, &items); err != nil || len(items) != 1 || items[0].Fields["body"] != body || !strings.Contains(visible, body) || items[0].Expected["expected_version"] != "0" {
		t.Fatalf("draft not bound to shown values: %+v %s %v", items, visible, err)
	}
	g.sql("INSERT INTO project_daily_briefs(tenant_key,app_id,project_id,business_date,title,body,updated_by,version) VALUES('tenant','app','north',$1,'其他用户手动编辑','正式正文','alice',1)", date)
	personalRequest(t, g, "alice", "workbench", "", "project", "version-confirm", "执行刚才这份")
	w, err := g.claim(g.ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := ToolCall{Binding: w.Binding, Turn: 1}
	if _, err = g.useTurn(g.ctx, c, true); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"business_date": date, "title": "联调周报", "body": body, "expected_version": 1})
	var args pmtools.Args
	_ = json.Unmarshal(payload, &args)
	_, err = g.projectTool(g.ctx, projectCall{ToolCall: c, Slot: 1, Name: "briefing_save", Args: args})
	if err == nil {
		t.Fatal("new version substituted into old displayed preview")
	}
	var persisted string
	if err = g.DB.QueryRow(g.ctx, "SELECT body FROM project_daily_briefs WHERE business_date=$1::date", date).Scan(&persisted); err != nil || persisted != "正式正文" {
		t.Fatalf("manual edit overwritten: %s %v", persisted, err)
	}
}

func TestHistoricalWorkReplaysOnlyItsCommittedSlot(t *testing.T) {
	g := newGate(t)
	id := g.projectMention("legacy-ack", "创建任务 校验交付")
	w, err := g.claim(g.ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := ToolCall{Binding: w.Binding, Turn: 1}
	if _, err = g.useTurn(g.ctx, c, true); err != nil {
		t.Fatal(err)
	}
	cmd := projectCall{ToolCall: c, Slot: 1, Name: "task_create", Args: pmtools.Args{"title": json.RawMessage(`"校验交付"`)}}
	original, err := g.projectTool(g.ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	g.sql("UPDATE agent_group_work SET semantic_plan=NULL WHERE id=$1", id)
	replay, err := g.projectTool(g.ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	var a, b map[string]any
	_ = json.Unmarshal(original, &a)
	_ = json.Unmarshal(replay, &b)
	if a["id"] != b["id"] {
		t.Fatal("legacy replay lost original task")
	}
	cmd.Slot = 2
	if _, err = g.projectTool(g.ctx, cmd); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("fresh slot on historical work: %v", err)
	}
}

func TestPriorResultNeedsCurrentUserEvidence(t *testing.T) {
	const text = "根据刚才查到的结论，新建一个跟进任务。"
	p := routing.Plan{Intent: "project", Mode: "execute", PriorResult: "刚才查到的结论", Actions: []routing.PlannedAction{{Tool: "task_create", Evidence: text, Generate: []string{"title"}}}}
	r := routing.PlanRequest{Text: text, Intent: "project"}
	if err := p.Validate(r); err != nil {
		t.Fatal(err)
	}
	p.PriorResult = "以前的私聊内容"
	if p.Validate(r) == nil {
		t.Fatal("accepted history scope absent from current request")
	}
	p.PriorResult = "刚才查到的结论"
	p.Mode = "read"
	p.Actions = nil
	if p.Validate(r) == nil {
		t.Fatal("read mode carried execution history authorization")
	}
}

func TestMalformedSemanticReplyNeverCompletesAdmission(t *testing.T) {
	g := newGate(t)
	id := g.projectMention("invalid-plan", "请拟一份简报草稿")
	g.PlanIntent = func(context.Context, routing.PlanRequest) (routing.Plan, error) { return routing.Plan{}, nil }
	if _, err := g.claim(g.ctx); err == nil {
		t.Fatal("empty response completed planning")
	}
	var pending bool
	if err := g.DB.QueryRow(g.ctx, "SELECT needs_plan AND semantic_plan IS NULL AND NOT write_requested AND lease_until IS NULL FROM agent_group_work WHERE id=$1", id).Scan(&pending); err != nil || !pending {
		t.Fatalf("invalid interpretation persisted: %v %v", pending, err)
	}
}

func TestPlannerCatalogSeparatesTargetsContentAndServerState(t *testing.T) {
	var found bool
	for _, tool := range routing.PlanningCatalog() {
		for _, group := range [][]string{tool.Required, tool.Content, tool.Literal, tool.Selector} {
			for _, field := range group {
				if routing.VersionField(field) {
					t.Fatalf("%s exposes server field %s", tool.Name, field)
				}
			}
		}
		if tool.Name == "briefing_save" {
			found = true
			if !slices.Equal(tool.Content, []string{"title", "body"}) || !slices.Equal(tool.Literal, []string{"business_date"}) || !slices.Equal(tool.Required, []string{"business_date", "title", "body"}) {
				t.Fatalf("bad brief planning contract: %+v", tool)
			}
		}
	}
	if !found {
		t.Fatal("brief contract missing")
	}
}
