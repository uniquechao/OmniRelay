package access

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

type LocalProjects struct{ Pool *pgxpool.Pool }

func (l LocalProjects) ResolveProjectAccess(ctx context.Context, p identity.Principal, id string) (ProjectAccess, error) {
	return localProjectGrant(ctx, l.Pool, p, id)
}

func localProjectGrant(ctx context.Context, db queryer, p identity.Principal, id string) (ProjectAccess, error) {
	var g ProjectAccess
	var role string
	err := db.QueryRow(ctx, `SELECT m.member_id,m.role,m.generation, EXISTS(SELECT FROM role_grants g WHERE
 (g.tenant_key,g.app_id,g.account_id)=(m.tenant_key,m.app_id,m.account_id) AND g.revoked_at IS NULL
 AND (g.role='system_admin' AND g.project_id IS NULL OR g.role='project_admin' AND g.project_id=m.project_id AND g.membership_generation=m.generation)) FROM project_members m JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(m.tenant_key,m.app_id,m.account_id)
 WHERE m.tenant_key=$1 AND m.app_id=$2 AND m.project_id=$3 AND m.account_id=$4 AND a.open_id=$5 AND a.active AND m.role!='revoked'`, p.TenantKey, p.AppID, id, p.AccountID, p.OpenID).Scan(&g.MemberID, &role, &g.MembershipGeneration, &g.CanAssign)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectAccess{}, nil
	}
	if err != nil {
		return ProjectAccess{}, err
	}
	g.Member = true
	g.Manager = role == "manager"
	g.CanAssign = g.CanAssign || g.Manager || role == "editor"
	g.CanComment = g.CanAssign || role != "reader"
	g.CanUpdateOwn = g.CanComment
	return g, nil
}
func (l LocalProjects) HasProjectAccess(ctx context.Context, p identity.Principal) (bool, error) {
	var found bool
	err := l.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT FROM project_members m JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(m.tenant_key,m.app_id,m.account_id) WHERE m.tenant_key=$1 AND m.app_id=$2 AND m.account_id=$3 AND a.open_id=$4 AND a.active AND m.role!='revoked')`, p.TenantKey, p.AppID, p.AccountID, p.OpenID).Scan(&found)
	return found, err
}
