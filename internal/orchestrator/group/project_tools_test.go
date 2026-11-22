package group

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/agent/dsh"
	"github.com/uniquechao/OmniRelay/internal/agent/pmtools"
	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/project"
)

func (g *gate) projectMention(key, text string) string {
	g.t.Helper()
	var id string
	err := pgx.BeginFunc(g.ctx, g.DB, func(tx pgx.Tx) error {
		var err error
		id, err = Accept(g.ctx, tx, g.Channels["internal"], Envelope{Tenant: g.Tenant, App: g.App, Channel: "internal", Account: "alice", Chat: "group-north", Thread: "topic-tools", Message: key, Text: text, Mention: true, Received: time.Now()})
		return err
	})
	if err != nil {
		g.t.Fatal(err)
	}
	return id
}
func TestProjectToolRecoveryGate(t *testing.T) {
	g := newGate(t)
	id := g.projectMention("retry", "创建我的任务")
	claim := func() ToolCall {
		t.Helper()
		w, err := g.claim(g.ctx)
		if err != nil {
			t.Fatal(err)
		}
		c := ToolCall{Binding: w.Binding, Turn: 7}
		if _, err = g.useTurn(g.ctx, c, true); err != nil {
			t.Fatal(err)
		}
		return c
	}
	cmd := projectCall{ToolCall: claim(), Name: "task_create", Slot: 1, Args: pmtools.Args{"title": json.RawMessage(`"recover"`)}}
	wrong := cmd
	wrong.Turn++
	if _, err := g.projectTool(g.ctx, wrong); !errors.Is(err, access.ErrForbidden) {
		t.Fatal("unbound turn", err)
	}
	// Concurrent retries, a lost acknowledgement and a new worker generation all return the same write.
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := g.projectTool(g.ctx, cmd); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	g.sql("UPDATE agent_group_work SET lease_until=now() WHERE id=$1", id)
	old := cmd
	cmd.ToolCall = claim()
	if _, err := g.projectTool(g.ctx, old); !errors.Is(err, access.ErrForbidden) {
		t.Fatal("stale generation", err)
	}
	data, err := g.projectTool(g.ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	var task struct{ ID string }
	if json.Unmarshal(data, &task) != nil || task.ID == "" {
		t.Fatal(string(data))
	}
	changed := cmd
	changed.Args = pmtools.Args{"title": json.RawMessage(`"changed"`)}
	if _, err := g.projectTool(g.ctx, changed); !errors.Is(err, errSlot) {
		t.Fatal("changed retry", err)
	}
	var count int
	if err := g.DB.QueryRow(g.ctx, "SELECT count(*) FROM project_tasks").Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate task", count, err)
	}
	progress := cmd
	progress.Slot = 2
	progress.Name = "task_add_progress"
	tid, _ := json.Marshal(task.ID)
	progress.Args = pmtools.Args{"task_id": tid, "expected_version": json.RawMessage("1"), "body": json.RawMessage(`"test progress"`)}
	for range 2 {
		if _, err := g.projectTool(g.ctx, progress); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.DB.QueryRow(g.ctx, "SELECT count(*) FROM project_task_activity WHERE kind='comment'").Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate progress", count, err)
	}
	g.sql(`INSERT INTO project_sources(tenant_key,app_id,project_id,kind,id,title,body) VALUES('tenant','app','north','document','secret','PRIVATE_ONLY_GATE','body');
 INSERT INTO project_source_readers VALUES('tenant','app','north','document','secret','alice')`)
	read := cmd
	read.Slot = 0
	read.Name = "file_get"
	read.Args = pmtools.Args{"file_ref": json.RawMessage(`"document:secret"`)}
	raw, err := g.projectTool(g.ctx, read)
	if err != nil || !strings.Contains(string(raw), "PRIVATE_ONLY_GATE") {
		t.Fatal("authorized source", err)
	}
	g.sql("DELETE FROM project_source_readers WHERE account_id='alice'")
	if _, err := g.projectTool(g.ctx, read); !errors.Is(err, access.ErrForbidden) {
		t.Fatal("revoked source", err)
	}
	g.sql("UPDATE project_members SET role='revoked' WHERE account_id='alice'")
	if _, err := g.projectTool(g.ctx, cmd); !errors.Is(err, access.ErrForbidden) {
		t.Fatal("revoked replay", err)
	}
}
func TestProjectToolHistoryGate(t *testing.T) {
	g := newGate(t)
	mux := http.NewServeMux()
	actor := identity.Principal{TenantKey: g.Tenant, AppID: g.App, AccountID: "alice", OpenID: "login-a"}
	g.RegisterWorkbench(mux, func(http.ResponseWriter, *http.Request, identity.SessionPolicy) (identity.Principal, bool) {
		return actor, true
	})
	request := func(method, key, body string) string {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/api/v1/projects/north/bot/messages", strings.NewReader(body))
		r.Header.Set("Idempotency-Key", key)
		mux.ServeHTTP(w, r)
		if w.Code != 200 && w.Code != 202 {
			t.Fatal(w.Code)
		}
		return w.Body.String()
	}
	g.sql(`INSERT INTO project_sources(tenant_key,app_id,project_id,kind,id,title,body) VALUES('tenant','app','north','document','secret','private file','PRIVATE_ONLY_GATE');
 INSERT INTO project_source_readers VALUES('tenant','app','north','document','secret','alice')`)
	revoke := false
	var sessions []string
	g.Execute = func(ctx context.Context, b dsh.Binding) (dsh.Result, error) {
		c := ToolCall{Binding: b, Turn: 1}
		sessions = append(sessions, b.Session)
		for _, bind := range []bool{true, false} {
			if _, err := g.useTurn(ctx, c, bind); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := g.projectTool(ctx, projectCall{ToolCall: c, Name: "file_get", Args: pmtools.Args{"file_ref": json.RawMessage(`"document:secret"`)}}); err != nil {
			t.Fatal(err)
		}
		if revoke {
			g.sql("DELETE FROM project_source_readers WHERE account_id='alice'")
		}
		return dsh.Result{Session: b.Session, Turn: 1, Answer: "PRIVATE_ONLY_GATE"}, nil
	}
	request("POST", "file-key-1", `{"text":"查文件"}`)
	g.step()
	if !strings.Contains(request("GET", "", ""), "PRIVATE_ONLY_GATE") {
		t.Fatal("authorized answer missing")
	}
	revoke = true
	request("POST", "file-key-2", `{"text":"再查文件"}`)
	g.step()
	history := request("GET", "", "")
	if strings.Contains(history, "PRIVATE_ONLY_GATE") || strings.Count(history, `"state":"suppressed"`) != 2 || len(sessions) != 2 || sessions[0] == sessions[1] {
		t.Fatal("private history or model transcript survived source revocation", history)
	}
}

type primaryReceiptChats struct {
	project.Chat
	visible map[string]bool
}

func (c primaryReceiptChats) ProjectGroup(_ context.Context, _ identity.Principal, id string) (chat.Group, error) {
	if !c.visible[id] {
		return chat.Group{}, chat.ErrAccessLost
	}
	return chat.Group{ID: id, Name: id}, nil
}

func (c primaryReceiptChats) ProjectGroupForMutation(ctx context.Context, _ pgx.Tx, actor identity.Principal, id string) (chat.Group, error) {
	return c.ProjectGroup(ctx, actor, id)
}

func TestProjectPrimaryReceiptAndStaleHistory(t *testing.T) {
	g := newGate(t)
	chats := primaryReceiptChats{visible: map[string]bool{"oc_a": true, "oc_b": true}}
	g.Projects = project.New(g.DB, g.Roles, nil, nil, chats, false)
	g.sql(`UPDATE project_members SET role='manager' WHERE account_id='alice';
		INSERT INTO feishu_bot_configs (tenant_key,app_id,credentials_generation) VALUES ('tenant','app',1);
		INSERT INTO feishu_chat_conversations (tenant_key,app_id,chat_id,kind)
		VALUES ('tenant','app','oc_a','group'), ('tenant','app','oc_b','group');
		INSERT INTO project_chat_links (tenant_key,app_id,chat_id,project_id)
		VALUES ('tenant','app','oc_a','north'), ('tenant','app','oc_b','north')`)
	mux := http.NewServeMux()
	g.RegisterWorkbench(mux, func(http.ResponseWriter, *http.Request, identity.SessionPolicy) (identity.Principal, bool) {
		return identity.Principal{TenantKey: g.Tenant, AppID: g.App, AccountID: "alice", OpenID: "login-a"}, true
	})
	request := func(method, key string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1/projects/north/bot/messages", strings.NewReader(`{"text":"项目主群"}`))
		req.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	var target string
	g.Execute = func(ctx context.Context, b dsh.Binding) (dsh.Result, error) {
		c := ToolCall{Binding: b, Turn: 1}
		for _, bind := range []bool{true, false} {
			if _, err := g.useTurn(ctx, c, bind); err != nil {
				t.Fatal(err)
			}
		}
		call := func(name string, slot int, args pmtools.Args) json.RawMessage {
			t.Helper()
			data, err := g.projectTool(ctx, projectCall{ToolCall: c, Name: name, Slot: slot, Args: args})
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			return data
		}
		var groups struct {
			PrimaryVersion json.RawMessage `json:"primary_version"`
			Items          []struct {
				ID      string          `json:"id"`
				Version json.RawMessage `json:"version"`
			} `json:"items"`
		}
		if err := json.Unmarshal(call("chat_list", 0, pmtools.Args{}), &groups); err != nil {
			t.Fatal(err)
		}
		if target != "" {
			groupID, _ := json.Marshal(target)
			candidate := pmtools.Args{"candidate_chat_id": groupID}
			call("chat_list", 0, candidate)
			for _, group := range groups.Items {
				if group.ID != target {
					continue
				}
				args := pmtools.Args{"chat_id": groupID, "expected_version": group.Version, "primary_version": groups.PrimaryVersion}
				first := call(setPrimaryTool, 1, args)
				retry := call(setPrimaryTool, 1, args)
				var compact bytes.Buffer
				if err := json.Compact(&compact, retry); err != nil || compact.String() != string(first) {
					t.Fatalf("receipt replay changed: %s and %s", first, retry)
				}
			}
			call("chat_list", 0, candidate)
			call("chat_list", 0, pmtools.Args{})
		}
		return dsh.Result{Session: b.Session, Turn: 1, Answer: "项目群状态已确认"}, nil
	}
	submit := func(key, groupID string) string {
		t.Helper()
		target = groupID
		w := request("POST", key)
		var accepted struct{ ID string }
		if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &accepted) != nil || accepted.ID == "" {
			t.Fatalf("submit: %d %s", w.Code, w.Body)
		}
		g.step()
		g.state(accepted.ID, "sent")
		return accepted.ID
	}
	lookup := func() map[string]chatMessage {
		t.Helper()
		w := request("GET", "")
		var history struct{ Items []chatMessage }
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &history) != nil {
			t.Fatalf("history: %d %s", w.Code, w.Body)
		}
		items := map[string]chatMessage{}
		for _, item := range history.Items {
			items[item.ID] = item
		}
		return items
	}
	query := submit("query-001", "")
	first := submit("primary-001", "oc_a")
	if history := lookup(); history[query].State != "stale" || history[query].Answer != "" ||
		history[query].PrimarySaved || history[first].State != "sent" || history[first].Answer == "" {
		t.Fatalf("successful write and stale query: %+v", history)
	}
	second := submit("primary-002", "oc_b")
	// Production recorded old writes as suppressed even though their state committed.
	g.sql("UPDATE agent_group_work SET state='suppressed',reason='',response_text=NULL WHERE id=$1", first)
	if history := lookup(); !history[first].PrimarySaved || history[first].Answer != "" ||
		history[second].State != "sent" || history[second].Answer == "" {
		t.Fatalf("historical write receipt: %+v", history)
	}
	chats.visible["oc_a"] = false
	if history := lookup(); history[first].PrimarySaved || history[second].Answer != "" || !history[second].PrimarySaved {
		t.Fatalf("revoked group visible through answer or receipt: %+v", history)
	}
	var groupID string
	var version int
	if err := g.DB.QueryRow(g.ctx, "SELECT chat_id,version FROM project_chat_primary WHERE project_id='north'").Scan(&groupID, &version); err != nil ||
		groupID != "oc_b" || version != 2 {
		t.Fatalf("unexpected final primary: %s version %d (%v)", groupID, version, err)
	}
}
func TestProjectToolAudienceImageGate(t *testing.T) {
	g, _, _, _ := openImageGate(t)
	who := "alice"
	mux := http.NewServeMux()
	g.RegisterWorkbench(mux, func(http.ResponseWriter, *http.Request, identity.SessionPolicy) (identity.Principal, bool) {
		open := "login-a"
		if who == "bob" {
			open = "login-b"
		}
		return identity.Principal{TenantKey: g.Tenant, AppID: g.App, AccountID: who, OpenID: open}, true
	})
	history := func() string {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/projects/north/bot/messages", nil))
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		return w.Body.String()
	}
	id := g.projectMention("group-tool", "PM_GATE_CREATE")
	g.step()
	g.state(id, "ready")
	g.step()
	g.state(id, "sent")
	var count int
	if err := g.DB.QueryRow(g.ctx, "SELECT count(*) FROM project_tasks WHERE project_id='north' AND title='组内创建的验收任务' AND owner_member_id='a' AND created_by='alice'").Scan(&count); err != nil || count != 1 {
		t.Fatal("actual runtime write", count, err)
	}
	if len(g.replies) != 1 || g.replies[0].Thread != "topic-tools" || g.replies[0].Message != "group-tool" || g.texts[0] != "项目 PM · 北辰项目\n请在本项目工作台的 Bot 对话中查看本次请求的结果。" {
		t.Fatal("public receipt", g.texts)
	}
	if !strings.Contains(history(), "已核对") {
		t.Fatal("private answer missing")
	}
	who = "bob"
	if strings.Contains(history(), "PM_GATE_CREATE") {
		t.Fatal("another member saw private result")
	}
}
