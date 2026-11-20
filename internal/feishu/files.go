package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	im "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/drive"
)

func (s *ChatSource) CheckShare(ctx context.Context, r drive.Ref, chatID string) error {
	return s.checkShare(ctx, r, chatID, drive.References, drive.ErrRevoked)
}

func (s *ChatSource) CheckPreviewShare(ctx context.Context, r drive.Ref, chatID string) error {
	return s.checkShare(ctx, r, chatID, drive.MessageReferences, drive.ErrPreviewMessageDeleted)
}

func (s *ChatSource) checkShare(ctx context.Context, r drive.Ref, chatID string, references func(string, string) []drive.Ref, deleted error) error {
	if r.ChatKind == "group" {
		if _, err := s.Group(ctx, chatID); err != nil {
			if errors.Is(err, chat.ErrAccessLost) {
				return drive.ErrRevoked
			}
			return drive.ErrUnavailable
		}
	}
	var data im.GetMessageRespData
	err := s.get(ctx, "/messages/"+url.PathEscape(r.MessageID), nil, &data)
	if errors.Is(err, chat.ErrAccessLost) {
		return drive.ErrRevoked
	}
	if err != nil {
		return drive.ErrUnavailable
	}
	for _, m := range data.Items {
		if m == nil || value(m.MessageId) != r.MessageID || value(m.ChatId) != chatID {
			continue
		}
		if m.Deleted != nil && *m.Deleted {
			return deleted
		}
		if m.Body == nil {
			continue
		}
		for _, live := range references(value(m.MsgType), value(m.Body.Content)) {
			if r.Kind == live.Kind && r.Token == live.Token {
				return nil
			}
		}
	}
	return drive.ErrRevoked
}

const fileLimit = 64 << 20

func (s *ChatSource) fileRequest(ctx context.Context, path string) (*http.Response, error) {
	token, err := s.credential(ctx)
	if err != nil {
		return nil, drive.ErrUnavailable
	}
	return s.fetchFile(ctx, path, token, false)
}

func (s *ChatSource) fetchFile(ctx context.Context, path, token string, user bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://open.feishu.cn/open-apis/"+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := http.Client{Transport: s.check.client.Transport, CheckRedirect: s.check.client.CheckRedirect}
	if user {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, drive.ErrUnavailable
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode == 403 || resp.StatusCode == 404 {
		return nil, drive.ErrRevoked
	}
	if resp.StatusCode == 401 {
		if user {
			return nil, drive.ErrPreviewAuthorization
		}
		s.mu.Lock()
		if s.token == token {
			s.token = ""
			s.tokenUntil = time.Time{}
		}
		s.mu.Unlock()
	}
	var failure struct{ Code int }
	_ = json.NewDecoder(io.LimitReader(resp.Body, 32768)).Decode(&failure)
	return nil, (fileGetter{user: user}).resultError(failure.Code)
}

func fileError(code int) error {
	switch code {
	case 91402, 91403, 90303, 90304, 90305, 99991672, 1770032, 1770033, 1254302, 1254040, 1254041, 1061003, 1061004, 2091005, 230006, 230011, 230027:
		return drive.ErrRevoked
	}
	return drive.ErrUnavailable
}

type fileGetter struct {
	get  func(context.Context, string) (*http.Response, error)
	user bool
}

func (f fileGetter) resultError(code int) error {
	if f.user && (code == 99991663 || code == 99991668 || code == 99991677) {
		return drive.ErrPreviewAuthorization
	}
	return fileError(code)
}

func (f fileGetter) fileJSON(ctx context.Context, path string, out any) error {
	resp, err := f.get(ctx, path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, fileLimit+1))
	if err != nil {
		return drive.ErrUnavailable
	}
	if len(b) > fileLimit {
		return drive.ErrPartial
	}
	var env struct {
		Code *int
		Data json.RawMessage
	}
	if json.Unmarshal(b, &env) != nil || env.Code == nil {
		return drive.ErrUnavailable
	}
	if *env.Code != 0 {
		return f.resultError(*env.Code)
	}
	if len(env.Data) == 0 || string(env.Data) == "null" || json.Unmarshal(env.Data, out) != nil {
		return drive.ErrUnavailable
	}
	return nil
}

func (s *ChatSource) ReadFile(ctx context.Context, r drive.Ref, probe bool) (drive.Snapshot, error) {
	return (fileGetter{get: s.fileRequest}).readFile(ctx, r, probe)
}

func (s *ChatSource) userFiles(token string) fileGetter {
	return fileGetter{user: true, get: func(ctx context.Context, path string) (*http.Response, error) {
		return s.fetchFile(ctx, path, token, true)
	}}
}

func (s *ChatSource) ReadPreviewFile(ctx context.Context, r drive.Ref, token string) (drive.Snapshot, error) {
	if r.Kind == "image" || r.Kind == "attachment" {
		return s.ReadFile(ctx, r, false)
	}
	if token == "" {
		return drive.Snapshot{}, drive.ErrPreviewAuthorization
	}
	return s.userFiles(token).readFile(ctx, r, false)
}

