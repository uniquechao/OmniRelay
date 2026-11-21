package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
)

// AcceptCandidateMessage shares the product inbox and archive writes in the
// isolated database. It only receives envelopes from the authenticated queue.
func (s *CandidateMessages) AcceptCandidateMessage(ctx context.Context, item orchestrator.CandidateMessage,
	authorize func(context.Context, pgx.Tx, identity.Principal, string) (bool, error),
) error {
	e := item.Event
	b := s.inbox.bot
	source, _, sender, _, _, recall := candidateSource(e)
	if !runID.MatchString(item.Run) || item.Revision < 1 || item.Project == "" || item.Source == "" ||
		source != item.Source || e.AppID != b.config.AppID || e.TenantID != b.config.TenantKey ||
		item.LeaseExpires.IsZero() || item.LeaseExpires.After(item.Expires) {
		return ErrCandidateScope
	}
	ctx, cancel := context.WithDeadline(ctx, item.LeaseExpires)
	defer cancel()
	return pgx.BeginFunc(ctx, b.pool, func(tx pgx.Tx) error {
		if err := b.lockGeneration(ctx, tx); err != nil {
			return err
		}
		var run string
		var rev int
		err := tx.QueryRow(ctx, `SELECT run_id,revision FROM feishu_candidate_message_receipts
			WHERE (tenant_key,app_id,source_id)=($1,$2,$3)`, e.TenantID, e.AppID, source).Scan(&run, &rev)
		switch {
		case err == nil && run != item.Run:
			return ErrCandidateScope
		case err == nil && rev >= item.Revision:
			return nil
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		if recall {
			if err := cancelCandidateWork(ctx, tx, e.TenantID, e.AppID, source); err != nil {
				return err
			}
		} else {
			id, err := identity.ResolveFeishuAccount(ctx, tx, e.TenantID, e.AppID, sender)
			if err != nil {
				return err
			}
			actor := identity.Principal{AppID: e.AppID, TenantKey: e.TenantID, OpenID: sender, AccountID: id}
			allowed, err := authorize(ctx, tx, actor, item.Project)
			if err != nil {
				return err
			}
			if !allowed {
				return ErrCandidateScope
			}
			if e.MessageID != "" && rev == 0 {
				if _, err := s.inbox.insertInbound(ctx, tx, e); err != nil {
					return err
				}
			}
		}
		if e.Archive != nil {
			archive := *e.Archive
			if archive.Message != nil {
				msg := *archive.Message
				msg.VisibleInChat = !msg.Deleted
				archive.Message = &msg
			}
			if err := s.archive(ctx, tx, archive); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO feishu_candidate_message_receipts
			(tenant_key,app_id,source_id,run_id,revision) VALUES($1,$2,$3,$4,$5)
			ON CONFLICT (tenant_key,app_id,source_id) DO UPDATE SET revision=EXCLUDED.revision`,
			e.TenantID, e.AppID, source, item.Run, item.Revision)
		return err
	})
}
