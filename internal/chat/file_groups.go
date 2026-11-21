package chat

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Service) RunFileGroups(ctx context.Context) {
	for ctx.Err() == nil {
		worked, err := s.refreshFileGroup(ctx)
		if err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "飞书群成员同步暂不可用，将自动重试")
		}
		delay := 2 * time.Second
		if worked && err == nil {
			delay = 100 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

func (s *Service) refreshFileGroup(ctx context.Context) (bool, error) {
	_, err := s.pool.Exec(ctx, `INSERT INTO feishu_drive_group_memberships(tenant_key,app_id,chat_id,generation)
		SELECT c.tenant_key,c.app_id,c.chat_id,b.credentials_generation
		FROM feishu_chat_conversations c JOIN feishu_bot_configs b USING (tenant_key,app_id)
		WHERE c.tenant_key=$1 AND c.app_id=$2 AND c.kind='group' AND b.enabled AND b.credentials_generation=$3
		ON CONFLICT DO NOTHING`, s.tenant, s.app, s.generation)
	if err != nil {
		return false, err
	}
	var id string
	var lease time.Time
	err = s.pool.QueryRow(ctx, `WITH due AS (
		SELECT m.chat_id FROM feishu_drive_group_memberships m JOIN feishu_bot_configs b USING (tenant_key,app_id)
		WHERE m.tenant_key=$1 AND m.app_id=$2 AND b.enabled AND b.credentials_generation=$3
		  AND (m.next_at<=now() OR m.generation<>$3)
		ORDER BY m.next_at,m.chat_id LIMIT 1 FOR UPDATE OF m SKIP LOCKED
	)
	UPDATE feishu_drive_group_memberships m
	SET members=CASE WHEN generation=$3 THEN members ELSE '{}' END,
	    verified_at=CASE WHEN generation=$3 THEN verified_at END,
	    generation=$3,next_at=clock_timestamp()+interval '1 minute'
	FROM due WHERE m.tenant_key=$1 AND m.app_id=$2 AND m.chat_id=due.chat_id
	RETURNING m.chat_id,m.next_at`, s.tenant, s.app, s.generation).Scan(&id, &lease)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	upstream, cancel := context.WithTimeout(ctx, 20*time.Second)
	members, listErr := s.fileGroupMembers(upstream, id)
	cancel()
	fresh := listErr == nil || errors.Is(listErr, ErrAccessLost)
	_, err = s.pool.Exec(ctx, `UPDATE feishu_drive_group_memberships
	SET members=CASE WHEN $6 THEN $7 ELSE members END,
	    verified_at=CASE WHEN $6 THEN clock_timestamp() ELSE verified_at END,
	    next_at=clock_timestamp()+CASE WHEN $6 THEN interval '30 seconds' ELSE interval '10 seconds' END
	WHERE (tenant_key,app_id,chat_id,generation,next_at)=($1,$2,$3,$4,$5)
	AND EXISTS (SELECT FROM feishu_bot_configs
	    WHERE tenant_key=$1 AND app_id=$2 AND enabled AND credentials_generation=$4)`,
		s.tenant, s.app, id, s.generation, lease, fresh, members)
	return true, err
}

func (s *Service) fileGroupMembers(ctx context.Context, id string) ([]string, error) {
	members := []string{}
	seen, cursors := map[string]bool{}, map[string]bool{}
	for cursor := ""; ; {
		page, err := s.source.Members(ctx, id, cursor)
		if err != nil {
			return []string{}, err
		}
		for _, member := range page.Items {
			if strings.HasPrefix(member.OpenID, "ou_") && validIdentifier(member.OpenID) && !seen[member.OpenID] {
				seen[member.OpenID] = true
				members = append(members, member.OpenID)
			}
		}
		if page.Next == "" {
			return members, nil
		}
		if cursors[page.Next] {
			return []string{}, ErrUnavailable
		}
		cursors[page.Next], cursor = true, page.Next
	}
}
