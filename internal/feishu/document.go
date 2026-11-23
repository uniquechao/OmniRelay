package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/uniquechao/OmniRelay/internal/drive"

	"github.com/uniquechao/OmniRelay/internal/identity"
)

const documentLimit = 12 << 20

type docBlock struct {
	ID       string                     `json:"block_id"`
	Kind     int                        `json:"block_type"`
	Children []string                   `json:"children"`
	Fields   map[string]json.RawMessage `json:"-"`
}

func (b *docBlock) UnmarshalJSON(data []byte) error {
	type basic docBlock
	if err := json.Unmarshal(data, (*basic)(b)); err != nil {
		return err
	}
	return json.Unmarshal(data, &b.Fields)
}

type docStyle struct {
	Bold      bool `json:"bold"`
	Italic    bool `json:"italic"`
	Underline bool `json:"underline"`
	Strike    bool `json:"strikethrough"`
	Code      bool `json:"inline_code"`
	Link      *struct {
		URL string `json:"url"`
	} `json:"link"`
	Color      int `json:"text_color"`
	Background int `json:"background_color"`
}
type docElement struct {
	Text *struct {
		Content string   `json:"content"`
		Style   docStyle `json:"text_element_style"`
	} `json:"text_run"`
	Equation *struct {
		Content string `json:"content"`
	} `json:"equation"`
	MentionUser *struct {
		ID    string   `json:"user_id"`
		Style docStyle `json:"text_element_style"`
	} `json:"mention_user"`
	MentionDoc *struct {
		Title string   `json:"title"`
		URL   string   `json:"url"`
		Style docStyle `json:"text_element_style"`
	} `json:"mention_doc"`
	Reminder *struct {
		Expire string   `json:"expire_time"`
		Style  docStyle `json:"text_element_style"`
	} `json:"reminder"`
	Inline *struct {
		ID string `json:"block_id"`
	} `json:"inline_block"`
}
type docText struct {
	Elements []docElement `json:"elements"`
	Style    struct {
		Align int  `json:"align"`
		Done  bool `json:"done"`
	} `json:"style"`
}

func documentRun(text string, style docStyle) drive.DocumentRun {
	run := drive.DocumentRun{Text: text, Bold: style.Bold, Italic: style.Italic, Underline: style.Underline, Strike: style.Strike, Code: style.Code, Color: style.Color, Background: style.Background}
	if style.Link != nil {
		run.Link = style.Link.URL
	}
	return run
}

