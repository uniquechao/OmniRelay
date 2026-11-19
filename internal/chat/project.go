package chat

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
)

type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type ProjectAssignment struct {
	ProjectID string `json:"project_id"`
	Primary   bool   `json:"primary"`
	Version   int64  `json:"version"`
}

// ProjectGroup checks a linked group using the reader's own source visibility.
func (s *Service) ProjectGroup(ctx context.Context, actor identity.Principal, id string) (Group, error) {
	return s.projectGroup(ctx, s.pool, actor, id)
}

func (s *Service) ProjectGroupForMutation(ctx context.Context, tx pgx.Tx, actor identity.Principal, id string) (Group, error) {
	return s.projectGroup(ctx, tx, actor, id)
}

func (s *Service) projectGroup(ctx context.Context, db rowQuerier, actor identity.Principal, id string) (Group, error) {
	if err := s.activeAccount(ctx, db, actor); err != nil {
		return Group{}, err
	}
	var group bool
	err := db.QueryRow(ctx, `SELECT kind=$4 FROM feishu_chat_conversations
		WHERE (tenant_key,app_id,chat_id)=($1,$2,$3)`, s.tenant, s.app, id, orchestrator.ChatGroup).Scan(&group)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !group) {
		return Group{}, ErrAccessLost
	}
	if err != nil {
		return Group{}, err
	}
	member, err := s.hasMember(ctx, id, actor.OpenID)
	if err != nil {
		return Group{}, err
	}
	if !member {
		return Group{}, ErrAccessLost
	}
	result, err := s.source.Group(ctx, id)
	if err != nil {
		return Group{}, err
	}
	if _, err := s.source.History(ctx, HistoryQuery{ChatID: id, Limit: 1}); err != nil {
		return Group{}, err
	}
	return result, nil
}
