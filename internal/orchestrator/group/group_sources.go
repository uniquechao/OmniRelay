package group

import (
	"context"
	"encoding/json"
	"maps"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/agent/pmtools"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/project"
)

const groupBoardRead = "group_open_tasks"

func (w work) groupSources() bool {
	return w.Address.Mode == GroupReply && w.Plan.GroupReply != nil && w.Plan.GroupReply.Scope == "group" && (w.Plan.Mode == "read" || w.Plan.Mode == "draft")
}

// Resolve the group and cutoff from the admitted work after model arguments have
// passed the public schema. Store these effective arguments for the delivery check.
func (s *Service) groupReadArgs(ctx context.Context, tx pgx.Tx, w work, name string, args pmtools.Args) (string, pmtools.Args, error) {
	if !w.groupSources() {
		return name, args, nil
	}
	switch name {
	case "chat_search_messages", "task_board_get":
		if target := args.String("chat_id"); target != "" && target != w.Address.Chat {
			return "", nil, access.ErrForbidden
		}
		out := maps.Clone(args)
		if out == nil {
			out = pmtools.Args{}
		}
		out["chat_id"], _ = json.Marshal(w.Address.Chat)
		if name == "task_board_get" {
			return groupBoardRead, out, nil
		}
		var cutoff time.Time
		if err := tx.QueryRow(ctx, "SELECT group_asked_at FROM agent_group_work WHERE id=$1", w.Work).Scan(&cutoff); err != nil {
			return "", nil, err
		}
		out["received_at"], _ = json.Marshal(cutoff.UTC())
		return project.GroupMessageRead, out, nil
	default:
		return name, args, nil
	}
}

func (s *Service) groupBoardProof(ctx context.Context, tx pgx.Tx, actor identity.Principal, projectID, chatID string) (any, error) {
	ok, err := s.Roles.CheckInOrganizationMutation(ctx, tx, actor, "project.read", projectID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, access.ErrForbidden
	}
	w := work{Actor: actor, Scope: Who{ProjectID: projectID}, Address: Envelope{Tenant: actor.TenantKey, App: actor.AppID, Mode: GroupReply, Chat: chatID, Project: projectID}}
	body, err := s.openGroupTasks(ctx, tx, w, false)
	return body.PlainText(), err
}
