package group

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/agent/dsh"
	"github.com/uniquechao/OmniRelay/internal/agent/pmtools"
	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/project"
	"github.com/uniquechao/OmniRelay/internal/routing"
)

// Reproduce the original failure at the durable work boundary: after a
// successful private read, claiming the same draft must produce the same DSH input.
func TestGroupDraftRetryKeepsNativeInput(t *testing.T) {
	g := newGate(t)
	const question = "请根据成员日报起草项目简报"
	g.PlanIntent = func(context.Context, routing.PlanRequest) (routing.Plan, error) {
		return routing.Plan{Intent: "project", Mode: "draft", GroupReply: &routing.GroupResponse{Kind: "answer", Scope: "personal"}, Actions: []routing.PlannedAction{{Tool: "briefing_save", Evidence: question, Fields: map[string]string{"business_date": "@today"}, Generate: []string{"title", "body"}}}}, nil
	}
	id := g.projectMention("weekly-retry", question)
	var first dsh.Binding
	g.Execute = func(ctx context.Context, b dsh.Binding) (dsh.Result, error) {
		first = b
		call := ToolCall{Binding: b, Turn: 1}
		if _, err := g.useTurn(ctx, call, true); err != nil {
			t.Fatal(err)
		}
		if _, err := g.useTurn(ctx, call, false); err != nil {
			t.Fatal(err)
		}
		if _, err := g.projectTool(ctx, projectCall{ToolCall: call, Name: "project_get", Args: pmtools.Args{}}); err != nil {
			t.Fatal(err)
		}
		return dsh.Result{}, errors.New("transient interruption after read")
	}
	if err := g.Step(g.ctx); err == nil {
		t.Fatal("expected transient execution error")
	}
	var private bool
	if err := g.DB.QueryRow(g.ctx, "SELECT (semantic_plan #>> '{group_reply,private_sources}')::bool FROM agent_group_work WHERE id=$1", id).Scan(&private); err != nil || !private {
		t.Fatal("private receipt was not retained", err)
	}
	g.sql("UPDATE agent_group_work SET available_at=now() WHERE id=$1", id)
	completedPersonalTurn(t, g, func(c ToolCall) string {
		if c.Prompt != first.Prompt || c.Session != first.Session || c.Generation <= first.Generation {
			t.Fatal("runtime retry changed its native input")
		}
		return "# 项目简报\n\n受限资料详情"
	})
	g.step()
	g.state(id, "sent")
	if len(g.texts) != 1 || !strings.Contains(g.texts[0], "工作台") || strings.Contains(g.texts[0], "受限资料详情") {
		t.Fatal("private material sent to group")
	}
}

func TestGroupImmutableConflictDeliversOneFailureReceipt(t *testing.T) {
	g := newGate(t)
	id := g.projectMention("immutable-clash", "请说明本群进度")
	g.Execute = func(context.Context, dsh.Binding) (dsh.Result, error) {
		return dsh.Result{}, dsh.ErrWorkBindingConflict
	}
	g.step()
	g.state(id, "ready")
	g.step()
	g.state(id, "sent")
	var reason string
	if err := g.DB.QueryRow(g.ctx, "SELECT reason FROM agent_group_work WHERE id=$1", id).Scan(&reason); err != nil || reason != workBindingConflict || len(g.texts) != 1 || !strings.Contains(g.texts[0], "未能生成答复") {
		t.Fatal("missing terminal group receipt", reason, err)
	}
	if err := g.Step(g.ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("permanent failure redelivered", err)
	}
}

func TestLiveGroupWeeklyBriefDelivery(t *testing.T) {
	if os.Getenv("OMNI_REAL_MODEL_ACCEPTANCE") != "1" {
		t.Skip("requires isolated live-model environment")
	}
	g, _, _, _ := openImageGate(t)
	g.Channels["feishu"] = g.Channels["internal"]
	chats := chat.New(g.DB, g.Tenant, g.App, 1, acceptanceChatSource{}, nil, g.Roles.Check, chat.ReceiptSources{})
	g.ReadThread = chats.AgentThread
	g.Projects = project.New(g.DB, g.Roles, nil, nil, chats, false)
	g.sql(`INSERT INTO feishu_bot_configs(tenant_key,app_id,credentials_generation,enabled) VALUES('tenant','app',1,true);
INSERT INTO feishu_chat_conversations(tenant_key,app_id,chat_id,kind) VALUES('tenant','app','group-north','group');
INSERT INTO project_chat_links(tenant_key,app_id,project_id,chat_id) VALUES('tenant','app','north','group-north');
INSERT INTO feishu_chat_messages(tenant_key,app_id,chat_id,message_id,sender_kind,body,search_text,sent_at,updated_at,visible_in_chat)
VALUES('tenant','app','group-north','msg-brief','user','本周联调进展：声学校准已通过验收，风险是转接板尚未到货。','本周联调进展：声学校准已通过验收，风险是转接板尚未到货。',now(),now(),true);
INSERT INTO project_sources(tenant_key,app_id,project_id,kind,id,title,body)
VALUES('tenant','app','north','document','secret','私有资料','PRIVATE-BRIEF-ONLY');
INSERT INTO project_source_readers VALUES('tenant','app','north','document','secret','alice')`)
	var id string
	err := pgx.BeginFunc(g.ctx, g.DB, func(tx pgx.Tx) error {
		var err error
		id, err = Accept(g.ctx, tx, g.Channels["feishu"], Envelope{Tenant: g.Tenant, App: g.App, Mode: GroupReply, Channel: "feishu", Account: "alice", Chat: "group-north", Message: "om_summary", Text: "总结下项目周截面简报", Received: time.Now(), Mention: true})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	g.step()
	g.state(id, "ready")
	g.step()
	g.state(id, "sent")
	var plan routing.Plan
	var sourceReads, writes, briefs int
	if err = g.DB.QueryRow(g.ctx, `SELECT semantic_plan,
        (SELECT count(*) FROM agent_project_tool_reads WHERE work_id=w.id AND tool=$2),
        (SELECT count(*) FROM agent_project_tool_calls WHERE work_id=w.id),(SELECT count(*) FROM project_daily_briefs)
        FROM agent_group_work w WHERE id=$1`, id, project.GroupMessageRead).Scan(&plan, &sourceReads, &writes, &briefs); err != nil {
		t.Fatal(err)
	}
	if plan.GroupReply == nil || plan.GroupReply.Scope != "group" || plan.GroupReply.PrivateSources || plan.Mode != "draft" || plan.EvidencePeriod == nil || plan.EvidencePeriod.Kind != "this_week" || sourceReads == 0 || writes != 0 || briefs != 0 {
		p, _ := json.Marshal(plan)
		t.Fatalf("unexpected outcome %s / reads=%d writes=%d saved=%d", p, sourceReads, writes, briefs)
	}
	if len(g.replies) != 1 || g.replies[0].Message != "om_summary" || !strings.Contains(g.texts[0], "声学") || !strings.Contains(g.texts[0], "转接板") || strings.Contains(g.texts[0], "PRIVATE-BRIEF-ONLY") || strings.Contains(g.texts[0], "工作台") {
		t.Fatalf("unexpected group receipt: %q", g.texts)
	}
}
