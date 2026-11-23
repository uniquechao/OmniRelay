package chat

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
	onDocument    func()
}

func (f *previewFixture) CheckPreviewShare(_ context.Context, ref drive.Ref, chat string) error {
	f.checks++
	if ref.MessageID == "" || (chat != "oc_preview" && chat != "oc_private") {
		return drive.ErrRevoked
	}
	return f.checkError
}

func (*previewFixture) ResolveDocumentWiki(_ context.Context, ref drive.Ref) (drive.Ref, error) {
	ref.Kind, ref.Token = "sheets", "linked_sheet"
	return ref, nil
}
func (f *previewFixture) ReadDocument(_ context.Context, ref drive.Ref) (drive.Snapshot, error) {
	f.reads++
	if f.onDocument != nil {
		f.onDocument()
	}
	if ref.Token == "direct_doc" {
		return drive.Snapshot{Name: "讨论记录", Content: []byte("已同步的私聊文档")}, nil
	}
	if ref.Token == "scope_missing" {
		return drive.Snapshot{}, drive.ErrAppScopeMissing
	}
	if ref.Token != "linked_sheet" {
		return drive.Snapshot{}, drive.ErrRevoked
	}
	content, err := json.Marshal([]drive.ChatSheet{{Name: "明细", Rows: 51, Pages: []json.RawMessage{[]byte(`[]`), []byte(`[["第二页"]]`)}}})
	return drive.Snapshot{Name: "归档表格", Content: content}, err
}
func (f *previewFixture) ReadFile(_ context.Context, ref drive.Ref, probe bool) (drive.Snapshot, error) {
	f.reads++
	if ref.Token == "clip" {
		return drive.Snapshot{Content: []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom")}, nil
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
	archive("oc_preview", "om_video", "media", `{"file_key":"clip","file_name":"录屏.mp4"}`, false)
	archive("oc_preview", "om_mp4", "file", `{"file_key":"clip","file_name":"录屏.MP4"}`, false)
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
	s.ConfigurePreview(fixture)
	if err := s.indexDocuments(ctx); err != nil {
		t.Fatal(err)
	}
	if worked, err := s.syncDocument(ctx); err != nil || !worked {
		t.Fatal("未提前同步文档", worked, err)
	}
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
		for _, path := range []string{"/om_video/resources/video/clip", "/om_mp4/resources/attachment/clip"} {
			r := httptest.NewRequest("GET", prefix+"/oc_preview/messages"+path, nil)
			r.Header.Set("Range", "bytes=4-7")
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, r)
			if response.Code != 206 || response.Body.String() != "ftyp" || response.Header().Get("Content-Type") != "video/mp4" {
				t.Fatal(response.Code, response.Body.String(), response.Header())
			}
		}
		w := get(prefix+image, 200, "PNG")
		if w.Header().Get("Content-Type") != "image/png" || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
			t.Fatal(w.Header())
		}
		get(prefix+"/oc_preview/messages/om_file/resources/attachment/file_demo", 200, "聊天附件预览")
		get(prefix+"/oc_preview/messages/om_unsupported/resources/attachment/file_unsupported", 200, `"stage":"unsupported"`)
		get(prefix+"/oc_preview/messages/om_active/resources/image/active_content", 415, `"error":"unsupported"`)
		get(prefix+"/oc_preview/messages", 200, `"name":"归档表格"`)
		reads := fixture.reads
		get(prefix+wiki, 200, "第二页")
		if reads != fixture.reads {
			t.Fatal("点击时重新读取了飞书文档正文")
		}
	}
	checks, reads := fixture.checks, fixture.reads
	get(global+image+"_forged", 404, "resource_not_found")
	get(project+"/oc_unlinked/messages/om_unlinked/resources/image/img_demo", 404, "chat_access_lost")
	p.AccountID, p.OpenID = "bob", "ou_bob"
	get(global+wiki, 404, "chat_access_lost")
	get(global+image, 404, "chat_access_lost")
	get(project+image, 404, "chat_access_lost")
	if fixture.checks != checks || fixture.reads != reads {
		t.Fatal("越权请求到达了飞书资源读取")
	}
	p.AccountID, p.OpenID = "alice", "ou_alice"
	fixture.checkError = drive.ErrPreviewMessageDeleted
	get(project+image, 410, "message_deleted")
	fixture.checkError = nil
	archive("oc_preview", "om_wiki", "text", "", true)
	get(global+wiki, 410, "message_deleted")
	var count int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM feishu_chat_documents WHERE message_id='om_wiki'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("撤回后仍有文档快照", count, err)
	}
	archive("oc_preview", "om_image", "image", "", true)
	get(global+image, 410, "message_deleted")
}

func TestPrivateDocumentSnapshotInvalidation(t *testing.T) {
	s, ctx := testChat(t, &fakeSource{})
	fixture := &previewFixture{}
	s.ConfigurePreview(fixture)
	const message = "om_private_document"
	archive := func(token string, deleted bool) {
		t.Helper()
		msg := exampleMessage("oc_private", message, "https://example.feishu.cn/docx/"+token, 1)
		msg.Deleted = deleted
		applyEvent(t, ctx, s, orchestrator.ArchiveEvent{ChatID: "oc_private", Kind: orchestrator.ChatPrivate, PeerOpenID: "ou_alice", Message: &msg})
	}
	work := func() {
		t.Helper()
		if err := s.indexDocuments(ctx); err != nil {
			t.Fatal(err)
		}
		if worked, err := s.syncDocument(ctx); !worked || err != nil {
			t.Fatal("私聊文档同步失败", worked, err)
		}
	}
	meta := func(token, state, title string) {
		t.Helper()
		item := messageItem{Resources: []messageResource{{Key: token, Kind: "docx"}}}
		if err := s.projectDocuments(ctx, map[string]*messageItem{message: &item}); err != nil {
			t.Fatal(err)
		}
		if r := item.Resources[0]; r.State != state || r.Name != title {
			t.Fatalf("卡片状态或标题不符：%+v", r)
		}
	}
	archive("direct_doc", false)
	work()
	meta("direct_doc", "ready", "讨论记录")
	archive("denied", false)
	meta("direct_doc", "", "")
	work()
	meta("denied", "resource_forbidden", "")
	archive("scope_missing", false)
	work()
	meta("scope_missing", "application_scope_missing", "")
	archive("direct_doc", false)
	fixture.onDocument = func() { archive("direct_doc", true) }
	work()
	meta("direct_doc", "", "")
}
