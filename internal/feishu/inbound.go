package feishu

import (
	"encoding/json"
	"time"

	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	im "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
)

type workerConfig struct {
	AppID, AppSecret, TenantKey, BotOpenID string
	Archive                                bool
	ObserveReceipts                        bool
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
		ProviderAt: providerTime(value(message.CreateTime)),
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

func archiveEnvelope(cfg workerConfig, base *larkevent.EventV2Base, eventType string) bool {
	return base != nil && base.Header != nil && base.Schema == "2.0" && base.Header.EventType == eventType &&
		base.Header.AppID == cfg.AppID && base.Header.TenantKey == cfg.TenantKey && base.Header.EventID != ""
}

func mapArchive(cfg workerConfig, incoming *im.P2MessageReceiveV1, received time.Time) *orchestrator.ArchiveEvent {
	if incoming == nil || !archiveEnvelope(cfg, incoming.EventV2Base, "im.message.receive_v1") || incoming.Event == nil {
		return nil
	}
	m, sender := incoming.Event.Message, incoming.Event.Sender
	if m == nil || sender == nil || sender.SenderId == nil ||
		!validIMID(value(m.ChatId), "oc_") || !validIMID(value(m.MessageId), "om_") ||
		(value(sender.TenantKey) != "" && value(sender.TenantKey) != cfg.TenantKey) {
		return nil
	}
	kind := orchestrator.ChatKind(value(m.ChatType))
	openID, senderKind := value(sender.SenderId.OpenId), value(sender.SenderType)
	event := &orchestrator.ArchiveEvent{ChatID: *m.ChatId, Kind: kind, EventID: incoming.EventV2Base.Header.EventID}
	if senderKind == "user" && value(sender.TenantKey) == cfg.TenantKey && validIMID(openID, "ou_") && openID != cfg.BotOpenID {
		event.VerifiedSender = openID
	}
	switch kind {
	case orchestrator.ChatPrivate:
		if senderKind != "user" || value(sender.TenantKey) != cfg.TenantKey ||
			!validIMID(openID, "ou_") || openID == cfg.BotOpenID {
			return nil
		}
		event.PeerOpenID = openID
	case orchestrator.ChatGroup:
	default:
		return nil
	}
	sentAt := providerTime(value(m.CreateTime))
	event.ProviderAt = sentAt
	if sentAt.IsZero() {
		sentAt = received.UTC()
	}
	event.Message = &orchestrator.ChatMessage{
		ID: *m.MessageId, ChatID: *m.ChatId, ThreadID: value(m.ThreadId), ParentID: value(m.ParentId),
		SenderID: openID, SenderKind: senderKind, Type: value(m.MessageType), Body: value(m.Content),
		SentAt: sentAt, UpdatedAt: providerTime(value(m.UpdateTime)),
	}
	return event
}

func mapRecall(cfg workerConfig, incoming *im.P2MessageRecalledV1, received time.Time) *orchestrator.ArchiveEvent {
	if incoming == nil || !archiveEnvelope(cfg, incoming.EventV2Base, "im.message.recalled_v1") || incoming.Event == nil {
		return nil
	}
	m := incoming.Event
	if !validIMID(value(m.ChatId), "oc_") || !validIMID(value(m.MessageId), "om_") {
		return nil
	}
	at := providerTime(value(m.RecallTime))
	if at.IsZero() {
		at = received.UTC()
	}
	return &orchestrator.ArchiveEvent{ChatID: *m.ChatId, EventID: incoming.EventV2Base.Header.EventID, Message: &orchestrator.ChatMessage{
		ID: *m.MessageId, ChatID: *m.ChatId, SentAt: at, UpdatedAt: at, Deleted: true,
	}}
}

func mapPrivateEntry(cfg workerConfig, incoming *im.P2ChatAccessEventBotP2pChatEnteredV1) *orchestrator.ArchiveEvent {
	if incoming == nil || !archiveEnvelope(cfg, incoming.EventV2Base, "im.chat.access_event.bot_p2p_chat_entered_v1") || incoming.Event == nil {
		return nil
	}
	m := incoming.Event
	if !validIMID(value(m.ChatId), "oc_") || m.OperatorId == nil ||
		!validIMID(value(m.OperatorId.OpenId), "ou_") || value(m.OperatorId.OpenId) == cfg.BotOpenID {
		return nil
	}
	return &orchestrator.ArchiveEvent{ChatID: *m.ChatId, Kind: orchestrator.ChatPrivate, PeerOpenID: *m.OperatorId.OpenId,
		EventID: incoming.EventV2Base.Header.EventID, VerifiedSender: *m.OperatorId.OpenId,
		ProviderAt: providerTime(incoming.EventV2Base.Header.CreateTime)}
}

func mapGroupMembersChanged(cfg workerConfig, body []byte, eventType string) *orchestrator.ArchiveEvent {
	var input struct {
		larkevent.EventV2Base
		Event struct {
			ChatID string `json:"chat_id"`
		} `json:"event"`
	}
	if json.Unmarshal(body, &input) != nil || !archiveEnvelope(cfg, &input.EventV2Base, eventType) ||
		!validIMID(input.Event.ChatID, "oc_") {
		return nil
	}
	return &orchestrator.ArchiveEvent{ChatID: input.Event.ChatID, EventID: input.Header.EventID, GroupMembersChanged: true}
}

func archiveInbound(cfg workerConfig, event *orchestrator.ArchiveEvent) orchestrator.InboundEvent {
	return orchestrator.InboundEvent{AppID: cfg.AppID, TenantID: cfg.TenantKey, Archive: event}
}
