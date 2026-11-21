package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	im "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
)

func (s *ChatSource) BotInGroup(ctx context.Context, target string) error {
	if !validIMID(target, "oc_") {
		return chat.ErrAccessLost
	}
	var data im.IsInChatChatMembersRespData
	if err := s.get(ctx, "/chats/"+target+"/members/is_in_chat", nil, &data); err != nil {
		return err
	}
	if data.IsInChat == nil {
		return chat.ErrUnavailable
	}
	if !*data.IsInChat {
		return chat.ErrAccessLost
	}
	return nil
}

func (s *ChatSource) Upload(ctx context.Context, kind, name string, data []byte) (string, error) {
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	path := "/files"
	switch kind {
	case "image":
		path = "/images"
		if err := writer.WriteField("image_type", "message"); err != nil {
			return "", chat.ErrAttachmentFailed
		}
	case "file":
		if err := writer.WriteField("file_type", "stream"); err != nil {
			return "", chat.ErrAttachmentFailed
		}
		if err := writer.WriteField("file_name", name); err != nil {
			return "", chat.ErrAttachmentFailed
		}
	default:
		return "", chat.ErrAttachmentFailed
	}
	if _, err := writer.CreateFormFile(kind, name); err != nil {
		return "", chat.ErrAttachmentFailed
	}
	// Retain the multipart framing separately from the uploaded bytes.
	prefix := bytes.Clone(form.Bytes())
	form.Reset()
	if err := writer.Close(); err != nil {
		return "", chat.ErrAttachmentFailed
	}
	body := io.MultiReader(bytes.NewReader(prefix), bytes.NewReader(data), bytes.NewReader(form.Bytes()))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, imURL+path, body)
	if err != nil {
		return "", chat.ErrAttachmentFailed
	}
	req.ContentLength = int64(len(prefix) + len(data) + form.Len())
	req.Header.Set("Content-Type", writer.FormDataContentType())
	var result map[string]string
	status, code, err := s.deliveryRequest(req, &result)
	key := result[kind+"_key"]
	if err != nil || status != http.StatusOK || code != 0 || !validIMID(key, map[string]string{"file": "file_", "image": "img_"}[kind]) {
		return "", chat.ErrAttachmentFailed
	}
	return key, nil
}

func (s *ChatSource) Deliver(ctx context.Context, target, bot, kind, content, uuid string) chat.DeliveryResult {
	if !validIMID(target, "oc_") {
		return chat.DeliveryResult{State: "failed", Reason: "invalid_target"}
	}
	body, _ := json.Marshal(map[string]string{"receive_id": target, "msg_type": kind, "content": content, "uuid": uuid})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, imURL+"/messages?receive_id_type=chat_id", bytes.NewReader(body))
	if err != nil {
		return chat.DeliveryResult{State: "failed", Reason: "delivery_unavailable"}
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	result := chat.DeliveryResult{State: "unknown", Reason: "receipt_unknown"}
	var data im.CreateMessageRespData
	status, code, err := s.deliveryRequest(req, &data)
	if err != nil {
		switch {
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			return chat.DeliveryResult{State: "failed", Reason: "access_lost"}
		case status == 0 && errorsBeforeDispatch(err):
			return chat.DeliveryResult{State: "failed", Reason: "delivery_unavailable"}
		default:
			return result
		}
	}
	if status >= 500 || status == http.StatusRequestTimeout || code == 230049 {
		return result
	}
	switch code {
	case 230001:
		return chat.DeliveryResult{State: "failed", Reason: "platform_rejected"}
	case 230002, 230006, 230013, 230027, 99991663, 99991668, 99991672:
		return chat.DeliveryResult{State: "failed", Reason: "access_lost"}
	case 230020, 99991400:
		return chat.DeliveryResult{State: "failed", Reason: "platform_rate_limited"}
	case 0:
		if status != http.StatusOK || !validIMID(value(data.MessageId), "om_") {
			return result
		}
	default:
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return chat.DeliveryResult{State: "failed", Reason: "access_lost"}
		}
		// An unclassified platform response is insufficient to permit retry.
		return result
	}
	if value(data.ChatId) != "" && value(data.ChatId) != target {
		return chat.DeliveryResult{State: "failed", Reason: "wrong_target_receipt"}
	}
	result.MessageID = value(data.MessageId)
	if value(data.ChatId) == target && value(data.MsgType) == kind && s.deliverySender(data.Sender, bot) {
		item := im.Message(data)
		message := deliveredMessage(target, &item)
		if message.ID == "" {
			verify, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			message, _ = s.ConfirmDelivery(verify, target, kind, bot, result.MessageID)
		}
		return chat.DeliveryResult{State: "success", MessageID: result.MessageID, Message: message}
	}
	return result
}

