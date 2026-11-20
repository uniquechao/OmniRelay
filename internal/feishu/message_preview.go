package feishu

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"

	"github.com/uniquechao/OmniRelay/internal/drive"
)

var previewResourceKey = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

func (s *ChatSource) ResolvePreviewWiki(ctx context.Context, ref drive.Ref, token string) (drive.Ref, error) {
	var result struct {
		Node *struct {
			Kind  string `json:"obj_type"`
			Token string `json:"obj_token"`
			Title string `json:"title"`
		} `json:"node"`
	}
	err := s.userFiles(token).fileJSON(ctx, "wiki/v2/spaces/get_node?token="+url.QueryEscape(ref.Token), &result)
	if err != nil {
		return drive.Ref{}, err
	}
	if result.Node == nil || !previewResourceKey.MatchString(result.Node.Token) {
		return drive.Ref{}, drive.ErrUnavailable
	}
	ref.Kind, ref.Token = result.Node.Kind, result.Node.Token
	switch ref.Kind {
	case "sheet":
		ref.Kind = "sheets"
	case "bitable":
		ref.Kind = "base"
	case "docx", "file", "minutes":
	default:
		return drive.Ref{}, drive.ErrPreviewUnsupported
	}
	if result.Node.Title != "" {
		ref.Name = result.Node.Title
	}
	return ref, nil
}

type previewSheet struct {
	ID     string `json:"sheet_id"`
	Title  string `json:"title"`
	Hidden bool   `json:"hidden"`
	Grid   *struct {
		Rows    int `json:"row_count"`
		Columns int `json:"column_count"`
	} `json:"grid_properties"`
}

func (s *ChatSource) ReadPreviewSheet(ctx context.Context, ref drive.Ref, token string, o drive.MessagePreviewOptions) (drive.MessagePreviewResult, error) {
	if o.Entry != 0 {
		return drive.MessagePreviewResult{Stage: "invalid_entry"}, nil
	}
	f := s.userFiles(token)
	id := url.PathEscape(ref.Token)
	root := "sheets/v3/spreadsheets/" + id
	var info struct {
		Spreadsheet *struct {
			Title string `json:"title"`
		} `json:"spreadsheet"`
	}
	if err := f.fileJSON(ctx, root, &info); err != nil {
		return drive.MessagePreviewResult{}, err
	}
	var result struct {
		Sheets []previewSheet `json:"sheets"`
	}
	if err := f.fileJSON(ctx, root+"/sheets/query", &result); err != nil {
		return drive.MessagePreviewResult{}, err
	}
	if len(result.Sheets) > 200 {
		return drive.MessagePreviewResult{}, drive.ErrPartial
	}
	var sheets []previewSheet
	var names []string
	for _, sheet := range result.Sheets {
		if !sheet.Hidden && sheet.Grid != nil && previewResourceKey.MatchString(sheet.ID) {
			sheets = append(sheets, sheet)
			names = append(names, sheet.Title)
		}
	}
	if len(sheets) == 0 {
		return drive.MessagePreviewResult{Stage: "unsupported"}, nil
	}
	if o.Sheet < 0 || o.Sheet >= len(sheets) {
		return drive.MessagePreviewResult{Stage: "invalid_sheet"}, nil
	}
	sheet := sheets[o.Sheet]
	if sheet.Grid.Rows < 0 || sheet.Grid.Columns < 0 || sheet.Grid.Rows > 100000 || sheet.Grid.Columns > 512 {
		return drive.MessagePreviewResult{}, drive.ErrPartial
	}
	const rowsPerPage = 50
	pages := max(1, (sheet.Grid.Rows+rowsPerPage-1)/rowsPerPage)
	if o.Page < 1 || o.Page > pages {
		return drive.MessagePreviewResult{Stage: "invalid_page"}, nil
	}
	first := (o.Page-1)*rowsPerPage + 1
	last := min(o.Page*rowsPerPage, sheet.Grid.Rows)
	values := make([][]json.RawMessage, 0)
	if last >= first && sheet.Grid.Columns > 0 {
		rangeID := sheet.ID + "!A" + strconv.Itoa(first) + ":" + previewColumn(sheet.Grid.Columns) + strconv.Itoa(last)
		path := "sheets/v2/spreadsheets/" + id + "/values/" + url.PathEscape(rangeID) + "?valueRenderOption=FormattedValue"
		var page struct {
			Range *struct {
				Values [][]json.RawMessage `json:"values"`
			} `json:"valueRange"`
		}
		if err := f.fileJSON(ctx, path, &page); err != nil {
			return drive.MessagePreviewResult{}, err
		}
		if page.Range != nil && page.Range.Values != nil {
			values = page.Range.Values
		}
	}
	if len(values) > rowsPerPage {
		return drive.MessagePreviewResult{}, drive.ErrPartial
	}
	data, err := json.Marshal(values)
	if err != nil || len(data) > 2<<20 {
		return drive.MessagePreviewResult{}, drive.ErrPartial
	}
	meta, _ := json.Marshal(map[string]any{
		"page": o.Page, "pages": pages, "total": sheet.Grid.Rows, "first": min(first, last), "last": last,
		"sheets": names, "sheet": o.Sheet,
	})
	if info.Spreadsheet != nil && info.Spreadsheet.Title != "" {
		ref.Name = info.Spreadsheet.Title
	}
	return drive.MessagePreviewResult{Stage: "ready", Kind: "xlsx", Name: ref.Name, Meta: meta, Data: data}, nil
}

func previewColumn(column int) string {
	var name string
	for column > 0 {
		column--
		name = string(rune('A'+column%26)) + name
		column /= 26
	}
	return name
}
