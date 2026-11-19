package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/uniquechao/OmniRelay/internal/identity"
)

type deliveryFixture struct {
	*fakeSource
	upload  func() (string, error)
	sent    func(target, kind, body, uuid string) DeliveryResult
	confirm func(target, kind, bot, id string) bool
}

func (f deliveryFixture) BotInGroup(_ context.Context, target string) error {
	if f.lost[target] {
		return ErrAccessLost
	}
	return nil
}

func (f deliveryFixture) Upload(context.Context, string, string, []byte) (string, error) {
	return f.upload()
}

func (f deliveryFixture) Deliver(_ context.Context, target, _, kind, body, uuid string) DeliveryResult {
	return f.sent(target, kind, body, uuid)
}

func (f deliveryFixture) ConfirmDelivery(_ context.Context, target, kind, bot, id string) bool {
	return f.confirm != nil && f.confirm(target, kind, bot, id)
}

func TestDeliveryContent(t *testing.T) {
	content := DeliveryContent{Paragraphs: [][]DeliverySpan{
		{{Text: "粗体 😀", Styles: []string{"bold"}}, {Mention: "ou_bob"}},
		{{Text: "第二行"}},
	}, Attachments: []string{"attachment"}}
	parts, mentions, err := content.parts()
	if err != nil || len(parts) != 2 || len(mentions) != 1 || mentions[0] != "ou_bob" ||
		parts[0].kind != "post" || !strings.Contains(parts[0].content, `"style":["bold"]`) ||
		!strings.Contains(parts[0].content, `"tag":"at","user_id":"ou_bob"`) {
		t.Fatalf("rich text projection: %#v %#v %v", parts, mentions, err)
	}
	parts, mentions, err = (DeliveryContent{Text: "测试\n<at user_id=\"all\">全体</at>"}).parts()
	if err != nil || len(mentions) != 0 || strings.Contains(parts[0].content, `"tag":"at"`) {
		t.Fatalf("literal text produced a mention: %#v %#v %v", parts, mentions, err)
	}
	for _, invalid := range []DeliveryContent{
		{Text: "  \n"},
		{Attachments: []string{"duplicate", "duplicate"}},
		{Paragraphs: [][]DeliverySpan{{{Mention: "all"}}}},
		{Paragraphs: [][]DeliverySpan{{{Styles: []string{"unsupported"}, Text: "示例"}}}},
	} {
		if _, _, err := invalid.parts(); !errors.Is(err, errDeliveryInput) {
			t.Fatalf("accepted invalid content: %#v", invalid)
		}
	}
}

