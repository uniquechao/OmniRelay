package project

import (
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

type projectShortcut struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s *Service) followedProjects(_ http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
	rows, err := s.pool.Query(r.Context(), `SELECT m.project_id, coalesce(p.name,m.project_id)
		FROM project_members m
		JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(m.tenant_key,m.app_id,m.account_id)
		LEFT JOIN project_profiles p ON (p.tenant_key,p.app_id,p.project_id)=(m.tenant_key,m.app_id,m.project_id)
		WHERE (m.tenant_key,m.app_id,m.account_id)=($1,$2,$3)
		AND a.open_id IS NOT DISTINCT FROM nullif($4,'') AND a.active AND m.role<>'revoked' AND m.followed
		ORDER BY lower(coalesce(p.name,m.project_id)), m.project_id`,
		actor.TenantKey, actor.AppID, actor.AccountID, actor.OpenID)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[projectShortcut])
	return map[string]any{"items": items}, err
}

func (s *Service) setFollow(_ http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
	followed := r.Method == http.MethodPut
	k := scope(actor, r.PathValue("projectID"))
	tag, err := s.pool.Exec(r.Context(), `UPDATE project_members m SET followed=$6
		FROM accounts a WHERE (a.tenant_key,a.app_id,a.id)=(m.tenant_key,m.app_id,m.account_id)
		AND (m.tenant_key,m.app_id,m.project_id,m.account_id)=($1,$2,$3,$4)
		AND a.open_id IS NOT DISTINCT FROM nullif($5,'') AND a.active AND m.role<>'revoked'`, k.args(actor.AccountID, actor.OpenID, followed)...)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() != 1 {
		return nil, errDenied
	}
	return map[string]bool{"followed": followed}, nil
}
