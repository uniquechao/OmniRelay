package postgres

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
)

var ErrCandidateScope = errors.New("candidate message operation is outside the run")

var (
	runID        = regexp.MustCompile(`^[a-f0-9]{32}$`)
	candidateSHA = regexp.MustCompile(`^[a-f0-9]{40}$`)
	openID       = regexp.MustCompile(`^ou_[A-Za-z0-9_-]{1,252}$`)
	chatID       = regexp.MustCompile(`^oc_[A-Za-z0-9_-]{1,252}$`)
)

type CandidateRunKey struct {
	Run, SHA, Pod string
}

func (k CandidateRunKey) valid() bool {
	return runID.MatchString(k.Run) && candidateSHA.MatchString(k.SHA) && k.Pod != "" && len(k.Pod) <= 128
}

type CandidateRun struct {
	CandidateRunKey
	Tenant, App, Sender, Project, GroupChat, PrivateChat, FileToken string
	Expires                                                         time.Time
	Budget                                                          int
	MayReply, MayReconcile                                          bool
}

func (s *CandidateMessages) Open(ctx context.Context, r CandidateRun) error {
	b := s.inbox.bot
	if !s.armed || !r.CandidateRunKey.valid() || r.Tenant != b.config.TenantKey || r.App != b.config.AppID ||
		!openID.MatchString(r.Sender) || r.Project == "" || len(r.Project) > 256 || len(r.FileToken) > 256 ||
		r.GroupChat == r.PrivateChat || (r.GroupChat != "" && !chatID.MatchString(r.GroupChat)) ||
		(r.PrivateChat != "" && !chatID.MatchString(r.PrivateChat)) || r.Budget < 1 || r.Budget > 1000 ||
		!r.Expires.After(time.Now()) || time.Until(r.Expires) > 24*time.Hour {
		return ErrCandidateScope
	}
	return pgx.BeginFunc(ctx, b.pool, func(tx pgx.Tx) error {
		if err := b.lockGeneration(ctx, tx); err != nil {
			return err
		}
		var active bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM feishu_candidate_message_runs
			WHERE (tenant_key,app_id,generation)=($1,$2,$3) AND expires_at>clock_timestamp() AND (transfer OR outbound))`,
			r.Tenant, r.App, b.generation).Scan(&active); err != nil {
			return err
		}
		if active {
			return ErrCandidateScope
		}
		_, err := tx.Exec(ctx, `INSERT INTO feishu_candidate_message_runs
			(id,tenant_key,app_id,generation,candidate_sha,candidate_pod,sender_open_id,project_id,group_chat,private_chat,file_token,expires_at,budget,outbound,may_reply,may_reconcile)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
			r.Run, r.Tenant, r.App, b.generation, r.SHA, r.Pod, r.Sender, r.Project, r.GroupChat, r.PrivateChat, r.FileToken, r.Expires, r.Budget,
			r.MayReply || r.MayReconcile, r.MayReply, r.MayReconcile)
		return err
	})
}

func (s *CandidateMessages) BindingActor(ctx context.Context, binding CandidateRun) (identity.Principal, error) {
	b := s.inbox.bot
	p := identity.Principal{TenantKey: b.config.TenantKey, AppID: b.config.AppID, OpenID: binding.Sender}
	if binding.Tenant != p.TenantKey || binding.App != p.AppID {
		return p, ErrCandidateScope
	}
	err := b.pool.QueryRow(ctx, `SELECT id FROM accounts
		WHERE (tenant_key,app_id,open_id)=($1,$2,$3) AND active`, p.TenantKey, p.AppID, p.OpenID).Scan(&p.AccountID)
	return p, err
}

func (s *CandidateMessages) Stop(ctx context.Context, key CandidateRunKey, transfer, outbound bool) error {
	b := s.inbox.bot
	if !key.valid() || (!transfer && !outbound) {
		return ErrCandidateScope
	}
	tag, err := b.pool.Exec(ctx, `UPDATE feishu_candidate_message_runs SET
		transfer=transfer AND NOT $6, outbound=outbound AND NOT $7,
		transfer_stopped_at=CASE WHEN $6 THEN coalesce(transfer_stopped_at,clock_timestamp()) ELSE transfer_stopped_at END
		WHERE (tenant_key,app_id,id,candidate_sha,candidate_pod)=($1,$2,$3,$4,$5)`,
		b.config.TenantKey, b.config.AppID, key.Run, key.SHA, key.Pod, transfer, outbound)
	if err == nil && tag.RowsAffected() != 1 {
		var exists bool
		err = b.pool.QueryRow(ctx, `SELECT EXISTS(SELECT FROM feishu_candidate_message_runs WHERE (tenant_key,app_id,id)=($1,$2,$3))`,
			b.config.TenantKey, b.config.AppID, key.Run).Scan(&exists)
		if err == nil && exists {
			return ErrCandidateScope
		}
	}
	return err
}

