package chat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
)

var (
	errDeliveryConflict = errors.New("operation key conflict")
	errDeliveryMissing  = errors.New("operation not found")
	errDeliveryTarget   = errors.New("invalid target conversation")
	errDeliveryMention  = errors.New("mention target is not a current group member")
	errDeliveryDisabled = errors.New("Bot sending is disabled")
	ErrAttachmentFailed = errors.New("attachment upload or reference failed")
)

// DeliverySource uses the configured application's tenant credentials.
type DeliverySource interface {
	BotInGroup(context.Context, string) error
	Upload(context.Context, string, string, []byte) (string, error)
	Deliver(context.Context, string, string, string, string, string) DeliveryResult
	ConfirmDelivery(context.Context, string, string, string, string) bool
}

// DeliveryResult.MessageID on an unknown result must come from the
// platform's reply to this exact create call.
type DeliveryResult struct {
	State, Reason, MessageID string
}

type DeliverySender struct {
	Kind   string `json:"kind"`
	OpenID string `json:"open_id,omitempty"`
	Name   string `json:"name,omitempty"`
}

type DeliveryPartStatus struct {
	Position     int    `json:"position"`
	Kind         string `json:"kind"`
	AttachmentID string `json:"attachment_id,omitempty"`
	State        string `json:"state"`
	Reason       string `json:"reason,omitempty"`
	MessageID    string `json:"message_id,omitempty"`
	messageID    string
}

type DeliveryStatus struct {
	OperationKey string               `json:"operation_key"`
	ChatID       string               `json:"chat_id"`
	Sender       DeliverySender       `json:"sender"`
	State        string               `json:"state"`
	Partial      bool                 `json:"partial"`
	Parts        []DeliveryPartStatus `json:"parts"`
	generation   int64
}

func (r *DeliveryStatus) summarize() {
	counts := make(map[string]int, 4)
	for _, part := range r.Parts {
		counts[part.State]++
	}
	r.Partial = counts["success"] > 0 && counts["success"] < len(r.Parts)
	switch {
	case counts["unknown"] > 0:
		r.State = "unknown"
	case counts["pending"] > 0:
		r.State = "pending"
	case counts["failed"] > 0:
		r.State = "failed"
	default:
		r.State = "success"
	}
}

func (s *Service) ConfigureDelivery(source DeliverySource, enabled bool) {
	s.delivery = source
	s.deliveryEnabled = enabled
	if s.receipts.Deliveries == nil {
		s.receipts.Deliveries = s
	}
}

// BriefingAuthority verifies a committed briefing, publication rights, current
// group binding and the exact content approved for the group's audience.
type BriefingAuthority func(context.Context, identity.Principal, BusinessReceiptKey, string, DeliveryContent) (bool, error)

func (s *Service) SendBriefing(ctx context.Context, actor identity.Principal, target, operation string,
	content DeliveryContent, receipt BusinessReceiptKey, authorize BriefingAuthority,
) (DeliveryStatus, error) {
	if authorize == nil || !receipt.valid() || receipt.Target.TargetKind != "briefing" {
		return DeliveryStatus{}, errDeliveryInput
	}
	return s.send(ctx, actor, target, operation, content, &receipt, func(ctx context.Context) error {
		ok, err := authorize(ctx, actor, receipt, target, content)
		if err != nil {
			return err
		}
		if !ok {
			return ErrAccessLost
		}
		return nil
	})
}

