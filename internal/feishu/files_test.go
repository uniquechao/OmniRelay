package feishu

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/uniquechao/OmniRelay/internal/drive"
)

func TestFileSourceFormatsAndScope(t *testing.T) {
	replies := map[string]string{
		"/open-apis/im/v1/chats/oc_group":    `{"code":0,"data":{"chat_id":"oc_group","tenant_key":"t","chat_mode":"group"}}`,
		"/open-apis/im/v1/chats/oc_old":      "denied",
		"/open-apis/im/v1/messages/om_share": `{"code":0,"data":{"items":[{"message_id":"om_share","chat_id":"oc_group","msg_type":"text","body":{"content":"{\"text\":\"https://test.feishu.cn/docx/Doc\"}"}}]}}`,
		"/open-apis/docx/v1/documents/Doc":   `{"code":0,"data":{"document":{"title":"说明"}}}`,
		"/open-apis/docx/v1/documents/Doc/blocks?document_revision_id=-1&page_size=500&user_id_type=open_id": `{"code":0,"data":{"items":[{"block_id":"text","block_type":2,"text":{"elements":[{"text_run":{"content":"正文"}}]}}],"has_more":false}}`,
		"/open-apis/minutes/v1/minutes/Minute":                                        `{"code":0,"data":{"minute":{"title":"评审"}}}`,
		"/open-apis/minutes/v1/minutes/Minute/transcript?file_format=txt":             "会议记录",
		"/open-apis/drive/v1/files/Cloud/download":                                    "PDF",
		"/open-apis/wiki/v2/spaces/get_node?token=Node":                               `{"code":0,"data":{"node":{"obj_type":"docx","obj_token":"Doc"}}}`,
		"/open-apis/im/v1/messages/om_share/resources/Img?type=image":                 "图片字节",
		"/open-apis/im/v1/messages/om_share/resources/Video?type=file":                "视频字节",
		"/open-apis/im/v1/messages/om_share/resources/Key?type=file":                  "",
		"/open-apis/bitable/v1/apps/Base":                                             `{"code":0,"data":{"app":{"name":"登记表"}}}`,
		"/open-apis/bitable/v1/apps/Base/tables?page_size=100&page_token=":            `{"code":0,"data":{"items":[{"table_id":"t1","name":"登记"}],"has_more":false}}`,
		"/open-apis/bitable/v1/apps/Base/tables/t1/records?page_size=100&page_token=": `{"code":0,"data":{"items":[{"fields":{"编号":1}}],"has_more":true,"page_token":"p2"}}`,
	}
	last := "/open-apis/bitable/v1/apps/Base/tables/t1/records?page_size=100&page_token=p2"
	replies[last] = `{"code":0,"data":{"items":[{"fields":{"编号":2}}],"has_more":false}}`
	src := NewChatSource(NewBotCheck("app", "secret", chatTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"code":0,"access_token":"test"}`
		status := 200
		if r.URL.String() != tokenURL {
			if r.Header.Get("Authorization") != "Bearer test" {
				t.Fatal("缺少应用凭据")
			}
			var ok bool
			body, ok = replies[strings.TrimSuffix(r.URL.RequestURI(), "?")]
			if !ok {
				t.Fatal(r.URL.RequestURI())
			}
			if body == "限流" {
				status = http.StatusTooManyRequests
				body = `{"code":99991400}`
			}
			if body == "denied" {
				status = 403
			}
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})), "t")
	ctx := context.Background()
	doc := drive.Ref{Kind: "docx", Token: "Doc", MessageID: "om_share", ChatKind: "group"}
	if err := src.CheckShare(ctx, doc, "oc_group"); err != nil {
		t.Fatal(err)
	}
	if err := src.CheckShare(ctx, doc, "oc_old"); !errors.Is(err, drive.ErrRevoked) {
		t.Fatal(err)
	}
	for _, tc := range []struct{ kind, token, want string }{{"docx", "Doc", "正文"}, {"minutes", "Minute", "会议记录"}, {"file", "Cloud", "PDF"}, {"attachment", "Key", ""}, {"image", "Img", "图片字节"}, {"video", "Video", "视频字节"}, {"base", "Base", `"编号":2`}} {
		f, err := src.ReadFile(ctx, drive.Ref{Kind: tc.kind, Token: tc.token, MessageID: "om_share"}, false)
		if err == nil && f.Stream != nil {
			f.Content, err = io.ReadAll(f.Stream)
			f.Stream.Close()
		}
		if err != nil || !strings.Contains(string(f.Content), tc.want) || f.Content == nil {
			t.Fatal(tc, f, err)
		}
	}
	wikiMessage := "/open-apis/im/v1/messages/om_share"
	replies[wikiMessage] = strings.ReplaceAll(replies[wikiMessage], "/docx/Doc", "/wiki/Node")
	if err := src.CheckShare(ctx, doc, "oc_group"); err != nil {
		t.Fatal(err)
	}
	replies["/open-apis/wiki/v2/spaces/get_node?token=Node"] = "denied"
	if err := src.CheckShare(ctx, doc, "oc_group"); !errors.Is(err, drive.ErrRevoked) {
		t.Fatal(err)
	}
	base := drive.Ref{Kind: "base", Token: "Base"}
	lastPage := replies[last]
	replies[last] = "限流"
	if f, err := src.ReadFile(ctx, base, false); !errors.Is(err, drive.ErrPartial) || f.Content != nil {
		t.Fatal("后续页限流时不能发布不完整的版本", f, err)
	}
	replies[last] = lastPage
	if f, err := src.ReadFile(ctx, base, false); err != nil || f.Name != "登记表" || !strings.Contains(string(f.Content), `"编号":1`) || !strings.Contains(string(f.Content), `"编号":2`) {
		t.Fatal("补采后应有两页记录", f, err)
	}
	download := "/open-apis/drive/v1/files/Cloud/download"
	replies[download] = "限流"
	if f, err := src.ReadFile(ctx, drive.Ref{Kind: "file", Token: "Cloud"}, false); !errors.Is(err, drive.ErrUnavailable) || f.Stream != nil {
		t.Fatal("原件下载限流应可重试", err)
	}
	replies[last] = `{"code":0,"data":{"items":[],"page_token":"p2","has_more":true}}`
	if f, err := src.ReadFile(ctx, base, false); !errors.Is(err, drive.ErrPartial) || len(f.Content) != 0 {
		t.Fatal(f, err)
	}
	replies[last] = "denied"
	if _, err := src.ReadFile(ctx, base, false); !errors.Is(err, drive.ErrRevoked) {
		t.Fatal(err)
	}
	replies["/open-apis/docx/v1/documents/Doc"] = "denied"
	if _, err := src.ReadFile(ctx, doc, true); !errors.Is(err, drive.ErrRevoked) {
		t.Fatal(err)
	}
}