const liveCandidateRun = `(r.tenant_key,r.app_id,r.id,r.candidate_sha,r.candidate_pod,r.generation)=($1,$2,$3,$4,$5,$6)
	AND r.expires_at>clock_timestamp()
	AND EXISTS (SELECT FROM feishu_bot_configs b
		WHERE (b.tenant_key,b.app_id,b.credentials_generation)=($1,$2,$6) AND b.enabled)`

func (s *CandidateMessages) runArgs(key CandidateRunKey) []any {
	b := s.inbox.bot
	return []any{b.config.TenantKey, b.config.AppID, key.Run, key.SHA, key.Pod, b.generation}
}

func (s *CandidateMessages) Claim(ctx context.Context, key CandidateRunKey) (*orchestrator.CandidateMessage, string, error) {
	if !s.armed || !key.valid() {
		return nil, "", ErrCandidateScope
	}
	b := s.inbox.bot
	var item orchestrator.CandidateMessage
	var file string
	err := b.pool.QueryRow(ctx, `WITH next AS (
		SELECT q.run_id,q.source_id,r.project_id,r.expires_at,r.file_token
		FROM feishu_candidate_message_queue q JOIN feishu_candidate_message_runs r ON r.id=q.run_id
		WHERE `+liveCandidateRun+` AND r.transfer AND q.ack_revision<q.revision
		AND (q.lease_until IS NULL OR q.lease_until<=clock_timestamp())
		ORDER BY q.created_at,q.source_id LIMIT 1 FOR UPDATE OF q SKIP LOCKED
	) UPDATE feishu_candidate_message_queue q
	SET lease_id=gen_random_uuid()::text,lease_until=LEAST(next.expires_at,clock_timestamp()+interval '25 seconds')
	FROM next WHERE (q.run_id,q.source_id)=(next.run_id,next.source_id)
	RETURNING q.run_id,q.source_id,q.lease_id,q.revision,q.event,next.project_id,next.expires_at,q.lease_until,next.file_token`,
		s.runArgs(key)...).Scan(&item.Run, &item.Source, &item.Lease, &item.Revision, &item.Event, &item.Project, &item.Expires, &item.LeaseExpires, &file)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", nil
	}
	return &item, file, err
}

func (s *CandidateMessages) ClaimCurrent(ctx context.Context, key CandidateRunKey, item *orchestrator.CandidateMessage) error {
	args := append(s.runArgs(key), item.Source, item.Lease, item.Revision)
	var allowed bool
	err := s.inbox.bot.pool.QueryRow(ctx, `SELECT EXISTS(SELECT FROM feishu_candidate_message_queue q
		JOIN feishu_candidate_message_runs r ON r.id=q.run_id WHERE `+liveCandidateRun+` AND r.transfer
		AND (q.source_id,q.lease_id,q.revision)=($7,$8,$9) AND q.lease_until>clock_timestamp())`, args...).Scan(&allowed)
	if err == nil && !allowed {
		return ErrCandidateScope
	}
	return err
}

func (s *CandidateMessages) SuppressCommand(ctx context.Context, key CandidateRunKey, item *orchestrator.CandidateMessage) error {
	args := append(s.runArgs(key), item.Source, item.Lease, item.Revision, item.Event)
	tag, err := s.inbox.bot.pool.Exec(ctx, `UPDATE feishu_candidate_message_queue q SET command=false,event=$10
		FROM feishu_candidate_message_runs r WHERE `+liveCandidateRun+` AND r.transfer AND q.run_id=r.id
		AND (q.source_id,q.lease_id,q.revision)=($7,$8,$9) AND q.lease_until>clock_timestamp()`, args...)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrCandidateScope
	}
	return err
}

