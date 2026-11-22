package drive

import (
	"encoding/json"
	"math"
)

// DocumentSnapshot stores formatting and embedded image bytes in one immutable,
// source-authorized file version. It never contains provider download URLs.
type DocumentSnapshot struct {
	Schema   string          `json:"schema"`
	Revision int64           `json:"revision"`
	Blocks   []DocumentBlock `json:"blocks"`
}

const DocumentSchema = "omnirelay.docx/v1"

type DocumentBlock struct {
	Type     string              `json:"type"`
	Level    int                 `json:"level,omitempty"`
	Runs     []DocumentRun       `json:"runs,omitempty"`
	Children []DocumentBlock     `json:"children,omitempty"`
	Rows     [][][]DocumentBlock `json:"rows,omitempty"`
	Image    *DocumentImage      `json:"image,omitempty"`
	Checked  bool                `json:"checked,omitempty"`
	Align    string              `json:"align,omitempty"`
}
type DocumentRun struct {
	Text       string `json:"text"`
	Bold       bool   `json:"bold,omitempty"`
	Italic     bool   `json:"italic,omitempty"`
	Underline  bool   `json:"underline,omitempty"`
	Strike     bool   `json:"strike,omitempty"`
	Code       bool   `json:"code,omitempty"`
	Link       string `json:"link,omitempty"`
	Color      int    `json:"color,omitempty"`
	Background int    `json:"background,omitempty"`
}
type DocumentImage struct {
	MIME   string `json:"mime"`
	Data   []byte `json:"data"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

func renderDocument(data []byte, o previewOptions) (previewOutput, bool) {
	var doc DocumentSnapshot
	if json.Unmarshal(data, &doc) != nil || doc.Schema != DocumentSchema {
		return previewOutput{}, false
	}
	const pageSize = 40
	total := len(doc.Blocks)
	pages := max(1, int(math.Ceil(float64(total)/pageSize)))
	if o.Page < 1 || o.Page > pages {
		return previewOutput{Stage: "invalid_page"}, true
	}
	start := min((o.Page-1)*pageSize, total)
	end := min(start+pageSize, total)
	body, _ := json.Marshal(map[string]any{"document": doc.Blocks[start:end]})
	meta, _ := json.Marshal(map[string]any{"page": o.Page, "pages": pages, "total": total, "first": min(start+1, total), "last": end, "document_format": "rich", "revision": doc.Revision})
	return previewOutput{Stage: "ready", MIME: "application/json", Body: body, Meta: meta}, true
}
