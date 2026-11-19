package orchestrator

import (
	"context"
	"errors"
	"time"
)

type ChatKind string

const (
	ChatPrivate ChatKind = "p2p"
	ChatGroup   ChatKind = "group"
)

var (
	ErrInvalidInbound   = errors.New("invalid inbound event")
	ErrStaleConnection  = errors.New("stale inbound connection")
	ErrInboxFull        = errors.New("inbox capacity exhausted")
	ErrInboxUnavailable = errors.New("inbox temporarily unavailable")
)

// ConnectionFence is captured when the trusted connection adapter starts a connection.
type ConnectionFence struct {
	ConfigVersion        int64
	ConnectionGeneration int64
}

// InboundEvent comes from the adapter after application, tenant, human sender
// and conversation eligibility checks. TenantID is the verified tenant key.
type InboundEvent struct {
	AppID, TenantID, EventID, MessageID string
	ChatID, ThreadID, SenderOpenID      string
	ChatKind                            ChatKind
	ReplyToMessageID                    string
	ReceivedAt                          time.Time
	Text                                string
	Connection                          ConnectionFence
	Archive                             *ArchiveEvent `json:",omitempty"`
}

type WorkID string

// AcceptResult is a receipt for durable ingestion. It grants no execution rights.
type AcceptResult struct {
	WorkID WorkID
}

type InboxAcceptor interface {
	AcceptInbound(ctx context.Context, event InboundEvent) (AcceptResult, error)
}

// ChatMessage is the provider message used by the read-only archive.
type ChatMessage struct {
	ID, ChatID, ThreadID, ParentID   string
	SenderID, SenderKind, SenderName string
	Type, Body                       string
	SentAt, UpdatedAt                time.Time
	Deleted, VisibleInChat           bool
}

// ArchiveEvent travels over the existing trusted connection. A message can be
// admitted to the archive and to the command inbox independently.
type ArchiveEvent struct {
	ChatID, PeerOpenID string
	Kind               ChatKind
	Message            *ChatMessage
}
