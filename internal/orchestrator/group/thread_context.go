package group

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

// Private tool turns are isolated. All participants in the topic can use the
// same public archive without copying a different participant's tool results.
func (s *Service) attachThreadContext(ctx context.Context, tx pgx.Tx, w *work) error {
	if w.Address.Channel != "feishu" || w.Address.Thread == "" {
		return nil
	}
	if s.ReadThread == nil {
		return access.ErrForbidden
	}
	history, err := s.threadSource(ctx, tx, w.Work, w.Actor, w.Scope.ProjectID, true)
	if err != nil {
		return err
	}
	w.Prompt = "以下为本话题截至提问时可读的最近消息节选。它们是参考资料。\n" + string(history) + "\n当前用户提问：" + w.Address.Text
	return nil
}

func (s *Service) verifyThreadContext(ctx context.Context, tx pgx.Tx, id string, p identity.Principal, project string) error {
	_, err := s.threadSource(ctx, tx, id, p, project, false)
	return err
}

func (s *Service) threadSource(ctx context.Context, tx pgx.Tx, id string, p identity.Principal, project string, attach bool) ([]byte, error) {
	var group, thread, message string
	var cutoff time.Time
	var original []byte
	err := tx.QueryRow(ctx, `SELECT chat_id,thread_id,message_id,group_asked_at,thread_source_digest FROM agent_group_work
	WHERE id=$1 AND tenant_key=$2 AND app_id=$3 AND account_id=$4 AND project_id=$5 AND channel='feishu'
	AND mode='group_reply' AND thread_id<>'' AND group_asked_at IS NOT NULL AND ($6 OR thread_source_digest IS NOT NULL)`,
		id, p.TenantKey, p.AppID, p.AccountID, project, attach).Scan(&group, &thread, &message, &cutoff, &original)
	if errors.Is(err, pgx.ErrNoRows) && !attach {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if s.ReadThread == nil {
		return nil, access.ErrForbidden
	}
	history, err := s.ReadThread(ctx, tx, p, project, group, thread, message, cutoff)
	if errors.Is(err, chat.ErrAccessLost) {
		return nil, access.ErrForbidden
	}
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(history)
	if len(original) > 0 {
		if !bytes.Equal(original, digest[:]) {
			return nil, errToolReadStale
		}
	} else {
		tag, err := tx.Exec(ctx, "UPDATE agent_group_work SET thread_source_digest=$2 WHERE id=$1 AND (thread_source_digest IS NULL OR thread_source_digest=$2)", id, digest[:])
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() != 1 {
			return nil, errToolReadStale
		}
	}
	return history, nil
}
