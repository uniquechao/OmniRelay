package drive

import "encoding/json"

// 工作表保存格式化的单元格，预览分页只读取同步结果。
type ChatSheet struct {
	Name  string
	Rows  int
	Pages []json.RawMessage
}

const ChatSheetRowsPerPage = 50

func renderChatWorkbook(content []byte, title string, o MessagePreviewOptions) (MessagePreviewResult, error) {
	var sheets []ChatSheet
	if err := json.Unmarshal(content, &sheets); err != nil {
		return MessagePreviewResult{}, err
	}
	if o.Entry != 0 {
		return MessagePreviewResult{Stage: "invalid_entry"}, nil
	}
	if o.Sheet < 0 || o.Sheet >= len(sheets) {
		return MessagePreviewResult{Stage: "invalid_sheet"}, nil
	}
	sheet := sheets[o.Sheet]
	if o.Page < 1 || o.Page > len(sheet.Pages) {
		return MessagePreviewResult{Stage: "invalid_page"}, nil
	}
	names := make([]string, len(sheets))
	for i, s := range sheets {
		names[i] = s.Name
	}
	meta, err := json.Marshal(map[string]any{"page": o.Page, "pages": len(sheet.Pages), "total": sheet.Rows,
		"first": min((o.Page-1)*ChatSheetRowsPerPage+1, sheet.Rows), "last": min(o.Page*ChatSheetRowsPerPage, sheet.Rows),
		"sheets": names, "sheet": o.Sheet})
	return MessagePreviewResult{Stage: "ready", Kind: "xlsx", Name: title, Meta: meta, Data: sheet.Pages[o.Page-1]}, err
}
