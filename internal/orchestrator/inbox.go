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
}

type WorkID string

// AcceptResult is a receipt for durable ingestion. It grants no execution rights.
type AcceptResult struct {
	WorkID WorkID
}

type InboxAcceptor interface {
	AcceptInbound(ctx context.Context, event InboundEvent) (AcceptResult, error)
}