func (s *Service) BriefingDeliveryStatus(ctx context.Context, actor identity.Principal, target, operation string) (DeliveryStatus, error) {
	if err := s.deliveryAccount(ctx, actor); err != nil {
		return DeliveryStatus{}, err
	}
	if s.receipts.Business == nil {
		return DeliveryStatus{}, ErrUnavailable
	}
	var receipt BusinessReceiptKey
	err := s.pool.QueryRow(ctx, `SELECT business_receipt FROM feishu_send_operations
		WHERE (tenant_key,app_id,operation_key,account_id,chat_id)=($1,$2,$3,$4,$5)
		AND business_receipt IS NOT NULL`, s.tenant, s.app, operation, actor.AccountID, target).Scan(&receipt)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryStatus{}, errDeliveryMissing
	}
	if err != nil {
		return DeliveryStatus{}, err
	}
	keys, err := s.receipts.Business.LookupAuthorizedReceipts(ctx, actor, []BusinessReceiptKey{receipt})
	if err != nil {
		return DeliveryStatus{}, err
	}
	if !slices.Contains(keys, receipt) {
		return DeliveryStatus{}, ErrAccessLost
	}
	return s.deliveryStatus(ctx, actor, target, operation, true)
}

func (s *Service) send(ctx context.Context, actor identity.Principal, target, operation string,
	content DeliveryContent, receipt *BusinessReceiptKey, authorize func(context.Context) error,
) (DeliveryStatus, error) {
	if !validIdentifier(operation) || len(operation) > 100 || !validIdentifier(target) || !strings.HasPrefix(target, "oc_") {
		return DeliveryStatus{}, errDeliveryInput
	}
	parts, mentions, err := content.parts()
	if err != nil {
		return DeliveryStatus{}, err
	}
	if err := s.deliveryAccount(ctx, actor); err != nil {
		return DeliveryStatus{}, err
	}
	if !s.deliveryEnabled || s.delivery == nil {
		return DeliveryStatus{}, errDeliveryDisabled
	}
	// Upload handles are immutable bindings to their content digests.
	raw, _ := json.Marshal([]any{content, receipt})
	hash := sha256.Sum256(raw)
	k := deliveryKey{s.tenant, s.app, operation}
	created, err := s.reserveDelivery(ctx, k, actor.AccountID, target, hash[:], receipt, parts)
	if err != nil {
		return DeliveryStatus{}, err
	}
	if !created {
		if authorize != nil {
			if err := authorize(ctx); err != nil {
				return DeliveryStatus{}, err
			}
		}
		return s.deliveryStatus(ctx, actor, target, operation, receipt != nil)
	}
	// Finish the bounded operation even if the caller loses the HTTP response.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 24*time.Second)
	defer cancel()
	err = s.dispatchDelivery(ctx, k, actor, target, parts, mentions, authorize)
	save, done := context.WithTimeout(context.WithoutCancel(ctx), 4*time.Second)
	defer done()
	if err != nil {
		return DeliveryStatus{}, err
	}
	result, err := s.loadDelivery(save, k, actor.AccountID, target, receipt != nil)
	// Keep confirmed IDs in the ledger while withholding them after a known revocation.
	for _, part := range result.Parts {
		if part.Reason == "access_lost" {
			for i := range result.Parts {
				result.Parts[i].MessageID = ""
			}
			break
		}
	}
	return result, err
}

