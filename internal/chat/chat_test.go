package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/directory"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
	"github.com/uniquechao/OmniRelay/migrations"
)

type fakeSource struct {
	groups      []Group
	members     map[string][]Member
	lost        map[string]bool
	unavailable map[string]bool
	queries     []HistoryQuery
	history     func(HistoryQuery) (Page[orchestrator.ChatMessage], error)
}

func (f *fakeSource) Groups(context.Context, string) (Page[Group], error) {
	return Page[Group]{Items: f.groups}, nil
}

func (f *fakeSource) Group(_ context.Context, id string) (Group, error) {
	if f.lost[id] {
		return Group{}, ErrAccessLost
	}
	return Group{ID: id}, nil
}

func (f *fakeSource) Members(_ context.Context, id, _ string) (Page[Member], error) {
	return Page[Member]{Items: f.members[id]}, nil
}

func (f *fakeSource) History(_ context.Context, q HistoryQuery) (Page[orchestrator.ChatMessage], error) {
	if f.unavailable[q.ChatID] {
		return Page[orchestrator.ChatMessage]{}, ErrUnavailable
	}
	if f.lost[q.ChatID] {
		return Page[orchestrator.ChatMessage]{}, ErrAccessLost
	}
	if q.Limit == 1 {
		return Page[orchestrator.ChatMessage]{}, nil
	}
	f.queries = append(f.queries, q)
	return f.history(q)
}

func (*fakeSource) Bot(context.Context) (Member, error) {
	return Member{OpenID: "ou_bot", Name: "Bot"}, nil
}

