package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	im "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/drive"
)

func (s *ChatSource) CheckShare(ctx context.Context, r drive.Ref, chatID string) error {
	return s.checkShare(ctx, r, chatID, drive.References, drive.ErrRevoked, true)
}

func (s *ChatSource) CheckPreviewShare(ctx context.Context, r drive.Ref, chatID string) error {
	return s.checkShare(ctx, r, chatID, drive.MessageReferences, drive.ErrPreviewMessageDeleted, false)
}

func (s *ChatSource) checkShare(ctx context.Context, r drive.Ref, chatID string, references func(string, string) []drive.Ref, deleted error, resolveWiki bool) error {
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
		refs := references(value(m.MsgType), value(m.Body.Content))
		for _, live := range refs {
			if r.Kind == live.Kind && r.Token == live.Token {
				return nil
			}
		}
		if !resolveWiki {
			continue
		}
		var unavailable error
		for _, live := range refs {
			if live.Kind != "wiki" {
				continue
			}
			target, err := s.ResolveWiki(ctx, live.Token)
			if err != nil && !errors.Is(err, drive.ErrRevoked) {
				unavailable = err
			}
			if err == nil && target.Kind == r.Kind && target.Token == r.Token {
				return nil
			}
		}
		if unavailable != nil {
			return unavailable
		}
	}
	return drive.ErrRevoked
}

const fileLimit = 64 << 20

func (s *ChatSource) fileRequest(ctx context.Context, path string) (*http.Response, error) {
	return s.fileRequestWithToken(ctx, path, true)
}

func (s *ChatSource) fileRequestWithToken(ctx context.Context, path string, retryToken bool) (*http.Response, error) {
	token, err := s.credential(ctx)
	if err != nil {
		return nil, drive.ErrUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://open.feishu.cn/open-apis/"+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := http.Client{Transport: s.check.client.Transport, CheckRedirect: s.check.client.CheckRedirect}
	resp, err := client.Do(req)
	if err != nil {
		return nil, drive.ErrUnavailable
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		if responseFileError(resp.Body) == drive.ErrAppScopeMissing {
			return nil, drive.ErrAppScopeMissing
		}
		return nil, drive.ErrRevoked
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, drive.ErrRevoked
	}
	if resp.StatusCode == 401 {
		s.mu.Lock()
		if s.token == token {
			s.token = ""
			s.tokenUntil = time.Time{}
		}
		s.mu.Unlock()
		if retryToken {
			resp.Body.Close()
			return s.fileRequestWithToken(ctx, path, false)
		}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		raw := resp.Header.Get("Retry-After")
		delay := 30 * time.Second
		if seconds, err := strconv.ParseInt(raw, 10, 32); err == nil && seconds >= 0 {
			delay = time.Duration(seconds) * time.Second
		} else if when, err := http.ParseTime(raw); err == nil {
			delay = time.Until(when)
		}
		return nil, &drive.RetryAfterError{Delay: max(delay, time.Second)}
	}
	return nil, responseFileError(resp.Body)
}

func responseFileError(body io.Reader) error {
	var failure struct{ Code int }
	_ = json.NewDecoder(io.LimitReader(body, 32768)).Decode(&failure)
	return fileError(failure.Code)
}

func fileError(code int) error {
	switch code {
	case 99991672:
		return drive.ErrAppScopeMissing
	case 91402, 91403, 90303, 90304, 90305, 1770032, 1770033, 1254302, 1254040, 1254041, 1061003, 1061004, 2091005, 230006, 230011, 230027:
		return drive.ErrRevoked
	}
	return drive.ErrUnavailable
}

type fileGetter struct {
	get func(context.Context, string) (*http.Response, error)
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
		return fileError(*env.Code)
	}
	if len(env.Data) == 0 || string(env.Data) == "null" || json.Unmarshal(env.Data, out) != nil {
		return drive.ErrUnavailable
	}
	return nil
}

func (s *ChatSource) ReadFile(ctx context.Context, r drive.Ref, probe bool) (drive.Snapshot, error) {
	return (fileGetter{get: s.fileRequest}).readFile(ctx, r, probe)
}

func (s fileGetter) readFile(ctx context.Context, r drive.Ref, probe bool) (drive.Snapshot, error) {
	result := drive.Snapshot{Name: r.Name}
	id := url.PathEscape(r.Token)
	var path string
	switch r.Kind {
	case "base":
		return s.baseFile(ctx, id, probe)
	case "docx":
		return s.readDocument(ctx, r.Token, probe)
	case "minutes":
		path = "minutes/v1/minutes/" + id
		var meta struct{ Minute *struct{ Title string } }
		if err := s.fileJSON(ctx, path, &meta); err != nil {
			return result, err
		}
		if meta.Minute == nil {
			return result, drive.ErrUnavailable
		}
		result.Name = meta.Minute.Title
		path += "/transcript?file_format=txt"
	case "attachment", "image", "video":
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
		// 合法 JSON 附件可能没有文件名；只将明确的平台错误信封识别为失败。
		head, err := io.ReadAll(io.LimitReader(resp.Body, 32769))
		if err != nil {
			resp.Body.Close()
			return result, drive.ErrPartial
		}
		var envelope struct {
			Code int
			Msg  *string
		}
		if len(head) <= 32768 && json.Unmarshal(head, &envelope) == nil && envelope.Code != 0 {
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(head, &fields)
			if envelope.Msg != nil || len(fields) == 1 {
				resp.Body.Close()
				return result, fileError(envelope.Code)
			}
		}
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(head), resp.Body), resp.Body}
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

func (s *ChatSource) ResolveWiki(ctx context.Context, token string) (drive.Ref, error) {
	target, err := s.ResolveDocumentWiki(ctx, drive.Ref{Kind: "wiki", Token: token})
	if errors.Is(err, drive.ErrPreviewUnsupported) || target.Kind == "sheets" {
		return drive.Ref{}, drive.ErrRevoked
	}
	return target, err
}