func TestDeliveryLedger(t *testing.T) {
	source := &fakeSource{members: map[string][]Member{
		"oc_group": {{"ou_alice", "Alice"}, {"ou_bob", "Bob"}},
		"oc_other": {{"ou_alice", "Alice"}},
	}}
	s, ctx := testChat(t, source)
	_, err := s.pool.Exec(ctx, `INSERT INTO feishu_chat_conversations
		(tenant_key,app_id,chat_id,kind,peer_open_id) VALUES
		('tenant_chat','cli_chat','oc_group','group',''),
		('tenant_chat','cli_chat','oc_other','group',''),
		('tenant_chat','cli_chat','oc_private','p2p','ou_bob')`)
	if err != nil {
		t.Fatal(err)
	}
	alice := identity.Principal{TenantKey: "tenant_chat", AppID: "cli_chat", AccountID: "alice", OpenID: "ou_alice"}
	bob := identity.Principal{TenantKey: "tenant_chat", AppID: "cli_chat", AccountID: "bob", OpenID: "ou_bob"}
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	calls := 0
	f := deliveryFixture{fakeSource: source, upload: func() (string, error) { return "file_example", nil }}
	f.sent = func(target, kind, _, uuid string) DeliveryResult {
		calls++
		if target != "oc_group" || len(uuid) != 48 {
			t.Errorf("unexpected dispatch to %q with key %q", target, uuid)
		}
		if kind == "file" {
			return DeliveryResult{State: "unknown", Reason: "receipt_unknown"}
		}
		close(entered)
		<-release
		return DeliveryResult{State: "success", MessageID: "om_text"}
	}
	s.ConfigureDelivery(f, true)
	mux := http.NewServeMux()
	s.Register(mux, func(w http.ResponseWriter, r *http.Request, policy identity.SessionPolicy) (identity.Principal, bool) {
		if r.Method == http.MethodPost && (policy != identity.RefreshSessionWithCSRF || r.Header.Get("X-Test-CSRF") != "valid") {
			w.WriteHeader(http.StatusForbidden)
			return identity.Principal{}, false
		}
		return alice, true
	})
	do := func(method, url, contentType string, body io.Reader) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, route+url, body)
		request.Header.Set("Content-Type", contentType)
		request.Header.Set("X-Test-CSRF", "valid")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, request)
		return w
	}
	upload := func() *httptest.ResponseRecorder {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("file", "测试.txt")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(part, "附件")
		_ = writer.Close()
		return do(http.MethodPost, "/oc_group/attachments", writer.FormDataContentType(), &body)
	}
	response := upload()
	var attachment struct{ ID string }
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &attachment) != nil || attachment.ID == "" ||
		strings.Contains(response.Body.String(), "file_example") {
		t.Fatalf("upload response: %d %s", response.Code, response.Body)
	}
	f.upload = func() (string, error) { return "", ErrAttachmentFailed }
	s.ConfigureDelivery(f, true)
	if response := upload(); response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "attachment_failed") {
		t.Fatalf("upload error: %d %s", response.Code, response.Body)
	}
	capability := do(http.MethodGet, "/oc_group/send-capabilities", "", nil)
	if capability.Code != http.StatusOK || !strings.Contains(capability.Body.String(), "ou_bot") {
		t.Fatalf("Bot capability: %d %s", capability.Code, capability.Body)
	}
	choices := do(http.MethodGet, "/oc_group/members?purpose=mention&offset=1", "", nil)
	if choices.Code != http.StatusOK || !strings.Contains(choices.Body.String(), `"mention_open_id":"ou_bob"`) {
		t.Fatalf("提及成员分页失败 %d %s", choices.Code, choices.Body)
	}
	ordinary := do(http.MethodGet, "/oc_group/members", "", nil)
	if ordinary.Code != http.StatusOK || strings.Contains(ordinary.Body.String(), "ou_bob") {
		t.Fatalf("普通成员投影错误 %d %s", ordinary.Code, ordinary.Body)
	}
	s.ConfigureDelivery(f, false)
	closed := do(http.MethodGet, "/oc_group/members?purpose=mention", "", nil)
	if closed.Code != http.StatusServiceUnavailable || strings.Contains(closed.Body.String(), "ou_bob") {
		t.Fatalf("发送关闭时的提及响应错误 %d %s", closed.Code, closed.Body)
	}
	s.ConfigureDelivery(f, true)
	content := DeliveryContent{Text: "你好\n世界 😀", Attachments: []string{attachment.ID}}
	type sendResult struct {
		status DeliveryStatus
		err    error
	}
	done := make(chan sendResult, 1)
	go func() {
		result, err := s.send(ctx, alice, "oc_group", "same", content, nil, nil)
		done <- sendResult{result, err}
	}()
	select {
	case <-entered:
	case early := <-done:
		t.Fatalf("dispatch stopped early: %#v %v", early.status, early.err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	payload, _ := json.Marshal(map[string]any{"operation_key": "same", "text": content.Text, "attachments": content.Attachments})
	duplicate := do(http.MethodPost, "/oc_group/messages", "application/json", bytes.NewReader(payload))
	if duplicate.Code != http.StatusAccepted || !strings.Contains(duplicate.Body.String(), "pending") {
		t.Fatalf("concurrent duplicate: %d %s", duplicate.Code, duplicate.Body)
	}
	if _, err := s.send(ctx, alice, "oc_other", "same", content, nil, nil); !errors.Is(err, errDeliveryConflict) {
		t.Fatalf("changed target: %v", err)
	}
	if _, err := s.send(ctx, alice, "oc_group", "same", DeliveryContent{Text: "different"}, nil, nil); !errors.Is(err, errDeliveryConflict) {
		t.Fatalf("changed content: %v", err)
	}
	if _, err := s.deliveryStatus(ctx, bob, "oc_group", "same", false); !errors.Is(err, errDeliveryMissing) {
		t.Fatalf("another actor queried operation: %v", err)
	}
	releaseOnce.Do(func() { close(release) })
	result := <-done
	if result.err != nil || result.status.State != "unknown" || !result.status.Partial ||
		len(result.status.Parts) != 2 || result.status.Parts[0].MessageID != "om_text" ||
		result.status.Parts[1].State != "unknown" || calls != 2 {
		t.Fatalf("partial result: %#v %v, dispatch count %d", result.status, result.err, calls)
	}
	duplicate = do(http.MethodPost, "/oc_group/messages", "application/json", bytes.NewReader(payload))
	status := do(http.MethodGet, "/oc_group/deliveries/same", "", nil)
	if duplicate.Code != http.StatusOK || status.Code != http.StatusOK ||
		!strings.Contains(status.Body.String(), "receipt_unknown") || calls != 2 {
		t.Fatalf("lost receipt retried or vanished: %d %d %s, %d calls", duplicate.Code, status.Code, status.Body, calls)
	}
	for _, test := range []struct {
		target, operation, reason string
		content                   DeliveryContent
	}{
		{"oc_other", "wrong-attachment", "attachment_failed", content},
		{"oc_missing", "wrong-target", "invalid_target", content},
		{"oc_private", "other-private", "access_lost", content},
		{"oc_group", "invalid-mention", "mention_not_allowed", DeliveryContent{Paragraphs: [][]DeliverySpan{{{Mention: "ou_outside"}}}}},
	} {
		failed, err := s.send(ctx, alice, test.target, test.operation, test.content, nil, nil)
		if err != nil || failed.State != "failed" || !hasDeliveryReason(failed, test.reason) || calls != 2 {
			t.Fatalf("%s: %#v %v, dispatch count %d", test.operation, failed, err, calls)
		}
	}
	source.members["oc_group"] = []Member{{"ou_bob", "Bob"}}
	failed, err := s.send(ctx, alice, "oc_group", "revoked", content, nil, nil)
	if err != nil || failed.State != "failed" || !hasDeliveryReason(failed, "access_lost") || calls != 2 {
		t.Fatalf("revocation: %#v %v, dispatch count %d", failed, err, calls)
	}
	if status := do(http.MethodGet, "/oc_group/deliveries/same", "", nil); status.Code != http.StatusNotFound ||
		strings.Contains(status.Body.String(), "om_text") {
		t.Fatalf("revoked status disclosure: %d %s", status.Code, status.Body)
	}
	source.members["oc_group"] = []Member{{"ou_alice", "Alice"}}
	f.sent = func(_, kind, _, _ string) DeliveryResult {
		calls++
		if kind != "text" {
			t.Fatal("dispatched attachment after revocation")
		}
		if _, err := s.pool.Exec(ctx, `UPDATE accounts SET active=false WHERE id='alice'`); err != nil {
			t.Fatal(err)
		}
		return DeliveryResult{State: "success", MessageID: "om_before_revocation"}
	}
	s.ConfigureDelivery(f, true)
	failed, err = s.send(ctx, alice, "oc_group", "revoked-between-parts", content, nil, nil)
	if err != nil || failed.State != "failed" || !failed.Partial || !hasDeliveryReason(failed, "access_lost") ||
		failed.Parts[0].MessageID != "" || calls != 3 {
		t.Fatalf("revocation during multipart operation: %#v %v, dispatch count %d", failed, err, calls)
	}
	f.sent = func(target, kind, _, _ string) DeliveryResult {
		calls++
		if target != "oc_private" || kind != "text" {
			t.Fatalf("unexpected private dispatch: %s %s", target, kind)
		}
		return DeliveryResult{State: "success", MessageID: "om_private"}
	}
	s.ConfigureDelivery(f, true)
	direct, err := s.send(ctx, bob, "oc_private", "own-private", DeliveryContent{Text: "你好"}, nil, nil)
	if err != nil || direct.State != "success" || direct.Parts[0].MessageID != "om_private" || calls != 4 {
		t.Fatalf("verified private delivery: %#v %v", direct, err)
	}
}

func hasDeliveryReason(result DeliveryStatus, reason string) bool {
	for _, part := range result.Parts {
		if part.Reason == reason {
			return true
		}
	}
	return false
}

func TestDeliveryReconcilesAnchoredBriefing(t *testing.T) {
	source := &fakeSource{members: map[string][]Member{"oc_group": {{"ou_alice", "Alice"}}}}
	s, ctx := testChat(t, source)
	if _, err := s.pool.Exec(ctx, `INSERT INTO feishu_chat_conversations (tenant_key,app_id,chat_id,kind)
		VALUES ('tenant_chat','cli_chat','oc_group','group')`); err != nil {
		t.Fatal(err)
	}
	actor := identity.Principal{TenantKey: "tenant_chat", AppID: "cli_chat", AccountID: "alice", OpenID: "ou_alice"}
	receipt := BusinessReceiptKey{OperationID: "published", Target: ReceiptTarget{
		ReceiptID: "receipt", ProjectID: "project", TargetKind: "briefing", TargetID: "briefing",
	}}
	body := DeliveryContent{Text: "项目简报"}
	const target, operation, id = "oc_group", "briefing-operation", "om_briefing"
	calls := 0
	confirmed := false
	allowed := false
	authority := func(_ context.Context, p identity.Principal, key BusinessReceiptKey, chatID string, content DeliveryContent) (bool, error) {
		return allowed && p.AccountID == actor.AccountID && key == receipt && chatID == target && content.Text == body.Text, nil
	}
	f := deliveryFixture{
		fakeSource: source,
		sent: func(_, _, _, _ string) DeliveryResult {
			calls++
			return DeliveryResult{State: "unknown", Reason: "receipt_unknown", MessageID: id}
		},
		confirm: func(chatID, kind, bot, messageID string) bool {
			return confirmed && chatID == target && kind == "text" && bot == "ou_bot" && messageID == id
		},
	}
	s.ConfigureDelivery(f, true)
	result, err := s.SendBriefing(ctx, actor, target, "denied", body, receipt, authority)
	if err != nil || result.State != "failed" || calls != 0 {
		t.Fatalf("unauthorized briefing: %#v %v", result, err)
	}
	allowed = true
	result, err = s.SendBriefing(ctx, actor, target, operation, body, receipt, authority)
	if err != nil || result.State != "unknown" || result.Parts[0].MessageID != "" || calls != 1 {
		t.Fatalf("unverified briefing: %#v %v", result, err)
	}
	scope := ReceiptScope{TenantKey: actor.TenantKey, AppID: actor.AppID}
	messages := []ReceiptMessage{{ChatID: target, MessageID: id}}
	if evidence, err := s.LookupDeliveries(ctx, scope, messages); err != nil || len(evidence) != 0 {
		t.Fatalf("unverified receipt projected: %#v %v", evidence, err)
	}
	canRead := false
	s.receipts.Business = businessLookup(func(_ context.Context, reader identity.Principal, keys []BusinessReceiptKey) ([]BusinessReceiptKey, error) {
		if canRead && reader.AccountID == actor.AccountID && len(keys) == 1 && keys[0] == receipt {
			return keys, nil
		}
		return nil, nil
	})
	confirmed = true
	if _, err := s.BriefingDeliveryStatus(ctx, actor, target, operation); !errors.Is(err, ErrAccessLost) {
		t.Fatalf("briefing status should require current project receipt access: %v", err)
	}
	canRead = true
	result, err = s.BriefingDeliveryStatus(ctx, actor, target, operation)
	if err != nil || result.State != "success" || result.Parts[0].MessageID != id || calls != 1 {
		t.Fatalf("reconciliation: %#v %v", result, err)
	}
	evidence, err := s.LookupDeliveries(ctx, scope, messages)
	if err != nil || len(evidence) != 1 || evidence[0].Receipt != receipt || evidence[0].Message != messages[0] {
		t.Fatalf("verified receipt: %#v %v", evidence, err)
	}
}

func TestDeliveryExpiredDispatchStaysUnknown(t *testing.T) {
	source := &fakeSource{}
	s, ctx := testChat(t, source)
	k := deliveryKey{s.tenant, s.app, "interrupted"}
	parts := []deliveryPart{{kind: "text"}, {kind: "file", attachmentID: "attachment"}}
	created, err := s.reserveDelivery(ctx, k, "alice", "oc_group", []byte{1}, nil, parts)
	if err != nil || !created {
		t.Fatalf("reserve: %v %v", created, err)
	}
	_, err = s.pool.Exec(ctx, `UPDATE feishu_send_operations SET deadline=now() WHERE operation_key='interrupted';
		UPDATE feishu_send_parts SET attempted=true WHERE operation_key='interrupted' AND position=0`)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.loadDelivery(ctx, k, "alice", "oc_group", false)
	if err != nil || result.State != "unknown" || result.Parts[0].State != "unknown" || result.Parts[1].State != "failed" ||
		result.Parts[1].Reason != "not_attempted" {
		t.Fatalf("interrupted dispatch: %#v %v", result, err)
	}
	created, err = s.reserveDelivery(ctx, k, "alice", "oc_group", []byte{1}, nil, parts)
	if err != nil || created {
		t.Fatalf("interrupted operation re-reserved: %v %v", created, err)
	}
}
