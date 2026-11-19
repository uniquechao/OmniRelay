package chat

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
)

const (
	archiveLock         = `hashtextextended(jsonb_build_array($1::text,$2::text)::text, 25970837)`
	syncStepTimeout     = 25 * time.Second
	syncProviderTimeout = 20 * time.Second
)

func (s *Service) Run(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		attempt, stop := context.WithTimeout(ctx, syncStepTimeout)
		worked, err := s.runPage(attempt)
		stop()
		if err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "Feishu chat synchronization step failed")
		}
		delay := 2 * time.Second
		if worked && err == nil {
			delay = 200 * time.Millisecond
		}
		timer.Reset(delay)
	}
}

// Hold a session lock for one provider page. There is no SQL transaction
// across network I/O. The next worker reads the committed cursor after a crash.
func (s *Service) runPage(ctx context.Context) (bool, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer func() {
		if conn != nil {
			conn.Release()
		}
	}()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(`+archiveLock+`)`, s.tenant, s.app).Scan(&locked); err != nil {
		// The response can be lost after the server acquires the session lock.
		raw := conn.Hijack()
		conn = nil
		_ = raw.Close(context.Background())
		return false, err
	}
	if !locked {
		return false, nil
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var unlocked bool
		if err := conn.QueryRow(cleanup, `SELECT pg_advisory_unlock(`+archiveLock+`)`, s.tenant, s.app).Scan(&unlocked); err != nil || !unlocked {
			raw := conn.Hijack()
			conn = nil
			_ = raw.Close(cleanup)
		}
	}()

	var enabled bool
	err = conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM feishu_bot_configs
		WHERE tenant_key=$1 AND app_id=$2 AND credentials_generation=$3 AND enabled)`, s.tenant, s.app, s.generation).Scan(&enabled)
	if err != nil || !enabled {
		return false, err
	}
	if _, err := conn.Exec(ctx, `INSERT INTO feishu_chat_discovery (tenant_key,app_id)
		VALUES ($1,$2) ON CONFLICT DO NOTHING`, s.tenant, s.app); err != nil {
		return false, err
	}

	var discovery string
	err = conn.QueryRow(ctx, `SELECT page_token FROM feishu_chat_discovery
		WHERE tenant_key=$1 AND app_id=$2 AND next_at<=clock_timestamp()`, s.tenant, s.app).Scan(&discovery)
	switch {
	case err == nil:
		return true, s.discoverPage(ctx, conn, discovery)
	case !errors.Is(err, pgx.ErrNoRows):
		return false, err
	}
	cursor, err := s.claimPage(ctx, conn)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, s.syncPage(ctx, conn, cursor)
}

func (s *Service) assertEnabled(ctx context.Context, tx pgx.Tx) error {
	var generation int64
	err := tx.QueryRow(ctx, `SELECT credentials_generation FROM feishu_bot_configs
		WHERE tenant_key=$1 AND app_id=$2 AND credentials_generation=$3 AND enabled FOR SHARE`,
		s.tenant, s.app, s.generation).Scan(&generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAccessLost
	}
	return err
}

func (s *Service) discoverPage(ctx context.Context, conn *pgxpool.Conn, token string) error {
	// Preserve time to persist a provider timeout and reschedule the page.
	provider, cancel := context.WithTimeout(ctx, syncProviderTimeout)
	page, sourceErr := s.source.Groups(provider, token)
	cancel()
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if err := s.assertEnabled(ctx, tx); err != nil {
			return err
		}
		state, next, wait := "complete", page.Next, 2*time.Minute
		switch {
		case sourceErr != nil:
			state, next, wait = syncFailure(sourceErr), token, 30*time.Second
			if state == "partial" {
				state = "failed"
			}
			if errors.Is(sourceErr, ErrCursorExpired) {
				next = ""
			}
		case next != "":
			state, wait = "syncing", time.Duration(0)
		}
		if sourceErr == nil {
			for _, group := range page.Items {
				if err := s.upsertConversation(ctx, tx, group.ID, orchestrator.ChatGroup, "", group.Name); err != nil {
					return err
				}
			}
		}
		_, err := tx.Exec(ctx, `UPDATE feishu_chat_discovery SET state=$3,page_token=$4,
			next_at=clock_timestamp()+$5::bigint*interval '1 microsecond',
			completed_at=CASE WHEN $3='complete' THEN clock_timestamp() ELSE completed_at END
			WHERE tenant_key=$1 AND app_id=$2`, s.tenant, s.app, state, next, wait.Microseconds())
		return err
	})
}

