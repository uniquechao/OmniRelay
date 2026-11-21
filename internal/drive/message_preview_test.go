package drive

import (
	"reflect"
	"testing"
)

func TestMessageReferences(t *testing.T) {
	const body = `{"content":[[
		{"tag":"a","text":"知识库","href":"https://example.feishu.cn/wiki/Wiki?sheet=grid"},
		{"tag":"img","image_key":"img_example"},
		{"tag":"file","file_key":"file_example","file_name":"指引.pdf"},
		{"tag":"text","text":"https://example.feishu.cn/sheets/Sheet。https://example.feishu.cn/wiki/Wiki\n https://example.feishu.cn.evil.test/docx/Wrong https://example.feishu.cn/docx/Wrong%2FMore https://example.feishu.cn/docx/Wrong/child"}
	]]}`
	want := []Ref{
		{Kind: "wiki", Token: "Wiki", URL: "https://example.feishu.cn/wiki/Wiki?sheet=grid", Name: "https://example.feishu.cn/wiki/Wiki?sheet=grid"},
		{Kind: "image", Token: "img_example", Name: "图片"},
		{Kind: "attachment", Token: "file_example", Name: "指引.pdf"},
		{Kind: "sheets", Token: "Sheet", URL: "https://example.feishu.cn/sheets/Sheet", Name: "https://example.feishu.cn/sheets/Sheet"},
	}
	if got := MessageReferences("post", body); !reflect.DeepEqual(got, want) {
		t.Fatalf("资源解析结果 %#v", got)
	}
	if got := MessageReferences("text", "https://example.feishu.cn/minutes/Minutes"); len(got) != 1 || got[0].Kind != "minutes" {
		t.Fatalf("历史文本链接解析结果 %#v", got)
	}
}
