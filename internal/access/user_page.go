package access

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

func (s *Service) handleAccount(w http.ResponseWriter, r *http.Request, actor identity.Principal) {
	if err := s.require(r.Context(), s.pool, actor, "account.read", ""); err != nil {
		writeError(w, err)
		return
	}
	account, err := scanAccount(s.pool.QueryRow(r.Context(), "SELECT "+accountColumns+" FROM accounts a WHERE tenant_key=$1 AND app_id=$2 AND id=$3", s.tenantKey, s.appID, r.PathValue("accountID")))
	if errors.Is(err, pgx.ErrNoRows) {
		err = errNotFound
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, account)
}

func (s *Service) handleUserPage(w http.ResponseWriter, r *http.Request, actor identity.Principal) {
	q := r.URL.Query()
	limit, limitErr := strconv.Atoi(q.Get("limit"))
	offset, offsetErr := strconv.Atoi(q.Get("offset"))
	order := map[string]string{"": "(account_id IS NULL), account_id, lower(name)", "name": "name COLLATE user_list_order", "status": "status", "org": "org COLLATE user_list_order"}[q.Get("sort")]
	state, dept, keyword := q.Get("status"), q.Get("department_id"), strings.TrimSpace(q.Get("q"))
	direction := q.Get("direction")
	validDirection := direction == "" || direction == "asc" || direction == "desc"
	validState := state == "" || state == "active" || state == "disabled" || state == "unprovisioned"
	if limitErr != nil || offsetErr != nil || limit < 1 || limit > maxPageSize || offset < 0 ||
		order == "" || !validDirection || !validState || (dept != "" && !validIdentifier(dept)) ||
		len(keyword) > 4096 || strings.ContainsRune(keyword, 0) {
		writeError(w, errInvalidInput)
		return
	}
	if direction == "desc" {
		order += " DESC"
	}
	order += ", id"
	ctx := r.Context()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		writeError(w, err)
		return
	}
	defer tx.Rollback(ctx)
	for _, action := range []string{"account.read", "directory.read", "role.read"} {
		if err := s.require(ctx, tx, actor, action, ""); err != nil {
			writeError(w, err)
			return
		}
	}
	var version *int64
	err = tx.QueryRow(ctx, "SELECT (SELECT active_version FROM directory_sources WHERE tenant_key=$1 AND app_id=$2 AND visible)", s.tenantKey, s.appID).Scan(&version)
	if err != nil {
		writeError(w, err)
		return
	}
	if q.Has("snapshot_version") {
		expected, err := strconv.ParseInt(q.Get("snapshot_version"), 10, 64)
		if err != nil || expected < 0 {
			writeError(w, errInvalidInput)
			return
		}
		if (version == nil && expected != 0) || (version != nil && *version != expected) {
			writeJSON(w, 409, map[string]string{"error": "directory_snapshot_changed"})
			return
		}
	}
	names := map[string]string{}
	for id, role := range roleCatalog {
		names[id] = role.Name
	}
	var items json.RawMessage
	var total int
	err = tx.QueryRow(ctx, `WITH RECURSIVE scoped(id) AS (
		SELECT id FROM departments WHERE tenant_key=$1 AND app_id=$2 AND snapshot_version=$3 AND id=$4
		UNION ALL SELECT d.id FROM departments d JOIN scoped ON scoped.id=d.parent_id
		WHERE d.tenant_key=$1 AND d.app_id=$2 AND d.snapshot_version=$3
	), accounts_here AS (SELECT * FROM accounts WHERE tenant_key=$1 AND app_id=$2),
	people AS (SELECT * FROM directory_people WHERE tenant_key=$1 AND app_id=$2 AND snapshot_version=$3),
	combined AS (
		SELECT coalesce(a.id,'directory:'||p.open_id) AS id,a.id AS account_id,
			coalesce(nullif(p.name,''),nullif(a.name,''),a.id,p.open_id) AS name,
			`+identity.FeishuPersonKeySQL("coalesce(a.open_id,p.open_id)")+` AS "personKey",
			'飞书'::text AS source, coalesce(paths.org,'') AS org,
			CASE WHEN a.id IS NULL THEN 'unprovisioned' WHEN a.active THEN 'active' ELSE 'disabled' END AS status,
			coalesce(p.directory_status,CASE WHEN r.resigned_at IS NOT NULL THEN 'resigned' END) AS "directoryStatus",
			r.resigned_at AS "resignedAt",coalesce(a.active AND r.resigned_at IS NOT NULL,false) AS "disableProtected",
			CASE WHEN a.active THEN array_prepend('system_user'::text,`+systemGrantRoles+`) ELSE `+systemGrantRoles+` END AS "roleIds"
		FROM accounts_here a FULL JOIN people p USING(open_id)
		LEFT JOIN directory_roster r ON (r.tenant_key,r.app_id,r.open_id)=(a.tenant_key,a.app_id,a.open_id)
		LEFT JOIN LATERAL (
			SELECT string_agg(array_to_string(d.path,' / '),'；' ORDER BY d.path,d.id) AS org,
				bool_or(m.department_id IN (SELECT id FROM scoped)) AS in_scope
			FROM department_memberships m JOIN departments d ON
				(d.tenant_key,d.app_id,d.snapshot_version,d.id)=(m.tenant_key,m.app_id,m.snapshot_version,m.department_id)
			WHERE (m.tenant_key,m.app_id,m.snapshot_version,m.open_id)=($1,$2,$3,p.open_id)
		) paths ON true WHERE $4='' OR paths.in_scope
	), matched AS NOT MATERIALIZED (
		SELECT * FROM combined WHERE ($5='' OR status=$5) AND ($6='' OR
			strpos(lower(concat_ws(' ',name,id,org,source,(SELECT string_agg($7::jsonb->>role,' ') FROM unnest("roleIds") role))),lower($6))>0)
	), page AS (SELECT *, ARRAY(SELECT $7::jsonb->>role FROM unnest("roleIds") role) AS "roleNames" FROM matched ORDER BY `+order+` LIMIT $8 OFFSET $9)
	SELECT coalesce(jsonb_agg(to_jsonb(page)-'account_id'-'roleIds' ORDER BY `+order+`),'[]'),(SELECT count(*) FROM matched) FROM page`,
		s.tenantKey, s.appID, version, dept, state, keyword, names, limit, offset).Scan(&items, &total)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": total, "snapshot_version": version, "visible": version != nil})
}
