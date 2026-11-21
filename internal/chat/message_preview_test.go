package chat

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/uniquechao/OmniRelay/internal/drive"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
)

type previewFixture struct {
	checks, reads int
	checkError    error
}

func (f *previewFixture) CheckPreviewShare(_ context.Context, ref drive.Ref, chat string) error {
	f.checks++
	if ref.MessageID == "" || chat != "oc_preview" {
		return drive.ErrRevoked
	}
	return f.checkError
}

func (*previewFixture) ResolvePreviewWiki(_ context.Context, ref drive.Ref, token string) (drive.Ref, error) {
	if token != "wiki-session" {
		return drive.Ref{}, drive.ErrRevoked
	}
	ref.Kind, ref.Token = "sheets", "linked_sheet"
	return ref, nil
}

func (f *previewFixture) ReadPreviewSheet(_ context.Context, ref drive.Ref, token string, options drive.MessagePreviewOptions) (drive.MessagePreviewResult, error) {
	f.reads++
	if ref.Token != "linked_sheet" || token != "sheet-session" || options.Page != 2 {
		return drive.MessagePreviewResult{}, drive.ErrRevoked
	}
	return drive.MessagePreviewResult{Stage: "ready", Kind: "xlsx", Data: []byte(`[["第二页"]]`)}, nil
}

func (f *previewFixture) ReadPreviewFile(_ context.Context, ref drive.Ref, token string) (drive.Snapshot, error) {
	f.reads++
	if token != "" {
		return drive.Snapshot{}, errors.New("unexpected user grant for a message resource")
	}
	if ref.Kind == "image" {
		if ref.Token == "active_content" {
			return drive.Snapshot{Content: []byte(`<svg onload="alert(1)"></svg>`)}, nil
		}
		png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO/aT+wAAAAASUVORK5CYII=")
		return drive.Snapshot{Content: png}, nil
	}
	return drive.Snapshot{Name: ref.Name, Content: []byte("聊天附件预览")}, nil
}

func TestMessagePreviewAuthorization(t *testing.T) {
	src := &fakeSource{members: map[string][]Member{"oc_preview": {{"ou_alice", "甲"}}, "oc_unlinked": {{"ou_alice", "甲"}}}}
	s, ctx := testChat(t, src)
	p := identity.Principal{AccountID: "alice", OpenID: "ou_alice", TenantKey: "tenant_chat", AppID: "cli_chat"}
	s.check = func(_ context.Context, _ identity.Principal, action, project string) (bool, error) {
		return action == "project.read" && project == "p1", nil
	}
	archive := func(chatID, id, kind, body string, deleted bool) {
		t.Helper()
		msg := exampleMessage(chatID, id, "", 1)
		msg.Type, msg.Body, msg.Deleted = kind, body, deleted
		applyEvent(t, ctx, s, orchestrator.ArchiveEvent{ChatID: chatID, Kind: orchestrator.ChatGroup, Message: &msg})
	}
	archive("oc_preview", "om_image", "image", `{"image_key":"img_demo"}`, false)
	archive("oc_preview", "om_active", "image", `{"image_key":"active_content"}`, false)
	archive("oc_preview", "om_file", "file", `{"file_key":"file_demo","file_name":"说明.txt"}`, false)
	archive("oc_preview", "om_unsupported", "file", `{"file_key":"file_unsupported","file_name":"说明.exe"}`, false)
	archive("oc_preview", "om_wiki", "text", `{"text":"https://example.feishu.cn/wiki/node_demo"}`, false)
	archive("oc_unlinked", "om_unlinked", "image", `{"image_key":"img_demo"}`, false)
	if _, err := s.pool.Exec(ctx, `INSERT INTO project_chat_links(tenant_key,app_id,project_id,chat_id)
		VALUES ('tenant_chat','cli_chat','p1','oc_preview')`); err != nil {
		t.Fatal(err)
	}
	fixture := &previewFixture{}
	grants := map[string]string{}
	s.ConfigurePreview(fixture, func(_ *http.Request, _ identity.Principal, kind string) string { return grants[kind] })
	mux := http.NewServeMux()
	s.Register(mux, func(http.ResponseWriter, *http.Request, identity.SessionPolicy) (identity.Principal, bool) {
		return p, true
	})
	get := func(path string, want int, expected string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != want || !strings.Contains(w.Body.String(), expected) {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		return w
	}
	const global = "/api/v1/feishu/chats"
	const project = "/api/v1/projects/p1/chat-reader"
	const image = "/oc_preview/messages/om_image/resources/image/img_demo"
	const wiki = "/oc_preview/messages/om_wiki/resources/wiki/node_demo?page=2"
	for _, prefix := range []string{global, project} {
		get(prefix+"/oc_preview/messages", 200, `"kind":"image","key":"img_demo"`)
		w := get(prefix+image, 200, "PNG")
		if w.Header().Get("Content-Type") != "image/png" || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
			t.Fatal(w.Header())
		}
		get(prefix+"/oc_preview/messages/om_file/resources/attachment/file_demo", 200, "聊天附件预览")
		get(prefix+"/oc_preview/messages/om_unsupported/resources/attachment/file_unsupported", 200, `"stage":"unsupported"`)
		get(prefix+"/oc_preview/messages/om_active/resources/image/active_content", 415, `"error":"unsupported"`)
		clear(grants)
		get(prefix+wiki, 403, `"kind":"wiki"`)
		grants["wiki"] = "wiki-session"
		get(prefix+wiki, 403, `"kind":"sheets"`)
		grants["sheets"] = "sheet-session"
		get(prefix+wiki, 200, "第二页")
	}
	checks, reads := fixture.checks, fixture.reads
	get(global+image+"_forged", 404, "resource_not_found")
	get(project+"/oc_unlinked/messages/om_unlinked/resources/image/img_demo", 404, "chat_access_lost")
	p.AccountID, p.OpenID = "bob", "ou_bob"
	get(global+image, 404, "chat_access_lost")
	get(project+image, 404, "chat_access_lost")
	if fixture.checks != checks || fixture.reads != reads {
		t.Fatal("越权请求到达了飞书资源读取")
	}
	p.AccountID, p.OpenID = "alice", "ou_alice"
	fixture.checkError = drive.ErrPreviewMessageDeleted
	get(project+image, 410, "message_deleted")
	fixture.checkError = nil
	archive("oc_preview", "om_image", "image", "", true)
	get(global+image, 410, "message_deleted")
}
