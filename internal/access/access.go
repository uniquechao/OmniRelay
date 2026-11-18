// Package access enforces account state and scoped roles for OmniRelay.
package access

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

const (
	RoleSystemAdmin    = "system_admin"
	RoleSystemOperator = "system_operator"
	RoleAuditViewer    = "audit_viewer"
	RoleProjectAdmin   = "project_admin"
	RoleProjectMember  = "project_member"
)

var (
	ErrBootstrapRequired = errors.New("bootstrap administrator is required")
	ErrForbidden         = errors.New("permission denied")
	errNotFound          = errors.New("resource not found")
	errLastAdmin         = errors.New("at least one active system administrator is required")
	errDuplicateGrant    = errors.New("role grant already exists")
	errDuplicateAccount  = errors.New("account already exists")
	errInvalidInput      = errors.New("invalid request")
)

// ProjectAccess comes from the current authoritative project membership and capabilities.
type ProjectAccess struct {
	Member    bool
	Manager   bool
	CanAssign bool

	// MembershipGeneration identifies this membership lifetime and changes on rejoin.
	// Delegated administration requires a nonempty generation from the authority.
	MembershipGeneration string
}

// ProjectAccessResolver checks current formal project access for a verified account.
type ProjectAccessResolver interface {
	ResolveProjectAccess(context.Context, identity.Principal, string) (ProjectAccess, error)
}

type Service struct {
	pool      *pgxpool.Pool
	tenantKey string
	appID     string

	// ProjectAccess is set at startup. A nil provider denies project access.
	ProjectAccess ProjectAccessResolver
}

func New(pool *pgxpool.Pool, tenantKey, appID string) *Service {
	return &Service{pool: pool, tenantKey: tenantKey, appID: appID}
}

type queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (s *Service) inScope(p identity.Principal) bool {
	return p.AccountID != "" && p.OpenID != "" && p.TenantKey == s.tenantKey && p.AppID == s.appID
}

// Bootstrap establishes the first system administrator once for this tenant and app.
func (s *Service) Bootstrap(ctx context.Context, openID string) error {
	return s.mutate(ctx, func(tx pgx.Tx) error {
		var initialized bool
		err := tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM access_bootstraps WHERE tenant_key=$1 AND app_id=$2
		)`, s.tenantKey, s.appID).Scan(&initialized)
		if err != nil || initialized {
			return err
		}
		if openID == "" {
			return ErrBootstrapRequired
		}
		if !validIdentifier(openID) {
			return errInvalidInput
		}
		var accountID string
		var active bool
		err = tx.QueryRow(ctx, `INSERT INTO accounts (tenant_key,app_id,open_id)
			VALUES ($1,$2,$3)
			ON CONFLICT (tenant_key,app_id,open_id) DO UPDATE SET open_id=EXCLUDED.open_id
			RETURNING id, active`, s.tenantKey, s.appID, openID).Scan(&accountID, &active)
		if err != nil {
			return err
		}
		if !active {
			return ErrForbidden
		}
		var grantID string
		err = tx.QueryRow(ctx, `INSERT INTO role_grants (tenant_key,app_id,account_id,role,source)
			VALUES ($1,$2,$3,'system_admin','bootstrap') RETURNING id`,
			s.tenantKey, s.appID, accountID).Scan(&grantID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO access_bootstraps (tenant_key,app_id,account_id)
			VALUES ($1,$2,$3)`, s.tenantKey, s.appID, accountID); err != nil {
			return err
		}
		version, err := incrementVersion(ctx, tx, accountID)
		if err != nil {
			return err
		}
		return s.audit(ctx, tx, accountID, "role.bootstrap", "role_grant", grantID, version)
	})
}

func (s *Service) mutate(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if err := s.lockOrganization(ctx, tx); err != nil {
			return err
		}
		return fn(tx)
	})
}

