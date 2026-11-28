package group

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/agent/pmtools"
	"github.com/uniquechao/OmniRelay/internal/routing"
	"os"
	"strings"
	"testing"
	"time"
)

const groupBoardQuestion = "当前群里有哪些没完成任务 分别at到人"

func exerciseGroupBoardReply(t *testing.T, g *gate) {
	t.Helper()
	// Use the production Feishu channel key, with the final HTTP transport captured.
	g.Channels["feishu"] = g.Channels["internal"]
	g.sql(`UPDATE accounts SET open_id='ou_'||id;
INSERT INTO feishu_bot_configs(tenant_key,app_id,credentials_generation,enabled) VALUES('tenant','app',1,true);
INSERT INTO feishu_chat_conversations(tenant_key,app_id,chat_id,kind) VALUES('tenant','app','group-north','group');
INSERT INTO project_chat_links(tenant_key,app_id,project_id,chat_id) VALUES('tenant','app','north','group-north');
INSERT INTO feishu_drive_group_memberships(tenant_key,app_id,chat_id,generation,members,verified_at)
VALUES('tenant','app','group-north',1,'{ou_alice,ou_bob}',now());
INSERT INTO project_task_boards(tenant_key,app_id,project_id) VALUES('tenant','app','north');
SELECT set_config('omnirelay.task_actor','alice',true);
INSERT INTO project_tasks(tenant_key,app_id,project_id,id,title,status,position,owner_member_id) VALUES
('tenant','app','north','one','核实图纸','in_progress',1,'a'),('tenant','app','north','two','确认物料','blocked',2,'b'),
('tenant','app','north','three','已交付项目','done',3,'b')`)
	receive := func() string {
		var id string
		err := pgx.BeginFunc(g.ctx, g.DB, func(tx pgx.Tx) error {
			var err error
			id, err = Accept(g.ctx, tx, g.Channels["feishu"], Envelope{Tenant: g.Tenant, App: g.App, Mode: GroupReply, Channel: "feishu", Account: "alice", Chat: "group-north", Thread: "omt_original", Message: "om_original", Text: groupBoardQuestion, Received: time.Now(), Mention: true})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	id := receive()
	g.step()
	g.state(id, "ready")
	var prepared string
	if err := g.DB.QueryRow(g.ctx, "SELECT response_text FROM agent_group_work WHERE id=$1", id).Scan(&prepared); err != nil || !strings.Contains(prepared, "未完成任务共 2 项") || strings.Contains(prepared, "已交付项目") {
		t.Fatalf("prepared %q, %v", prepared, err)
	}
	// An item that closes while delivery is queued must not ping its former owner.
	g.sql(`SELECT set_config('omnirelay.task_actor','alice',true); UPDATE project_tasks SET status='done' WHERE id='one'`)
	g.step()
	g.state(id, "sent")
	if len(g.replies) != 1 {
		t.Fatal("missing or extra group reply", len(g.replies))
	}
	e := g.replies[0]
	if e.Chat != "group-north" || e.Message != "om_original" || e.Thread != "omt_original" || e.ReplyBody == nil || !strings.Contains(g.texts[0], "确认物料") || !strings.Contains(g.texts[0], "未完成任务共 1 项") || strings.Contains(g.texts[0], "核实图纸") || strings.Contains(g.texts[0], "工作台") {
		t.Fatalf("wrong destination/content: %+v %q", e, g.texts)
	}
	var ids []string
	for _, line := range e.ReplyBody.Lines {
		if line.MentionOpenID != "" {
			ids = append(ids, line.MentionOpenID)
		}
	}
	if len(ids) != 1 || ids[0] != "ou_bob" || receive() != id {
		t.Fatalf("wrong mention or duplicate delivery: %v", ids)
	}
}

func TestGroupBoardReply(t *testing.T) {
	g := newGate(t)
	g.PlanIntent = func(_ context.Context, r routing.PlanRequest) (routing.Plan, error) {
		if !r.Group || r.Text != groupBoardQuestion {
			t.Fatal("unexpected planner request", r)
		}
		return routing.Plan{Intent: "project", Mode: "read", GroupReply: &routing.GroupResponse{Kind: "open_tasks", MentionEvidence: "分别at到人"}}, nil
	}
	exerciseGroupBoardReply(t, g)
}

func TestLiveGroupBoardReply(t *testing.T) {
	if os.Getenv("OMNI_REAL_MODEL_ACCEPTANCE") != "1" {
		t.Skip("requires isolated live-model environment")
	}
	g, _, _, _ := openImageGate(t)
	exerciseGroupBoardReply(t, g)
}

func TestLiveGroupReplySemantics(t *testing.T) {
	if os.Getenv("OMNI_REAL_MODEL_ACCEPTANCE") != "1" {
		t.Skip("requires isolated live-model environment")
	}
	g, client, _, _ := openImageGate(t)
	for _, tc := range []struct {
		question, kind string
		mention        bool
	}{
		{"请列一下现在还在排队或卡住的工作，顺便提醒各自负责人。", "open_tasks", true},
		{"列出本项目未完成任务和负责人，别通知他们。", "open_tasks", false},
		{"总结本群昨天的讨论。", "answer", false},
	} {
		r := routing.PlanRequest{Text: tc.question, Group: true, ProjectSelected: true}
		p, err := client.Plan(g.ctx, r)
		if err != nil || p.Validate(r) != nil || p.GroupReply == nil || p.GroupReply.Kind != tc.kind || (p.GroupReply.MentionEvidence != "") != tc.mention {
			t.Fatalf("question %q, plan %+v (%v)", tc.question, p, err)
		}
	}
}

func TestGroupReplyAudienceFromActualSources(t *testing.T) {
	g := newGate(t)
	g.PlanIntent = func(context.Context, routing.PlanRequest) (routing.Plan, error) {
		return routing.Plan{Mode: "read", Intent: "project", GroupReply: &routing.GroupResponse{Kind: "answer"}}, nil
	}
	g.sql(`INSERT INTO project_sources(tenant_key,app_id,project_id,kind,id,title,body) VALUES('tenant','app','north','document','secret','private file','private answer');
INSERT INTO project_source_readers VALUES('tenant','app','north','document','secret','alice')`)
	for _, private := range []bool{false, true} {
		key, answer := "public", "本话题的讨论摘要"
		if private {
			key, answer = "restricted", "private answer"
		}
		id := g.projectMention(key, "请回答我的问题")
		completedPersonalTurn(t, g, func(c ToolCall) string {
			if private {
				if _, err := g.projectTool(g.ctx, projectCall{ToolCall: c, Name: "file_get", Args: pmtools.Args{"file_ref": json.RawMessage(`"document:secret"`)}}); err != nil {
					t.Fatal(err)
				}
			}
			return answer
		})
		g.step()
		g.state(id, "sent")
		text := g.texts[len(g.texts)-1]
		if strings.Contains(text, answer) == private || strings.Contains(text, "工作台") != private {
			t.Fatalf("unexpected audience %t: %s", private, text)
		}
	}
}