func (s *ChatSource) deliverySender(sender *im.Sender, bot string) bool {
	if sender == nil || value(sender.SenderType) != "app" ||
		(value(sender.TenantKey) != "" && value(sender.TenantKey) != s.tenant) {
		return false
	}
	switch value(sender.IdType) {
	case "app_id":
		return value(sender.Id) == s.check.appID
	case "open_id":
		return value(sender.Id) == bot
	default:
		return false
	}
}

func (s *ChatSource) ConfirmDelivery(ctx context.Context, target, kind, bot, messageID string) (orchestrator.ChatMessage, bool) {
	if !validIMID(messageID, "om_") {
		return orchestrator.ChatMessage{}, false
	}
	var data im.GetMessageRespData
	if err := s.get(ctx, "/messages/"+messageID, url.Values{}, &data); err != nil || len(data.Items) != 1 {
		return orchestrator.ChatMessage{}, false
	}
	item := data.Items[0]
	if item == nil || value(item.MessageId) != messageID || value(item.ChatId) != target ||
		value(item.MsgType) != kind || !s.deliverySender(item.Sender, bot) {
		return orchestrator.ChatMessage{}, false
	}
	return deliveredMessage(target, item), true
}

func deliveredMessage(target string, item *im.Message) orchestrator.ChatMessage {
	message, err := historyMessage(chat.HistoryQuery{ChatID: target}, item)
	if err != nil || (!message.Deleted && !json.Valid([]byte(message.Body))) {
		return orchestrator.ChatMessage{}
	}
	message.VisibleInChat = true
	return message
}

// Errors before a create request leaves this process are distinct from
// network or decode errors, which can occur after Feishu accepts the message.
type deliveryPreflightError struct{ error }

func errorsBeforeDispatch(err error) bool {
	_, ok := err.(deliveryPreflightError)
	return ok
}

func (s *ChatSource) deliveryRequest(req *http.Request, target any) (int, int, error) {
	token, err := s.credential(req.Context())
	if err != nil {
		return 0, 0, deliveryPreflightError{err}
	}
	if err := req.Context().Err(); err != nil {
		return 0, 0, deliveryPreflightError{err}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.check.client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	const limit = 512 * 1024
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	var envelope struct {
		Code *int            `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err != nil || len(b) > limit || json.Unmarshal(b, &envelope) != nil || envelope.Code == nil {
		return resp.StatusCode, 0, chat.ErrUnavailable
	}
	code := *envelope.Code
	if resp.StatusCode == http.StatusUnauthorized || code == 99991663 || code == 99991668 {
		s.mu.Lock()
		if s.token == token {
			s.tokenUntil = time.Time{}
		}
		s.mu.Unlock()
	}
	if code == 0 && (len(envelope.Data) == 0 || strings.TrimSpace(string(envelope.Data)) == "null" ||
		json.Unmarshal(envelope.Data, target) != nil) {
		return resp.StatusCode, code, chat.ErrUnavailable
	}
	return resp.StatusCode, code, nil
}
