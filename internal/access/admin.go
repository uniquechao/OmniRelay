package access

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

type Account struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	AvatarURL string    `json:"avatarUrl"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

const accountColumns = "id, name, avatar_url, active, created_at, updated_at"

func scanAccount(row pgx.Row) (Account, error) {
	var a Account
	err := row.Scan(&a.ID, &a.Name, &a.AvatarURL, &a.Active, &a.CreatedAt, &a.UpdatedAt)
	return a, err
}

func (s *Service) listAccounts(ctx context.Context, after string, limit int, active *bool) ([]Account, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+accountColumns+` FROM accounts
		WHERE tenant_key=$1 AND app_id=$2 AND id>$3 AND ($5::bool IS NULL OR active=$5)
		ORDER BY id LIMIT $4`, s.tenantKey, s.appID, after, limit, active)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Account, error) {
		return scanAccount(row)
	})
}

func (s *Service) preauthorizeAccount(ctx context.Context, actor identity.Principal, openID, name string) (Account, error) {
	var account Account
	err := s.mutate(ctx, func(tx pgx.Tx) error {
		if err := s.require(ctx, tx, actor, "account.manage", ""); err != nil {
			return err
		}
		var err error
		account, err = scanAccount(tx.QueryRow(ctx, `INSERT INTO accounts (tenant_key,app_id,open_id,name)
			VALUES ($1,$2,$3,$4) ON CONFLICT (tenant_key,app_id,open_id) DO NOTHING
			RETURNING `+accountColumns, s.tenantKey, s.appID, openID, name))
		if errors.Is(err, pgx.ErrNoRows) {
			return errDuplicateAccount
		}
		if err != nil {
			return err
		}
		return s.audit(ctx, tx, actor.AccountID, "account.preauthorize", "account", account.ID, 0)
	})
	return account, err
}

func (s *Service) setAccountActive(ctx context.Context, actor identity.Principal, accountID string, active bool) (Account, error) {
	var account Account
	err := s.mutate(ctx, func(tx pgx.Tx) error {
		if err := s.require(ctx, tx, actor, "account.manage", ""); err != nil {
			return err
		}
		var err error
		account, err = scanAccount(tx.QueryRow(ctx, `SELECT `+accountColumns+` FROM accounts
			WHERE tenant_key=$1 AND app_id=$2 AND id=$3 FOR NO KEY UPDATE`, s.tenantKey, s.appID, accountID))
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil || account.Active == active {
			return err
		}
		if !active {
			if err := s.protectLastAdmin(ctx, tx, accountID); err != nil {
				return err
			}
		}
		var version int64
		err = tx.QueryRow(ctx, `UPDATE accounts SET active=$2, updated_at=now(), permission_version=permission_version+1
			WHERE id=$1 RETURNING active, updated_at, permission_version`, accountID, active).
			Scan(&account.Active, &account.UpdatedAt, &version)
		if err != nil {
			return err
		}
		action := "account.enable"
		if !active {
			action = "account.disable"
			if err := revokeSessions(ctx, tx, accountID); err != nil {
				return err
			}
		}
		return s.audit(ctx, tx, actor.AccountID, action, "account", accountID, version)
	})
	return account, err
}

func (s *Service) protectLastAdmin(ctx context.Context, tx pgx.Tx, accountID string) error {
	var wouldRemoveLast bool
	err := tx.QueryRow(ctx, `SELECT
		COALESCE(bool_or(a.id=$3), false) AND NOT COALESCE(bool_or(a.id<>$3), false)
		FROM accounts a JOIN role_grants g
		ON g.tenant_key=a.tenant_key AND g.app_id=a.app_id AND g.account_id=a.id
		WHERE a.tenant_key=$1 AND a.app_id=$2 AND a.active
		AND g.project_id IS NULL AND g.role='system_admin' AND g.revoked_at IS NULL`,
		s.tenantKey, s.appID, accountID).Scan(&wouldRemoveLast)
	if err == nil && wouldRemoveLast {
		return errLastAdmin
	}
	return err
}

func revokeSessions(ctx context.Context, tx pgx.Tx, accountID string) error {
	_, err := tx.Exec(ctx, "DELETE FROM web_sessions WHERE account_id=$1", accountID)
	return err
}

type RoleGrant struct {
	ID        string    `json:"id"`
	AccountID string    `json:"accountId"`
	Role      string    `json:"role"`
	ProjectID string    `json:"projectId,omitempty"`
	Source    string    `json:"source"`
	GrantedBy string    `json:"grantedBy,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

const grantColumns = "id, account_id, role, coalesce(project_id,''), source, coalesce(granted_by,''), created_at"

func scanGrant(row pgx.Row) (RoleGrant, error) {
	var grant RoleGrant
	err := row.Scan(&grant.ID, &grant.AccountID, &grant.Role, &grant.ProjectID, &grant.Source, &grant.GrantedBy, &grant.CreatedAt)
	return grant, err
}

func (s *Service) listGrants(ctx context.Context, projectID, accountID, after string, limit int) ([]RoleGrant, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+grantColumns+` FROM role_grants
		WHERE tenant_key=$1 AND app_id=$2 AND project_id IS NOT DISTINCT FROM nullif($3,'')
		AND revoked_at IS NULL AND ($4='' OR account_id=$4) AND id>$5 ORDER BY id LIMIT $6`,
		s.tenantKey, s.appID, projectID, accountID, after, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (RoleGrant, error) {
		return scanGrant(row)
	})
}

type grantRequest struct {
	AccountID string `json:"accountId"`
	Role      string `json:"role"`
	ProjectID string `json:"projectId"`
}

