package group

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
	"github.com/uniquechao/OmniRelay/internal/project"
	"github.com/uniquechao/OmniRelay/internal/routing"
)

// Only upstream chat membership is simulated. Search reads dated PostgreSQL messages.
type acceptanceChatSource struct{ chat.Source }

func (acceptanceChatSource) Members(context.Context, string, string) (chat.Page[chat.Member], error) {
	return chat.Page[chat.Member]{Items: []chat.Member{{OpenID: "login-a"}}}, nil
}
func (acceptanceChatSource) Group(_ context.Context, id string) (chat.Group, error) {
	return chat.Group{ID: id, Name: "北辰项目群"}, nil
}
func (acceptanceChatSource) History(context.Context, chat.HistoryQuery) (chat.Page[orchestrator.ChatMessage], error) {
	return chat.Page[orchestrator.ChatMessage]{}, nil
}

// Run this gate only with a disposable database, a private Runtime and a real model.
// The workbench, admission, model tool calls and committed receipts all use production code.
func TestRealModelBriefConversation(t *testing.T) {
	if os.Getenv("OMNI_REAL_MODEL_ACCEPTANCE") != "1" {
		t.Skip("requires isolated live-model environment")
	}
	g, client, _, _ := openImageGate(t)
	g.PlanIntent = client.Plan
	mux := http.NewServeMux()
	actor := identity.Principal{TenantKey: g.Tenant, AppID: g.App, AccountID: "alice", OpenID: "login-a"}
	g.RegisterWorkbench(mux, func(http.ResponseWriter, *http.Request, identity.SessionPolicy) (identity.Principal, bool) {
		return actor, true
	})
	g.sql("UPDATE project_members SET role='manager' WHERE account_id='alice'")
	marker := "UAT-" + strings.ToUpper(rand.Text()[:10])
	g.sql(`INSERT INTO project_sources(tenant_key,app_id,project_id,kind,id,title,body)
   VALUES('tenant','app','north','document','acceptance-minute','项目会议纪要',$1)`, "验收结论已确认，编号 "+marker)
	g.sql("INSERT INTO project_source_readers VALUES('tenant','app','north','document','acceptance-minute','alice')")
	zone, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(zone)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, zone)
	week := today.AddDate(0, 0, -(int(today.Weekday())+6)%7)
	first := today.AddDate(0, 0, -1).Format(time.DateOnly)
	direct := today.Format(time.DateOnly)
	groupMarker := "MSG-" + strings.ToUpper(rand.Text()[:10])
	excluded := "LATE-" + strings.ToUpper(rand.Text()[:10])
	g.sql("UPDATE project_sources SET created_at=$1 WHERE id='acceptance-minute'", week.AddDate(0, 0, -6).Add(9*time.Hour))
	excludedMinute := "NEXT-" + strings.ToUpper(rand.Text()[:10])
	g.sql(`INSERT INTO project_sources(tenant_key,app_id,project_id,kind,id,title,body,created_at) VALUES('tenant','app','north','document','next-minute','项目会议纪要',$1,$2)`, "下一周追加验收编号 "+excludedMinute, week.Add(10*time.Hour))
	g.sql("INSERT INTO project_source_readers VALUES('tenant','app','north','document','next-minute','alice')")
	g.sql(`INSERT INTO feishu_bot_configs(tenant_key,app_id,credentials_generation,enabled) VALUES('tenant','app',1,true);
	INSERT INTO feishu_chat_conversations(tenant_key,app_id,chat_id,kind) VALUES('tenant','app','uat-group','group');
	INSERT INTO project_chat_links(tenant_key,app_id,chat_id,project_id) VALUES('tenant','app','uat-group','north');`)
	g.sql(`INSERT INTO feishu_chat_messages(tenant_key,app_id,chat_id,message_id,body,search_text,sent_at,updated_at)
	VALUES('tenant','app','uat-group','event1',$1,$1,$3,$3),
	('tenant','app','uat-group','event2',$2,$2,$4,$4)`, "现场验证完成，群内复核编号 "+groupMarker, "追加计划，复核编号 "+excluded, week.AddDate(0, 0, -3).Add(16*time.Hour), week)
	g.Projects = project.New(g.DB, g.Roles, nil, nil, chat.New(g.DB, g.Tenant, g.App, 1, acceptanceChatSource{}, nil, g.Roles.Check, chat.ReceiptSources{}), false)
	const path = "/api/v1/projects/north/bot/messages"
	postAs := func(key, question, intent string) string {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"text": question, "intent": intent})
		r := httptest.NewRequest("POST", path, strings.NewReader(string(body)))
		r.Header.Set("Idempotency-Key", key)
		out := httptest.NewRecorder()
		mux.ServeHTTP(out, r)
		var v struct{ ID string }
		if out.Code != 202 || json.Unmarshal(out.Body.Bytes(), &v) != nil || v.ID == "" {
			t.Fatalf("workbench POST %d: %s", out.Code, out.Body.String())
		}
		return v.ID
	}
	post := func(key, question string) string { return postAs(key, question, "project") }
	run := func(id string) chatMessage {
		t.Helper()
		g.step()
		out := httptest.NewRecorder()
		mux.ServeHTTP(out, httptest.NewRequest("GET", path, nil))
		var msg struct{ Items []chatMessage }
		if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &msg) != nil {
			t.Fatalf("workbench history %d", out.Code)
		}
		for _, v := range msg.Items {
			if v.ID == id && v.State == "sent" {
				return v
			}
		}
		t.Fatalf("work did not reach signed delivery: %s", id)
		return chatMessage{}
	}
	version := func(date string) int {
		t.Helper()
		var v int
		if err := g.DB.QueryRow(g.ctx, `SELECT coalesce(max(version),0) FROM project_daily_briefs WHERE
    tenant_key='tenant' AND app_id='app' AND project_id='north' AND business_date=$1::text::date`, date).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	draft := run(post("weekly-draft-1", "请先给我看 "+first+" 的项目简报草稿，读 document:acceptance-minute，保留纪要里的完整验收编号。"))
	if version(first) != 0 || !strings.Contains(draft.Answer, marker) {
		t.Fatalf("draft missing source or already saved: %s", draft.Answer)
	}
	const confirm = "确认写入"
	id := post("weekly-confirm-1", confirm)
	saved := run(id)
	if version(first) != 1 || len(saved.Briefs) != 1 || saved.Briefs[0].Date != first || !strings.Contains(saved.Answer, "已保存") {
		var plan, proposals, answer, binding string
		_ = g.DB.QueryRow(g.ctx, "SELECT semantic_plan::text, coalesce(proposal_items::text,'(none)'), response_text FROM agent_group_work WHERE id=$1", draft.ID).Scan(&plan, &proposals, &answer)
		_ = g.DB.QueryRow(g.ctx, "SELECT jsonb_build_array(semantic_plan,confirmation_origin,confirmation_item)::text FROM agent_group_work WHERE id=$1", id).Scan(&binding)
		t.Fatalf("unconfirmed business outcome: %+v; draft plan %s; proposal %s; shown draft %s; confirmation %s", saved, plan, proposals, answer, binding)
	}
	var title, body string
	if err := g.DB.QueryRow(g.ctx, "SELECT title,body FROM project_daily_briefs WHERE project_id='north' AND business_date=$1::text::date", first).Scan(&title, &body); err != nil || !strings.Contains(body, marker) {
		t.Fatal("saved body lost source evidence", err)
	}
	_, visible, ok := strings.Cut(draft.Answer, "\n")
	if !ok || visible != "# "+title+"\n\n"+body {
		t.Fatal("the committed title and body differ from the displayed draft")
	}
	if post("weekly-confirm-1", confirm) != id {
		t.Fatal("request replay returned a different work")
	}
	_ = run(post("weekly-confirm-2", confirm))
	if version(first) != 1 {
		t.Fatal("second confirmation of the same draft wrote again")
	}
	message := run(post("weekly-direct-save", "作为项目经理，基于上周的会议纪要和群聊总结本项目的周截面简报并存入简报。请保留纪要的验收编号和群里给出的复核编号。"))
	if version(direct) != 1 || len(message.Briefs) != 1 || message.Briefs[0].Date != direct {
		t.Fatalf("direct save failed: %+v", message)
	}
	if err := g.DB.QueryRow(g.ctx, "SELECT body FROM project_daily_briefs WHERE project_id='north' AND business_date=$1::text::date", direct).Scan(&body); err != nil || !strings.Contains(body, marker) {
		t.Fatal("direct save lost minute evidence", err)
	}
	var from, to string
	if err := g.DB.QueryRow(g.ctx, `SELECT semantic_plan#>>'{evidence_period,from}',semantic_plan#>>'{evidence_period,to}' FROM agent_group_work WHERE id=$1`, message.ID).Scan(&from, &to); err != nil || from != week.AddDate(0, 0, -7).Format(time.DateOnly) || to != week.AddDate(0, 0, -1).Format(time.DateOnly) {
		t.Fatalf("persisted reporting period %s to %s: %v", from, to, err)
	}
	if !strings.Contains(body, groupMarker) || strings.Contains(body, excluded) || strings.Contains(body, excludedMinute) {
		t.Fatalf("weekly scope or group evidence mismatch: %s", body)
	}
	qa := run(postAs("weekly-qa", "读 document:acceptance-minute。会议纪要里的验收编号和结论分别是什么？", "knowledge"))
	if !strings.Contains(qa.Answer, marker) {
		t.Fatalf("Q&A lost the source: %s", qa.Answer)
	}
	tomorrow := today.AddDate(0, 0, 1).Format(time.DateOnly)
	follow := run(post("weekly-use-answer", "把你刚才回答里的编号和结论整理成一篇新的项目简报，保存到 "+tomorrow+"。"))
	var referenced bool
	if err := g.DB.QueryRow(g.ctx, "SELECT $2=ANY(personal_turns) FROM agent_group_work WHERE id=$1", follow.ID, qa.ID).Scan(&referenced); err != nil || !referenced {
		t.Fatalf("did not reference the Q&A turn: %+v %v", follow, err)
	}
	if len(follow.Briefs) != 1 || follow.Briefs[0].Date != tomorrow || version(tomorrow) != 1 {
		t.Fatalf("Q&A follow-up not saved: %+v", follow)
	}
	if err := g.DB.QueryRow(g.ctx, "SELECT body FROM project_daily_briefs WHERE project_id='north' AND business_date=$1::text::date", tomorrow).Scan(&body); err != nil || !strings.Contains(body, marker) {
		t.Fatal("Q&A evidence lost on follow-up", err)
	}
	t.Logf("Draft %s was stored verbatim at v1. Two replays added zero versions. Direct weekly report %s v1 includes both in-period markers and excludes the out-of-period message.", first, direct)
}

