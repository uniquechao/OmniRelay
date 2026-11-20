package access

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

type Account struct {
	ID               string     `json:"id"`
	PersonKey        string     `json:"personKey"`
	Name             string     `json:"name"`
	AvatarURL        string     `json:"avatarUrl"`
	Active           bool       `json:"active"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
	SystemRoles      []string   `json:"systemRoles"`
	DirectoryStatus  string     `json:"directoryStatus,omitempty"`
	ResignedAt       *time.Time `json:"resignedAt,omitempty"`
	DisableProtected bool       `json:"disableProtected,omitempty"`
}

var accountColumns = "a.id, a.name, a.avatar_url, a.active, a.created_at, a.updated_at, " + identity.FeishuPersonKeySQL("a.open_id") + ", " + systemGrantRoles + ", " + resignationColumn

// Read only the durable departure marker here. The directory API supplies
// current directory fields under its separate read permission.
const resignationColumn = `(SELECT r.resigned_at FROM directory_roster r
	WHERE (r.tenant_key,r.app_id,r.open_id)=(a.tenant_key,a.app_id,a.open_id))`

func scanAccount(row pgx.Row, extra ...any) (Account, error) {
	var a Account
	fields := []any{&a.ID, &a.Name, &a.AvatarURL, &a.Active, &a.CreatedAt, &a.UpdatedAt, &a.PersonKey, &a.SystemRoles, &a.ResignedAt}
	err := row.Scan(append(fields, extra...)...)
	if a.ResignedAt != nil {
		a.DirectoryStatus = "resigned"
		a.DisableProtected = a.Active
	}
	a.SystemRoles = effectiveSystemRoles(a.Active, a.SystemRoles)
	return a, err
}

func (s *Service) listAccounts(ctx context.Context, after string, limit int, active *bool) ([]Account, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+accountColumns+` FROM accounts a
		WHERE tenant_key=$1 AND app_id=$2 AND id>$3 AND ($5::bool IS NULL OR active=$5)
		ORDER BY id LIMIT $4`, s.tenantKey, s.appID, after, limit, active)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Account, error) {
		return scanAccount(row)
	})
}

func (s *Service) preauthorizeAccount(ctx context.Context, actor identity.Principal, openID string) (Account, error) {
	var account Account
	err := s.WithOrganizationMutation(ctx, func(tx pgx.Tx) error {
		if err := s.require(ctx, tx, actor, "account.manage", ""); err != nil {
			return err
		}
		var name string
		err := tx.QueryRow(ctx, `SELECT person.name FROM directory_sources AS source
			JOIN directory_people AS person ON (person.tenant_key, person.app_id, person.snapshot_version)
				= (source.tenant_key, source.app_id, source.active_version)
			WHERE source.tenant_key=$1 AND source.app_id=$2 AND source.visible
				AND person.open_id=$3 AND person.directory_status='active'
			FOR SHARE OF source`, s.tenantKey, s.appID, openID).Scan(&name)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		account, err = scanAccount(tx.QueryRow(ctx, `INSERT INTO accounts AS a (tenant_key,app_id,open_id,name)
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
	err := s.WithOrganizationMutation(ctx, func(tx pgx.Tx) error {
		if err := s.require(ctx, tx, actor, "account.manage", ""); err != nil {
			return err
		}
		var err error
		account, err = scanAccount(tx.QueryRow(ctx, `SELECT `+accountColumns+` FROM accounts a
			WHERE tenant_key=$1 AND app_id=$2 AND id=$3 FOR NO KEY UPDATE`, s.tenantKey, s.appID, accountID))
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil || account.Active == active {
			return err
		}
		if active && account.ResignedAt != nil {
			return errAccountResigned
		}
		var version int64
		account, version, err = s.changeAccountActive(ctx, tx, accountID, active)
		if err != nil {
			return err
		}
		action := "account.disable"
		if active {
			action = "account.enable"
		}
		return s.audit(ctx, tx, actor.AccountID, action, "account", accountID, version)
	})
	return account, err
}

// changeAccountActive requires the organization lock and a locked account row.
func (s *Service) changeAccountActive(ctx context.Context, tx pgx.Tx, accountID string, active bool) (Account, int64, error) {
	if !active {
		if err := s.protectLastAdmin(ctx, tx, accountID); err != nil {
			return Account{}, 0, err
		}
	}
	var version int64
	account, err := scanAccount(tx.QueryRow(ctx, `UPDATE accounts AS a
		SET active=$2, updated_at=now(), permission_version=permission_version+1
		WHERE id=$1 RETURNING `+accountColumns+`, permission_version`, accountID, active), &version)
	if err == nil && !active {
		err = revokeSessions(ctx, tx, accountID)
	}
	return account, version, err
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
	role := roleCatalog[r.Role]
	if !validIdentifier(r.AccountID) || !role.Grantable {
		return false
	}
	switch role.Scope {
	case "system":
		return r.ProjectID == ""
	case "project":
		return validIdentifier(r.ProjectID)
	default:
		return false
	}
}

func (s *Service) grantRole(ctx context.Context, actor identity.Principal, r grantRequest) (RoleGrant, error) {
	if !r.valid() {
		return RoleGrant{}, errInvalidInput
	}
	var grant RoleGrant
	err := s.WithOrganizationMutation(ctx, func(tx pgx.Tx) error {
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
			access, err := s.ProjectGrant(ctx, tx, target, r.ProjectID)
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
	return s.WithOrganizationMutation(ctx, func(tx pgx.Tx) error {
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
			if !errors.Is(err, ErrForbidden) {
				return err
			}
			if grant.ProjectID != "" {
				return errNotFound
			}
			canRead, err := s.check(ctx, tx, actor, "role.read", "")
			if err != nil {
				return err
			}
			if !canRead {
				return errNotFound
			}
			return ErrForbidden
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
	ID                 int64           `json:"id"`
	ActorAccountID     string          `json:"actorAccountId"`
	Action             string          `json:"action"`
	ResourceType       string          `json:"resourceType"`
	ResourceID         string          `json:"resourceId"`
	Result             string          `json:"result"`
	ChangeVersion      int64           `json:"changeVersion"`
	CorrelationID      string          `json:"correlationId"`
	CreatedAt          time.Time       `json:"createdAt"`
	Details            json.RawMessage `json:"details,omitempty"`
	DirectorySyncRunID *int64          `json:"directorySyncRunId,omitempty"`
}

func (s *Service) listAudit(ctx context.Context, before *int64, limit int) ([]AuditEvent, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, coalesce(actor_account_id,''), action, resource_type,
		resource_id, result, change_version, correlation_id, created_at, directory_sync_run_id, details FROM audit_events
		WHERE tenant_key=$1 AND app_id=$2 AND ($3::bigint IS NULL OR id<$3) ORDER BY id DESC LIMIT $4`,
		s.tenantKey, s.appID, before, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (AuditEvent, error) {
		var event AuditEvent
		err := row.Scan(&event.ID, &event.ActorAccountID, &event.Action, &event.ResourceType, &event.ResourceID,
			&event.Result, &event.ChangeVersion, &event.CorrelationID, &event.CreatedAt, &event.DirectorySyncRunID, &event.Details)
		return event, err
	})
}
