package feishu

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/uniquechao/OmniRelay/internal/drive"
)

func TestDocumentPreservesBlocksAndDownloadsImages(t *testing.T) {
	image, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a9xkAAAAASUVORK5CYII=")
	replies := map[string]string{
		"docx/v1/documents/Doc": `{"code":0,"data":{"document":{"title":"图文","revision_id":7}}}`,
		"docx/v1/documents/Doc/blocks?document_revision_id=-1&page_size=500":                 `{"code":0,"data":{"items":[{"block_id":"Doc","block_type":1,"children":["h","p","pic","table"]},{"block_id":"h","block_type":4,"heading2":{"elements":[{"text_run":{"content":"标题","text_element_style":{"bold":true}}}]}},{"block_id":"p","block_type":12,"bullet":{"elements":[{"text_run":{"content":"重点","text_element_style":{"italic":true,"link":{"url":"https://example.test"}}}}]}}],"has_more":true,"page_token":"next"}}`,
		"docx/v1/documents/Doc/blocks?document_revision_id=-1&page_size=500&page_token=next": `{"code":0,"data":{"items":[{"block_id":"pic","block_type":27,"image":{"token":"img_token","width":1,"height":1}},{"block_id":"table","block_type":31,"children":["cell"],"table":{"property":{"column_size":1,"row_size":1},"cells":["cell"]}},{"block_id":"cell","block_type":32,"children":["text"]},{"block_id":"text","block_type":2,"text":{"elements":[{"text_run":{"content":"表格"}}]}}],"has_more":false}}`,
	}
	downloads := 0
	f := fileGetter{get: func(_ context.Context, path string) (*http.Response, error) {
		if strings.HasPrefix(path, "drive/v1/medias/") {
			downloads++
			if !strings.Contains(path, "drive_route_token") {
				t.Fatal("missing parent document routing")
			}
			return &http.Response{Body: io.NopCloser(strings.NewReader(string(image)))}, nil
		}
		value, ok := replies[path]
		if !ok {
			t.Fatalf("unexpected request %s", path)
		}
		return &http.Response{Body: io.NopCloser(strings.NewReader(value))}, nil
	}}
	snapshot, err := f.readDocument(t.Context(), "Doc", false)
	if err != nil {
		t.Fatal(err)
	}
	var doc drive.DocumentSnapshot
	if json.Unmarshal(snapshot.Content, &doc) != nil || doc.Schema != drive.DocumentSchema || doc.Revision != 7 || len(doc.Blocks) != 4 {
		t.Fatalf("bad document %s", snapshot.Content)
	}
	if doc.Blocks[0].Type != "heading" || doc.Blocks[0].Level != 2 || !doc.Blocks[0].Runs[0].Bold {
		t.Fatal(doc.Blocks[0])
	}
	if !doc.Blocks[1].Runs[0].Italic || doc.Blocks[1].Runs[0].Link != "https://example.test" {
		t.Fatal(doc.Blocks[1])
	}
	if downloads != 1 || string(doc.Blocks[2].Image.Data) != string(image) {
		t.Fatal("image bytes not saved")
	}
	if doc.Blocks[3].Rows[0][0][0].Runs[0].Text != "表格" {
		t.Fatal(doc.Blocks[3])
	}
	// Missing image permission must fail the new snapshot, preserving the previously published version.
	original := f.get
	f.get = func(ctx context.Context, path string) (*http.Response, error) {
		if strings.HasPrefix(path, "drive/v1/medias/") {
			return nil, drive.ErrAppScopeMissing
		}
		return original(ctx, path)
	}
	failed, err := f.readDocument(t.Context(), "Doc", false)
	if !errors.Is(err, drive.ErrAppScopeMissing) || len(failed.Content) > 0 {
		t.Fatal("published document with missing images", err)
	}
}
func TestDocumentRejectsIncompletePagesAndChangedRevision(t *testing.T) {
	for _, scenario := range []string{"missing_child", "cycle", "page_loop", "changed_revision"} {
		t.Run(scenario, func(t *testing.T) {
			metas := 0
			f := fileGetter{get: func(_ context.Context, path string) (*http.Response, error) {
				body := `{"code":0,"data":{"items":[{"block_id":"p","block_type":2,"text":{"elements":[{"text_run":{"content":"内容"}}]}}],"has_more":false}}`
				if path == "docx/v1/documents/Doc" {
					metas++
					revision := 7
					if scenario == "changed_revision" && metas > 1 {
						revision = 8
					}
					b, _ := json.Marshal(map[string]any{"code": 0, "data": map[string]any{"document": map[string]any{"title": "T", "revision_id": revision}}})
					body = string(b)
				} else {
					switch scenario {
					case "missing_child":
						body = `{"code":0,"data":{"items":[{"block_id":"root","block_type":1,"children":["missing"]}],"has_more":false}}`
					case "cycle":
						body = `{"code":0,"data":{"items":[{"block_id":"root","block_type":1,"children":["root"]}],"has_more":false}}`
					case "page_loop":
						body = `{"code":0,"data":{"items":[],"has_more":true,"page_token":"same"}}`
					}
				}
				return &http.Response{Body: io.NopCloser(strings.NewReader(body))}, nil
			}}
			snapshot, err := f.readDocument(t.Context(), "Doc", false)
			if !errors.Is(err, drive.ErrPartial) || len(snapshot.Content) > 0 {
				t.Fatalf("%s: err=%v", scenario, err)
			}
		})
	}
}
