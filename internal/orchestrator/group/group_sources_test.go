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
	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/project"
	"github.com/uniquechao/OmniRelay/internal/routing"
)

func TestGroupSourceReadPublication(t *testing.T) {
	g := newGate(t)
	g.Projects = project.New(g.DB, g.Roles, nil, nil, chat.New(g.DB, g.Tenant, g.App, 1, acceptanceChatSource{}, nil, g.Roles.Check, chat.ReceiptSources{}), false)
	g.PlanIntent = func(context.Context, routing.PlanRequest) (routing.Plan, error) {
		return routing.Plan{Intent: "project", Mode: "read", GroupReply: &routing.GroupResponse{Kind: "answer", Scope: "group"}}, nil
	}
	g.sql(`INSERT INTO feishu_bot_configs(tenant_key,app_id,credentials_generation,enabled) VALUES('tenant','app',1,true);
INSERT INTO project_relay_settings(tenant_key,app_id,project_id,timezone,local_time,version) VALUES('tenant','app','north','UTC','18:00',1);
INSERT INTO feishu_chat_conversations(tenant_key,app_id,chat_id,kind) VALUES('tenant','app','group-north','group');
INSERT INTO project_chat_links(tenant_key,app_id,project_id,chat_id) VALUES('tenant','app','north','group-north');
INSERT INTO project_task_boards(tenant_key,app_id,project_id) VALUES('tenant','app','north');
SELECT set_config('omnirelay.task_actor','alice',true);
INSERT INTO project_tasks(tenant_key,app_id,project_id,id,title,description,position) VALUES('tenant','app','north','one','共同排期','私有工单备注',1);
INSERT INTO feishu_chat_messages(tenant_key,app_id,chat_id,message_id,sent_at,updated_at,search_text,thread_id,sender_id)
VALUES('tenant','app','group-north','original',now()-interval '1 minute',now(),'已确认周三交付','','ou_private_id'),
      ('tenant','app','group-north','private-topic',now()-interval '1 minute',now(),'仅话题可见','private-thread','unknown');`)
	for _, phase := range []string{"normal", "before_ready", "sender_reassigned", "before_send"} {
		id := g.projectMention(phase, "根据本群进展做一份总结")
		completedPersonalTurn(t, g, func(c ToolCall) string {
			err := pgx.BeginFunc(g.ctx, g.DB, func(tx pgx.Tx) error {
				w, err := g.boundWork(g.ctx, tx, c)
				if err != nil {
					return err
				}
				list, err := g.allowedTools(g.ctx, tx, w)
				if err != nil {
					return err
				}
				var names []string
				for _, tool := range list {
					names = append(names, tool.Name)
				}
				slices.Sort(names)
				if !slices.Equal(names, []string{"chat_search_messages", "task_board_get", "task_business_time"}) {
					t.Fatalf("unexpected catalog %v", names)
				}
				w.Plan.EvidencePeriod = &routing.EvidencePeriod{}
				period, err := g.allowedTools(g.ctx, tx, w)
				if err == nil && slices.ContainsFunc(period, func(t pmtools.Tool) bool { return t.Name == "task_board_get" }) {
					t.Fatal("undated board exposed to a dated report")
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, denied := range []projectCall{{ToolCall: c, Name: "project_get"}, {ToolCall: c, Name: "chat_list"}, {ToolCall: c, Name: "file_get"}, {ToolCall: c, Name: "chat_search_messages", Args: pmtools.Args{"chat_id": json.RawMessage(`"foreign"`)}}} {
				if _, err := g.projectTool(g.ctx, denied); !errors.Is(err, access.ErrForbidden) {
					t.Fatalf("permitted %+v: %v", denied, err)
				}
			}
			read := func(name string) string {
				v, err := g.projectTool(g.ctx, projectCall{ToolCall: c, Name: name, Args: pmtools.Args{}})
				if err != nil {
					t.Fatal(name, err)
				}
				return string(v)
			}
			if got := read("chat_search_messages"); !strings.Contains(got, "已确认周三交付") || strings.Contains(got, "仅话题可见") || strings.Contains(got, "ou_private_id") {
				t.Fatal(got)
			}
			if got := read("task_board_get"); !strings.Contains(got, "共同排期") || strings.Contains(got, "私有工单备注") {
				t.Fatal(got)
			}
			if phase == "normal" {
				g.sql(`UPDATE feishu_chat_messages SET sender_name='已补全群名片',updated_at=now() WHERE message_id='original'`)
				g.sql(`INSERT INTO feishu_chat_messages(tenant_key,app_id,chat_id,message_id,sent_at,updated_at,search_text) VALUES('tenant','app','group-north','later',now(),now(),'新到群消息')`)
				if got := read("chat_search_messages"); strings.Contains(got, "新到群消息") {
					t.Fatal("intake cutoff changed", got)
				}
			} else if phase == "before_ready" {
				g.sql(`UPDATE feishu_chat_messages SET search_text='已编辑' WHERE message_id='original'`)
			}
			if phase == "sender_reassigned" {
				g.sql(`UPDATE feishu_chat_messages SET sender_id='ou_another_member' WHERE message_id='original'`)
			}
			return "本群已确认周三交付"
		})
		if phase == "before_ready" || phase == "sender_reassigned" {
			g.state(id, "suppressed")
			g.sql(`UPDATE feishu_chat_messages SET search_text='已确认周三交付',sender_id='ou_private_id' WHERE message_id='original'`)
			continue
		}
		g.state(id, "ready")
		if phase == "before_send" {
			g.sql(`UPDATE feishu_chat_messages SET search_text='',body='',deleted=true WHERE message_id='original'`)
		}
		g.step()
		if phase == "normal" {
			g.state(id, "sent")
		} else {
			g.state(id, "suppressed")
		}
	}
	if len(g.replies) != 1 || !strings.Contains(g.texts[0], "本群已确认周三交付") {
		t.Fatal(g.texts)
	}
}
