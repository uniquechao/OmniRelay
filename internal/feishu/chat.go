package feishu

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	im "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
)

const imURL = "https://open.feishu.cn/open-apis/im/v1"

type ChatSource struct {
	documentUsers func(context.Context, []string) (map[string]string, error)
	check         *BotCheck
	tenant        string
	mu            sync.Mutex
	token         string
	tokenUntil    time.Time
}

func NewChatSource(check *BotCheck, tenant string) *ChatSource {
	return &ChatSource{check: check, tenant: tenant}
}

func (s *ChatSource) ConfigureDocumentUsers(resolve func(context.Context, []string) (map[string]string, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.documentUsers = resolve
}

func (s *ChatSource) credential(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Now().Before(s.tokenUntil) {
		return s.token, nil
	}
	token, failure := s.check.TenantToken(ctx)
	if failure != "" {
		return "", chat.ErrUnavailable
	}
	s.token, s.tokenUntil = token, time.Now().Add(5*time.Minute)
	return token, nil
}

func (s *ChatSource) Bot(ctx context.Context) (chat.Member, error) {
	token, err := s.credential(ctx)
	if err != nil {
		return chat.Member{}, err
	}
	bot, failure := s.check.BotInfo(ctx, token)
	if failure != "" {
		return chat.Member{}, chat.ErrUnavailable
	}
	return chat.Member{OpenID: bot.OpenID, Name: bot.Name}, nil
}

func (s *ChatSource) get(ctx context.Context, path string, params url.Values, target any) error {
	token, err := s.credential(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imURL+path+"?"+params.Encode(), nil)
	if err != nil {
		return chat.ErrUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := s.check.client.Do(req)
	if err != nil {
		return chat.ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return chat.ErrUnavailable
	}
	var payload struct {
		Code *int            `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	const maxPageSize = 16 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageSize+1))
	if err != nil || len(body) > maxPageSize {
		return chat.ErrUnavailable
	}
	decodeErr := json.Unmarshal(body, &payload)
	code := 0
	if payload.Code != nil {
		code = *payload.Code
	}
	switch {
	case resp.StatusCode == 401 || code == 99991663 || code == 99991668:
		s.mu.Lock()
		if s.token == token {
			s.tokenUntil = time.Time{}
		}
		s.mu.Unlock()
		return chat.ErrUnavailable
	case resp.StatusCode == 403 || resp.StatusCode == 404:
		return chat.ErrAccessLost
	}
	switch code {
	case 230002, 230006, 230013, 230027, 231203, 232010, 232011, 232014, 232024, 99991672:
		return chat.ErrAccessLost
	case 230073, 230110:
		return chat.ErrPartial
	case 230001, 232001:
		if params.Get("page_token") != "" {
			return chat.ErrCursorExpired
		}
	}
	if resp.StatusCode != 200 || decodeErr != nil || payload.Code == nil || code != 0 ||
		len(payload.Data) == 0 || string(payload.Data) == "null" || json.Unmarshal(payload.Data, target) != nil {
		return chat.ErrUnavailable
	}
	return nil
}

func pageCursor(previous string, more *bool, token *string) (string, error) {
	if more == nil {
		return "", chat.ErrUnavailable
	}
	if !*more {
		return "", nil
	}
	if value(token) == "" || value(token) == previous || strings.ContainsRune(value(token), 0) {
		return "", chat.ErrUnavailable
	}
	return *token, nil
}

func (s *ChatSource) Groups(ctx context.Context, cursor string) (chat.Page[chat.Group], error) {
	var data im.ListChatRespData
	var page chat.Page[chat.Group]
	params := url.Values{"page_size": {"50"}}
	if cursor != "" {
		params.Set("page_token", cursor)
	}
	if err := s.get(ctx, "/chats", params, &data); err != nil {
		return page, err
	}
	for _, group := range data.Items {
		if group == nil || !validIMID(value(group.ChatId), "oc_") {
			return page, chat.ErrUnavailable
		}
		if (group.External != nil && *group.External) || (value(group.TenantKey) != "" && value(group.TenantKey) != s.tenant) ||
			value(group.ChatMode) == "p2p" {
			continue
		}
		page.Items = append(page.Items, chat.Group{ID: *group.ChatId, Name: value(group.Name)})
	}
	var err error
	page.Next, err = pageCursor(cursor, data.HasMore, data.PageToken)
	return page, err
}

func (s *ChatSource) Group(ctx context.Context, id string) (chat.Group, error) {
	if !validIMID(id, "oc_") {
		return chat.Group{}, chat.ErrAccessLost
	}
	var data im.GetChatRespData
	if err := s.get(ctx, "/chats/"+url.PathEscape(id), nil, &data); err != nil {
		return chat.Group{}, err
	}
	if (data.External != nil && *data.External) || (value(data.TenantKey) != "" && value(data.TenantKey) != s.tenant) ||
		(value(data.ChatMode) != "group" && value(data.ChatMode) != "topic") {
		return chat.Group{}, chat.ErrAccessLost
	}
	return chat.Group{ID: id, Name: value(data.Name)}, nil
}

func (s *ChatSource) Members(ctx context.Context, id, cursor string) (chat.Page[chat.Member], error) {
	var page chat.Page[chat.Member]
	if !validIMID(id, "oc_") {
		return page, chat.ErrAccessLost
	}
	params := url.Values{"page_size": {"100"}, "member_id_type": {"open_id"}}
	if cursor != "" {
		params.Set("page_token", cursor)
	}
	var data im.GetChatMembersRespData
	if err := s.get(ctx, "/chats/"+url.PathEscape(id)+"/members", params, &data); err != nil {
		return page, err
	}
	for _, member := range data.Items {
		if member == nil {
			return page, chat.ErrUnavailable
		}
		if value(member.TenantKey) != "" && value(member.TenantKey) != s.tenant {
			continue
		}
		if !validIMID(value(member.MemberId), "ou_") ||
			(value(member.MemberIdType) != "" && value(member.MemberIdType) != "open_id") {
			return page, chat.ErrUnavailable
		}
		page.Items = append(page.Items, chat.Member{OpenID: *member.MemberId, Name: value(member.Name)})
	}
	var err error
	page.Next, err = pageCursor(cursor, data.HasMore, data.PageToken)
	if err == nil && data.TriggerSecurityConfLimit != nil && *data.TriggerSecurityConfLimit {
		err = chat.ErrPartial
	}
	return page, err
}

func (s *ChatSource) History(ctx context.Context, query chat.HistoryQuery) (chat.Page[orchestrator.ChatMessage], error) {
	var page chat.Page[orchestrator.ChatMessage]
	if !validIMID(query.ChatID, "oc_") {
		return page, chat.ErrAccessLost
	}
	containerType, containerID := "chat", query.ChatID
	if query.ThreadID != "" {
		if !validIMID(query.ThreadID, "omt_") {
			return page, chat.ErrPartial
		}
		containerType, containerID = "thread", query.ThreadID
	}
	params := url.Values{"container_id_type": {containerType}, "container_id": {containerID},
		"sort_type": {"ByCreateTimeAsc"}, "page_size": {strconv.Itoa(query.Limit)}, "with_sender_name": {"true"}}
	if query.Cursor != "" {
		params.Set("page_token", query.Cursor)
	}
	if query.Start != nil && query.ThreadID == "" {
		params.Set("start_time", strconv.FormatInt(query.Start.Unix(), 10))
	}
	if query.End != nil && query.ThreadID == "" {
		params.Set("end_time", strconv.FormatInt(query.End.Unix(), 10))
	}
	var data im.ListMessageRespData
	if err := s.get(ctx, "/messages", params, &data); err != nil {
		return page, err
	}
	for _, item := range data.Items {
		msg, err := historyMessage(query, item)
		if err != nil {
			return page, err
		}
		page.Items = append(page.Items, msg)
	}
	var err error
	page.Next, err = pageCursor(query.Cursor, data.HasMore, data.PageToken)
	return page, err
}

func historyMessage(query chat.HistoryQuery, item *im.Message) (orchestrator.ChatMessage, error) {
	var msg orchestrator.ChatMessage
	if item == nil || !validIMID(value(item.MessageId), "om_") || value(item.ChatId) != query.ChatID {
		return msg, chat.ErrUnavailable
	}
	msg.ID, msg.ChatID, msg.ThreadID, msg.ParentID = *item.MessageId, query.ChatID, value(item.ThreadId), value(item.ParentId)
	if query.ThreadID != "" {
		if msg.ThreadID != "" && msg.ThreadID != query.ThreadID {
			return msg, chat.ErrUnavailable
		}
		msg.ThreadID = query.ThreadID
	}
	msg.SentAt, msg.UpdatedAt = providerTime(value(item.CreateTime)), providerTime(value(item.UpdateTime))
	if msg.SentAt.IsZero() {
		return msg, chat.ErrUnavailable
	}
	msg.Type = value(item.MsgType)
	msg.Deleted = item.Deleted != nil && *item.Deleted
	if item.Body != nil && !msg.Deleted {
		msg.Body = value(item.Body.Content)
	}
	if item.Sender != nil {
		msg.SenderKind, msg.SenderName = value(item.Sender.SenderType), value(item.Sender.SenderName)
		msg.SenderID = value(item.Sender.Id)
		if value(item.Sender.OpenBotId) != "" {
			msg.SenderID = *item.Sender.OpenBotId
		}
	}
	return msg, nil
}

func providerTime(raw string) time.Time {
	stamp, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || stamp <= 0 || stamp > 253402300799999 {
		return time.Time{}
	}
	return time.UnixMilli(stamp).UTC()
}

func validIMID(id, prefix string) bool {
	if !strings.HasPrefix(id, prefix) || len(id) <= len(prefix) || len(id) > 160 {
		return false
	}
	for _, ch := range id {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-') {
			return false
		}
	}
	return true
}

var _ chat.Source = (*ChatSource)(nil)