func (r grantRequest) valid() bool {
	if !validIdentifier(r.AccountID) {
		return false
	}
	switch r.Role {
	case RoleSystemAdmin, RoleSystemOperator, RoleAuditViewer:
		return r.ProjectID == ""
	case RoleProjectAdmin:
		return validIdentifier(r.ProjectID)
	default:
		return false
	}
}

func (s *Service) grantRole(ctx context.Context, actor identity.Principal, r grantRequest) (RoleGrant, error) {
	var grant RoleGrant
	err := s.mutate(ctx, func(tx pgx.Tx) error {
		source := "platform_admin"
		if r.ProjectID == "" {
			if err := s.require(ctx, tx, actor, "role.manage", ""); err != nil {
				return err
			}
		} else {
			if err := s.require(ctx, tx, actor, "project.delegate", r.ProjectID); err != nil {
				return err
			}
			source = "project_manager"
		}
		target := identity.Principal{TenantKey: s.tenantKey, AppID: s.appID, AccountID: r.AccountID}
		err := tx.QueryRow(ctx, `SELECT open_id, name, avatar_url FROM accounts
			WHERE tenant_key=$1 AND app_id=$2 AND id=$3 AND active`, s.tenantKey, s.appID, r.AccountID).
			Scan(&target.OpenID, &target.Name, &target.AvatarURL)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		var generation, supersededID string
		if r.ProjectID != "" {
			access, err := s.projectScope(ctx, target, r.ProjectID)
			if err != nil {
				return err
			}
			if !access.Member {
				return errNotFound
			}
			generation = access.MembershipGeneration
			if !validIdentifier(generation) {
				return ErrForbidden
			}
			err = tx.QueryRow(ctx, `UPDATE role_grants SET revoked_at=now() WHERE tenant_key=$1 AND app_id=$2
				AND account_id=$3 AND role='project_admin' AND project_id=$4 AND revoked_at IS NULL
				AND membership_generation<>$5 RETURNING id`,
				s.tenantKey, s.appID, target.AccountID, r.ProjectID, generation).Scan(&supersededID)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		grant, err = scanGrant(tx.QueryRow(ctx, `INSERT INTO role_grants
			(tenant_key,app_id,account_id,role,project_id,membership_generation,source,granted_by)
			VALUES ($1,$2,$3,$4,nullif($5,''),nullif($6,''),$7,$8) ON CONFLICT DO NOTHING
			RETURNING `+grantColumns, s.tenantKey, s.appID, r.AccountID, r.Role, r.ProjectID, generation, source, actor.AccountID))
		if errors.Is(err, pgx.ErrNoRows) {
			return errDuplicateGrant
		}
		if err != nil {
			return err
		}
		version, err := incrementVersion(ctx, tx, grant.AccountID)
		if err != nil {
			return err
		}
		if supersededID != "" {
			if err := revokeSessions(ctx, tx, grant.AccountID); err != nil {
				return err
			}
			if err := s.audit(ctx, tx, actor.AccountID, "role.revoke", "role_grant", supersededID, version); err != nil {
				return err
			}
		}
		return s.audit(ctx, tx, actor.AccountID, "role.grant", "role_grant", grant.ID, version)
	})
	return grant, err
}

func (s *Service) revokeRole(ctx context.Context, actor identity.Principal, grantID string) error {
	return s.mutate(ctx, func(tx pgx.Tx) error {
		if !s.inScope(actor) {
			return errNotFound
		}
		grant, err := scanGrant(tx.QueryRow(ctx, `SELECT `+grantColumns+` FROM role_grants
			WHERE tenant_key=$1 AND app_id=$2 AND id=$3 AND revoked_at IS NULL`, s.tenantKey, s.appID, grantID))
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		action := "role.manage"
		if grant.ProjectID != "" {
			action = "project.delegate"
		}
		if err := s.require(ctx, tx, actor, action, grant.ProjectID); err != nil {
			if errors.Is(err, ErrForbidden) {
				return errNotFound
			}
			return err
		}
		if grant.Role == RoleSystemAdmin {
			if err := s.protectLastAdmin(ctx, tx, grant.AccountID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, "UPDATE role_grants SET revoked_at=now() WHERE id=$1", grant.ID); err != nil {
			return err
		}
		version, err := incrementVersion(ctx, tx, grant.AccountID)
		if err != nil {
			return err
		}
		if err := revokeSessions(ctx, tx, grant.AccountID); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor.AccountID, "role.revoke", "role_grant", grant.ID, version)
	})
}

type AuditEvent struct {
	ID             int64     `json:"id"`
	ActorAccountID string    `json:"actorAccountId"`
	Action         string    `json:"action"`
	ResourceType   string    `json:"resourceType"`
	ResourceID     string    `json:"resourceId"`
	Result         string    `json:"result"`
	ChangeVersion  int64     `json:"changeVersion"`
	CorrelationID  string    `json:"correlationId"`
	CreatedAt      time.Time `json:"createdAt"`
}

func (s *Service) listAudit(ctx context.Context, before *int64, limit int) ([]AuditEvent, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, actor_account_id, action, resource_type,
		resource_id, result, change_version, correlation_id, created_at FROM audit_events
		WHERE tenant_key=$1 AND app_id=$2 AND ($3::bigint IS NULL OR id<$3) ORDER BY id DESC LIMIT $4`,
		s.tenantKey, s.appID, before, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (AuditEvent, error) {
		var event AuditEvent
		err := row.Scan(&event.ID, &event.ActorAccountID, &event.Action, &event.ResourceType, &event.ResourceID,
			&event.Result, &event.ChangeVersion, &event.CorrelationID, &event.CreatedAt)
		return event, err
	})
}
