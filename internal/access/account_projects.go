package access

import (
	"encoding/json"
	"net/http"

	"github.com/uniquechao/OmniRelay/internal/identity"
)

func (s *Service) handleAccountProjects(w http.ResponseWriter, r *http.Request, actor identity.Principal) {
	ctx := r.Context()
	for _, action := range []string{"account.read", "role.read"} {
		if err := s.require(ctx, s.pool, actor, action, ""); err != nil {
			writeError(w, err)
			return
		}
	}
	if _, ok := s.ProjectAccess.(LocalProjects); !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "project_source_unavailable"})
		return
	}
	var exists, canDelegate bool
	var items json.RawMessage
	err := s.pool.QueryRow(ctx, `WITH managed AS (
        SELECT m.project_id FROM project_members m JOIN accounts a
        ON (a.tenant_key,a.app_id,a.id)=(m.tenant_key,m.app_id,m.account_id)
        WHERE m.tenant_key=$1 AND m.app_id=$2 AND m.account_id=$3 AND a.open_id IS NOT DISTINCT FROM nullif($4,'') AND a.active AND m.role='manager'
    ), target AS (
        SELECT id,active FROM accounts WHERE tenant_key=$1 AND app_id=$2 AND id=$5
    ), candidates AS (
        SELECT m.project_id AS id,m.project_id AS name,m.generation,g.id AS grant
        FROM managed JOIN project_members m USING(project_id) JOIN target t ON t.id=m.account_id AND t.active
        LEFT JOIN role_grants g ON (g.tenant_key,g.app_id,g.account_id,g.project_id,g.membership_generation)=
            (m.tenant_key,m.app_id,m.account_id,m.project_id,m.generation) AND g.revoked_at IS NULL AND g.role='project_admin'
        WHERE m.tenant_key=$1 AND m.app_id=$2 AND m.role!='revoked'
    ) SELECT EXISTS(SELECT FROM target),EXISTS(SELECT FROM managed),
        coalesce((SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM candidates c),'[]')`,
		s.tenantKey, s.appID, actor.AccountID, actor.OpenID, r.PathValue("accountID")).Scan(&exists, &canDelegate, &items)
	if err != nil {
		writeError(w, err)
		return
	}
	if !exists {
		writeError(w, errNotFound)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "canDelegate": canDelegate})
}

func (s *Service) handleRoleCounts(w http.ResponseWriter, r *http.Request, actor identity.Principal) {
	ctx := r.Context()
	for _, action := range []string{"role.read", "account.read"} {
		if err := s.require(ctx, s.pool, actor, action, ""); err != nil {
			writeError(w, err)
			return
		}
	}
	_, local := s.ProjectAccess.(LocalProjects)
	var counts json.RawMessage
	err := s.pool.QueryRow(ctx, `WITH managed AS (
        SELECT project_id FROM project_members WHERE tenant_key=$1 AND app_id=$2 AND account_id=$3 AND role='manager' AND $4
    ), counts AS (
        SELECT role,count(*) AS n FROM role_grants WHERE tenant_key=$1 AND app_id=$2 AND project_id IS NULL AND revoked_at IS NULL GROUP BY role
        UNION ALL SELECT 'system_user',count(*) FROM accounts WHERE tenant_key=$1 AND app_id=$2 AND active
        UNION ALL SELECT 'project_admin',count(*) FROM managed JOIN project_members m USING(project_id)
        JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(m.tenant_key,m.app_id,m.account_id)
        JOIN role_grants g ON (g.tenant_key,g.app_id,g.account_id,g.project_id,g.membership_generation)=
            (m.tenant_key,m.app_id,m.account_id,m.project_id,m.generation)
        WHERE m.tenant_key=$1 AND m.app_id=$2 AND a.active AND m.role!='revoked' AND g.role='project_admin' AND g.revoked_at IS NULL
        HAVING EXISTS(SELECT FROM managed)
    ) SELECT jsonb_object_agg(role,n) FROM counts`, s.tenantKey, s.appID, actor.AccountID, local).Scan(&counts)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, counts)
}
