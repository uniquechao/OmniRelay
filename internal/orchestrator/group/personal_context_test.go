package group

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/agent/dsh"
	"github.com/uniquechao/OmniRelay/internal/agent/pmtools"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/routing"
)

func personalGate(t *testing.T) *gate {
	t.Helper()
	g := newGate(t)
	g.PlanIntent = func(_ context.Context, r routing.PlanRequest) (routing.Plan, error) {
		return routing.Plan{Intent: r.Intent, Mode: "read"}, nil
	}
	g.RegisterWorkbench(http.NewServeMux(), func(http.ResponseWriter, *http.Request, identity.SessionPolicy) (identity.Principal, bool) {
		return identity.Principal{}, false
	})
	return g
}

func personalRequest(t *testing.T, g *gate, actor, channel, thread, intent, message, text string) string {
	t.Helper()
	mode, chat := ProjectChat, "north"
	if channel != "workbench" {
		mode, chat = PrivateReply, "dm-"+actor
	}
	var id string
	err := pgx.BeginFunc(g.ctx, g.DB, func(tx pgx.Tx) error {
		var err error
		id, err = Accept(g.ctx, tx, g.Channels[channel], Envelope{Tenant: g.Tenant, App: g.App, Channel: channel, Chat: chat, Thread: thread,
			Account: actor, Project: "north", Message: message, Text: text, Intent: intent, Mode: mode, Received: time.Now()})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func completedPersonalTurn(t *testing.T, g *gate, run func(ToolCall) string) {
	t.Helper()
	g.Execute = func(ctx context.Context, b dsh.Binding) (dsh.Result, error) {
		c := ToolCall{Binding: b, Turn: 1}
		if _, err := g.useTurn(ctx, c, true); err != nil {
			return dsh.Result{}, err
		}
		if _, err := g.useTurn(ctx, c, false); err != nil {
			return dsh.Result{}, err
		}
		return dsh.Result{Session: b.Session, Turn: 1, AppliedVersion: b.ConfigVersion, Kind: "answer", Answer: run(c)}, nil
	}
	g.step()
}

func TestPersonalContextAcrossFreshSessions(t *testing.T) {
	g := personalGate(t)
	original := personalRequest(t, g, "alice", "workbench", "", "project", "draft-1", "先拟两个方案，第二个用蓝色封面")
	var firstSession string
	completedPersonalTurn(t, g, func(c ToolCall) string { firstSession = c.Session; return "候选一：绿色；候选二：蓝色" })
	g.state(original, "sent")
	another := personalRequest(t, g, "alice", "workbench", "", "knowledge", "read-other-role", "只预览本轮")
	completedPersonalTurn(t, g, func(c ToolCall) string {
		if strings.Contains(c.Prompt, "蓝色") {
			t.Fatal("different role inherited a draft")
		}
		return "检查完毕"
	})
	g.state(another, "sent")
	personalRequest(t, g, "alice", "workbench", "", "project", "read-same-role", "先聊点别的")
	completedPersonalTurn(t, g, func(c ToolCall) string { return "只读答复" })
	follow := personalRequest(t, g, "alice", "workbench", "", "project", "confirm-2", "确认写入第二项")
	before, err := g.claim(g.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before.Work != follow || before.Session == firstSession || len(before.PersonalContext) != 2 ||
		before.PersonalContext[0].Work != original || before.PersonalContext[0].Committed ||
		!strings.Contains(before.Prompt, "蓝色封面") || !strings.Contains(before.Prompt, "候选二：蓝色") ||
		!strings.HasSuffix(before.Prompt, "确认写入第二项") || strings.Contains(before.Prompt, "只预览本轮") {
		t.Fatalf("follow-up lost its authorized choices: %#v", before.PersonalContext)
	}
	g.sql("UPDATE agent_group_work SET lease_until=NULL WHERE id=$1", follow)
	restarted := *g.Service
	again, err := restarted.claim(g.ctx)
	if err != nil || again.Prompt != before.Prompt || again.Session != before.Session || again.Generation <= before.Generation {
		t.Fatalf("work changed across process recovery: %v", err)
	}
	id := personalRequest(t, g, "alice", "workbench", "", "project", "confirm-2", "确认写入第二项")
	if id != follow {
		t.Fatal("retry duplicated the work")
	}
	g.sql("UPDATE agent_group_work SET state='failed',lease_until=NULL WHERE id=$1", follow)
	personalRequest(t, g, "bob", "workbench", "", "project", "other-member", "继续刚才的提议")
	completedPersonalTurn(t, g, func(c ToolCall) string {
		if strings.Contains(c.Prompt, "蓝色") {
			t.Fatal("other member received Alice's draft")
		}
		return "没有之前的草案"
	})
	g.sql(`INSERT INTO agent_personal_conversations(tenant_key,app_id,account_id,project_id,membership_generation,id,title,request_key)
      SELECT tenant_key,app_id,account_id,project_id,generation,'another-chat','另一会话','another-key' FROM project_members WHERE account_id='alice'`)
	personalRequest(t, g, "alice", "workbench", "another-chat", "project", "other-chat", "继续刚才的提议")
	completedPersonalTurn(t, g, func(c ToolCall) string {
		if strings.Contains(c.Prompt, "蓝色") {
			t.Fatal("another thread received a draft")
		}
		return "请说明"
	})
}

func TestPersonalContextRechecksTransitiveSources(t *testing.T) {
	for _, tc := range []struct {
		name, change string
		want         error
	}{
		{"revoked", "DELETE FROM project_source_readers WHERE account_id='alice'", access.ErrForbidden},
		{"revised", "UPDATE project_sources SET body='new revision' WHERE id='minutes'", errToolReadStale},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := personalGate(t)
			g.sql(`INSERT INTO project_sources(tenant_key,app_id,project_id,kind,id,title,body)
                VALUES('tenant','app','north','document','minutes','纪要','ONLY_ALLOWED_91');
                INSERT INTO project_source_readers VALUES('tenant','app','north','document','minutes','alice')`)
			original := personalRequest(t, g, "alice", "workbench", "", "project", "first-turn", "根据纪要拟定草案")
			completedPersonalTurn(t, g, func(c ToolCall) string {
				text, err := g.projectTool(g.ctx, projectCall{ToolCall: c, Name: "file_get", Args: pmtools.Args{"file_ref": json.RawMessage(`"document:minutes"`)}})
				if err != nil || !strings.Contains(string(text), "ONLY_ALLOWED_91") {
					t.Fatal(string(text), err)
				}
				return "草案正文 ONLY_ALLOWED_91"
			})
			derived := personalRequest(t, g, "alice", "workbench", "", "project", "second-turn", "把第二条改短一点")
			completedPersonalTurn(t, g, func(c ToolCall) string {
				if !strings.Contains(c.Prompt, "草案正文 ONLY_ALLOWED_91") {
					t.Fatal("missing authorized previous draft")
				}
				return "修改后的草案 ONLY_ALLOWED_91"
			})
			third := personalRequest(t, g, "alice", "workbench", "", "project", "third-turn", "确认写入")
			w, err := g.claim(g.ctx)
			if err != nil || w.Work != third || !strings.Contains(w.Prompt, "修改后的草案") {
				t.Fatal("missing revised draft", err)
			}
			c := ToolCall{Binding: w.Binding, Turn: 1}
			if _, err = g.useTurn(g.ctx, c, true); err != nil {
				t.Fatal(err)
			}
			g.sql(tc.change)
			_, err = g.projectTool(g.ctx, projectCall{ToolCall: c, Name: "project_get", Args: pmtools.Args{}})
			if !errors.Is(err, tc.want) {
				t.Fatalf("read from work retaining stale context: %v", err)
			}
			actor := identity.Principal{TenantKey: g.Tenant, AppID: g.App, AccountID: "alice", OpenID: "login-a"}
			for _, id := range []string{original, derived, third} {
				err = pgx.BeginFunc(g.ctx, g.DB, func(tx pgx.Tx) error { return g.checkToolReads(g.ctx, tx, id, actor, "north") })
				if !errors.Is(err, tc.want) {
					t.Fatalf("stale source survived on %s: %v", id, err)
				}
			}
			g.sql("UPDATE agent_group_work SET lease_until=NULL WHERE id=$1", third)
			g.step()
			g.state(third, "suppressed")
			personalRequest(t, g, "alice", "workbench", "", "project", "new-clean-turn", "继续")
			completedPersonalTurn(t, g, func(c ToolCall) string {
				if strings.Contains(c.Prompt, "ONLY_ALLOWED_91") {
					t.Fatal("revoked evidence reappeared via a derived answer")
				}
				return "请重新指定可用资料"
			})
		})
	}
}

func TestPrivatePersonalContextIsScopedToChannel(t *testing.T) {
	g := personalGate(t)
	ch := g.Channels["internal"]
	ch.Resolve = func(_ context.Context, _ pgx.Tx, e Envelope) (Assignment, error) {
		if e.Chat != "dm-"+e.Account {
			return Assignment{}, access.ErrForbidden
		}
		return Assignment{Project: e.Project, Bot: "internal-bot", Version: 1}, nil
	}
	g.Channels["feishu"] = ch
	first := personalRequest(t, g, "alice", "feishu", "dm-thread", "project", "dm-first", "先拟草案")
	completedPersonalTurn(t, g, func(c ToolCall) string { return "私聊草案 DM_ONLY" })
	g.step()
	g.state(first, "sent")
	personalRequest(t, g, "alice", "feishu", "dm-thread", "project", "dm-second", "继续修改")
	completedPersonalTurn(t, g, func(c ToolCall) string {
		if !strings.Contains(c.Prompt, "私聊草案 DM_ONLY") {
			t.Fatal("private channel lost its draft")
		}
		return "已整理"
	})
	g.step()
	personalRequest(t, g, "alice", "workbench", "", "project", "not-dm", "继续")
	completedPersonalTurn(t, g, func(c ToolCall) string {
		if strings.Contains(c.Prompt, "DM_ONLY") {
			t.Fatal("private chat crossed channels")
		}
		return "请指定"
	})
}

func TestConfirmedBriefSurvivesRevisionAndLostAcknowledgement(t *testing.T) {
	g := personalGate(t)
	g.sql("UPDATE project_members SET role='manager' WHERE account_id='alice'")
	const draft = "先拟一份 2026-11-28 项目简报给我确认"
	const confirm = "确认写入"
	const date = "2026-11-28"
	g.PlanIntent = func(_ context.Context, r routing.PlanRequest) (routing.Plan, error) {
		p := routing.Plan{Intent: "project", Mode: "confirm"}
		if r.Text == draft {
			p.Mode = "draft"
			p.Actions = []routing.PlannedAction{{Tool: "briefing_save", Evidence: draft, Fields: map[string]string{"business_date": date}, Generate: []string{"title", "body"}}}
		}
		return p, nil
	}
	first := personalRequest(t, g, "alice", "workbench", "", "project", "brief-draft", draft)
	completedPersonalTurn(t, g, func(c ToolCall) string {
		data, err := g.projectTool(g.ctx, projectCall{ToolCall: c, Name: "briefing_get", Args: pmtools.Args{"business_date": json.RawMessage(`"2026-11-28"`)}})
		if err != nil || !strings.Contains(string(data), `"version":0`) {
			t.Fatalf("existing version: %s %v", data, err)
		}
		return "# 周六简报\n\n交付结果已复核。"
	})
	g.state(first, "sent")
	save := personalRequest(t, g, "alice", "workbench", "", "project", "brief-confirm", confirm)
	w, err := g.claim(g.ctx)
	if err != nil || w.Work != save || w.OriginWork != first || w.OriginItem != 1 || len(w.Plan.Actions) != 1 {
		t.Fatalf("confirmation missing: %+v %v", w.Plan, err)
	}
	c := ToolCall{Binding: w.Binding, Turn: 1}
	for _, start := range []bool{true, false} {
		if _, err = g.useTurn(g.ctx, c, start); err != nil {
			t.Fatal(err)
		}
	}
	args := pmtools.Args{"business_date": json.RawMessage(`"2026-11-28"`), "title": json.RawMessage(`"周六简报"`), "body": json.RawMessage(`"交付结果已复核。"`), "expected_version": json.RawMessage(`0`)}
	call := projectCall{ToolCall: c, Name: "briefing_save", Slot: 1, Args: args}
	ack, err := g.projectTool(g.ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	// Kill the worker immediately after Gateway commit, before returning a native
	// answer. A fresh worker and a duplicate HTTP delivery cannot rerun the action.
	recovered := *g.Service
	if err = recovered.Step(g.ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("completed action reopened", err)
	}
	secondAck, err := recovered.projectTool(g.ctx, call)
	if err != nil || string(secondAck) != string(ack) || strings.Contains(string(secondAck), "交付结果") {
		t.Fatalf("replayed wrong receipt %s %s %v", ack, secondAck, err)
	}
	g.state(save, "sent")
	var actual string
	if err = g.DB.QueryRow(g.ctx, "SELECT response_text FROM agent_group_work WHERE id=$1", save).Scan(&actual); err != nil || !strings.Contains(actual, "已保存 2026-11-28 项目简报（版本 1）") {
		t.Fatal(actual, err)
	}
	p := identity.Principal{TenantKey: g.Tenant, AppID: g.App, AccountID: "alice", OpenID: "login-a"}
	if err = pgx.BeginFunc(g.ctx, g.DB, func(tx pgx.Tx) error { return recovered.checkToolReads(g.ctx, tx, save, p, "north") }); err != nil {
		t.Fatal("committed receipt invalidated by its own update", err)
	}
	if same := personalRequest(t, g, "alice", "workbench", "", "project", "brief-confirm", confirm); same != save {
		t.Fatal("duplicate intake created a new work")
	}
	next := personalRequest(t, g, "alice", "workbench", "", "project", "brief-confirm-again", confirm)
	twice, err := recovered.claim(g.ctx)
	if err != nil || twice.Work != next || len(twice.Plan.Actions) != 0 || twice.OriginWork != "" {
		t.Fatalf("reconfirmed a consumed proposal: %+v %v", twice.Plan, err)
	}
	var version, count int
	if err = g.DB.QueryRow(g.ctx, "SELECT version,(SELECT count(*) FROM agent_action_grants_used WHERE origin_work=$1) FROM project_daily_briefs WHERE business_date=$2", first, date).Scan(&version, &count); err != nil || version != 1 || count != 1 {
		t.Fatalf("duplicate side effect: %d %d %v", version, count, err)
	}
}

func TestPersonalRevisionAndSelectedItem(t *testing.T) {
	g := personalGate(t)
	const draftText = "先起草两项任务供我选择"
	const revisionText = "把第二项改为交付验收"
	const confirmText = "确认写入第二项"
	const readText = "插问一句，现在有哪些相关事项"
	g.PlanIntent = func(_ context.Context, r routing.PlanRequest) (routing.Plan, error) {
		p := routing.Plan{Intent: "project", Mode: "read"}
		switch r.Text {
		case draftText:
			p.Mode = "draft"
			for range 2 {
				p.Actions = append(p.Actions, routing.PlannedAction{Tool: "task_create", Evidence: draftText, Generate: []string{"title"}})
			}
		case revisionText:
			p.Mode = "revise"
			p.Selection = 2
		case confirmText:
			p.Mode = "confirm"
			p.Selection = 2
		}
		return p, nil
	}
	first := personalRequest(t, g, "alice", "workbench", "", "project", "options-first", draftText)
	completedPersonalTurn(t, g, func(c ToolCall) string {
		return "请确认方案。\n" + proposalMarker + ` [{"title":"准备评审"},{"title":"初步检查"}]`
	})
	g.state(first, "sent")
	edited := personalRequest(t, g, "alice", "workbench", "", "project", "options-edit", revisionText)
	completedPersonalTurn(t, g, func(c ToolCall) string {
		if !strings.Contains(c.Prompt, "初步检查") {
			t.Fatal("revision lost original options")
		}
		return "已经修订第二项。\n" + proposalMarker + ` [{"title":"准备评审"},{"title":"交付验收"}]`
	})
	g.state(edited, "sent")
	personalRequest(t, g, "alice", "workbench", "", "project", "options-read", readText)
	completedPersonalTurn(t, g, func(c ToolCall) string { return "本轮只读。" })
	id := personalRequest(t, g, "alice", "workbench", "", "project", "options-save", confirmText)
	w, err := g.claim(g.ctx)
	if err != nil || w.Work != id || w.OriginWork != edited || w.OriginItem != 2 || len(w.Plan.Actions) != 1 {
		t.Fatalf("resolved wrong proposal %s %d %+v %v", w.OriginWork, w.OriginItem, w.Plan, err)
	}
	c := ToolCall{Binding: w.Binding, Turn: 1}
	for _, start := range []bool{true, false} {
		if _, err = g.useTurn(g.ctx, c, start); err != nil {
			t.Fatal(err)
		}
	}
	_, err = g.projectTool(g.ctx, projectCall{ToolCall: c, Name: "task_create", Slot: 1, Args: pmtools.Args{"title": json.RawMessage(`"交付验收"`)}})
	if err != nil {
		t.Fatal(err)
	}
	g.state(id, "sent")
	personalRequest(t, g, "alice", "workbench", "", "project", "options-save-again", confirmText)
	repeated, err := g.claim(g.ctx)
	if err != nil || repeated.OriginWork != "" || len(repeated.Plan.Actions) > 0 {
		t.Fatal("old or unselected task authorized after duplicate confirmation", err)
	}
	var titles []string
	if err = g.DB.QueryRow(g.ctx, "SELECT array_agg(title) FROM project_tasks").Scan(&titles); err != nil || len(titles) != 1 || titles[0] != "交付验收" {
		t.Fatal(titles, err)
	}
}

func TestPrivateConfirmationWithMultipleProjects(t *testing.T) {
	g := personalGate(t)
	g.sql(`INSERT INTO project_profiles(tenant_key,app_id,project_id,name,status,version)
        VALUES('tenant','app','south','南星项目','active',1);
        INSERT INTO project_members(tenant_key,app_id,project_id,account_id,member_id,role)
        VALUES('tenant','app','south','alice','s','manager')`)
	ch := g.Channels["internal"]
	ch.Resolve = func(_ context.Context, _ pgx.Tx, e Envelope) (Assignment, error) {
		if e.Chat != "dm-"+e.Account {
			return Assignment{}, access.ErrForbidden
		}
		return Assignment{Project: e.Project, Bot: "internal-bot", Version: 1}, nil
	}
	g.Channels["feishu"] = ch
	const draft = "在北辰项目先拟一项任务"
	g.PlanIntent = func(_ context.Context, r routing.PlanRequest) (routing.Plan, error) {
		p := routing.Plan{Intent: "project", Mode: "confirm"}
		if r.Text == draft {
			p.Mode = "draft"
			p.Project = "北辰项目"
			p.Actions = []routing.PlannedAction{{Tool: "task_create", Evidence: draft, Generate: []string{"title"}}}
		}
		return p, nil
	}
	accept := func(key, text string) string {
		t.Helper()
		var id string
		err := pgx.BeginFunc(g.ctx, g.DB, func(tx pgx.Tx) error {
			e := Envelope{Tenant: g.Tenant, App: g.App, Channel: "feishu", Account: "alice", Chat: "dm-alice", Thread: "private-thread", Message: key, Text: text, Mode: PrivateReply, Received: time.Now()}
			if _, err := ResolvePrivateProject(g.ctx, tx, &e); err != nil {
				return err
			}
			var err error
			id, err = Accept(g.ctx, tx, ch, e)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	first := accept("draft-private", draft)
	completedPersonalTurn(t, g, func(c ToolCall) string {
		return "待确认。\n" + proposalMarker + ` [{"title":"整理交付材料"}]`
	})
	g.step()
	g.state(first, "sent")
	next := accept("confirm-private", "确认写入")
	w, err := g.claim(g.ctx)
	if err != nil || w.Work != next || w.Scope.ProjectID != "north" || w.OriginWork != first || w.Address.ChoicePrompt != "" {
		t.Fatalf("private follow-up lost its previously selected project: %#v %v", w, err)
	}
	c := ToolCall{Binding: w.Binding, Turn: 1}
	for _, start := range []bool{true, false} {
		if _, err = g.useTurn(g.ctx, c, start); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = g.projectTool(g.ctx, projectCall{ToolCall: c, Slot: 1, Name: "task_create", Args: pmtools.Args{"title": json.RawMessage(`"整理交付材料"`)}}); err != nil {
		t.Fatal(err)
	}
	g.state(next, "ready")
	g.step()
	g.state(next, "sent")
	if len(g.replies) != 2 || g.replies[1].Chat != "dm-alice" || g.replies[1].Thread != "private-thread" || !strings.Contains(g.texts[1], "已提交") {
		t.Fatal("wrong confirmation recipient or receipt", g.replies, g.texts)
	}
}
