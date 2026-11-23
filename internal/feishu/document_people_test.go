package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/uniquechao/OmniRelay/internal/drive"
)

func TestDocumentUserAndOtherInlineReferences(t *testing.T) {
	elements := []any{map[string]any{"text_run": map[string]any{"content": "参会人："}}}
	for i := 0; i < 11; i++ {
		elements = append(elements, map[string]any{"mention_user": map[string]any{"user_id": fmt.Sprintf("ou_user%d", i), "text_element_style": map[string]bool{"bold": true}}})
	}
	// Repeated user references are resolved once, retaining the original source order.
	elements = append(elements, map[string]any{"mention_user": map[string]string{"user_id": "ou_user10"}}, map[string]any{"mention_doc": map[string]string{"title": "验收文档", "url": "https%3A%2F%2Fexample.feishu.cn%2Fdocx%2FDoc2"}}, map[string]any{"reminder": map[string]string{"expire_time": "0"}}, map[string]any{"mention_doc": map[string]string{"title": "恶意链接", "url": "javascript%3Aalert(1)"}})
	blocks := []any{map[string]any{"block_id": "p", "block_type": 2, "text": map[string]any{"elements": elements}}}
	lookups := 0
	getter := fileGetter{resolveUsers: func(_ context.Context, ids []string) (map[string]string, error) {
		if len(ids) != 11 {
			t.Fatalf("not deduplicated: %d", len(ids))
		}
		out := map[string]string{}
		for i := 0; i < 10; i++ {
			out[fmt.Sprintf("ou_user%d", i)] = fmt.Sprintf("用户%d", i)
		}
		return out, nil
	}, get: func(_ context.Context, path string) (*http.Response, error) {
		var data any
		switch {
		case path == "docx/v1/documents/Doc":
			data = map[string]any{"document": map[string]any{"title": "纪要", "revision_id": 1}}
		case strings.HasPrefix(path, "docx/v1/documents/Doc/blocks?"):
			if !strings.Contains(path, "user_id_type=open_id") {
				t.Fatal(path)
			}
			data = map[string]any{"items": blocks, "has_more": false}
		case path == "contact/v3/users/ou_user10?user_id_type=open_id":
			lookups++
			data = map[string]any{"user": map[string]string{"name": "外部参会人", "open_id": "ou_user10"}}
		default:
			t.Fatal(path)
		}
		body, _ := json.Marshal(map[string]any{"code": 0, "data": data})
		return &http.Response{Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	}}
	snap, err := getter.readDocument(t.Context(), "Doc", false)
	if err != nil {
		t.Fatal(err)
	}
	var doc drive.DocumentSnapshot
	if err = json.Unmarshal(snap.Content, &doc); err != nil {
		t.Fatal(err)
	}
	runs := doc.Blocks[0].Runs
	if lookups != 1 || len(runs) != 16 {
		t.Fatal(lookups, len(runs))
	}
	for i := 1; i <= 12; i++ {
		r := runs[i]
		if r.Kind != "person" || r.Unresolved || r.PersonKey == "" || strings.Contains(r.Text, "[引用]") {
			t.Fatal(r)
		}
	}
	if !runs[1].Bold || runs[1].Text != "@用户0 " || runs[11].Text != "@外部参会人 " {
		t.Fatal(runs)
	}
	if runs[13].Link != "https://example.feishu.cn/docx/Doc2" || runs[13].Text != "验收文档" || runs[14].Text != "1970-01-01T00:00:00Z" || runs[15].Link != "" {
		t.Fatal(runs[13:])
	}
	if strings.Contains(string(snap.Content), "ou_user") {
		t.Fatal("raw user IDs escaped into normalized snapshot")
	}
}

func TestDocumentUnresolvableUserIsExplicit(t *testing.T) {
	getter := fileGetter{get: func(_ context.Context, path string) (*http.Response, error) {
		body := `{"code":0,"data":{"document":{"title":"纪要","revision_id":1}}}`
		if strings.Contains(path, "/blocks?") {
			body = `{"code":0,"data":{"items":[{"block_id":"p","block_type":2,"text":{"elements":[{"mention_user":{"user_id":"ou_hidden"}}]}}],"has_more":false}}`
		}
		if strings.HasPrefix(path, "contact/") {
			body = `{"code":99991672,"msg":"scope missing"}`
		}
		return &http.Response{Body: io.NopCloser(strings.NewReader(body))}, nil
	}}
	snap, err := getter.readDocument(t.Context(), "Doc", false)
	if err != nil {
		t.Fatal(err)
	}
	var doc drive.DocumentSnapshot
	_ = json.Unmarshal(snap.Content, &doc)
	if run := doc.Blocks[0].Runs[0]; !run.Unresolved || run.Text != "[未解析用户] " || run.PersonKey == "" {
		t.Fatal(run)
	}
}