func testChat(t *testing.T, source *fakeSource) (*Service, context.Context) {
	t.Helper()
	dsn := os.Getenv("OMNIRELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	root, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	var schema string
	if err := root.QueryRow(ctx, `SELECT 'chat_' || replace(gen_random_uuid()::text,'-','')`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = root.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO feishu_bot_configs (tenant_key,app_id,credentials_generation,enabled)
		VALUES ('tenant_chat','cli_chat',1,true);
		INSERT INTO accounts (id,tenant_key,app_id,open_id) VALUES
			('alice','tenant_chat','cli_chat','ou_alice'),('bob','tenant_chat','cli_chat','ou_bob'),
			('admin','tenant_chat','cli_chat','ou_admin')`); err != nil {
		t.Fatal(err)
	}
	roles := access.New(pool, "tenant_chat", "cli_chat")
	if err := roles.Bootstrap(ctx, "ou_admin"); err != nil {
		t.Fatal(err)
	}
	people, err := directory.New(pool, directory.Config{TenantKey: "tenant_chat", AppID: "cli_chat", Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	return New(pool, "tenant_chat", "cli_chat", 1, source, people, roles.Check, ReceiptSources{}), ctx
}

func TestUnboundFileGroupRequiresCurrentMembership(t *testing.T) {
	source := &fakeSource{members: map[string][]Member{"oc_group": {{OpenID: "ou_alice"}}}}
	service, ctx := testChat(t, source)
	if _, err := service.pool.Exec(ctx, `INSERT INTO feishu_chat_conversations
		(tenant_key,app_id,chat_id,kind,peer_open_id)
		VALUES ('tenant_chat','cli_chat','oc_group','group',''),
		       ('tenant_chat','cli_chat','oc_private','p2p','ou_alice')`); err != nil {
		t.Fatal(err)
	}
	alice := identity.Principal{TenantKey: "tenant_chat", AppID: "cli_chat", AccountID: "alice", OpenID: "ou_alice"}
	admin := identity.Principal{TenantKey: "tenant_chat", AppID: "cli_chat", AccountID: "admin", OpenID: "ou_admin"}
	for _, test := range []struct {
		reader identity.Principal
		chat   string
		want   bool
	}{{alice, "oc_group", true}, {admin, "oc_group", false}, {alice, "oc_private", false}} {
		if allowed, err := service.CanReadFileGroup(ctx, test.reader, "", test.chat); err != nil || allowed != test.want {
			t.Fatalf("%s / %s: allowed=%v err=%v", test.reader.AccountID, test.chat, allowed, err)
		}
	}
	source.members["oc_group"] = nil
	if allowed, err := service.CanReadFileGroup(ctx, alice, "", "oc_group"); err != nil || allowed {
		t.Fatalf("membership was revoked: allowed=%v err=%v", allowed, err)
	}
}

func exampleMessage(chatID, id, text string, seconds int) orchestrator.ChatMessage {
	return orchestrator.ChatMessage{ID: id, ChatID: chatID, Type: "text", Body: `{"text":"` + text + `"}`,
		SenderID: "ou_alice", SenderName: "林晓", SenderKind: "user", SentAt: time.Date(2024, 1, 1, 0, 0, seconds, 0, time.UTC)}
}

func applyEvent(t *testing.T, ctx context.Context, s *Service, event orchestrator.ArchiveEvent) {
	t.Helper()
	if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return s.AcceptEvent(ctx, tx, event) }); err != nil {
		t.Fatal(err)
	}
}

func TestChatHistoryResumesAndKeepsRecall(t *testing.T) {
	src := &fakeSource{groups: []Group{{"oc_group", "项目群"}}}
	s, ctx := testChat(t, src)
	root := exampleMessage("oc_group", "om_root", "话题", 1)
	root.ThreadID = "omt_topic"
	older := exampleMessage("oc_group", "om_old", "应撤回的正文", 2)
	reply := exampleMessage("oc_group", "om_reply", "非 @ 回复", 3)
	reply.ThreadID = root.ThreadID
	fail := true
	src.history = func(q HistoryQuery) (Page[orchestrator.ChatMessage], error) {
		if q.ThreadID == root.ThreadID {
			if q.Start != nil || q.End != nil {
				t.Fatal("thread request used a range")
			}
			return Page[orchestrator.ChatMessage]{Items: []orchestrator.ChatMessage{root, reply}}, nil
		}
		if q.Cursor == "" {
			return Page[orchestrator.ChatMessage]{Items: []orchestrator.ChatMessage{root}, Next: "page-2"}, nil
		}
		if fail {
			fail = false
			return Page[orchestrator.ChatMessage]{}, ErrUnavailable
		}
		return Page[orchestrator.ChatMessage]{Items: []orchestrator.ChatMessage{older}}, nil
	}
	page := func() {
		t.Helper()
		if worked, err := s.runPage(ctx); err != nil || !worked {
			t.Fatalf("worker: %v %v", worked, err)
		}
	}
	page() // Discover the group without a live event.
	page() // Persist the first page and discover the topic.
	page() // Root page may run before or after the newly discovered topic.
	page()
	var state, token string
	err := s.pool.QueryRow(ctx, `SELECT state,page_token FROM feishu_chat_sync WHERE chat_id='oc_group' AND thread_id=''`).Scan(&state, &token)
	if err != nil || state != "failed" || token != "page-2" {
		t.Fatalf("cursor not preserved after provider failure: %q %q %v", state, token, err)
	}

	recall := older
	recall.Deleted = true
	recall.Body = ""
	recall.Type = ""
	// The recall arrives before the second page and a later delivery repeats it.
	applyEvent(t, ctx, s, orchestrator.ArchiveEvent{ChatID: "oc_group", Message: &recall})
	if _, err := s.pool.Exec(ctx, `UPDATE feishu_chat_sync SET next_at=now() WHERE chat_id='oc_group' AND thread_id=''`); err != nil {
		t.Fatal(err)
	}
	page()
	applyEvent(t, ctx, s, orchestrator.ArchiveEvent{ChatID: "oc_group", Kind: orchestrator.ChatGroup, Message: &older})
	applyEvent(t, ctx, s, orchestrator.ArchiveEvent{ChatID: "oc_group", Kind: orchestrator.ChatGroup, Message: &root})
	var count, commands int
	var body, search string
	var deleted bool
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM feishu_chat_messages`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT deleted,body,search_text FROM feishu_chat_messages WHERE message_id='om_old'`).Scan(&deleted, &body, &search); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM feishu_inbound_work`).Scan(&commands); err != nil {
		t.Fatal(err)
	}
	if count != 3 || !deleted || body != "" || search != "" || commands != 0 {
		t.Fatalf("unexpected archive: count=%d deleted=%v body=%q search=%q commands=%d", count, deleted, body, search, commands)
	}
	var rootQueries []HistoryQuery
	for _, q := range src.queries {
		if q.ThreadID == "" {
			rootQueries = append(rootQueries, q)
		}
	}
	if len(rootQueries) != 3 || rootQueries[0].Start != nil || rootQueries[0].Cursor != "" ||
		rootQueries[1].Cursor != "page-2" || rootQueries[2].Cursor != "page-2" ||
		!rootQueries[0].End.Equal(*rootQueries[2].End) {
		t.Fatalf("unexpected backfill requests: %#v", rootQueries)
	}
}

func TestChatPrivateHistoryUsesVerifiedPeer(t *testing.T) {
	src := &fakeSource{}
	s, ctx := testChat(t, src)
	peer := exampleMessage("oc_private", "om_peer", "历史私聊", 1)
	bot := exampleMessage("oc_private", "om_bot", "Bot 回复", 2)
	bot.SenderID, bot.SenderKind, bot.SenderName = "ou_bot", "app", "Bot"
	src.history = func(q HistoryQuery) (Page[orchestrator.ChatMessage], error) {
		if q.ChatID != "oc_private" || q.ThreadID != "" || q.Start != nil {
			t.Fatalf("unexpected private query %#v", q)
		}
		return Page[orchestrator.ChatMessage]{Items: []orchestrator.ChatMessage{peer, bot}}, nil
	}
	applyEvent(t, ctx, s, orchestrator.ArchiveEvent{ChatID: "oc_private", PeerOpenID: "ou_alice", Kind: orchestrator.ChatPrivate})
	for range 2 {
		if worked, err := s.runPage(ctx); err != nil || !worked {
			t.Fatalf("worker: %v %v", worked, err)
		}
	}
	var name, owner string
	var count int
	err := s.pool.QueryRow(ctx, `SELECT c.name,c.peer_open_id,count(m.message_id)
		FROM feishu_chat_conversations c JOIN feishu_chat_messages m USING (tenant_key,app_id,chat_id)
		WHERE c.chat_id='oc_private' GROUP BY c.name,c.peer_open_id`).Scan(&name, &owner, &count)
	if err != nil || name != "林晓" || owner != "ou_alice" || count != 2 {
		t.Fatalf("private history: name=%q owner=%q count=%d err=%v", name, owner, count, err)
	}
}

func TestChatReadAuthorizationAndPagination(t *testing.T) {
	src := &fakeSource{
		members:     map[string][]Member{"oc_group": {{"ou_alice", "林晓"}}, "oc_other": {{"ou_bob", "赵可"}}},
		lost:        map[string]bool{},
		unavailable: map[string]bool{},
	}
	s, ctx := testChat(t, src)
	for i, record := range []struct {
		id, peer, chatID string
		kind             orchestrator.ChatKind
	}{
		{"om_a", "", "oc_group", orchestrator.ChatGroup},
		{"om_b", "", "oc_group", orchestrator.ChatGroup},
		{"om_c", "", "oc_other", orchestrator.ChatGroup},
		{"om_d", "ou_alice", "oc_private", orchestrator.ChatPrivate},
		{"om_e", "ou_bob", "oc_bob", orchestrator.ChatPrivate},
	} {
		msg := exampleMessage(record.chatID, record.id, "会议纪要", i+1)
		applyEvent(t, ctx, s, orchestrator.ArchiveEvent{ChatID: record.chatID, PeerOpenID: record.peer, Kind: record.kind, Message: &msg})
	}
	mux := http.NewServeMux()
	s.Register(mux, func(_ http.ResponseWriter, r *http.Request, policy identity.SessionPolicy) (identity.Principal, bool) {
		if r.Method == http.MethodPost && policy != identity.RefreshSessionWithCSRF {
			t.Fatal("refresh did not use CSRF policy")
		}
		user := r.Header.Get("test-user")
		return identity.Principal{AccountID: user, OpenID: "ou_" + user, TenantKey: "tenant_chat", AppID: "cli_chat"}, true
	})
	request := func(user, method, path string, status int) map[string]json.RawMessage {
		t.Helper()
		req := httptest.NewRequest(method, route+path, nil)
		req.Header.Set("test-user", user)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != status {
			t.Fatalf("%s %s %s: got %d, want %d: %s", user, method, path, w.Code, status, w.Body)
		}
		var data map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		return data
	}
	expect := func(data map[string]json.RawMessage, key, value string) {
		t.Helper()
		if got := string(data[key]); got != value {
			t.Fatalf("%s = %s, want %s", key, got, value)
		}
	}
	expect(request("alice", "GET", "", 200), "total", "2")
	expect(request("admin", "GET", "?kind=p2p", 200), "total", "2")
	expect(request("admin", "GET", "", 200), "total", "4")
	expect(request("alice", "GET", "/search?q=会议", 200), "total", "3")
	expect(request("alice", "GET", "/search?q=会议&kind=p2p", 200), "total", "1")
	expect(request("alice", "GET", "/search?q=会议&kind=group", 200), "total", "2")
	count := request("alice", "GET", "/messages/count", 200)
	expect(count, "total", "3")
	if len(count) != 1 {
		t.Fatalf("count returned other fields: %v", count)
	}
	expect(request("admin", "GET", "/messages/count", 200), "total", "5")
	direct := request("alice", "GET", "/oc_private", 200)
	expect(direct, "total", "1")
	members := request("alice", "GET", "/oc_private/members", 200)
	var rows []conversationItem
	if err := json.Unmarshal(direct["items"], &rows); err != nil || len(rows) != 1 ||
		rows[0].Person == nil || rows[0].Person.PersonKey == "" ||
		!strings.Contains(string(members["items"]), rows[0].Person.PersonKey) ||
		strings.Contains(string(direct["items"]), "ou_alice") {
		t.Fatalf("inconsistent private peer projection: %s / %s", direct["items"], members["items"])
	}
	expect(request("alice", "GET", "/oc_other", 404), "error", `"chat_access_lost"`)
	expect(request("bob", "GET", "/search?q=会议", 200), "total", "2")
	expect(request("alice", "GET", "/oc_bob/messages", 404), "error", `"chat_access_lost"`)
	expect(request("alice", "GET", "/oc_group/members", 200), "total", "1")
	expect(request("alice", "GET", "/oc_private/members", 200), "total", "2")
	expect(request("alice", "GET", "/sync", 200), "total", "2")
	if _, err := s.pool.Exec(ctx, `UPDATE feishu_chat_sync SET state='failed' WHERE chat_id='oc_group'`); err != nil {
		t.Fatal(err)
	}
	if status := request("alice", "GET", "/oc_group/sync", 200); !strings.Contains(string(status["items"]), `"state":"failed"`) {
		t.Fatalf("temporary sync failure missing: %s", status["items"])
	}
	src.unavailable["oc_group"] = true
	expect(request("alice", "GET", "/oc_group/messages", 503), "error", `"chat_unavailable"`)
	expect(request("alice", "GET", "/messages/count", 503), "error", `"chat_unavailable"`)
	src.unavailable["oc_group"] = false
	expect(request("alice", "POST", "/oc_group/sync", 202), "total", "1")
	first := request("alice", "GET", "/oc_group/messages?limit=1", 200)
	expect(first, "total", "2")
	var next string
	if err := json.Unmarshal(first["next_cursor"], &next); err != nil || next == "" {
		t.Fatalf("invalid next cursor: %s %v", first["next_cursor"], err)
	}
	second := request("alice", "GET", "/oc_group/messages?limit=1&cursor="+next, 200)
	if !strings.Contains(string(first["items"]), `"id":"om_b"`) || !strings.Contains(string(second["items"]), `"id":"om_a"`) {
		t.Fatalf("unstable history: %s then %s", first["items"], second["items"])
	}
	expect(request("alice", "GET", "/oc_group/messages/om_a/context", 200), "anchor_id", `"om_a"`)
	expect(request("alice", "GET", "/oc_group/messages/om_d/context", 404), "error", `"message_not_found"`)
	deleted := exampleMessage("oc_group", "om_a", "", 1)
	deleted.Deleted = true
	applyEvent(t, ctx, s, orchestrator.ArchiveEvent{ChatID: "oc_group", Message: &deleted})
	expect(request("alice", "GET", "/oc_group/messages/om_a/context", 410), "error", `"message_deleted"`)
	expect(request("alice", "GET", "/oc_group/search?q=会议", 200), "total", "1")
	expect(request("alice", "GET", "/messages/count", 200), "total", "2")
	src.members["oc_group"] = nil
	expect(request("alice", "GET", "/oc_group", 404), "error", `"chat_access_lost"`)
	expect(request("alice", "GET", "/oc_group/messages/om_a/context", 404), "error", `"chat_access_lost"`)
	expect(request("alice", "GET", "/oc_group/sync", 404), "error", `"chat_access_lost"`)
	expect(request("alice", "GET", "/search?q=会议", 200), "total", "1")
	expect(request("alice", "GET", "/messages/count", 200), "total", "1")
	expect(request("admin", "GET", "/oc_group/messages/om_b/context", 200), "anchor_id", `"om_b"`)
	src.lost["oc_group"] = true
	expect(request("admin", "GET", "/oc_group/messages/om_b/context", 404), "error", `"chat_access_lost"`)
	expect(request("admin", "GET", "", 200), "total", "3")
}

func TestProjectChatReaderScope(t *testing.T) {
	src := &fakeSource{members: map[string][]Member{"oc_g1": {{"ou_alice", "甲"}}, "oc_g2": {{"ou_bob", "乙"}}}}
	s, ctx := testChat(t, src)
	for _, id := range []string{"oc_g1", "oc_g2"} {
		msg := exampleMessage(id, "m"+id, "项目消息", 1)
		applyEvent(t, ctx, s, orchestrator.ArchiveEvent{ChatID: id, Kind: orchestrator.ChatGroup, Message: &msg})
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO project_chat_links(tenant_key,app_id,project_id,chat_id) VALUES ('tenant_chat','cli_chat','p1','oc_g1'),('tenant_chat','cli_chat','p1','oc_g2')`); err != nil {
		t.Fatal(err)
	}
	s.check = func(_ context.Context, _ identity.Principal, action, project string) (bool, error) {
		return action == "chat.read_all" || (action == "project.read" && project == "p1"), nil
	}
	mux := http.NewServeMux()
	s.Register(mux, func(http.ResponseWriter, *http.Request, identity.SessionPolicy) (identity.Principal, bool) {
		return identity.Principal{AccountID: "alice", OpenID: "ou_alice", TenantKey: "tenant_chat", AppID: "cli_chat"}, true
	})
	get := func(path string, status int) string {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/projects/"+path, nil))
		if w.Code != status {
			t.Fatalf("请求 %s 得到 %d %s", path, w.Code, w.Body)
		}
		return w.Body.String()
	}
	result := get("p1/chat-reader", 200)
	if !strings.Contains(result, "oc_g1") || strings.Contains(result, "oc_g2") {
		t.Fatal("项目群可见范围错误", result)
	}
	get("p1/chat-reader/oc_g2/messages", 404)
	get("p2/chat-reader/oc_g1/messages", 404)
	get("p1/chat-reader/oc_g1/send-capabilities", 200)
	get("p1/chat-reader/oc_g2/send-capabilities", 404)
	get("p2/chat-reader/oc_g1/send-capabilities", 404)
	get("p1/chat-reader/oc_g1/messages", 200)
	s.ConfigureDelivery(deliveryFixture{fakeSource: src, sent: func(target, kind, body, uuid string) DeliveryResult {
		if target != "oc_g1" {
			t.Fatalf("项目消息发送到了其他群 %s", target)
		}
		return DeliveryResult{State: "success", MessageID: "om_scoped"}
	}}, true)
	req := httptest.NewRequest("POST", "/api/v1/projects/p1/chat-reader/oc_g1/messages",
		strings.NewReader(`{"operation_key":"project-send","text":"项目消息"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "om_scoped") {
		t.Fatalf("项目发送未确认 %d %s", w.Code, w.Body)
	}
	for _, prefix := range []string{"/api/v1/feishu/chats", "/api/v1/projects/p1/chat-reader"} {
		receipt := httptest.NewRecorder()
		mux.ServeHTTP(receipt, httptest.NewRequest("GET", prefix+"/oc_g1/deliveries/project-send", nil))
		if receipt.Code != http.StatusOK || !strings.Contains(receipt.Body.String(), "om_scoped") {
			t.Fatalf("发送回执与项目发送结果不一致 %s %d %s", prefix, receipt.Code, receipt.Body)
		}
	}
	if _, err := s.pool.Exec(ctx, `UPDATE project_chat_links SET project_id='p2' WHERE chat_id='oc_g1'`); err != nil {
		t.Fatal(err)
	}
	get("p1/chat-reader/oc_g1/messages", 404)
	get("p1/chat-reader/oc_g1/send-capabilities", 404)
	for _, suffix := range []string{"attachments", "messages"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/projects/p1/chat-reader/oc_g1/"+suffix, nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("群已移出，%s 应拒绝，得到 %d", suffix, w.Code)
		}
	}
}