func (s *CandidateMessages) Ack(ctx context.Context, key CandidateRunKey, source, lease string, revision int) error {
	if !s.armed || !key.valid() || revision < 1 || source == "" || lease == "" {
		return ErrCandidateScope
	}
	args := append(s.runArgs(key), source, lease, revision)
	tag, err := s.inbox.bot.pool.Exec(ctx, `UPDATE feishu_candidate_message_queue q SET ack_revision=q.revision
		FROM feishu_candidate_message_runs r WHERE `+liveCandidateRun+` AND r.transfer AND q.run_id=r.id
		AND (q.source_id,q.lease_id,q.revision)=($7,$8,$9)
		AND (q.ack_revision=q.revision OR q.lease_until>clock_timestamp())`, args...)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrCandidateScope
	}
	return err
}

// CandidateOutbound resolves the target and actor from an acknowledged work
// item. The caller owns no authority over the destination or operation key.
type CandidateOutbound struct {
	Actor         identity.Principal
	Chat, Project string
	Expires       time.Time
}

const candidateOutboundSource = `
		FROM feishu_candidate_message_runs r
		JOIN feishu_candidate_message_queue q ON q.run_id=r.id
		JOIN feishu_inbound_events e ON (e.tenant_id,e.app_id,e.message_id)=(r.tenant_key,r.app_id,q.source_id)
		JOIN feishu_inbound_work w ON (w.app_id,w.tenant_id,w.message_id)=(e.app_id,e.tenant_id,e.message_id)
		JOIN accounts a ON (a.tenant_key,a.app_id,a.id,a.open_id)=(r.tenant_key,r.app_id,w.account_id,r.sender_open_id)
		WHERE ` + liveCandidateRun + ` AND r.outbound AND q.source_id=$7
		AND (CASE WHEN $8 THEN r.may_reply ELSE r.may_reconcile END)
		AND q.command AND NOT q.withdrawn AND q.ack_revision=q.revision AND a.active AND w.state='candidate'`

func (s *CandidateMessages) ReserveOutbound(ctx context.Context, key CandidateRunKey, source string, writes bool) error {
	if !s.armed || !key.valid() || source == "" {
		return ErrCandidateScope
	}
	args := append(s.runArgs(key), source, writes)
	tag, err := s.inbox.bot.pool.Exec(ctx, `UPDATE feishu_candidate_message_runs SET budget=budget-1
		WHERE id=$3 AND budget>0 AND EXISTS(SELECT 1`+candidateOutboundSource+`)`, args...)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrCandidateScope
	}
	return err
}

func (s *CandidateMessages) WithOutbound(ctx context.Context, key CandidateRunKey, source string, writes bool,
	deliver func(CandidateOutbound) error) error {
	args := append(s.runArgs(key), source, writes)
	return pgx.BeginFunc(ctx, s.inbox.bot.pool, func(tx pgx.Tx) error {
		var out CandidateOutbound
		err := tx.QueryRow(ctx, `SELECT a.id,a.open_id,r.tenant_key,r.app_id,e.chat_id,r.project_id,r.expires_at`+
			candidateOutboundSource+` FOR SHARE OF r`, args...).Scan(
			&out.Actor.AccountID, &out.Actor.OpenID, &out.Actor.TenantKey, &out.Actor.AppID, &out.Chat, &out.Project, &out.Expires)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCandidateScope
		}
		if err != nil {
			return err
		}
		return deliver(out)
	})
}

func (s *CandidateMessages) OutboundCurrent(ctx context.Context, key CandidateRunKey, source string, writes bool) error {
	var allowed bool
	args := append(s.runArgs(key), source, writes)
	err := s.inbox.bot.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1`+candidateOutboundSource+`)`, args...).Scan(&allowed)
	if err == nil && !allowed {
		return ErrCandidateScope
	}
	return err
}

func (s *CandidateMessages) CandidateActor(ctx context.Context, key CandidateRunKey) (identity.Principal, error) {
	var p identity.Principal
	err := s.inbox.bot.pool.QueryRow(ctx, `SELECT a.id,a.open_id,r.tenant_key,r.app_id
		FROM feishu_candidate_message_runs r JOIN accounts a
		ON (a.tenant_key,a.app_id,a.open_id)=(r.tenant_key,r.app_id,r.sender_open_id)
		WHERE `+liveCandidateRun+` AND r.transfer AND a.active`, s.runArgs(key)...).Scan(&p.AccountID, &p.OpenID, &p.TenantKey, &p.AppID)
	return p, err
}
