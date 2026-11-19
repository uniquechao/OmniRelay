package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
)

type deliveryKey struct {
	tenant, app, operation string
}

func (k deliveryKey) args() []any { return []any{k.tenant, k.app, k.operation} }

func (s *Service) reserveDelivery(ctx context.Context, k deliveryKey, account, target string, fingerprint []byte,
	receipt *BusinessReceiptKey, parts []deliveryPart,
) (bool, error) {
	var business any
	if receipt != nil {
		business = *receipt
	}
	var created bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO feishu_send_operations
			(tenant_key,app_id,operation_key,account_id,chat_id,generation,fingerprint,business_receipt)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`,
			k.tenant, k.app, k.operation, account, target, s.generation, fingerprint, business)
		if err != nil {
			return err
		}
		created = tag.RowsAffected() == 1
		if !created {
			var owner, chatID string
			var saved []byte
			err := tx.QueryRow(ctx, `SELECT account_id,chat_id,fingerprint FROM feishu_send_operations
				WHERE (tenant_key,app_id,operation_key)=($1,$2,$3)`, k.args()...).Scan(&owner, &chatID, &saved)
			if err != nil {
				return err
			}
			if owner != account || chatID != target || !bytes.Equal(saved, fingerprint) {
				return errDeliveryConflict
			}
			return nil
		}
		for i, part := range parts {
			_, err := tx.Exec(ctx, `INSERT INTO feishu_send_parts
				(tenant_key,app_id,operation_key,position,kind,attachment_id) VALUES ($1,$2,$3,$4,$5,$6)`,
				k.tenant, k.app, k.operation, i, part.kind, part.attachmentID)
			if err != nil {
				return err
			}
		}
		return nil
	})
	return created, err
}

func (s *Service) loadDelivery(ctx context.Context, k deliveryKey, owner, target string, business bool) (DeliveryStatus, error) {
	var result DeliveryStatus
	result.OperationKey, result.ChatID = k.operation, target
	err := s.pool.QueryRow(ctx, `SELECT bot_open_id,bot_name,generation FROM feishu_send_operations
		WHERE (tenant_key,app_id,operation_key,account_id,chat_id)=($1,$2,$3,$4,$5)
		AND (business_receipt IS NOT NULL)=$6`, k.tenant, k.app, k.operation, owner, target, business).
		Scan(&result.Sender.OpenID, &result.Sender.Name, &result.generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryStatus{}, errDeliveryMissing
	}
	if err != nil {
		return DeliveryStatus{}, err
	}
	result.Sender.Kind = "bot"
	// A crash can leave a pending part. Once the dispatch deadline has passed,
	// an attempted part is ambiguous and an untouched part never left this service.
	_, err = s.pool.Exec(ctx, `UPDATE feishu_send_parts p SET
		state=CASE WHEN attempted THEN 'unknown' ELSE 'failed' END,
		reason=CASE WHEN attempted THEN 'receipt_unknown' ELSE 'not_attempted' END
		FROM feishu_send_operations o WHERE (p.tenant_key,p.app_id,p.operation_key)=(o.tenant_key,o.app_id,o.operation_key)
		AND (o.tenant_key,o.app_id,o.operation_key)=($1,$2,$3) AND p.state='pending' AND o.deadline<=now()`, k.args()...)
	if err != nil {
		return DeliveryStatus{}, err
	}
	rows, err := s.pool.Query(ctx, `SELECT position,kind,attachment_id,state,reason,coalesce(message_id,'')
		FROM feishu_send_parts WHERE (tenant_key,app_id,operation_key)=($1,$2,$3) ORDER BY position`, k.args()...)
	if err != nil {
		return DeliveryStatus{}, err
	}
	result.Parts, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (DeliveryPartStatus, error) {
		var part DeliveryPartStatus
		err := row.Scan(&part.Position, &part.Kind, &part.AttachmentID, &part.State, &part.Reason, &part.messageID)
		if part.State == "success" {
			part.MessageID = part.messageID
		}
		return part, err
	})
	result.summarize()
	return result, err
}

func (s *Service) finishDelivery(ctx context.Context, k deliveryKey, p deliveryPart) error {
	tag, err := s.pool.Exec(ctx, `UPDATE feishu_send_parts SET state=$5,reason=$6,message_id=nullif($7,'')
		WHERE (tenant_key,app_id,operation_key,position)=($1,$2,$3,$4) AND state='pending'`,
		k.tenant, k.app, k.operation, p.position, p.state, p.reason, p.messageID)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrUnavailable
	}
	return err
}

func (s *Service) abortDelivery(ctx context.Context, k deliveryKey, position int, reason string) error {
	start := position
	if reason == "attachment_failed" {
		start = 0
	}
	_, err := s.pool.Exec(ctx, `UPDATE feishu_send_parts SET state='failed',
		reason=CASE WHEN position=$4 THEN $5 ELSE 'not_attempted' END
		WHERE (tenant_key,app_id,operation_key)=($1,$2,$3) AND position>=$6 AND state='pending' AND NOT attempted`,
		k.tenant, k.app, k.operation, position, reason, start)
	return err
}

// LookupDeliveries exposes only confirmed Feishu parts tied to a trusted
// business operation. The chat reader independently authorizes the receipt.
func (s *Service) LookupDeliveries(ctx context.Context, scope ReceiptScope, messages []ReceiptMessage) ([]DeliveryEvidence, error) {
	if scope != (ReceiptScope{TenantKey: s.tenant, AppID: s.app}) || len(messages) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(messages))
	chats := make([]string, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.MessageID)
		chats = append(chats, message.ChatID)
	}
	rows, err := s.pool.Query(ctx, `SELECT o.chat_id,p.message_id,o.operation_key,p.position,o.business_receipt
		FROM feishu_send_operations o JOIN feishu_send_parts p USING (tenant_key,app_id,operation_key)
		JOIN unnest($3::text[],$4::text[]) q(message_id,chat_id)
			ON q.message_id=p.message_id AND q.chat_id=o.chat_id
		WHERE (o.tenant_key,o.app_id)=($1,$2) AND o.business_receipt IS NOT NULL AND p.state='success'`,
		s.tenant, s.app, ids, chats)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (DeliveryEvidence, error) {
		evidence := DeliveryEvidence{Scope: scope, State: DeliveryConfirmed}
		var key string
		var position int
		var raw []byte
		err := row.Scan(&evidence.Message.ChatID, &evidence.Message.MessageID, &key, &position, &raw)
		if err == nil {
			evidence.DeliveryID = key + "_" + strconv.Itoa(position)
			err = json.Unmarshal(raw, &evidence.Receipt)
		}
		return evidence, err
	})
}
