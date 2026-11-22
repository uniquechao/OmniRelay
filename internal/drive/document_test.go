package drive

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRichDocumentPreviewAndLegacyDetection(t *testing.T) {
	doc := DocumentSnapshot{Schema: DocumentSchema, Revision: 9, Blocks: []DocumentBlock{}}
	for i := 0; i < 41; i++ {
		doc.Blocks = append(doc.Blocks, DocumentBlock{Type: "heading", Level: 2, Runs: []DocumentRun{{Text: "标题", Bold: true}}})
	}
	data, _ := json.Marshal(doc)
	out := renderPreview(context.Background(), "docs", data, previewOptions{Page: 2})
	if out.Stage != "ready" || out.MIME != "application/json" || !strings.Contains(string(out.Meta), `"document_format":"rich"`) {
		t.Fatal(out)
	}
	var result struct {
		Document []DocumentBlock `json:"document"`
	}
	if json.Unmarshal(out.Body, &result) != nil || len(result.Document) != 1 || !result.Document[0].Runs[0].Bold {
		t.Fatal(string(out.Body))
	}
	if out := renderPreview(context.Background(), "docs", data, previewOptions{Page: 3}); out.Stage != "invalid_page" {
		t.Fatal(out)
	}
	if _, ok := renderDocument([]byte("历史纯文本"), previewOptions{Page: 1}); ok {
		t.Fatal("legacy text misidentified")
	}
	if got := contentType(item{Category: "docs"}, "application/vnd.omnirelay.docx+json"); got != "application/json" {
		t.Fatal(got)
	}
	if got := contentType(item{Category: "docs"}, "text/plain"); !strings.HasPrefix(got, "text/plain") {
		t.Fatal(got)
	}
}
