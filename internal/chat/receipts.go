package chat

import (
	"context"
	"time"

	"github.com/uniquechao/OmniRelay/internal/identity"
)

const DeliveryConfirmed = "confirmed"

type ReceiptScope struct {
	TenantKey, AppID string
}

type ReceiptMessage struct {
	ChatID, MessageID string
}

type ReceiptTarget struct {
	ReceiptID  string `json:"id"`
	ProjectID  string `json:"project_id"`
	TargetKind string `json:"target_kind"`
	TargetID   string `json:"target_id"`
}

// BusinessReceiptKey identifies the authority's committed operation and target.
type BusinessReceiptKey struct {
	OperationID string
	Target      ReceiptTarget
}

type DeliveryEvidence struct {
	Scope      ReceiptScope
	Message    ReceiptMessage
	DeliveryID string
	State      string
	Receipt    BusinessReceiptKey
}

// DeliveryReceiptSource reads the durable E1 reconciliation ledger in the
// requested deployment. Return all records for each requested message so
// conflicting claims can be withheld. A delivery ID identifies a single
// ledger record within its deployment. A confirmed record must contain the
// Feishu-assigned message ID and destination confirmed by reconciliation.
type DeliveryReceiptSource interface {
	LookupDeliveries(context.Context, ReceiptScope, []ReceiptMessage) ([]DeliveryEvidence, error)
}

// BusinessReceiptSource reads F1's formal receipts as the verified reader.
// Return only committed, verified receipts whose exact target the reader can
// currently access. The returned IDs must come from the business authority.
type BusinessReceiptSource interface {
	LookupAuthorizedReceipts(context.Context, identity.Principal, []BusinessReceiptKey) ([]BusinessReceiptKey, error)
}

type ReceiptSources struct {
	Deliveries DeliveryReceiptSource
	Business   BusinessReceiptSource
}

// Read both authorities for the authorized page. The delivery ledger owns the
// stable mapping, so chat does not create a second association or cache grants.
func (s ReceiptSources) project(ctx context.Context, actor identity.Principal, items []messageItem) {
	for i := range items {
		items[i].BusinessReceipt = nil
	}
	if s.Deliveries == nil || s.Business == nil || len(items) == 0 {
		return
	}
	var messages []ReceiptMessage
	index := make(map[string]int, len(items))
	for i := range items {
		if items[i].State == "available" {
			index[items[i].ID] = i
			messages = append(messages, ReceiptMessage{ChatID: items[i].ChatID, MessageID: items[i].ID})
		}
	}
	if len(messages) == 0 {
		return
	}

	// An optional integration outage leaves ordinary message reading available.
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	scope := ReceiptScope{TenantKey: actor.TenantKey, AppID: actor.AppID}
	deliveries, err := s.Deliveries.LookupDeliveries(ctx, scope, messages)
	if err != nil || ctx.Err() != nil {
		return
	}

	claims := make(map[string]DeliveryEvidence)
	deliveryMessages := make(map[string]string)
	conflicts := make(map[string]bool)
	for _, evidence := range deliveries {
		id := evidence.Message.MessageID
		i, requested := index[id]
		if !requested || evidence.Scope != scope {
			continue
		}
		if evidence.Message.ChatID != items[i].ChatID {
			conflicts[id] = true
		}
		if previous, seen := claims[id]; seen && previous != evidence {
			conflicts[id] = true
		}
		claims[id] = evidence
		if evidence.DeliveryID != "" {
			if other, seen := deliveryMessages[evidence.DeliveryID]; seen && other != id {
				conflicts[id], conflicts[other] = true, true
			}
			deliveryMessages[evidence.DeliveryID] = id
		}
	}

	var wanted []BusinessReceiptKey
	candidates := make(map[int]BusinessReceiptKey)
	requested := make(map[BusinessReceiptKey]bool)
	for _, message := range messages {
		evidence := claims[message.MessageID]
		if conflicts[message.MessageID] || evidence.State != DeliveryConfirmed ||
			!validIdentifier(evidence.DeliveryID) || !evidence.Receipt.valid() {
			continue
		}
		candidates[index[message.MessageID]] = evidence.Receipt
		if !requested[evidence.Receipt] {
			requested[evidence.Receipt] = true
			wanted = append(wanted, evidence.Receipt)
		}
	}
	if len(wanted) == 0 {
		return
	}

	verified, err := s.Business.LookupAuthorizedReceipts(ctx, actor, wanted)
	if err != nil || ctx.Err() != nil {
		return
	}
	allowed := make(map[BusinessReceiptKey]bool, len(verified))
	for _, key := range verified {
		if requested[key] {
			allowed[key] = true
		}
	}
	for i, key := range candidates {
		if allowed[key] {
			target := key.Target
			items[i].BusinessReceipt = &target
		}
	}
}

func (key BusinessReceiptKey) valid() bool {
	target := key.Target
	return validIdentifier(key.OperationID) && validIdentifier(target.ReceiptID) &&
		validIdentifier(target.ProjectID) && validIdentifier(target.TargetID) &&
		(target.TargetKind == "task" || target.TargetKind == "briefing")
}