func (s fileGetter) readDocument(ctx context.Context, id string, probe bool) (drive.Snapshot, error) {
	var meta struct {
		Document *struct {
			Title    string `json:"title"`
			Revision int64  `json:"revision_id"`
		} `json:"document"`
	}
	base := "docx/v1/documents/" + url.PathEscape(id)
	if err := s.fileJSON(ctx, base, &meta); err != nil {
		return drive.Snapshot{}, err
	}
	if meta.Document == nil {
		return drive.Snapshot{}, drive.ErrUnavailable
	}
	result := drive.Snapshot{Name: meta.Document.Title}
	if probe {
		return result, nil
	}
	blocks := map[string]docBlock{}
	order := []string{}
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 100; page++ {
		// Feishu can reject explicitly numbered revisions even when the bot can
		// read the latest document. Read latest blocks and fence publication
		// with the metadata revision check below instead of querying history.
		q := url.Values{"page_size": {"500"}, "document_revision_id": {"-1"}, "user_id_type": {"open_id"}}
		if cursor != "" {
			q.Set("page_token", cursor)
		}
		var reply struct {
			Items *[]docBlock `json:"items"`
			More  *bool       `json:"has_more"`
			Next  string      `json:"page_token"`
		}
		if err := s.fileJSON(ctx, base+"/blocks?"+q.Encode(), &reply); err != nil {
			return result, err
		}
		if reply.Items == nil || reply.More == nil {
			return result, drive.ErrPartial
		}
		for _, b := range *reply.Items {
			if b.ID == "" || blocks[b.ID].ID != "" {
				return result, drive.ErrPartial
			}
			blocks[b.ID] = b
			order = append(order, b.ID)
		}
		if len(blocks) > 10000 {
			return result, drive.ErrPartial
		}
		if !*reply.More {
			cursor = ""
			break
		}
		if reply.Next == "" || seen[reply.Next] || page == 99 {
			return result, drive.ErrPartial
		}
		seen[reply.Next] = true
		cursor = reply.Next
	}
	users, err := s.documentUsers(ctx, blocks)
	if err != nil {
		return result, err
	}
	referenced := map[string]bool{}
	for _, b := range blocks {
		for _, child := range b.Children {
			referenced[child] = true
		}
		if b.Kind == 31 {
			var table struct {
				Cells []string `json:"cells"`
			}
			_ = json.Unmarshal(b.Fields["table"], &table)
			for _, cell := range table.Cells {
				referenced[cell] = true
			}
		}
	}
	roots := []string{}
	for _, bid := range order {
		if !referenced[bid] {
			roots = append(roots, bid)
		}
	}
	if len(roots) == 0 && len(blocks) > 0 {
		return result, drive.ErrPartial
	}
	bytesUsed := 0
	images := map[string]*drive.DocumentImage{}
	active := map[string]bool{}
	visited := map[string]bool{}
	var convert func(string, int) ([]drive.DocumentBlock, error)
	convert = func(bid string, depth int) ([]drive.DocumentBlock, error) {
		b, ok := blocks[bid]
		if !ok || active[bid] || visited[bid] || depth > 40 {
			return nil, drive.ErrPartial
		}
		active[bid] = true
		visited[bid] = true
		defer delete(active, bid)
		kind := "text"
		field := "text"
		node := drive.DocumentBlock{Type: kind}
		switch {
		case b.Kind == 1:
			node.Type = "container"
			field = "page"
		case b.Kind >= 3 && b.Kind <= 11:
			node.Type = "heading"
			node.Level = b.Kind - 2
			field = fmt.Sprintf("heading%d", node.Level)
		case b.Kind == 12:
			node.Type = "bullet"
			field = "bullet"
		case b.Kind == 13:
			node.Type = "ordered"
			field = "ordered"
		case b.Kind == 14:
			node.Type = "code"
			field = "code"
		case b.Kind == 15:
			node.Type = "quote"
			field = "quote"
		case b.Kind == 17:
			node.Type = "todo"
			field = "todo"
		case b.Kind == 22:
			node.Type = "divider"
		case b.Kind == 27:
			node.Type = "image"
			var img struct {
				Token  string `json:"token"`
				Width  int    `json:"width"`
				Height int    `json:"height"`
			}
			if json.Unmarshal(b.Fields["image"], &img) != nil || img.Token == "" {
				return nil, drive.ErrPartial
			}
			asset := images[img.Token]
			if asset == nil {
				q := url.Values{"extra": {`{"drive_route_token":"` + id + `"}`}}
				resp, err := s.get(ctx, "drive/v1/medias/"+url.PathEscape(img.Token)+"/download?"+q.Encode())
				if err != nil {
					return nil, err
				}
				data, err := io.ReadAll(io.LimitReader(resp.Body, int64(documentLimit-bytesUsed)+1))
				resp.Body.Close()
				if err != nil || len(data) > documentLimit-bytesUsed {
					return nil, drive.ErrPartial
				}
				mime := http.DetectContentType(data)
				if !strings.HasPrefix(mime, "image/") || mime == "image/svg+xml" {
					return nil, drive.ErrPartial
				}
				dimensions, _, decodeErr := image.DecodeConfig(bytes.NewReader(data))
				if decodeErr != nil || dimensions.Width <= 0 || dimensions.Height <= 0 || int64(dimensions.Width)*int64(dimensions.Height) > 40000000 {
					return nil, drive.ErrPartial
				}
				bytesUsed += len(data)
				asset = &drive.DocumentImage{MIME: mime, Data: data, Width: img.Width, Height: img.Height}
				images[img.Token] = asset
			}
			node.Image = asset
		case b.Kind == 31:
			node.Type = "table"
			var table struct {
				Property struct {
					Columns int `json:"column_size"`
					Rows    int `json:"row_size"`
				} `json:"property"`
				Cells []string `json:"cells"`
			}
			if json.Unmarshal(b.Fields["table"], &table) != nil || table.Property.Columns < 1 {
				return nil, drive.ErrPartial
			}
			cells := table.Cells
			if len(cells) == 0 {
				cells = b.Children
			}
			if len(cells)%table.Property.Columns != 0 {
				return nil, drive.ErrPartial
			}
			for i, cell := range cells {
				children, err := convert(cell, depth+1)
				if err != nil {
					return nil, err
				}
				if i%table.Property.Columns == 0 {
					node.Rows = append(node.Rows, [][]drive.DocumentBlock{})
				}
				last := len(node.Rows) - 1
				node.Rows[last] = append(node.Rows[last], children)
			}
			return []drive.DocumentBlock{node}, nil
		case b.Kind == 24 || b.Kind == 25 || b.Kind == 32 || b.Kind == 34:
			node.Type = "container"
		case b.Kind != 2:
			node.Type = "container"
			node.Runs = []drive.DocumentRun{{Text: "[此内容块暂不支持预览]"}}
		}
		if raw := b.Fields[field]; len(raw) > 0 {
			var text docText
			if json.Unmarshal(raw, &text) != nil {
				return nil, drive.ErrPartial
			}
			node.Checked = text.Style.Done
			node.Align = map[int]string{1: "left", 2: "center", 3: "right"}[text.Style.Align]
			for _, element := range text.Elements {
				switch {
				case element.Text != nil:
					node.Runs = append(node.Runs, documentRun(element.Text.Content, element.Text.Style))
				case element.Equation != nil:
					node.Runs = append(node.Runs, drive.DocumentRun{Text: element.Equation.Content, Code: true})
				case element.MentionUser != nil:
					id := element.MentionUser.ID
					name := users[id]
					run := documentRun("@"+name+" ", element.MentionUser.Style)
					run.Kind = "person"
					if name == "" {
						run.Text = "[未解析用户] "
						run.Unresolved = true
					}
					if strings.HasPrefix(id, "ou_") {
						run.PersonKey = identity.FeishuPersonKey(id)
					}
					node.Runs = append(node.Runs, run)
				case element.MentionDoc != nil:
					title := strings.TrimSpace(element.MentionDoc.Title)
					if title == "" {
						title = "关联文档"
					}
					run := documentRun(title, element.MentionDoc.Style)
					run.Kind = "document"
					link := element.MentionDoc.URL
					if decoded, err := url.QueryUnescape(link); err == nil {
						link = decoded
					}
					if u, err := url.Parse(link); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
						run.Link = u.String()
					} else {
						run.Link = ""
					}
					node.Runs = append(node.Runs, run)
				case element.Reminder != nil:
					label := "[日期未解析]"
					if ms, err := strconv.ParseInt(element.Reminder.Expire, 10, 64); err == nil {
						label = time.UnixMilli(ms).UTC().Format(time.RFC3339)
					}
					run := documentRun(label, element.Reminder.Style)
					run.Kind = "date"
					node.Runs = append(node.Runs, run)
				case element.Inline != nil:
					node.Runs = append(node.Runs, drive.DocumentRun{Text: "[内嵌内容]"})
				default:
					node.Runs = append(node.Runs, drive.DocumentRun{Text: "[暂不支持的行内内容]"})
				}
			}
		}
		for _, child := range b.Children {
			nodes, err := convert(child, depth+1)
			if err != nil {
				return nil, err
			}
			node.Children = append(node.Children, nodes...)
		}
		if node.Type == "container" && len(node.Runs) == 0 {
			return node.Children, nil
		}
		return []drive.DocumentBlock{node}, nil
	}
	doc := drive.DocumentSnapshot{Schema: drive.DocumentSchema, Revision: meta.Document.Revision, Blocks: []drive.DocumentBlock{}}
	for _, root := range roots {
		nodes, err := convert(root, 0)
		if err != nil {
			return result, err
		}
		doc.Blocks = append(doc.Blocks, nodes...)
	}
	if len(visited) != len(blocks) {
		return result, drive.ErrPartial
	}
	var after struct {
		Document *struct {
			Revision int64 `json:"revision_id"`
		} `json:"document"`
	}
	if err := s.fileJSON(ctx, base, &after); err != nil {
		return result, err
	}
	if after.Document == nil || after.Document.Revision != meta.Document.Revision {
		return result, drive.ErrPartial
	}
	data, err := json.Marshal(doc)
	if err != nil || len(data) > 16<<20 {
		return result, drive.ErrPartial
	}
	result.Content = data
	return result, nil
}

