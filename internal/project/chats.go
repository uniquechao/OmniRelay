package project

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

type assignedGroup struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	chat.ProjectAssignment
}

type projectGroups struct {
	ProjectID      string          `json:"project_id"`
	PrimaryVersion int64           `json:"primary_version"`
	Items          []assignedGroup `json:"items"`
	Total          int             `json:"total"`
}

func (s *Service) projectGroups(ctx context.Context, actor identity.Principal, id string) (projectGroups, error) {
	result := projectGroups{ProjectID: id, Items: []assignedGroup{}}
	rows, err := s.pool.Query(ctx, `SELECT coalesce(p.version,0),coalesce(a.chat_id,''),
		coalesce(a.primary_group,false),coalesce(a.version,0)
		FROM (VALUES ($1::text,$2::text,$3::text)) AS scope(tenant_key,app_id,project_id)
		LEFT JOIN project_chat_primary p USING (tenant_key,app_id,project_id)
		LEFT JOIN project_chat_assignments a USING (tenant_key,app_id,project_id)
		ORDER BY a.chat_id`, actor.TenantKey, actor.AppID, id)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		group := assignedGroup{ProjectAssignment: chat.ProjectAssignment{ProjectID: id}}
		if err := rows.Scan(&result.PrimaryVersion, &group.ID, &group.Primary, &group.Version); err != nil {
			return result, err
		}
		if group.ID != "" {
			result.Items = append(result.Items, group)
		}
	}
	return result, rows.Err()
}