func (s *Service) deliveryAccount(ctx context.Context, actor identity.Principal) error {
	if actor.TenantKey != s.tenant || actor.AppID != s.app || actor.AccountID == "" || actor.OpenID == "" {
		return ErrAccessLost
	}
	var active bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM accounts
		WHERE (tenant_key,app_id,id,open_id)=($1,$2,$3,$4) AND active)`,
		s.tenant, s.app, actor.AccountID, actor.OpenID).Scan(&active)
	if err == nil && !active {
		return ErrAccessLost
	}
	return err
}

func (s *Service) deliveryTarget(ctx context.Context, actor identity.Principal, target string, mentions []string, groupOnly bool) (conversation, error) {
	if err := s.deliveryAccount(ctx, actor); err != nil {
		return conversation{}, err
	}
	var c conversation
	var enabled bool
	var generation int64
	err := s.pool.QueryRow(ctx, `SELECT coalesce(c.chat_id,''),coalesce(c.kind,''),coalesce(c.peer_open_id,''),
		b.enabled,b.credentials_generation FROM feishu_bot_configs b LEFT JOIN feishu_chat_conversations c
		ON (c.tenant_key,c.app_id,c.chat_id)=(b.tenant_key,b.app_id,$3)
		WHERE (b.tenant_key,b.app_id)=($1,$2)`, s.tenant, s.app, target).
		Scan(&c.ID, &c.Kind, &c.Peer, &enabled, &generation)
	if err != nil {
		return c, err
	}
	if !enabled || generation != s.generation {
		return c, ErrAccessLost
	}
	if c.ID == "" || (groupOnly && c.Kind != orchestrator.ChatGroup) {
		return c, errDeliveryTarget
	}
	if c.Kind == orchestrator.ChatPrivate {
		if c.Peer != actor.OpenID {
			return c, ErrAccessLost
		}
		if len(mentions) > 0 {
			return c, errDeliveryMention
		}
		_, err := s.source.History(ctx, HistoryQuery{ChatID: target, Limit: 1})
		return c, err
	}
	if _, err := s.source.Group(ctx, target); err != nil {
		return c, err
	}
	if err := s.delivery.BotInGroup(ctx, target); err != nil {
		return c, err
	}
	return c, s.deliveryMembers(ctx, target, actor.OpenID, mentions)
}

func (s *Service) deliveryMembers(ctx context.Context, target, caller string, mentions []string) error {
	want := append(slices.Clone(mentions), caller)
	seen := make(map[string]bool)
	for cursor := ""; ; {
		page, err := s.source.Members(ctx, target, cursor)
		if err != nil && !errors.Is(err, ErrPartial) {
			return err
		}
		for _, member := range page.Items {
			want = slices.DeleteFunc(want, func(id string) bool { return id == member.OpenID })
		}
		if len(want) == 0 {
			return nil
		}
		if page.Next == "" || errors.Is(err, ErrPartial) {
			if slices.Contains(want, caller) {
				return ErrAccessLost
			}
			return errDeliveryMention
		}
		if seen[page.Next] {
			return ErrUnavailable
		}
		seen[page.Next], cursor = true, page.Next
	}
}

func (s *Service) deliveryStatus(ctx context.Context, actor identity.Principal, target, operation string, business bool) (DeliveryStatus, error) {
	if err := s.deliveryAccount(ctx, actor); err != nil {
		return DeliveryStatus{}, err
	}
	k := deliveryKey{s.tenant, s.app, operation}
	result, err := s.loadDelivery(ctx, k, actor.AccountID, target, business)
	if err != nil || result.Sender.OpenID == "" {
		return result, err
	}
	if s.delivery == nil {
		return DeliveryStatus{}, ErrUnavailable
	}
	if _, err := s.deliveryTarget(ctx, actor, target, nil, business); err != nil {
		return DeliveryStatus{}, err
	}
	if result.State != "unknown" || result.generation != s.generation {
		return result, nil
	}
	changed := false
	for _, part := range result.Parts {
		if part.State != "unknown" || part.messageID == "" ||
			!s.delivery.ConfirmDelivery(ctx, target, part.Kind, result.Sender.OpenID, part.messageID) {
			continue
		}
		_, err := s.pool.Exec(ctx, `UPDATE feishu_send_parts SET state='success',reason='',confirmed_at=now()
			WHERE (tenant_key,app_id,operation_key,position,message_id)=($1,$2,$3,$4,$5) AND state='unknown'`,
			k.tenant, k.app, k.operation, part.Position, part.messageID)
		if err != nil {
			return DeliveryStatus{}, err
		}
		changed = true
	}
	if changed {
		return s.loadDelivery(ctx, k, actor.AccountID, target, business)
	}
	return result, nil
}

func deliveryReason(err error) string {
	switch {
	case errors.Is(err, errDeliveryTarget):
		return "invalid_target"
	case errors.Is(err, ErrAccessLost), errors.Is(err, errHidden):
		return "access_lost"
	case errors.Is(err, errDeliveryMention):
		return "mention_not_allowed"
	case errors.Is(err, ErrAttachmentFailed):
		return "attachment_failed"
	default:
		return "delivery_unavailable"
	}
}

func (s *Service) dispatchDelivery(ctx context.Context, k deliveryKey, actor identity.Principal, target string,
	parts []deliveryPart, mentions []string, authorize func(context.Context) error,
) error {
	fail := func(position int, err error) error {
		save, cancel := context.WithTimeout(context.WithoutCancel(ctx), 4*time.Second)
		defer cancel()
		return s.abortDelivery(save, k, position, deliveryReason(err))
	}
	check := func(mentions []string) error {
		if authorize != nil {
			if err := authorize(ctx); err != nil {
				return err
			}
		}
		_, err := s.deliveryTarget(ctx, actor, target, mentions, authorize != nil)
		return err
	}
	if err := check(mentions); err != nil {
		return fail(0, err)
	}
	bot, err := s.source.Bot(ctx)
	if err != nil || !validIdentifier(bot.OpenID) || strings.TrimSpace(bot.Name) == "" {
		return fail(0, err)
	}
	for i := range parts {
		part := &parts[i]
		part.position = i
		if part.attachmentID == "" {
			continue
		}
		var resource string
		err := s.pool.QueryRow(ctx, `SELECT kind,resource_key FROM feishu_send_uploads
			WHERE (id,tenant_key,app_id,account_id,chat_id,generation)=($1,$2,$3,$4,$5,$6) AND expires_at>now()`,
			part.attachmentID, s.tenant, s.app, actor.AccountID, target, s.generation).Scan(&part.kind, &resource)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(i, ErrAttachmentFailed)
		}
		if err != nil {
			return fail(0, err)
		}
		payload, _ := json.Marshal(map[string]string{part.kind + "_key": resource})
		part.content = string(payload)
	}
	_, err = s.pool.Exec(ctx, `UPDATE feishu_send_operations SET bot_open_id=$4,bot_name=$5
		WHERE (tenant_key,app_id,operation_key)=($1,$2,$3)`, k.tenant, k.app, k.operation, bot.OpenID, bot.Name)
	if err != nil {
		return err
	}
	for i, part := range parts {
		partMentions := mentions
		if part.attachmentID != "" {
			partMentions = nil
		}
		if err := check(partMentions); err != nil {
			return fail(i, err)
		}
		tag, err := s.pool.Exec(ctx, `UPDATE feishu_send_parts p SET attempted=true,kind=$5
			FROM feishu_send_operations o WHERE (p.tenant_key,p.app_id,p.operation_key)=(o.tenant_key,o.app_id,o.operation_key)
			AND (p.tenant_key,p.app_id,p.operation_key,p.position)=($1,$2,$3,$4)
			AND p.state='pending' AND NOT p.attempted AND o.deadline>now()
			AND EXISTS(SELECT 1 FROM accounts a JOIN feishu_bot_configs b USING (tenant_key,app_id)
				WHERE (a.tenant_key,a.app_id,a.id,a.open_id)=(o.tenant_key,o.app_id,o.account_id,$6)
				AND a.active AND b.enabled AND b.credentials_generation=o.generation)`,
			k.tenant, k.app, k.operation, i, part.kind, actor.OpenID)
		if err != nil {
			return fail(i, err)
		}
		if tag.RowsAffected() != 1 {
			return fail(i, ErrAccessLost)
		}
		nonce := sha256.Sum256([]byte(k.tenant + "\x00" + k.app + "\x00" + k.operation + "\x00" + strconv.Itoa(i)))
		result := s.delivery.Deliver(ctx, target, bot.OpenID, part.kind, part.content, hex.EncodeToString(nonce[:24]))
		part.state, part.reason, part.messageID = result.State, result.Reason, result.MessageID
		save, cancel := context.WithTimeout(context.WithoutCancel(ctx), 4*time.Second)
		err = s.finishDelivery(save, k, part)
		if err == nil && part.state != "success" {
			err = s.abortDelivery(save, k, i+1, "not_attempted")
		}
		cancel()
		if err != nil || part.state != "success" {
			return err
		}
	}
	return nil
}
