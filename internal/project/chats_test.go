package project

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
)

type assignmentSource struct {
	chat.Source
	denied atomic.Bool
}

func (s *assignmentSource) Members(context.Context, string, string) (chat.Page[chat.Member], error) {
	if s.denied.Load() {
		return chat.Page[chat.Member]{}, chat.ErrAccessLost
	}
	return chat.Page[chat.Member]{Items: []chat.Member{{OpenID: testActor.OpenID}}}, nil
}

func (s *assignmentSource) Group(_ context.Context, id string) (chat.Group, error) {
	return chat.Group{ID: id, Name: id}, nil
}

func (s *assignmentSource) History(context.Context, chat.HistoryQuery) (chat.Page[orchestrator.ChatMessage], error) {
	return chat.Page[orchestrator.ChatMessage]{}, nil
}

func TestProjectChatAssignmentTransactions(t *testing.T) {
	pool := testProjectPool(t, "d1_project_assignments")
	ctx := t.Context()
	exec := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO accounts (id,tenant_key,app_id,open_id,active)
		VALUES ('relay-user','tenant','app','ou_reader',true);
		INSERT INTO feishu_bot_configs (tenant_key,app_id,credentials_generation,enabled)
		VALUES ('tenant','app',1,true),('other','app',1,true);
		INSERT INTO feishu_chat_conversations (tenant_key,app_id,chat_id,kind,peer_open_id)
		VALUES ('tenant','app','oc_one','group',''),('tenant','app','oc_two','group',''),
			('tenant','app','oc_private','p2p','ou_reader'),('other','app','oc_one','group','');
		INSERT INTO project_chat_links (tenant_key,app_id,chat_id,project_id)
		VALUES ('other','app','oc_one','allowed');
		INSERT INTO project_chat_primary (tenant_key,app_id,project_id,chat_id,version)
		VALUES ('other','app','allowed','oc_one',1)`)
	grid, fixture := testGrid(t)
	roles := access.New(pool, testActor.TenantKey, testActor.AppID)
	roles.ProjectAccess = grid
	source := &assignmentSource{}
	chats := chat.New(pool, testActor.TenantKey, testActor.AppID, 1, source, nil, roles.Check, chat.ReceiptSources{})
	service := New(grid, pool, roles, nil, nil, chats, false)
	mux := http.NewServeMux()
	auth := func(_ http.ResponseWriter, r *http.Request, policy identity.SessionPolicy) (identity.Principal, bool) {
		if r.Method == http.MethodPut && policy != identity.RefreshSessionWithCSRF {
			t.Error("mutation requires CSRF")
		}
		return testActor, true
	}
	service.Register(mux, auth)
	chats.Register(mux, auth)
	call := func(method, path, payload string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(payload))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	expect := func(w *httptest.ResponseRecorder, status int) string {
		t.Helper()
		if w.Code != status {
			t.Fatalf("status=%d want=%d: %s", w.Code, status, w.Body)
		}
		return w.Body.String()
	}
	move := func(group, project string, version int64) *httptest.ResponseRecorder {
		return call("PUT", "/feishu/chats/"+group+"/project", fmt.Sprintf(`{"project_id":%q,"version":%d}`, project, version))
	}
	primary := func(group string, version, primaryVersion int64) *httptest.ResponseRecorder {
		return call("PUT", "/projects/allowed/chats/primary", fmt.Sprintf(
			`{"chat_id":%q,"assignment_version":%d,"primary_version":%d}`, group, version, primaryVersion))
	}
	list := func(id string) projectGroups {
		t.Helper()
		var result projectGroups
		if err := json.Unmarshal([]byte(expect(call("GET", "/projects/"+id+"/chats", ""), 200)), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	global := func(group string) *chat.ProjectAssignment {
		t.Helper()
		var result struct {
			Items []struct {
				Assignment *chat.ProjectAssignment `json:"project_assignment"`
			} `json:"items"`
		}
		body := expect(call("GET", "/feishu/chats/"+group, ""), 200)
		if err := json.Unmarshal([]byte(body), &result); err != nil || len(result.Items) != 1 {
			t.Fatalf("global chat: %s (%v)", body, err)
		}
		return result.Items[0].Assignment
	}
	expect(move("oc_private", "allowed", 0), 404)
	expect(move("oc_one", "allowed", 0), 200)
	expect(move("oc_two", "allowed", 0), 200)

	// Competing candidates use the same observed project revision.
	results := make(chan *httptest.ResponseRecorder, 2)
	go func() { results <- primary("oc_one", 1, 0) }()
	go func() { results <- primary("oc_two", 1, 0) }()
	first, second := <-results, <-results
	if first.Code != 200 {
		first, second = second, first
	}
	expect(first, 200)
	expect(second, 409)
	snapshot := list("allowed")
	if snapshot.Total != 2 || snapshot.PrimaryVersion != 1 || snapshot.Items[0].Primary == snapshot.Items[1].Primary {
		t.Fatalf("expected one primary: %+v", snapshot)
	}
	old, replacement := snapshot.Items[0], snapshot.Items[1]
	if !old.Primary {
		old, replacement = replacement, old
	}
	expect(primary(replacement.ID, replacement.Version, 1), 200)
	if previous, next := global(old.ID), global(replacement.ID); previous == nil || previous.Primary ||
		next == nil || !next.Primary || next.Version != replacement.Version+1 {
		t.Fatalf("global primary mismatch: prior=%+v next=%+v", previous, next)
	}
	version := replacement.Version + 1

	// The transaction must preserve the existing link, primary and plan on failure.
	exec(fmt.Sprintf(`INSERT INTO project_relay_settings VALUES ('tenant','app','allowed','%s','UTC','18:00',true,true,1)`, replacement.ID))
	for _, readOnlyID := range []string{"allowed", "second"} {
		fixture.readOnlyProject.Store(&readOnlyID)
		list(readOnlyID)
		expect(call("GET", "/projects/allowed/chat-reader/"+replacement.ID, ""), 200)
		expect(move(replacement.ID, "second", version), 404)
		if readOnlyID == "allowed" {
			expect(primary(old.ID, global(old.ID).Version, 2), 404)
			expect(move(replacement.ID, "", version), 404)
		}
	}
	fixture.readOnlyProject.Store(nil)
	fixture.secondDenied.Store(true)
	expect(move(replacement.ID, "second", version), 404)
	fixture.secondDenied.Store(false)
	fixture.projectLost.Store(true)
	expect(call("GET", "/projects/allowed/chat-reader/"+replacement.ID, ""), 404)
	expect(move(replacement.ID, "second", version), 404)
	if got := global(replacement.ID); got != nil {
		t.Fatalf("revoked project metadata leaked: %+v", got)
	}
	fixture.projectLost.Store(false)
	source.denied.Store(true)
	expect(move(replacement.ID, "second", version), 404)
	expect(primary(replacement.ID, version, 2), 404)
	source.denied.Store(false)
	expect(move(replacement.ID, "second", version-1), 409)
	exec(`ALTER TABLE audit_events ADD CONSTRAINT d1_reject_move CHECK (
		action <> 'project.chat.assign' OR details->>'project_id' <> 'second')`)
	expect(move(replacement.ID, "second", version), 503)
	afterFailure := list("allowed")
	cfg, err := service.settings(ctx, testActor, "allowed")
	if afterFailure.Total != 2 || afterFailure.PrimaryVersion != 2 || !cfg.Enabled || cfg.Target != replacement.ID || err != nil {
		t.Fatalf("failed move wrote state: groups=%+v plan=%+v error=%v", afterFailure, cfg, err)
	}
	exec(`ALTER TABLE audit_events DROP CONSTRAINT d1_reject_move`)

	// A move and a clear use the same observed group revision.
	go func() { results <- move(replacement.ID, "second", version) }()
	go func() { results <- move(replacement.ID, "", version) }()
	first, second = <-results, <-results
	if first.Code != 200 {
		first, second = second, first
	}
	expect(first, 200)
	expect(second, 409)
	state := global(replacement.ID)
	if state == nil || state.Primary || state.Version != version+1 {
		t.Fatalf("unexpected assignment: %+v", state)
	}
	if state.ProjectID == "" {
		expect(move(replacement.ID, "second", state.Version), 200)
		state = global(replacement.ID)
	}
	if moved, left := list("second"), list("allowed"); moved.Total != 1 || moved.Items[0].ID != replacement.ID ||
		moved.Items[0].ProjectAssignment != *state || left.Total != 1 || left.Items[0].Primary || left.PrimaryVersion != 3 {
		t.Fatalf("move diverged: destination=%+v origin=%+v global=%+v", moved, left, state)
	}
	cfg, err = service.settings(ctx, testActor, "allowed")
	if err != nil || cfg.Target != "" || cfg.Enabled || cfg.PublicCount || cfg.Version != 2 {
		t.Fatalf("departed target still configured: %+v (%v)", cfg, err)
	}

	fixture.secondRenamed.Store(true)
	catalogue := expect(call("GET", "/projects", ""), 200)
	if !strings.Contains(catalogue, `"id":"second","name":"已更名"`) || *global(replacement.ID) != *state {
		t.Fatalf("rename changed association: %s", catalogue)
	}
	expect(move(replacement.ID, "", state.Version), 200)
	cleared := global(replacement.ID)
	if cleared == nil || cleared.ProjectID != "" || cleared.Primary || cleared.Version != state.Version+1 || list("second").Total != 0 {
		t.Fatalf("clear diverged: %+v", cleared)
	}
	var clearsPrimary, otherPrimary bool
	err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM audit_events WHERE resource_id=$1
			AND details->>'previous_project_id'='allowed' AND details->>'cleared_primary'='true'),
		(SELECT primary_group FROM project_chat_assignments WHERE (tenant_key,app_id,chat_id)=('other','app','oc_one'))`,
		replacement.ID).Scan(&clearsPrimary, &otherPrimary)
	if err != nil || !clearsPrimary || !otherPrimary {
		t.Fatalf("missing audit or cross-scope mutation: %v %v (%v)", clearsPrimary, otherPrimary, err)
	}
}