func (s *Service) lockOrganization(ctx context.Context, tx pgx.Tx) error {
	// Serialize writes before reading current authorization or last-admin counts.
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(
		hashtextextended(jsonb_build_array($1::text,$2::text)::text, 418619942))`, s.tenantKey, s.appID)
	return err
}

// AuthorizeDirectorySync checks the current actor while holding the organization's access
// lock for the caller's READ COMMITTED transaction. Call it before locking directory rows.
func (s *Service) AuthorizeDirectorySync(ctx context.Context, tx pgx.Tx, p identity.Principal) error {
	if !s.inScope(p) {
		return ErrForbidden
	}
	if err := s.lockOrganization(ctx, tx); err != nil {
		return err
	}
	return s.require(ctx, tx, p, "directory.sync", "")
}

// Check authorizes a currently active verified account against a platform or project action.
func (s *Service) Check(ctx context.Context, p identity.Principal, action, projectID string) (bool, error) {
	return s.check(ctx, s.pool, p, action, projectID)
}

func (s *Service) check(ctx context.Context, db queryer, p identity.Principal, action, projectID string) (bool, error) {
	if !s.inScope(p) {
		return false, nil
	}
	var active bool
	var roles []string
	var delegatedGeneration string
	err := db.QueryRow(ctx, `SELECT a.active, ARRAY(
		SELECT g.role FROM role_grants g
		WHERE g.tenant_key=a.tenant_key AND g.app_id=a.app_id AND g.account_id=a.id
		AND g.project_id IS NULL AND g.revoked_at IS NULL
	), coalesce((
		SELECT g.membership_generation FROM role_grants g
		WHERE g.tenant_key=a.tenant_key AND g.app_id=a.app_id AND g.account_id=a.id
		AND g.project_id=nullif($5,'') AND g.role='project_admin' AND g.revoked_at IS NULL
	), '') FROM accounts a WHERE a.tenant_key=$1 AND a.app_id=$2 AND a.id=$3 AND a.open_id=$4`,
		s.tenantKey, s.appID, p.AccountID, p.OpenID, projectID).Scan(&active, &roles, &delegatedGeneration)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil || !active {
		return false, err
	}
	if projectID == "" {
		return platformAllowed(roles, action), nil
	}
	switch action {
	case "project.read", "project.assign", "project.settings", "project.delegate":
	default:
		return false, nil
	}
	access, err := s.projectScope(ctx, p, projectID)
	if err != nil || !access.Member {
		return false, err
	}
	switch action {
	case "project.read":
		return true, nil
	case "project.assign":
		return access.CanAssign, nil
	case "project.settings":
		return access.Manager || (delegatedGeneration != "" && access.MembershipGeneration == delegatedGeneration), nil
	default:
		return access.Manager, nil
	}
}

// WorkbenchAccess provides navigation visibility. Every administrative request still
// executes its own authorization checks against the current account and grants.
func (s *Service) WorkbenchAccess(ctx context.Context, p identity.Principal) (identity.WorkbenchAccess, error) {
	view := identity.WorkbenchAccess{Roles: []string{}, Admin: map[string]bool{}}
	if !s.inScope(p) {
		return view, nil
	}
	err := s.pool.QueryRow(ctx, `SELECT ARRAY(
		SELECT g.role FROM role_grants g
		WHERE g.account_id=a.id AND g.tenant_key=a.tenant_key AND g.app_id=a.app_id
		AND g.project_id IS NULL AND g.revoked_at IS NULL ORDER BY g.role
	) FROM accounts a WHERE a.id=$1 AND a.tenant_key=$2 AND a.app_id=$3 AND a.open_id=$4 AND a.active`,
		p.AccountID, p.TenantKey, p.AppID, p.OpenID).Scan(&view.Roles)
	if errors.Is(err, pgx.ErrNoRows) {
		return view, nil
	}
	if err != nil {
		return view, err
	}
	view.Admin["users"] = platformAllowed(view.Roles, "account.read")
	view.Admin["roles"] = platformAllowed(view.Roles, "role.read")
	view.Admin["audit"] = platformAllowed(view.Roles, "audit.read")
	view.Admin["feishu_bot"] = platformAllowed(view.Roles, "system.read")
	return view, nil
}

func platformAllowed(roles []string, action string) bool {
	admin := slices.Contains(roles, RoleSystemAdmin)
	switch action {
	case "directory.read", "directory.sync", "account.read", "account.manage", "role.read", "role.manage", "system.configure":
		return admin
	case "system.read":
		return admin || slices.Contains(roles, RoleSystemOperator)
	case "system.operate":
		return slices.Contains(roles, RoleSystemOperator)
	case "audit.read":
		return admin || slices.Contains(roles, RoleAuditViewer)
	default:
		return false
	}
}

func (s *Service) projectScope(ctx context.Context, p identity.Principal, projectID string) (ProjectAccess, error) {
	if s.ProjectAccess == nil || !validIdentifier(projectID) {
		return ProjectAccess{}, nil
	}
	return s.ProjectAccess.ResolveProjectAccess(ctx, p, projectID)
}

func (s *Service) require(ctx context.Context, db queryer, p identity.Principal, action, projectID string) error {
	allowed, err := s.check(ctx, db, p, action, projectID)
	if err == nil && !allowed {
		return ErrForbidden
	}
	return err
}

func validIdentifier(value string) bool {
	return value != "" && len(value) <= 256 && strings.TrimSpace(value) == value && !strings.ContainsRune(value, '\x00')
}

func incrementVersion(ctx context.Context, tx pgx.Tx, accountID string) (int64, error) {
	var version int64
	err := tx.QueryRow(ctx, `UPDATE accounts SET permission_version=permission_version+1,
		updated_at=now() WHERE id=$1 RETURNING permission_version`, accountID).Scan(&version)
	return version, err
}

func (s *Service) audit(ctx context.Context, tx pgx.Tx, actorID, action, resourceType, resourceID string, version int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit_events
		(tenant_key,app_id,actor_account_id,action,resource_type,resource_id,result,change_version)
		VALUES ($1,$2,$3,$4,$5,$6,'success',$7)`, s.tenantKey, s.appID, actorID, action, resourceType, resourceID, version)
	return err
}

// AuditIdentity records a successful login or logout in identity's transaction.
func (s *Service) AuditIdentity(ctx context.Context, tx pgx.Tx, p identity.Principal, action string) error {
	if !s.inScope(p) || (action != "identity.login" && action != "identity.logout" && action != "identity.password_change") {
		return errInvalidInput
	}
	var version int64
	err := tx.QueryRow(ctx, `SELECT permission_version FROM accounts
		WHERE tenant_key=$1 AND app_id=$2 AND id=$3 AND open_id=$4`,
		s.tenantKey, s.appID, p.AccountID, p.OpenID).Scan(&version)
	if err != nil {
		return err
	}
	return s.audit(ctx, tx, p.AccountID, action, "account", p.AccountID, version)
}

// AuthorizeBotWrite rechecks current grants in the transaction that changes Bot state.
func (s *Service) AuthorizeBotWrite(ctx context.Context, tx pgx.Tx, p identity.Principal, action string) error {
	if action != "system.read" && action != "system.configure" {
		return ErrForbidden
	}
	if err := s.lockOrganization(ctx, tx); err != nil {
		return err
	}
	return s.require(ctx, tx, p, action, "")
}

// AuditBotConfig records an actual version change in the configuration transaction.
func (s *Service) AuditBotConfig(ctx context.Context, tx pgx.Tx, p identity.Principal, version int64) error {
	return s.audit(ctx, tx, p.AccountID, "feishu_bot.configure", "feishu_bot", s.appID, version)
}