func TestRealModelSemanticModes(t *testing.T) {
	if os.Getenv("OMNI_REAL_MODEL_ACCEPTANCE") != "1" {
		t.Skip("requires isolated live-model environment")
	}
	g, client, _, _ := openImageGate(t)
	for _, tc := range []struct{ name, text, mode, intent, role string }{
		{"read-policy", "讲讲项目简报怎么保存。我只想了解操作流程。", "read", "project", "project"},
		{"draft-brief", "按会议记录给我拟一份能交给管理层的项目简报，让我先过目。", "draft", "project", "project"},
		{"commit-brief", "把本周进展归到今天的项目简报里。", "execute", "project", "project"},
		{"bare-confirm", "确认写入", "confirm", "project", "project"},
		{"confirm-preview", "行，就用刚才给我看的那份。请正式入库。", "confirm", "project", "project"},
		{"knowledge-question", "知识问答：项目跟进中的风险和阻塞分别是什么意思？", "read", "", "knowledge"},
		{"portfolio-overview", "请全局管理 Agent 查看我负责的所有项目，概括各项目本周的风险。", "read", "", "admin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := routing.PlanRequest{Text: tc.text, Intent: tc.intent, Private: tc.intent == ""}
			plan, err := client.Plan(g.ctx, request)
			if err != nil || plan.Validate(request) != nil || plan.Mode != tc.mode || plan.Intent != tc.role {
				t.Fatalf("unexpected semantic result %+v: %v", plan, err)
			}
			if (tc.mode == "execute" || tc.mode == "draft") && (len(plan.Actions) != 1 || plan.Actions[0].Tool != "briefing_save") {
				t.Fatalf("wrong brief operation: %+v", plan.Actions)
			}
		})
	}
}
