package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	ProviderAt                          time.Time `json:",omitzero"`
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

func InboundReference(e InboundEvent) string {
	id := e.EventID
	if id == "" && e.Archive != nil {
		id = e.Archive.EventID
	}
	sum := sha256.Sum256([]byte(e.TenantID + "\x00" + e.AppID + "\x00" + id))
	return hex.EncodeToString(sum[:12])
}

// ChatMessage is the provider message used by the read-only archive.
type ChatMessage struct {
	ID, ChatID, ThreadID, ParentID   string
	SenderID, SenderKind, SenderName string
	Type, Body                       string
	MentionNames                     map[string]string `json:",omitempty"`
	SentAt, UpdatedAt                time.Time
	Deleted, VisibleInChat           bool
}

// ArchiveEvent travels over the existing trusted connection. A message can be
// admitted to the archive and to the command inbox independently.
type ArchiveEvent struct {
	ChatID, PeerOpenID      string
	Kind                    ChatKind
	Message                 *ChatMessage
	EventID, VerifiedSender string
	ProviderAt              time.Time `json:",omitzero"`
	GroupMembersChanged     bool      `json:",omitempty"`
}
