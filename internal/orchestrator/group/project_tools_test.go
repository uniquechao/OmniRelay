package group

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/uniquechao/OmniRelay/internal/agent/dsh"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/agent/pmtools"
	"github.com/uniquechao/OmniRelay/internal/identity"
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

// The callback path must deduplicate newly registered writes as well as the task domain.
func TestProjectToolCRUDRecoveryGate(t *testing.T) {
	g := newGate(t)
	g.sql("UPDATE project_members SET role='manager' WHERE account_id='alice'")
	g.projectMention("crud-recovery", "维护成员职责并清理重复任务")
	work, err := g.claim(g.ctx)
	if err != nil {
		t.Fatal(err)
	}
	binding := ToolCall{Binding: work.Binding, Turn: 9}
	if _, err = g.useTurn(g.ctx, binding, true); err != nil {
		t.Fatal(err)
	}
	call := func(name string, slot int, input string) json.RawMessage {
		t.Helper()
		var args pmtools.Args
		if err := json.Unmarshal([]byte(input), &args); err != nil {
			t.Fatal(err)
		}
		var first json.RawMessage
		for range 2 {
			result, err := g.projectTool(g.ctx, projectCall{ToolCall: binding, Name: name, Slot: slot, Args: args})
			if err != nil {
				t.Fatal(name, err)
			}
			if first != nil {
				var before, after any
				if json.Unmarshal(first, &before) != nil || json.Unmarshal(result, &after) != nil || !reflect.DeepEqual(before, after) {
					t.Fatal("receipt changed", name)
				}
			}
			first = result
		}
		return first
	}
	call("member_update", 1, `{"account_id":"bob","expected_version":1,"role_title":"验收负责人","responsibility":"质量验收"}`)
	var version int
	if err = g.DB.QueryRow(g.ctx, "SELECT version FROM project_members WHERE account_id='bob'").Scan(&version); err != nil || version != 2 {
		t.Fatal("duplicate member mutation", version, err)
	}
	created := call("task_create", 2, `{"title":"重复任务样本","due_at":"2026-12-01","acceptance":"接口验收"}`)
	var task struct{ ID string }
	if err = json.Unmarshal(created, &task); err != nil || task.ID == "" {
		t.Fatal(string(created), err)
	}
	raw, _ := json.Marshal(task.ID)
	call("task_delete", 3, `{"task_id":`+string(raw)+`,"expected_version":1,"change_reason":"重复任务清理"}`)
	var events int
	if err = g.DB.QueryRow(g.ctx, "SELECT count(*) FROM project_task_activity WHERE task_id=$1 AND action='task.deleted'", task.ID).Scan(&events); err != nil || events != 1 {
		t.Fatal("duplicate deletion", events, err)
	}
	g.sql("UPDATE project_members SET role='reader' WHERE account_id='alice'")
	_, err = g.projectTool(g.ctx, projectCall{ToolCall: binding, Name: "task_delete", Slot: 3, Args: pmtools.Args{"task_id": raw, "expected_version": json.RawMessage("1"), "change_reason": json.RawMessage(`"重复任务清理"`)}})
	if !errors.Is(err, access.ErrForbidden) {
		t.Fatal("revoked delete replay", err)
	}
}
