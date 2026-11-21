package feishu

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/uniquechao/OmniRelay/internal/drive"
)

func TestMessagePreviewFeishuSources(t *testing.T) {
	source := NewChatSource(NewBotCheck("app", "secret", chatTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() == tokenURL {
			return deliveryResponse(200, `{"code":0,"access_token":"app-fixture"}`), nil
		}
		path := r.URL.Path
		if r.URL.Host != "open.feishu.cn" || r.Method != "GET" {
			t.Fatal("非预期预览来源", r.Method, r.URL.Host)
		}
		want := "Bearer app-fixture"
		if r.Header.Get("Authorization") != want {
			t.Fatal("读取消息资源与云文档时未使用对应身份", path)
		}
		var body string
		switch path {
		case "/open-apis/im/v1/messages/om":
			body = `{"code":0,"data":{"items":[{"message_id":"om","chat_id":"oc","msg_type":"image","body":{"content":"{\"image_key\":\"img\"}"}}]}}`
		case "/open-apis/im/v1/messages/recalled":
			body = `{"code":0,"data":{"items":[{"message_id":"recalled","chat_id":"oc","deleted":true}]}}`
		case "/open-apis/im/v1/messages/om/resources/img":
			if r.URL.Query().Get("type") != "image" {
				t.Fatal("图片资源类型缺失")
			}
			body = "图像字节"
		case "/open-apis/wiki/v2/spaces/get_node":
			if r.URL.Query().Get("token") == "WikiWithoutScope" {
				return deliveryResponse(403, `{"code":99991672}`), nil
			}
			if r.URL.Query().Get("token") != "Wiki" {
				t.Fatal("知识库节点参数错误")
			}
			body = `{"code":0,"data":{"node":{"obj_type":"sheet","obj_token":"Sheet","title":"共享表格"}}}`
		case "/open-apis/sheets/v3/spreadsheets/TitleOnly":
			body = `{"code":0,"data":{"spreadsheet":{"title":"已有标题的表格"}}}`
		case "/open-apis/sheets/v3/spreadsheets/TitleOnly/sheets/query":
			body = `{"code":1254302}`
		case "/open-apis/sheets/v3/spreadsheets/Sheet":
			body = `{"code":0,"data":{"spreadsheet":{"title":"真实表格标题"}}}`
		case "/open-apis/sheets/v3/spreadsheets/Sheet/sheets/query":
			body = `{"code":0,"data":{"sheets":[{"sheet_id":"hidden","hidden":true,"grid_properties":{"row_count":100}},
				{"sheet_id":"Tab","title":"周报","grid_properties":{"row_count":56,"column_count":27}}]}}`
		case "/open-apis/sheets/v2/spreadsheets/Sheet/values/Tab!A1:AA50", "/open-apis/sheets/v2/spreadsheets/Sheet/values/Tab!A51:AA56":
			if r.URL.Query().Get("valueRenderOption") != "FormattedValue" {
				t.Fatal("预览应读取格式化的表格值")
			}
			body = `{"code":0,"data":{"valueRange":{"values":[["周报正文",null]]}}}`
		case "/open-apis/minutes/v1/minutes/Minute":
			body = `{"code":0,"data":{"minute":{"title":"会议纪要"}}}`
		case "/open-apis/minutes/v1/minutes/Minute/transcript":
			if r.URL.Query().Get("file_format") != "txt" {
				t.Fatal("妙记预览应读取文本")
			}
			body = "会议逐字稿"
		case "/open-apis/docx/v1/documents/Denied":
			return deliveryResponse(403, `{"code":1770032}`), nil
		default:
			t.Fatal("非预期读取路径", path)
		}
		return deliveryResponse(200, body), nil
	})), "tenant")
	ctx := t.Context()
	image := drive.Ref{Kind: "image", Token: "img", MessageID: "om"}
	if err := source.CheckPreviewShare(ctx, image, "oc"); err != nil {
		t.Fatal(err)
	}
	if snap, err := source.ReadFile(ctx, image, false); err != nil {
		t.Fatal(err)
	} else if data, err := drive.ReadPreviewBytes(snap); err != nil || string(data) != "图像字节" {
		t.Fatal("未读取原始消息图片", err)
	}
	image.MessageID = "recalled"
	if err := source.CheckPreviewShare(ctx, image, "oc"); !errors.Is(err, drive.ErrPreviewMessageDeleted) {
		t.Fatal("撤回的原消息应被识别", err)
	}
	if _, err := source.ResolveDocumentWiki(ctx, drive.Ref{Kind: "wiki", Token: "WikiWithoutScope"}); !errors.Is(err, drive.ErrAppScopeMissing) || !errors.Is(err, drive.ErrRevoked) {
		t.Fatal("知识库缺应用权限时应保留拒读语义并上报原因", err)
	}
	sheet, titleErr := source.ReadDocument(ctx, drive.Ref{Kind: "sheets", Token: "TitleOnly"})
	if !errors.Is(titleErr, drive.ErrRevoked) || errors.Is(titleErr, drive.ErrAppScopeMissing) || sheet.Name != "已有标题的表格" {
		t.Fatal("表格资源拒读时应保留已成功取得的标题", sheet.Name, titleErr)
	}
	wiki, err := source.ResolveDocumentWiki(ctx, drive.Ref{Kind: "wiki", Token: "Wiki"})
	if err != nil || wiki.Kind != "sheets" || wiki.Token != "Sheet" {
		t.Fatal(wiki, err)
	}
	page, err := drive.RenderMessagePreview(ctx, drive.MessagePreviewOptions{Page: 2}, wiki, func() (drive.Snapshot, error) { return source.ReadDocument(ctx, wiki) })
	if err != nil || page.Stage != "ready" || page.Name != "真实表格标题" || !strings.Contains(string(page.Data), "周报正文") || strings.Contains(string(page.Meta), "hidden") {
		t.Fatalf("工作表预览错误 %+v %v", page, err)
	}
	minute, err := source.ReadDocument(ctx, drive.Ref{Kind: "minutes", Token: "Minute"})
	if err != nil {
		t.Fatal(err)
	}
	if data, err := drive.ReadPreviewBytes(minute); err != nil || string(data) != "会议逐字稿" {
		t.Fatal("妙记逐字稿读取失败", err)
	}
	doc := drive.Ref{Kind: "docx", Token: "Denied"}
	if _, err := source.ReadDocument(ctx, doc); !errors.Is(err, drive.ErrRevoked) {
		t.Fatal("机器人无文档权限时应拒绝", err)
	}
}
