package access

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"

	"github.com/uniquechao/OmniRelay/internal/identity"
)

// ProjectRoleDefinitions is the same catalog used by system role management.
func ProjectRoleDefinitions() []roleDefinition {
	result := []roleDefinition{}
	for _, role := range listRoleDefinitions() {
		if role.Scope == "project" {
			result = append(result, role)
		}
	}
	return result
}
func IsProjectRole(id string) bool { return roleCatalog[id].Scope == "project" }

// EffectiveProjectRoleSQL uses the project_members alias m. Expired grants do not count.
const EffectiveProjectRoleSQL = `CASE WHEN m.role='manager' OR EXISTS(SELECT FROM role_grants g
 WHERE (g.tenant_key,g.app_id,g.project_id,g.account_id,g.membership_generation)=(m.tenant_key,m.app_id,m.project_id,m.account_id,m.generation)
 AND g.role='project_admin' AND g.revoked_at IS NULL) THEN 'project_admin' ELSE 'project_member' END`

// SetProjectAdministratorInMutation shares validation, audit, permission invalidation
// and session revocation with system role management. The caller holds the organization lock.
func (s *Service) SetProjectAdministratorInMutation(ctx context.Context, tx pgx.Tx, actor identity.Principal, projectID, accountID string, enabled bool) error {
	if !validIdentifier(projectID) || !validIdentifier(accountID) {
		return errInvalidInput
	}
	if err := s.require(ctx, tx, actor, "project.delegate", projectID); err != nil {
		return err
	}
	if enabled {
		_, err := s.grantRoleInMutation(ctx, tx, actor, grantRequest{AccountID: accountID, Role: RoleProjectAdmin, ProjectID: projectID})
		return err
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM role_grants WHERE tenant_key=$1 AND app_id=$2 AND project_id=$3 AND account_id=$4 AND role='project_admin' AND revoked_at IS NULL`, s.tenantKey, s.appID, projectID, accountID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.revokeRoleInMutation(ctx, tx, actor, id)
}
