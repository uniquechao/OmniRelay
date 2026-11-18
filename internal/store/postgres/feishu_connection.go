package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
	"github.com/uniquechao/OmniRelay/internal/system"
)

// ConnectionFence reads the configuration snapshot used for connection activation.
func (s *FeishuBot) ConnectionFence(ctx context.Context) (orchestrator.ConnectionFence, error) {
	var fence orchestrator.ConnectionFence
	err := s.pool.QueryRow(ctx, `SELECT version, connection_generation FROM feishu_bot_configs
		WHERE tenant_key=$1 AND app_id=$2 AND credentials_generation=$3 AND enabled`,
		s.config.TenantKey, s.config.AppID, s.generation).Scan(&fence.ConfigVersion, &fence.ConnectionGeneration)
	return fence, connectionError(err)
}

// StartConnection replaces the snapshot generation after the coordinator acquires
// the external lease. The returned fence belongs to the new connection only.
func (s *FeishuBot) StartConnection(ctx context.Context, snapshot orchestrator.ConnectionFence) (orchestrator.ConnectionFence, error) {
	var fence orchestrator.ConnectionFence
	err := s.withGeneration(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `UPDATE feishu_bot_configs
			SET connection_generation=connection_generation+1, connection_state='connecting',
			connection_lease_until=clock_timestamp()+$5::bigint*interval '1 microsecond',
			last_heartbeat_at=NULL, last_message_at=NULL
			WHERE `+connectionMatch+` RETURNING version, connection_generation`,
			s.config.TenantKey, s.config.AppID, snapshot.ConfigVersion, snapshot.ConnectionGeneration,
			s.heartbeat.Microseconds()).Scan(&fence.ConfigVersion, &fence.ConnectionGeneration)
	})
	if err != nil {
		return orchestrator.ConnectionFence{}, connectionError(err)
	}
	return fence, nil
}

const connectionMatch = `tenant_key=$1 AND app_id=$2 AND enabled AND version=$3 AND connection_generation=$4`
const liveConnection = connectionMatch + ` AND connection_lease_until>clock_timestamp()`

// RenewConnection publishes a healthy heartbeat while the coordinator still owns
// the external lease. An expired database lease must start a fresh generation.
func (s *FeishuBot) RenewConnection(ctx context.Context, fence orchestrator.ConnectionFence) error {
	return s.updateConnection(ctx, fence, `WITH instant AS (SELECT clock_timestamp() AS at)
		UPDATE feishu_bot_configs SET connection_state='connected', last_heartbeat_at=at,
		connection_lease_until=at+$5::bigint*interval '1 microsecond'
		FROM instant WHERE `+liveConnection, s.heartbeat.Microseconds())
}

// EndConnection fences late callbacks when the coordinator releases or loses ownership.
func (s *FeishuBot) EndConnection(ctx context.Context, fence orchestrator.ConnectionFence) error {
	return s.updateConnection(ctx, fence, `UPDATE feishu_bot_configs SET `+resetConnection+
		` WHERE `+connectionMatch+` AND connection_lease_until IS NOT NULL`)
}

func (s *FeishuBot) updateConnection(ctx context.Context, fence orchestrator.ConnectionFence, query string, extra ...any) error {
	err := s.withGeneration(ctx, func(tx pgx.Tx) error {
		args := []any{s.config.TenantKey, s.config.AppID, fence.ConfigVersion, fence.ConnectionGeneration}
		tag, err := tx.Exec(ctx, query, append(args, extra...)...)
		if err == nil && tag.RowsAffected() == 0 {
			return orchestrator.ErrStaleConnection
		}
		return err
	})
	return connectionError(err)
}

func connectionError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, system.ErrStaleGeneration) {
		return orchestrator.ErrStaleConnection
	}
	return err
}