func (s fileGetter) readFile(ctx context.Context, r drive.Ref, probe bool) (drive.Snapshot, error) {
	result := drive.Snapshot{Name: r.Name}
	id := url.PathEscape(r.Token)
	var path string
	switch r.Kind {
	case "base":
		return s.baseFile(ctx, id, probe)
	case "docx":
		path = "docx/v1/documents/" + id
		var meta struct{ Document *struct{ Title string } }
		if err := s.fileJSON(ctx, path, &meta); err != nil {
			return result, err
		}
		if meta.Document == nil {
			return result, drive.ErrUnavailable
		}
		result.Name = meta.Document.Title
		var raw struct{ Content *string }
		if err := s.fileJSON(ctx, path+"/raw_content", &raw); err != nil {
			return result, err
		}
		if raw.Content == nil {
			return result, drive.ErrUnavailable
		}
		if !probe {
			result.Content = []byte(*raw.Content)
		}
		return result, nil
	case "minutes":
		path = "minutes/v1/minutes/" + id
		if !s.user {
			var meta struct{ Minute *struct{ Title string } }
			if err := s.fileJSON(ctx, path, &meta); err != nil {
				return result, err
			}
			if meta.Minute == nil {
				return result, drive.ErrUnavailable
			}
			result.Name = meta.Minute.Title
		}
		path += "/transcript?file_format=txt"
	case "attachment", "image":
		kind := "file"
		if r.Kind == "image" {
			kind = "image"
		}
		path = "im/v1/messages/" + url.PathEscape(r.MessageID) + "/resources/" + id + "?type=" + kind
	case "file":
		path = "drive/v1/files/" + id + "/download"
	default:
		return result, drive.ErrUnavailable
	}
	resp, err := s.get(ctx, path)
	if err != nil {
		return result, err
	}
	_, params, _ := mime.ParseMediaType(resp.Header.Get("Content-Disposition"))
	if name := params["filename"]; name != "" && (r.Kind == "file" || (r.Kind == "attachment" && r.Name == "")) {
		result.Name = name
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") && params["filename"] == "" {
		defer resp.Body.Close()
		var envelope struct{ Code *int }
		if json.NewDecoder(io.LimitReader(resp.Body, 32768)).Decode(&envelope) == nil && envelope.Code != nil {
			return result, s.resultError(*envelope.Code)
		}
		return result, drive.ErrUnavailable
	}
	if probe {
		resp.Body.Close()
	} else {
		result.Stream = resp.Body
	}
	return result, nil
}

func (s fileGetter) basePages(ctx context.Context, path string, visit func(json.RawMessage) error) error {
	seen := map[string]bool{}
	cursor := ""
	for {
		var page struct {
			Items *[]json.RawMessage
			More  *bool  `json:"has_more"`
			Next  string `json:"page_token"`
		}
		err := s.fileJSON(ctx, path+"?page_size=100&page_token="+url.QueryEscape(cursor), &page)
		if err != nil {
			if cursor != "" && errors.Is(err, drive.ErrUnavailable) {
				return drive.ErrPartial
			}
			return err
		}
		if page.Items == nil || page.More == nil {
			return drive.ErrPartial
		}
		for _, item := range *page.Items {
			if err := visit(item); err != nil {
				return err
			}
		}
		if !*page.More {
			return nil
		}
		if page.Next == "" || seen[page.Next] {
			return drive.ErrPartial
		}
		cursor = page.Next
		seen[cursor] = true
	}
}
func (s fileGetter) baseFile(ctx context.Context, id string, probe bool) (drive.Snapshot, error) {
	path := "bitable/v1/apps/" + id
	var meta struct{ App *struct{ Name string } }
	var result drive.Snapshot
	if err := s.fileJSON(ctx, path, &meta); err != nil {
		return result, err
	}
	if meta.App == nil {
		return result, drive.ErrUnavailable
	}
	result.Name = meta.App.Name
	type table struct {
		ID      string            `json:"table_id"`
		Name    string            `json:"name"`
		Records []json.RawMessage `json:"records"`
	}
	tables := []table{}
	size := 0
	err := s.basePages(ctx, path+"/tables", func(item json.RawMessage) error {
		var t table
		if json.Unmarshal(item, &t) != nil || t.ID == "" {
			return drive.ErrPartial
		}
		t.Records = []json.RawMessage{}
		err := s.basePages(ctx, path+"/tables/"+url.PathEscape(t.ID)+"/records", func(row json.RawMessage) error {
			if !probe {
				size += len(row)
				if size > fileLimit {
					return drive.ErrPartial
				}
				t.Records = append(t.Records, row)
			}
			return nil
		})
		if err == nil && !probe {
			tables = append(tables, t)
		}
		return err
	})
	if err == nil && !probe {
		result.Content, err = json.Marshal(tables)
	}
	return result, err
}
