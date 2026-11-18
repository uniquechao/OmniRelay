package feishu

import (
	"encoding/json"
	"time"

	im "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
)

type workerConfig struct {
	AppID, AppSecret, TenantKey, BotOpenID string
}

func mapInbound(cfg workerConfig, incoming *im.P2MessageReceiveV1, received time.Time) (orchestrator.InboundEvent, bool) {
	var event orchestrator.InboundEvent
	if incoming == nil || incoming.EventV2Base == nil || incoming.EventV2Base.Header == nil || incoming.Event == nil {
		return event, false
	}
	header, sender, message := incoming.EventV2Base.Header, incoming.Event.Sender, incoming.Event.Message
	if incoming.Schema != "2.0" || header.EventType != "im.message.receive_v1" ||
		header.AppID != cfg.AppID || header.TenantKey != cfg.TenantKey || header.EventID == "" ||
		sender == nil || value(sender.SenderType) != "user" || value(sender.TenantKey) != cfg.TenantKey ||
		sender.SenderId == nil || value(sender.SenderId.OpenId) == "" || value(sender.SenderId.OpenId) == cfg.BotOpenID ||
		message == nil || value(message.MessageId) == "" || value(message.ChatId) == "" {
		return event, false
	}
	kind := orchestrator.ChatKind(value(message.ChatType))
	switch kind {
	case orchestrator.ChatPrivate:
	case orchestrator.ChatGroup:
		if !mentionsBot(cfg, message.Mentions) {
			return event, false
		}
	default:
		return event, false
	}

	// The existing inbox carries text. Preserve the provider's structured content
	// for other message types so that filtering does not silently discard them.
	body := value(message.Content)
	if value(message.MessageType) == "text" {
		var content struct{ Text *string }
		if json.Unmarshal([]byte(body), &content) != nil || content.Text == nil {
			return event, false
		}
		body = *content.Text
	}
	return orchestrator.InboundEvent{
		AppID: header.AppID, TenantID: header.TenantKey, EventID: header.EventID,
		MessageID: value(message.MessageId), ChatID: value(message.ChatId), ChatKind: kind,
		ThreadID: value(message.ThreadId), ReplyToMessageID: value(message.ParentId),
		SenderOpenID: value(sender.SenderId.OpenId), ReceivedAt: received.UTC(), Text: body,
	}, true
}

func mentionsBot(cfg workerConfig, mentions []*im.MentionEvent) bool {
	for _, mention := range mentions {
		if mention != nil && mention.Id != nil && cfg.BotOpenID != "" && value(mention.Id.OpenId) == cfg.BotOpenID &&
			(value(mention.TenantKey) == "" || value(mention.TenantKey) == cfg.TenantKey) &&
			(value(mention.MentionedType) == "" || value(mention.MentionedType) == "bot") {
			return true
		}
	}
	return false
}

func value(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
