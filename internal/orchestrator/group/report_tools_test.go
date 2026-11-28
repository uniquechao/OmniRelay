package group

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/agent/pmtools"
	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/project"
	"github.com/uniquechao/OmniRelay/internal/routing"
)

func TestBoundReportSourcesAndReplay(t *testing.T) {
	g := personalGate(t)
	const request = "根据 2026-11-16 至 2026-11-22 的会议纪要和群聊写项目周报；额外参阅 document:override.extra，保存到 2026-11-28。"
	g.PlanIntent = func(_ context.Context, r routing.PlanRequest) (routing.Plan, error) {
		p := routing.Plan{Intent: "project", Mode: "read"}
		if r.Text == request {
			p.Mode = "execute"
			p.Actions = []routing.PlannedAction{{Tool: "briefing_save", Evidence: request, Fields: map[string]string{"business_date": "2026-11-28"}, Generate: []string{"title", "body"}}}
			p.EvidencePeriod = &routing.EvidencePeriod{Kind: "range", From: "2026-11-16", To: "2026-11-22", Evidence: "2026-11-16 至 2026-11-22"}
		}
		return p, nil
	}
	g.sql(`UPDATE project_members SET role='manager' WHERE account_id='alice';
    INSERT INTO feishu_bot_configs(tenant_key,app_id,credentials_generation,enabled) VALUES('tenant','app',1,true);
    INSERT INTO feishu_chat_conversations(tenant_key,app_id,chat_id,kind) VALUES('tenant','app','week-group','group');
    INSERT INTO project_chat_links(tenant_key,app_id,chat_id,project_id) VALUES('tenant','app','week-group','north');
    INSERT INTO feishu_chat_messages(tenant_key,app_id,chat_id,message_id,body,search_text,sent_at,updated_at)
     SELECT 'tenant','app','week-group',id,'关键词'||id,'关键词'||id,stamp,stamp FROM (VALUES
      ('early','2026-11-15 15:59:59+00'::timestamptz),('begin','2026-11-15 16:00:00+00'),
      ('end','2026-11-22 15:59:59+00'),('late','2026-11-22 16:00:00+00')) AS v(id,stamp);
    UPDATE feishu_chat_messages SET sender_id='ou_author' WHERE message_id='begin';
    INSERT INTO project_sources(tenant_key,app_id,project_id,kind,id,title,body,created_at)
     SELECT 'tenant','app','north','document',id,id,body,stamp FROM (VALUES
      ('inside','周内纪要正文','2026-11-15 16:00:00+00'::timestamptz),
      ('override','禁止的过期纪要正文','2026-11-22 16:00:00+00'),('override.extra','单独指定的参考资料','2026-11-23 15:00:00+00'))AS v(id,body,stamp);
    INSERT INTO project_source_readers SELECT tenant_key,app_id,project_id,kind,id,'alice' FROM project_sources;
    INSERT INTO project_meeting_files(tenant_key,app_id,project_id,file_ref,marked_by)
     SELECT tenant_key,app_id,project_id,'document:'||id,'alice' FROM project_sources;
    INSERT INTO project_daily_briefs(tenant_key,app_id,project_id,title,business_date,body,version,updated_by)
     VALUES('tenant','app','north','区间报告','2026-11-17','周内简报正文',1,'alice'),('tenant','app','north','今日标题','2026-11-28','界外简报正文',2,'alice');
    INSERT INTO project_member_reports(tenant_key,app_id,project_id,account_id,work_date,note,version)
     VALUES('tenant','app','north','alice','2026-11-19','本周日报',1),('tenant','app','north','alice','2026-11-23','界外日报',1);`)
	g.Projects = project.New(g.DB, g.Roles, nil, nil, chat.New(g.DB, g.Tenant, g.App, 1, acceptanceChatSource{}, nil, g.Roles.Check, chat.ReceiptSources{}), false)
	claim := func(key, text string) (work, ToolCall) {
		t.Helper()
		id := personalRequest(t, g, "alice", "workbench", "", "project", key, text)
		w, err := g.claim(g.ctx)
		if err != nil || w.Work != id {
			t.Fatal(w.Work, err)
		}
		c := ToolCall{Binding: w.Binding, Turn: 1}
		for _, bind := range []bool{true, false} {
			if _, err = g.useTurn(g.ctx, c, bind); err != nil {
				t.Fatal(err)
			}
		}
		return w, c
	}
	w, c := claim("frozen-week", request)
	if w.Plan.EvidencePeriod == nil || w.Plan.EvidencePeriod.Timezone != "Asia/Shanghai" {
		t.Fatal(w.Plan)
	}
	call := func(name, args string, want int) string {
		t.Helper()
		var a pmtools.Args
		if err := json.Unmarshal([]byte(args), &a); err != nil {
			t.Fatal(err)
		}
		val, err := g.projectTool(g.ctx, projectCall{ToolCall: c, Name: name, Args: a})
		status := 200
		if err != nil {
			status = callbackStatus(err)
		}
		if status != want {
			t.Fatalf("%s %s: got %d, want %d: %s %v", name, args, status, want, val, err)
		}
		return string(val)
	}
	for _, q := range []string{`{"chat_id":"week-group","q":"关键词"}`, `{"chat_id":"week-group"}`,
		`{"chat_id":"week-group","date_from":"2020-01-01","date_to":"2030-01-01","business_timezone":"UTC"}`} {
		val := call("chat_search_messages", q, 200)
		if !strings.Contains(val, "关键词begin") || !strings.Contains(val, "关键词end") || strings.Contains(val, "关键词early") || strings.Contains(val, "关键词late") {
			t.Fatal(val)
		}
	}
	val := call("chat_search_messages", `{"chat_id":"week-group","date_from":"2026-11-22"}`, 200)
	if strings.Contains(val, "关键词begin") || !strings.Contains(val, "关键词end") {
		t.Fatal(val)
	}
	call("chat_search_messages", `{"chat_id":"week-group","date_from":"2026-11-23","date_to":"2026-12-01"}`, 400)
	call("chat_search_messages", `{"chat_id":"week-group","q":"关键词","date_from":"invalid"}`, 400)
	files := call("file_search", `{"category":"minutes"}`, 200)
	if !strings.Contains(files, "document:inside") || strings.Contains(files, "document:override") {
		t.Fatal(files)
	}
	if got := call("file_get", `{"file_ref":"document:inside"}`, 200); !strings.Contains(got, "周内纪要正文") {
		t.Fatal(got)
	}
	call("file_get", `{"file_ref":"document:override"}`, 403)
	if got := call("file_get", `{"file_ref":"document:override.extra"}`, 200); !strings.Contains(got, "单独指定的参考资料") {
		t.Fatal(got)
	}
	if got := call("daily_report_query", "{}", 200); !strings.Contains(got, "本周日报") || strings.Contains(got, "界外日报") {
		t.Fatal(got)
	}
	dir := call("briefing_list", `{"date_from":"2026-11-18","date_to":"2026-11-18"}`, 200)
	if !strings.Contains(dir, `"total":0`) || !strings.Contains(dir, "catalog_metadata_only") || !strings.Contains(dir, "今日标题") || strings.Contains(dir, "界外简报正文") {
		t.Fatal(dir)
	}
	if got := call("briefing_get", `{"business_date":"2026-11-17"}`, 200); !strings.Contains(got, "周内简报正文") {
		t.Fatal(got)
	}
	if got := call("briefing_get", `{"business_date":"2026-11-28"}`, 200); !strings.Contains(got, `"version":2`) || strings.Contains(got, "界外简报正文") || strings.Contains(got, "今日标题") {
		t.Fatal(got)
	}
	call("briefing_get", `{"business_date":"2026-11-27"}`, 403)
	var proven bool
	if err := g.DB.QueryRow(g.ctx, `SELECT coalesce(bool_and(args->>'date_from'>='2026-11-16' AND args->>'date_to'='2026-11-22'
        AND args->>'business_timezone'='Asia/Shanghai'),false) FROM agent_project_tool_reads WHERE work_id=$1 AND tool='chat_search_messages'`, w.Work).Scan(&proven); err != nil || !proven {
		t.Fatal("missing effective read parameters", err)
	}
	recheck := func() error {
		return pgx.BeginFunc(g.ctx, g.DB, func(tx pgx.Tx) error {
			_, e := g.verifyWorkReads(g.ctx, tx, w.Work, w.Actor, w.Scope.ProjectID, true)
			return e
		})
	}
	legacyArgs := pmtools.Args{"chat_id": json.RawMessage(`"week-group"`), "q": json.RawMessage(`"关键词begin"`)}
	var legacyDigest string
	if err := pgx.BeginFunc(g.ctx, g.DB, func(tx pgx.Tx) error {
		val, err := g.Projects.RecheckToolRead(g.ctx, tx, w.Actor, w.Scope.ProjectID, "chat_search_messages", legacyArgs)
		if err != nil {
			return err
		}
		legacyDigest, err = toolDigest(val)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	g.sql("UPDATE feishu_chat_messages SET sender_name='已补全',updated_at=now() WHERE message_id='begin'")
	call("chat_search_messages", `{"chat_id":"week-group","q":"关键词begin"}`, 200)
	if err := recheck(); err != nil {
		t.Fatal("display metadata invalidated personal reads", err)
	}
	g.sql("INSERT INTO agent_project_tool_reads(work_id,tool,fingerprint,args,digest) VALUES($1,'chat_search_messages','old-format',$2,$3)", w.Work, legacyArgs, legacyDigest)
	if err := recheck(); !errors.Is(err, errToolReadStale) {
		t.Fatal("historical receipt lost its complete-response check", err)
	}
	g.sql("DELETE FROM agent_project_tool_reads WHERE work_id=$1 AND fingerprint='old-format'", w.Work)
	g.sql("UPDATE feishu_chat_messages SET search_text='界外更新' WHERE message_id='late'")
	if err := recheck(); err != nil {
		t.Fatal("out-of-period edit invalidated the frozen source", err)
	}
	g.sql("UPDATE feishu_chat_messages SET search_text='关键词周内更新' WHERE message_id='begin'")
	if err := recheck(); !errors.Is(err, errToolReadStale) {
		t.Fatal("in-period change missed", err)
	}
	var tools []pmtools.Tool
	if err := pgx.BeginFunc(g.ctx, g.DB, func(tx pgx.Tx) error { var err error; tools, err = g.allowedTools(g.ctx, tx, w); return err }); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(tools, func(t pmtools.Tool) bool { return t.Name == "briefing_save" }) {
		t.Fatal("period removed authorized write")
	}
	g.sql("UPDATE agent_group_work SET state='failed' WHERE id=$1", w.Work)
	_, c = claim("ordinary-qa", "不限定时间，查资料")
	if got := call("file_get", `{"file_ref":"document:override"}`, 200); !strings.Contains(got, "禁止的过期纪要正文") {
		t.Fatal("ordinary conversation inherited report window", got)
	}
}

func TestReportCandidateWindowKeepsDSTAndWriteAuthority(t *testing.T) {
	g := newGate(t)
	g.sql("UPDATE project_members SET role='manager' WHERE account_id='alice'")
	period := &routing.EvidencePeriod{Kind: "range", From: "2025-11-02", To: "2025-11-02", Timezone: "America/New_York"}
	w := work{Plan: routing.Plan{Mode: "execute", EvidencePeriod: period, Actions: []routing.PlannedAction{{Tool: "task_create"}, {Tool: "task_candidate_submit"}}}}
	w.Actor.TenantKey, w.Actor.AppID, w.Actor.AccountID, w.Actor.OpenID = g.Tenant, g.App, "alice", "login-a"
	w.Role, w.Scope.ProjectID = "project-pm", "north"
	var args pmtools.Args
	if err := json.Unmarshal([]byte(`{"chat_id":"oc","from_at":"2025-10-15T00:00:00Z","to_at":"2025-12-01T00:00:00Z"}`), &args); err != nil {
		t.Fatal(err)
	}
	bound, err := w.reportToolArgs("task_candidate_chat_sources", args)
	if err != nil || bound.String("from_at") != "2025-11-02T00:00:00-04:00" || bound.String("to_at") != "2025-11-03T00:00:00-05:00" {
		t.Fatalf("incorrect 25-hour business day: %s %s %v", bound["from_at"], bound["to_at"], err)
	}
	err = pgx.BeginFunc(g.ctx, g.DB, func(tx pgx.Tx) error {
		tools, err := g.allowedTools(g.ctx, tx, w)
		if err != nil {
			return err
		}
		for _, name := range []string{"task_create", "task_candidate_submit", "task_candidate_chat_sources"} {
			if !slices.ContainsFunc(tools, func(spec pmtools.Tool) bool { return spec.Name == name }) {
				t.Fatal("authorized reporting workflow lost tool", name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
