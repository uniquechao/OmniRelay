package group

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/agent/dsh"
	"github.com/uniquechao/OmniRelay/internal/agent/pmtools"
	"github.com/uniquechao/OmniRelay/internal/routing"
)

func TestWorkScopesRejectFabricatedPlannerEvidence(t *testing.T) {
	r := routing.PlanRequest{Text: "请据此整理纪要，先给我看草稿"}
	p := routing.Plan{Intent: "project", Mode: "execute", Actions: []routing.PlannedAction{{Tool: "task_delete", Evidence: "删除任务", Fields: map[string]string{"task_id": "outside"}, Generate: []string{"change_reason"}}}}
	if p.Validate(r) == nil {
		t.Fatal("fabricated user evidence accepted")
	}
	p.Actions[0].Evidence = r.Text
	if p.Validate(r) == nil {
		t.Fatal("ungrounded task accepted")
	}
	p.Mode = "read"
	if p.Validate(r) == nil {
		t.Fatal("read-mode action accepted")
	}
}
func TestWorkBoundToolsRejectWrongVerbAndTarget(t *testing.T) {
	g := newGate(t)
	g.projectMention("test-scope", "创建任务 校验交付")
	w, err := g.claim(g.ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := ToolCall{Binding: w.Binding, Turn: 1}
	if _, err = g.useTurn(g.ctx, c, true); err != nil {
		t.Fatal(err)
	}
	slot := 0
	run := func(name, title string) error {
		slot++
		_, err := g.projectTool(g.ctx, projectCall{ToolCall: c, Name: name, Slot: slot, Args: pmtools.Args{"title": json.RawMessage("\"" + title + "\"")}})
		return err
	}
	if err = run("task_delete", "校验交付"); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("wrong action: %v", err)
	}
	if err = run("task_create", "其他事项"); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("wrong target: %v", err)
	}
	if err = run("task_create", "校验交付"); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateProjectContinuation(t *testing.T) {
	g := newGate(t)
	g.sql(`INSERT INTO project_profiles(tenant_key,app_id,project_id,name,status,version) VALUES('tenant','app','south','南项目','active',1);
	INSERT INTO project_members(tenant_key,app_id,project_id,account_id,member_id,role) VALUES('tenant','app','south','alice','sa','member')`)
	e := Envelope{Tenant: g.Tenant, App: g.App, Chat: "dm-alice", Account: "alice", Mode: PrivateReply, Channel: "internal", Config: 1, Message: "choice-one", Text: "创建任务 接口联调", Received: time.Now()}
	ch := g.Channels["internal"]
	old := ch.Resolve
	ch.Resolve = func(ctx context.Context, tx pgx.Tx, e Envelope) (Assignment, error) {
		a, err := old(ctx, tx, e)
		a.Project = e.Project
		return a, err
	}
	g.Channels["internal"] = ch
	var id string
	err := pgx.BeginFunc(g.ctx, g.DB, func(tx pgx.Tx) error {
		if _, err := ResolvePrivateProject(g.ctx, tx, &e); err != nil {
			return err
		}
		if e.ChoicePrompt == "" || e.Project != "" {
			t.Fatalf("ambiguous project accepted: %+v", e)
		}
		var err error
		id, err = Accept(g.ctx, tx, ch, e)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	g.step()
	g.step()
	g.state(id, "awaiting_project")
	choose := func(user, option string) error {
		return pgx.BeginFunc(g.ctx, g.DB, func(tx pgx.Tx) error {
			e.Account = user
			e.Text = "选择项目 " + id + " " + option
			_, err := ResolvePrivateProject(g.ctx, tx, &e)
			return err
		})
	}
	for _, bad := range [][2]string{{"bob", "north"}, {"alice", "missing"}} {
		if err = choose(bad[0], bad[1]); !errors.Is(err, access.ErrForbidden) {
			t.Fatalf("accepted invalid selector: %v", err)
		}
	}
	e.Thread = "another-private-topic"
	if err = choose("alice", "south"); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("project selection crossed private topics: %v", err)
	}
	e.Thread = ""
	if err = choose("alice", "south"); err != nil {
		t.Fatal(err)
	}
	w, err := g.claim(g.ctx)
	if err != nil || w.Work != id || w.Scope.ProjectID != "south" || w.Role != "project-pm" || w.Address.Text != "创建任务 接口联调" {
		t.Fatalf("wrong continuation %+v %v", w, err)
	}
}

func TestDuplicateAssigneeNeedsDisambiguation(t *testing.T) {
	g := newGate(t)
	g.sql(`UPDATE project_members SET role='manager' WHERE account_id='alice';
	INSERT INTO accounts(id,tenant_key,app_id,open_id,name) VALUES('bob2','tenant','app','login-b2','小李');
	INSERT INTO project_members(tenant_key,app_id,project_id,account_id,member_id,role) VALUES('tenant','app','north','bob2','c','member')`)
	g.projectMention("two-li", "把接口联调建成任务，交给小李")
	w, err := g.claim(g.ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := ToolCall{Binding: w.Binding, Turn: 1}
	if _, err = g.useTurn(g.ctx, c, true); err != nil {
		t.Fatal(err)
	}
	_, err = g.projectTool(g.ctx, projectCall{ToolCall: c, Name: "task_create", Slot: 1, Args: pmtools.Args{"title": json.RawMessage(`"接口联调"`), "owner_member_id": json.RawMessage(`"b"`)}})
	if !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("ambiguous assignee accepted: %v", err)
	}
}

func TestGlobalDelegationAuthority(t *testing.T) {
	g := newGate(t)
	g.sql(`INSERT INTO role_grants(tenant_key,app_id,account_id,role,source) VALUES('tenant','app','alice','system_admin','bootstrap');
	INSERT INTO project_profiles(tenant_key,app_id,project_id,name,status,version) VALUES
	('tenant','app','secret','保密项目','active',1),('tenant','app','south','南星','active',1);
	INSERT INTO project_members(tenant_key,app_id,project_id,account_id,member_id,role) VALUES ('tenant','app','south','alice','s-a','member')`)
	g.RegisterWorkbench(http.NewServeMux(), nil)
	var id string
	err := pgx.BeginFunc(g.ctx, g.DB, func(tx pgx.Tx) error {
		var err error
		id, err = Accept(g.ctx, tx, g.Channels["workbench"], Envelope{Tenant: g.Tenant, App: g.App, Account: "alice", Mode: ProjectChat, Channel: "workbench", Chat: "north", Project: "north", Intent: "admin", Text: "委派给北辰项目，创建任务 联调", Message: "mgmt-test", Received: time.Now()})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	w, err := g.claim(g.ctx)
	if err != nil || w.Role != "management-pm" {
		t.Fatalf("management not dispatched %v %+v", err, w)
	}
	b := ToolCall{Binding: w.Binding, Turn: 1}
	if _, err = g.useTurn(g.ctx, b, true); err != nil {
		t.Fatal(err)
	}
	data, err := g.projectTool(g.ctx, projectCall{ToolCall: b, Name: "visible_projects", Args: pmtools.Args{}})
	if err != nil || !strings.Contains(string(data), "南星") || strings.Contains(string(data), "保密项目") {
		t.Fatalf("wrong scope %s %v", data, err)
	}
	for i, projectID := range []string{"secret", "south", "north", "north"} {
		key, _ := json.Marshal(projectID)
		_, err = g.projectTool(g.ctx, projectCall{ToolCall: b, Name: "delegate_project", Args: pmtools.Args{"project_id": key}, Slot: min(i+1, 3)})
		if projectID == "north" && err != nil || projectID != "north" && !errors.Is(err, access.ErrForbidden) {
			t.Fatalf("delegation scope: %s %v", projectID, err)
		}
	}
	var n int
	if err = g.DB.QueryRow(g.ctx, "SELECT count(*) FROM agent_group_work WHERE parent_work=$1", id).Scan(&n); err != nil || n != 1 {
		t.Fatalf("delegation duplicated: %d %v", n, err)
	}
	if err = pgx.BeginFunc(g.ctx, g.DB, func(tx pgx.Tx) error { return g.checkToolReads(g.ctx, tx, w.Work, w.Actor, w.Scope.ProjectID) }); err != nil {
		t.Fatal(err)
	}
	g.sql("UPDATE project_members SET role='revoked' WHERE project_id='south' AND account_id='alice'")
	if err = pgx.BeginFunc(g.ctx, g.DB, func(tx pgx.Tx) error { return g.checkToolReads(g.ctx, tx, w.Work, w.Actor, w.Scope.ProjectID) }); !errors.Is(err, errToolReadStale) {
		t.Fatalf("stale cross-project result released: %v", err)
	}
}

func TestPartialWriteReceiptMatchesLedger(t *testing.T) {
	g := newGate(t)
	g.projectMention("partial-test", "创建任务 第一项，创建任务 第二项")
	w, err := g.claim(g.ctx)
	if err != nil {
		t.Fatal(err)
	}
	binding := ToolCall{Binding: w.Binding, Turn: 1}
	if _, err = g.useTurn(g.ctx, binding, true); err != nil {
		t.Fatal(err)
	}
	command := projectCall{ToolCall: binding, Name: "task_create", Slot: 1, Args: pmtools.Args{"title": json.RawMessage(`"第一项"`)}}
	if _, err = g.projectTool(g.ctx, command); err != nil {
		t.Fatal(err)
	}
	command.Slot = 2
	command.Args = pmtools.Args{"title": json.RawMessage(`"第二项"`), "owner_member_id": json.RawMessage(`"missing"`)}
	if _, err = g.projectTool(g.ctx, command); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("bad owner unexpectedly accepted: %v", err)
	}
	command.Args = pmtools.Args{"title": json.RawMessage(`"第二项"`)}
	if _, err = g.projectTool(g.ctx, command); !errors.Is(err, errSlot) {
		t.Fatalf("refused slot mutated: %v", err)
	}
	result := dsh.Result{Kind: "writes", WriteSlots: []int{1}, FailedWrites: []dsh.FailedWrite{{Slot: 2, Tool: "task_create", Status: 403}}}
	err = pgx.BeginFunc(g.ctx, g.DB, func(tx pgx.Tx) error {
		answer, err := g.verifiedAnswer(g.ctx, tx, w, result)
		if err != nil {
			return err
		}
		if !strings.Contains(answer, "已提交") || !strings.Contains(answer, "未提交") || !strings.Contains(answer, "第二项") {
			t.Fatalf("incomplete answer %s", answer)
		}
		result.FailedWrites = nil
		if _, err = g.verifiedAnswer(g.ctx, tx, w, result); !errors.Is(err, errSlot) {
			t.Fatalf("omitted failure accepted: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAssigneeBoundToSingleTaskInstruction(t *testing.T) {
	g := newGate(t)
	g.sql(`UPDATE project_members SET role='manager' WHERE account_id='alice';
	INSERT INTO accounts(id,tenant_key,app_id,open_id,name) VALUES('wang','tenant','app','login-wang','小王');
	INSERT INTO project_members(tenant_key,app_id,project_id,account_id,member_id,role) VALUES('tenant','app','north','wang','w','member')`)
	for i, tc := range []struct {
		text, title, owner string
		allowed            bool
	}{
		{"把接口联调建成任务，交给小李", "接口联调", "b", true},
		{"创建任务 接口联调，交给成员 b；创建任务 文档发布，交给成员 w", "接口联调", "w", false},
		{"创建任务 接口联调，交给小李；创建任务 文档发布，交给小王", "文档发布", "b", false},
		{"创建任务 接口联调，交给小李；创建任务 文档发布，交给小王", "文档发布", "w", true},
		{"创建任务 接口联调，交给小李，备注小王负责协助", "接口联调", "w", false},
		{"创建任务 接口联调，备注提到小王", "接口联调", "w", false},
		{"创建任务 接口联调；交给小王", "接口联调", "w", false},
		{"创建任务 接口联调，交给小李", "接口联调", "", false},
		{"创建任务 接口联调，交给小李，创建任务 文档发布，交给小王", "接口联调", "w", false},
	} {
		g.projectMention(fmt.Sprint("assignee-", i), tc.text)
		work, err := g.claim(g.ctx)
		if err != nil {
			t.Fatal(err)
		}
		call := ToolCall{Binding: work.Binding, Turn: 1}
		if _, err = g.useTurn(g.ctx, call, true); err != nil {
			t.Fatal(err)
		}
		title, _ := json.Marshal(tc.title)
		args := pmtools.Args{"title": title}
		if tc.owner != "" {
			owner, _ := json.Marshal(tc.owner)
			args["owner_member_id"] = owner
		}
		_, err = g.projectTool(g.ctx, projectCall{ToolCall: call, Name: "task_create", Slot: 1, Args: args})
		if tc.allowed && err != nil || !tc.allowed && !errors.Is(err, access.ErrForbidden) {
			t.Fatalf("%q: %q -> %q: %v", tc.text, tc.title, tc.owner, err)
		}
		g.sql("UPDATE agent_group_work SET state='failed' WHERE id=$1", work.Work)
	}
}