// Contact lookup supplements the system directory only for IDs referenced by this
// authorized document. A contact permission failure never invents a user's name.
func (s fileGetter) documentUsers(ctx context.Context, blocks map[string]docBlock) (map[string]string, error) {
	seen := map[string]bool{}
	for _, block := range blocks {
		for _, raw := range block.Fields {
			var text docText
			if json.Unmarshal(raw, &text) != nil {
				continue
			}
			for _, element := range text.Elements {
				if u := element.MentionUser; u != nil && strings.HasPrefix(u.ID, "ou_") && len(u.ID) <= 256 {
					seen[u.ID] = true
				}
			}
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) > 200 {
		return nil, drive.ErrPartial
	}
	names := map[string]string{}
	if len(ids) > 0 && s.resolveUsers != nil {
		found, err := s.resolveUsers(ctx, ids)
		if err != nil {
			return nil, drive.ErrUnavailable
		}
		for id, name := range found {
			if seen[id] {
				names[id] = strings.TrimSpace(name)
			}
		}
	}
	lookups := 0
	for _, id := range ids {
		if names[id] != "" {
			continue
		}
		if lookups >= 64 {
			continue
		}
		lookups++
		var reply struct {
			User *struct {
				Name string `json:"name"`
				ID   string `json:"open_id"`
			} `json:"user"`
		}
		err := s.fileJSON(ctx, "contact/v3/users/"+url.PathEscape(id)+"?user_id_type=open_id", &reply)
		if ctx.Err() != nil {
			return nil, drive.ErrUnavailable
		}
		if err == nil && reply.User != nil && (reply.User.ID == "" || reply.User.ID == id) {
			names[id] = strings.TrimSpace(reply.User.Name)
		}
	}
	return names, nil
}
