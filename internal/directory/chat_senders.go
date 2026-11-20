package directory

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type chatSenderCheck struct {
	id       string
	resigned *bool
}

func (s *Service) checkChatSenders(ctx context.Context, f *collector, result *snapshot) error {
	if f.left == 0 {
		return nil
	}
	present := make([]string, 0, len(result.People))
	for id := range result.People {
		present = append(present, id)
	}
	rows, err := s.db.Query(ctx, `WITH senders AS (
		SELECT DISTINCT m.sender_id AS open_id FROM feishu_chat_messages m
		JOIN feishu_chat_conversations c USING (tenant_key,app_id,chat_id)
		WHERE m.tenant_key=$1 AND m.app_id=$2 AND c.kind='group' AND m.sender_kind='user'
			AND m.sender_id ~ '^ou_[A-Za-z0-9_-]{1,253}$' AND m.sender_id<>ALL($3::text[])
	) SELECT s.open_id FROM senders s LEFT JOIN feishu_chat_sender_checks k
		ON (k.tenant_key,k.app_id,k.open_id)=($1,$2,s.open_id)
		ORDER BY k.checked_at NULLS FIRST,s.open_id LIMIT $4`, s.cfg.TenantKey, s.cfg.AppID, present, min(32, f.left))
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, id := range ids {
		person, err := f.readPerson(probeCtx, id)
		check := chatSenderCheck{id: id}
		resigned := person.Status.Resigned || userDoesNotExist(err)
		if err == nil || resigned {
			check.resigned = &resigned
		}
		result.ChatSenderChecks = append(result.ChatSenderChecks, check)
		if probeCtx.Err() != nil {
			break
		}
	}
	return nil
}

func (s *Service) publishChatSenderChecks(ctx context.Context, tx pgx.Tx, runID int64, checks []chatSenderCheck) error {
	tenant, app := s.cfg.TenantKey, s.cfg.AppID
	_, err := tx.Exec(ctx, `DELETE FROM feishu_chat_sender_checks k USING directory_people p
		WHERE (k.tenant_key,k.app_id,k.open_id)=(p.tenant_key,p.app_id,p.open_id)
		AND p.tenant_key=$1 AND p.app_id=$2 AND p.snapshot_version=$3`, tenant, app, runID)
	if err != nil {
		return err
	}
	for _, check := range checks {
		_, err := tx.Exec(ctx, `INSERT INTO feishu_chat_sender_checks AS k (tenant_key,app_id,open_id,resigned)
			VALUES ($1,$2,$3,$4) ON CONFLICT (tenant_key,app_id,open_id) DO UPDATE SET
			resigned=COALESCE(EXCLUDED.resigned,k.resigned),checked_at=EXCLUDED.checked_at`, tenant, app, check.id, check.resigned)
		if err != nil {
			return err
		}
	}
	return nil
}