func (s *Service) claimPage(ctx context.Context, conn *pgxpool.Conn) (syncCursor, error) {
	var c syncCursor
	err := conn.QueryRow(ctx, `WITH due AS (
		SELECT chat_id,thread_id FROM feishu_chat_sync
		WHERE tenant_key=$1 AND app_id=$2 AND next_at<=clock_timestamp()
		ORDER BY next_at,chat_id,thread_id LIMIT 1
	)
	UPDATE feishu_chat_sync j SET attempted_at=clock_timestamp(),state='syncing',
		range_start=CASE WHEN j.thread_id<>'' OR j.range_end IS NOT NULL THEN j.range_start
			WHEN j.full_at>clock_timestamp()-interval '1 day' THEN j.covered_until-interval '2 minutes' END,
		range_end=CASE WHEN j.thread_id='' THEN COALESCE(j.range_end,date_trunc('second',clock_timestamp())) END
	FROM due,feishu_chat_conversations c
	WHERE (j.tenant_key,j.app_id,j.chat_id)=(c.tenant_key,c.app_id,c.chat_id)
		AND j.tenant_key=$1 AND j.app_id=$2 AND (j.chat_id,j.thread_id)=(due.chat_id,due.thread_id)
	RETURNING j.chat_id,j.thread_id,j.page_token,j.range_start,j.range_end,c.kind`,
		s.tenant, s.app).Scan(&c.ChatID, &c.ThreadID, &c.Token, &c.Start, &c.End, &c.Kind)
	return c, err
}

func (s *Service) syncPage(ctx context.Context, conn *pgxpool.Conn, c syncCursor) error {
	provider, cancel := context.WithTimeout(ctx, syncProviderTimeout)
	var page Page[orchestrator.ChatMessage]
	var sourceErr error
	if c.Kind == orchestrator.ChatGroup && c.Token == "" {
		_, sourceErr = s.source.Group(provider, c.ChatID)
	}
	if sourceErr == nil {
		page, sourceErr = s.source.History(provider, HistoryQuery{
			ChatID: c.ChatID, ThreadID: c.ThreadID, Cursor: c.Token, Start: c.Start, End: c.End, Limit: 50,
		})
	}
	cancel()
	return s.savePage(ctx, conn, c, page, sourceErr)
}

func syncFailure(err error) string {
	switch {
	case errors.Is(err, ErrAccessLost):
		return "access_lost"
	case errors.Is(err, ErrPartial):
		return "partial"
	default:
		return "failed"
	}
}

func (s *Service) savePage(ctx context.Context, conn *pgxpool.Conn, c syncCursor,
	page Page[orchestrator.ChatMessage], sourceErr error,
) error {
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if err := s.assertEnabled(ctx, tx); err != nil {
			return err
		}
		state, next, wait := "complete", page.Next, 2*time.Minute
		if c.ThreadID != "" {
			wait = 15 * time.Minute
		}
		switch {
		case sourceErr != nil:
			state, next = syncFailure(sourceErr), c.Token
			if state == "failed" {
				wait = 30 * time.Second
			}
			if errors.Is(sourceErr, ErrCursorExpired) {
				next = ""
			}
		case next != "":
			state, wait = "syncing", time.Duration(0)
		}
		if sourceErr == nil {
			for _, message := range page.Items {
				if message.ChatID != c.ChatID {
					return ErrUnavailable
				}
				message.VisibleInChat = c.ThreadID == ""
				if err := s.saveMessage(ctx, tx, message); err != nil {
					return err
				}
				if c.Kind == orchestrator.ChatPrivate && !message.Deleted && message.SenderName != "" {
					_, err := tx.Exec(ctx, `UPDATE feishu_chat_conversations SET name=$5
						WHERE tenant_key=$1 AND app_id=$2 AND chat_id=$3 AND peer_open_id=$4`,
						s.tenant, s.app, c.ChatID, message.SenderID, cleanText(message.SenderName))
					if err != nil {
						return err
					}
				}
				if c.Kind == orchestrator.ChatGroup && c.ThreadID == "" && message.ThreadID != "" {
					if err := s.ensureCursor(ctx, tx, c.ChatID, message.ThreadID); err != nil {
						return err
					}
				}
			}
		}
		_, err := tx.Exec(ctx, `UPDATE feishu_chat_sync SET state=$5,page_token=$6,
			next_at=clock_timestamp()+$7::bigint*interval '1 microsecond',
			source_blocked=CASE WHEN $5 IN ('access_lost','partial') THEN true
				WHEN $5='complete' THEN false ELSE source_blocked END,
			covered_until=CASE WHEN $5='complete' AND range_end IS NOT NULL THEN range_end ELSE covered_until END,
			full_at=CASE WHEN $5='complete' AND range_start IS NULL THEN clock_timestamp() ELSE full_at END,
			completed_at=CASE WHEN $5='complete' THEN clock_timestamp() ELSE completed_at END,
			range_start=CASE WHEN $5='complete' THEN NULL ELSE range_start END,
			range_end=CASE WHEN $5='complete' THEN NULL ELSE range_end END
			WHERE tenant_key=$1 AND app_id=$2 AND chat_id=$3 AND thread_id=$4`,
			s.tenant, s.app, c.ChatID, c.ThreadID, state, next, wait.Microseconds())
		return err
	})
}
