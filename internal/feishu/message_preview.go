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

func (s *ChatSource) ResolveDocumentWiki(ctx context.Context, ref drive.Ref) (drive.Ref, error) {
	var result struct {
		Node *struct {
			Kind  string `json:"obj_type"`
			Token string `json:"obj_token"`
			Title string `json:"title"`
		} `json:"node"`
	}
	err := (fileGetter{get: s.fileRequest}).fileJSON(ctx, "wiki/v2/spaces/get_node?token="+url.QueryEscape(ref.Token), &result)
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

func (s *ChatSource) ReadDocument(ctx context.Context, ref drive.Ref) (drive.Snapshot, error) {
	if ref.Kind != "sheets" {
		return s.ReadFile(ctx, ref, false)
	}
	f := fileGetter{get: s.fileRequest}
	snap := drive.Snapshot{Name: ref.Name}
	id := url.PathEscape(ref.Token)
	root := "sheets/v3/spreadsheets/" + id
	var info struct {
		Spreadsheet *struct {
			Title string `json:"title"`
		} `json:"spreadsheet"`
	}
	if err := f.fileJSON(ctx, root, &info); err != nil {
		return snap, err
	}
	if info.Spreadsheet != nil && info.Spreadsheet.Title != "" {
		snap.Name = info.Spreadsheet.Title
	}
	var result struct {
		Sheets []previewSheet `json:"sheets"`
	}
	if err := f.fileJSON(ctx, root+"/sheets/query", &result); err != nil {
		return snap, err
	}
	if len(result.Sheets) > 200 {
		return snap, drive.ErrPartial
	}
	var workbook []drive.ChatSheet
	size := 0
	for _, sheet := range result.Sheets {
		if sheet.Hidden || sheet.Grid == nil || !previewResourceKey.MatchString(sheet.ID) {
			continue
		}
		if sheet.Grid.Rows < 0 || sheet.Grid.Columns < 0 || sheet.Grid.Rows > 100000 || sheet.Grid.Columns > 512 {
			return snap, drive.ErrPartial
		}
		tab := drive.ChatSheet{Name: sheet.Title, Rows: sheet.Grid.Rows}
		for first := 1; first <= max(1, sheet.Grid.Rows); first += drive.ChatSheetRowsPerPage {
			last := min(first+drive.ChatSheetRowsPerPage-1, sheet.Grid.Rows)
			data, err := readSheetRange(ctx, f, id, sheet, first, last)
			if err != nil {
				return snap, err
			}
			size += len(data)
			if size > 16<<20 {
				return snap, drive.ErrPartial
			}
			tab.Pages = append(tab.Pages, data)
		}
		workbook = append(workbook, tab)
	}
	if len(workbook) == 0 {
		return snap, drive.ErrPreviewUnsupported
	}
	data, err := json.Marshal(workbook)
	snap.Content = data
	return snap, err
}

func readSheetRange(ctx context.Context, f fileGetter, id string, sheet previewSheet, first, last int) (json.RawMessage, error) {
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
			return nil, err
		}
		if page.Range != nil && page.Range.Values != nil {
			values = page.Range.Values
		}
	}
	if len(values) > drive.ChatSheetRowsPerPage {
		return nil, drive.ErrPartial
	}
	data, err := json.Marshal(values)
	if err != nil || len(data) > 2<<20 {
		return nil, drive.ErrPartial
	}
	return data, nil
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