func (s *Service) getChats(_ http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
	ctx, id := r.Context(), r.PathValue("projectID")
	snapshot, err := s.projectGroups(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	visible := make([]assignedGroup, 0, len(snapshot.Items))
	for _, group := range snapshot.Items {
		entry, err := s.chat.ProjectGroup(ctx, actor, group.ID)
		if errors.Is(err, chat.ErrAccessLost) {
			continue
		}
		if err != nil {
			return nil, err
		}
		group.Name = entry.Name
		visible = append(visible, group)
	}
	current, err := s.projectGroups(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	if current.PrimaryVersion != snapshot.PrimaryVersion || !slices.Equal(current.Items, snapshot.Items) {
		return nil, errConflict
	}
	snapshot.Items, snapshot.Total = visible, len(visible)
	return snapshot, nil
}

func assignment(ctx context.Context, tx interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, actor identity.Principal, groupID string) (chat.ProjectAssignment, error) {
	var result chat.ProjectAssignment
	err := tx.QueryRow(ctx, `SELECT coalesce(project_id,''),primary_group,version FROM project_chat_assignments
		WHERE (tenant_key,app_id,chat_id)=($1,$2,$3)`, actor.TenantKey, actor.AppID, groupID).
		Scan(&result.ProjectID, &result.Primary, &result.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, errDenied
	}
	return result, err
}

func writeAssignment(ctx context.Context, tx pgx.Tx, actor identity.Principal, groupID, projectID string) error {
	if projectID == "" {
		_, err := tx.Exec(ctx, `DELETE FROM project_chat_links WHERE (tenant_key,app_id,chat_id)=($1,$2,$3)`,
			actor.TenantKey, actor.AppID, groupID)
		if err != nil {
			return err
		}
	} else {
		_, err := tx.Exec(ctx, `INSERT INTO project_chat_links (tenant_key,app_id,chat_id,project_id)
			VALUES ($1,$2,$3,$4) ON CONFLICT (tenant_key,app_id,chat_id)
			DO UPDATE SET project_id=EXCLUDED.project_id`, actor.TenantKey, actor.AppID, groupID, projectID)
		if err != nil {
			return err
		}
	}
	return bumpAssignmentVersions(ctx, tx, actor, groupID)
}

func bumpAssignmentVersions(ctx context.Context, tx pgx.Tx, actor identity.Principal, groupIDs ...string) error {
	_, err := tx.Exec(ctx, `UPDATE feishu_chat_conversations SET project_assignment_version=project_assignment_version+1
		WHERE (tenant_key,app_id)=($1,$2) AND chat_id=ANY($3::text[])`, actor.TenantKey, actor.AppID, groupIDs)
	return err
}

func (s *Service) requireSettingsMutation(ctx context.Context, tx pgx.Tx, actor identity.Principal, projectID string) error {
	allowed, err := s.roles.CheckInOrganizationMutation(ctx, tx, actor, "project.settings", projectID)
	if err != nil {
		return err
	}
	if !allowed {
		return errDenied
	}
	return nil
}

func (s *Service) setAssignment(ctx context.Context, actor identity.Principal, groupID, next string, version int64) (chat.ProjectAssignment, error) {
	if !identifier.MatchString(groupID) || (next != "" && !identifier.MatchString(next)) || version < 0 {
		return chat.ProjectAssignment{}, errInput
	}
	var result chat.ProjectAssignment
	err := s.roles.WithOrganizationMutation(ctx, func(tx pgx.Tx) error {
		if _, err := s.chat.ProjectGroupForMutation(ctx, tx, actor, groupID); err != nil {
			return err
		}
		prior, err := assignment(ctx, tx, actor, groupID)
		if err != nil {
			return err
		}
		if prior.ProjectID != "" {
			if err := s.requireSettingsMutation(ctx, tx, actor, prior.ProjectID); err != nil {
				return err
			}
		}
		if next != "" && next != prior.ProjectID {
			if err := s.requireSettingsMutation(ctx, tx, actor, next); err != nil {
				return err
			}
		}
		if prior.Version != version {
			return errConflict
		}
		if prior.ProjectID == next {
			result = prior
			return nil
		}
		if prior.Primary {
			if _, err := tx.Exec(ctx, `UPDATE project_chat_primary SET chat_id=NULL,version=version+1
				WHERE (tenant_key,app_id,project_id)=($1,$2,$3)`, actor.TenantKey, actor.AppID, prior.ProjectID); err != nil {
				return err
			}
		}
		// Disarm any plan using the departing group before its link is removed.
		if prior.ProjectID != "" {
			if _, err := tx.Exec(ctx, `UPDATE project_relay_settings SET target_chat_id=NULL,
				plan_enabled=false,public_submission_count=false,version=version+1
				WHERE (tenant_key,app_id,project_id,target_chat_id)=($1,$2,$3,$4)`,
				actor.TenantKey, actor.AppID, prior.ProjectID, groupID); err != nil {
				return err
			}
		}
		if err := writeAssignment(ctx, tx, actor, groupID, next); err != nil {
			return err
		}
		result = chat.ProjectAssignment{ProjectID: next, Version: prior.Version + 1}
		action := "project.chat.assign"
		if next == "" {
			action = "project.chat.clear"
		}
		return auditAssignment(ctx, tx, actor, action, groupID, result.Version,
			map[string]any{"previous_project_id": prior.ProjectID, "project_id": next, "cleared_primary": prior.Primary})
	})
	return result, err
}

type primaryGroup struct {
	ChatID  string `json:"chat_id"`
	Version int64  `json:"primary_version"`
}

func (s *Service) setPrimary(ctx context.Context, actor identity.Principal, id, groupID string, linkVersion, primaryVersion int64) (primaryGroup, error) {
	if !identifier.MatchString(groupID) || linkVersion < 0 || primaryVersion < 0 {
		return primaryGroup{}, errInput
	}
	var result primaryGroup
	err := s.roles.WithOrganizationMutation(ctx, func(tx pgx.Tx) error {
		if _, err := s.chat.ProjectGroupForMutation(ctx, tx, actor, groupID); err != nil {
			return err
		}
		if err := s.requireSettingsMutation(ctx, tx, actor, id); err != nil {
			return err
		}
		link, err := assignment(ctx, tx, actor, groupID)
		if err != nil {
			return err
		}
		if link.ProjectID != id || link.Version != linkVersion {
			return errConflict
		}
		err = tx.QueryRow(ctx, `SELECT coalesce(chat_id,''),version FROM project_chat_primary
			WHERE (tenant_key,app_id,project_id)=($1,$2,$3)`, actor.TenantKey, actor.AppID, id).
			Scan(&result.ChatID, &result.Version)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if result.Version != primaryVersion {
			return errConflict
		}
		if result.ChatID == groupID {
			return nil
		}
		priorID := result.ChatID
		err = tx.QueryRow(ctx, `INSERT INTO project_chat_primary (tenant_key,app_id,project_id,chat_id,version)
			VALUES ($1,$2,$3,$4,1) ON CONFLICT (tenant_key,app_id,project_id)
			DO UPDATE SET chat_id=EXCLUDED.chat_id,version=project_chat_primary.version+1 RETURNING version`,
			actor.TenantKey, actor.AppID, id, groupID).Scan(&result.Version)
		if err != nil {
			return err
		}
		result.ChatID = groupID
		if err := bumpAssignmentVersions(ctx, tx, actor, groupID, priorID); err != nil {
			return err
		}
		return auditAssignment(ctx, tx, actor, "project.chat.set_primary", groupID, result.Version,
			map[string]string{"project_id": id, "previous_primary_chat_id": priorID})
	})
	return result, err
}

func auditAssignment(ctx context.Context, tx pgx.Tx, actor identity.Principal, action, groupID string, version int64, details any) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit_events
		(tenant_key,app_id,actor_account_id,action,resource_type,resource_id,result,change_version,details)
		VALUES ($1,$2,$3,$4,'chat',$5,'success',$6,$7)`,
		actor.TenantKey, actor.AppID, actor.AccountID, action, groupID, version, details)
	return err
}
