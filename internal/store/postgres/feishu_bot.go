package postgres

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/system"
)

const (
	botLeaseSeconds = 30
	bodyFingerprint = "{}"
	resetConnection = `connection_generation=feishu_bot_configs.connection_generation+1,
		connection_state='unavailable', last_heartbeat_at=NULL, last_message_at=NULL`
	checkColumns = `id, state, credentials_generation, created_at, started_at, finished_at, results`
)

type FeishuBot struct {
	pool       *pgxpool.Pool
	config     system.Config
	generation int64
	heartbeat  time.Duration
	authorize  system.AuthorizeWrite
	audit      system.AuditChange
}

func NewFeishuBot(pool *pgxpool.Pool, config system.Config, generation int64, heartbeat time.Duration,
	authorize system.AuthorizeWrite, audit system.AuditChange) *FeishuBot {
	return &FeishuBot{pool: pool, config: config, generation: generation, heartbeat: heartbeat, authorize: authorize, audit: audit}
}

// Activate selects the deployment's credentials generation. An older process cannot
// restore its generation, even if it restarts after a rotation.
func (s *FeishuBot) Activate(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO feishu_bot_configs (tenant_key, app_id, credentials_generation)
			VALUES ($1,$2,$3) ON CONFLICT (tenant_key,app_id) DO UPDATE
			SET credentials_generation=EXCLUDED.credentials_generation, bot_name='', bot_open_id='', `+resetConnection+`
			WHERE feishu_bot_configs.credentials_generation < EXCLUDED.credentials_generation`,
			s.config.TenantKey, s.config.AppID, s.generation)
		if err != nil {
			return err
		}
		if err := s.lockGeneration(ctx, tx); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `WITH instant AS (SELECT clock_timestamp() AS at)
			UPDATE feishu_bot_checks SET state='failed', lease_until=NULL,
			started_at=coalesce(started_at, at), finished_at=at,
			results=CASE WHEN results->0->>'state' = 'passed' THEN results || jsonb_build_array(
				jsonb_build_object('name','bot_info','state','failed','checked_at',to_char(at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
				'error','provider_temporarily_unavailable'))
			ELSE jsonb_build_array(
				jsonb_build_object('name','tenant_token','state','failed','checked_at',to_char(at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
				'error','provider_temporarily_unavailable'),
				jsonb_build_object('name','bot_info','state','skipped','checked_at',to_char(at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'))) END
			FROM instant WHERE tenant_key=$1 AND app_id=$2 AND credentials_generation<$3 AND state IN ('queued','running')`,
			s.config.TenantKey, s.config.AppID, s.generation)
		return err
	})
}

type queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (s *FeishuBot) Status(ctx context.Context) (system.Status, error) {
	return s.status(ctx, s.pool)
}

func (s *FeishuBot) status(ctx context.Context, q queryer) (system.Status, error) {
	status := system.Status{Config: s.config, Connection: system.Connection{State: "disabled"}}
	var generation int64
	err := q.QueryRow(ctx, `WITH instant AS (SELECT clock_timestamp() AS at)
		SELECT credentials_generation, enabled, version, bot_name,
		CASE WHEN NOT enabled THEN 'disabled'
			WHEN last_heartbeat_at IS NULL OR last_heartbeat_at > at
				OR last_heartbeat_at <= at - $3::bigint * interval '1 microsecond' THEN 'unavailable'
			ELSE connection_state END,
		CASE WHEN enabled AND last_heartbeat_at <= at THEN last_heartbeat_at END,
		CASE WHEN enabled AND last_message_at <= at THEN last_message_at END
		FROM feishu_bot_configs CROSS JOIN instant WHERE tenant_key=$1 AND app_id=$2`,
		s.config.TenantKey, s.config.AppID, s.heartbeat.Microseconds()).Scan(
		&generation, &status.Enabled, &status.Version, &status.BotName,
		&status.Connection.State, &status.Connection.LastHeartbeatAt, &status.Connection.LastMessageAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return status, nil
	}
	status.CredentialsConfigured = status.CredentialsConfigured && generation == s.generation
	utc(status.Connection.LastHeartbeatAt)
	utc(status.Connection.LastMessageAt)
	return status, err
}

func (s *FeishuBot) Configure(ctx context.Context, actor identity.Principal, match string, enabled bool) (system.Status, error) {
	var status system.Status
	err := s.write(ctx, actor, "system.configure", func(tx pgx.Tx) error {
		var err error
		status, err = s.status(ctx, tx)
		if err != nil {
			return err
		}
		if match != status.ETag() {
			return system.ErrVersionConflict
		}
		if status.Enabled == enabled {
			return nil
		}
		_, err = tx.Exec(ctx, `UPDATE feishu_bot_configs SET enabled=$3, version=version+1, `+resetConnection+
			` WHERE tenant_key=$1 AND app_id=$2`, s.config.TenantKey, s.config.AppID, enabled)
		if err != nil {
			return err
		}
		if err := s.audit(ctx, tx, actor, status.Version+1); err != nil {
			return err
		}
		status, err = s.status(ctx, tx)
		return err
	})
	return status, err
}

func (s *FeishuBot) Submit(ctx context.Context, actor identity.Principal, key string) (system.Submission, error) {
	var submission system.Submission
	err := s.write(ctx, actor, "system.read", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM feishu_bot_check_keys k USING feishu_bot_checks c
			WHERE k.tenant_key=$1 AND k.app_id=$2 AND k.check_id=c.id
			AND c.finished_at <= clock_timestamp() - interval '24 hours'`, s.config.TenantKey, s.config.AppID)
		if err != nil {
			return err
		}
		var fingerprint string
		err = tx.QueryRow(ctx, `SELECT c.id,c.state,k.fingerprint FROM feishu_bot_check_keys k
			JOIN feishu_bot_checks c ON c.id=k.check_id WHERE k.tenant_key=$1 AND k.app_id=$2 AND k.key=$3`,
			s.config.TenantKey, s.config.AppID, key).Scan(&submission.ID, &submission.State, &fingerprint)
		switch {
		case err == nil:
			submission.Reused = true
			if fingerprint != bodyFingerprint {
				return system.ErrKeyConflict
			}
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		err = tx.QueryRow(ctx, `SELECT id,state FROM feishu_bot_checks WHERE tenant_key=$1 AND app_id=$2
			AND credentials_generation=$3 AND state IN ('queued','running')`, s.config.TenantKey, s.config.AppID, s.generation).
			Scan(&submission.ID, &submission.State)
		switch {
		case err == nil:
			submission.Reused = true
		case errors.Is(err, pgx.ErrNoRows):
			err = tx.QueryRow(ctx, `INSERT INTO feishu_bot_checks (tenant_key,app_id,credentials_generation)
				VALUES ($1,$2,$3) RETURNING id,state`, s.config.TenantKey, s.config.AppID, s.generation).
				Scan(&submission.ID, &submission.State)
			if err != nil {
				return err
			}
		default:
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO feishu_bot_check_keys (tenant_key,app_id,key,credentials_generation,fingerprint,check_id)
			VALUES ($1,$2,$3,$4,$5,$6)`, s.config.TenantKey, s.config.AppID, key, s.generation, bodyFingerprint, submission.ID)
		return err
	})
	return submission, err
}

func (s *FeishuBot) Check(ctx context.Context, id string) (system.Check, error) {
	var check system.Check
	var generation int64
	err := s.pool.QueryRow(ctx, `SELECT `+checkColumns+` FROM feishu_bot_checks
		WHERE tenant_key=$1 AND app_id=$2 AND id=$3`, s.config.TenantKey, s.config.AppID, id).
		Scan(&check.ID, &check.State, &generation, &check.CreatedAt, &check.StartedAt, &check.FinishedAt, &check.Results)
	if errors.Is(err, pgx.ErrNoRows) {
		return check, system.ErrCheckNotFound
	}
	check.Generation = strconv.FormatInt(generation, 10)
	check.CreatedAt = check.CreatedAt.UTC()
	utc(check.StartedAt)
	utc(check.FinishedAt)
	return check, err
}

func utc(t *time.Time) {
	if t != nil {
		*t = t.UTC()
	}
}

func (s *FeishuBot) write(ctx context.Context, actor identity.Principal, action string, update func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.authorize(ctx, tx, actor, action); err != nil {
			return err
		}
		if err := s.lockGeneration(ctx, tx); err != nil {
			return err
		}
		return update(tx)
	})
}

func (s *FeishuBot) lockGeneration(ctx context.Context, tx pgx.Tx) error {
	var current int64
	err := tx.QueryRow(ctx, `SELECT credentials_generation FROM feishu_bot_configs
		WHERE tenant_key=$1 AND app_id=$2 FOR UPDATE`, s.config.TenantKey, s.config.AppID).Scan(&current)
	if err != nil {
		return err
	}
	if s.generation != current {
		return system.ErrStaleGeneration
	}
	return nil
}

func (s *FeishuBot) Claim(ctx context.Context) (system.Claim, error) {
	var claim system.Claim
	err := s.withGeneration(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `UPDATE feishu_bot_checks SET state='running', claim_version=claim_version+1,
			started_at=coalesce(started_at,clock_timestamp()), lease_until=clock_timestamp()+$4::integer*interval '1 second', results='[]'
			WHERE tenant_key=$1 AND app_id=$2 AND credentials_generation=$3
			AND (state='queued' OR (state='running' AND lease_until<=clock_timestamp()))
			RETURNING id,claim_version`, s.config.TenantKey, s.config.AppID, s.generation, botLeaseSeconds).
			Scan(&claim.ID, &claim.Version)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	return claim, err
}

const heldClaim = `tenant_key=$1 AND app_id=$2 AND credentials_generation=$3
	AND id=$4 AND claim_version=$5 AND state='running' AND lease_until>clock_timestamp()`

func (s *FeishuBot) Renew(ctx context.Context, claim system.Claim, stage system.Result) (time.Time, error) {
	var at time.Time
	err := s.withGeneration(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&at); err != nil {
			return err
		}
		stage.CheckedAt = at.UTC()
		tag, err := tx.Exec(ctx, `UPDATE feishu_bot_checks SET lease_until=clock_timestamp()+$6::integer*interval '1 second',
			results=$7 WHERE `+heldClaim, s.config.TenantKey, s.config.AppID, s.generation,
			claim.ID, claim.Version, botLeaseSeconds, []system.Result{stage})
		return requireClaim(tag.RowsAffected(), err)
	})
	return at.UTC(), err
}

func (s *FeishuBot) Finish(ctx context.Context, claim system.Claim, results []system.Result, info system.BotInfo) error {
	return s.withGeneration(ctx, func(tx pgx.Tx) error {
		var finished time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&finished); err != nil {
			return err
		}
		results[1].CheckedAt = finished.UTC()
		state := "failed"
		if results[1].State == "passed" {
			state = "succeeded"
		}
		tag, err := tx.Exec(ctx, `UPDATE feishu_bot_checks SET state=$6, lease_until=NULL, finished_at=$7, results=$8 WHERE `+
			heldClaim, s.config.TenantKey, s.config.AppID, s.generation, claim.ID, claim.Version, state, finished, results)
		if err := requireClaim(tag.RowsAffected(), err); err != nil {
			return err
		}
		if state == "succeeded" {
			_, err = tx.Exec(ctx, `UPDATE feishu_bot_configs SET bot_name=$3, bot_open_id=$4
				WHERE tenant_key=$1 AND app_id=$2`, s.config.TenantKey, s.config.AppID, info.Name, info.OpenID)
		}
		return err
	})
}

func requireClaim(affected int64, err error) error {
	if err == nil && affected != 1 {
		return system.ErrCheckNotFound
	}
	return err
}

func (s *FeishuBot) withGeneration(ctx context.Context, update func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.lockGeneration(ctx, tx); err != nil {
			return err
		}
		return update(tx)
	})
}
