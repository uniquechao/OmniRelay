package directory

import (
	"context"
	"strings"
)

// ResolveDocumentUsers resolves names for user references already obtained from
// a Bot-authorized document. It exposes no organization, phone or email fields.
func (s *Service) ResolveDocumentUsers(ctx context.Context, ids []string) (map[string]string, error) {
	names := map[string]string{}
	if len(ids) == 0 {
		return names, nil
	}
	rows, err := s.db.Query(ctx, `WITH refs AS(SELECT unnest($3::text[]) AS open_id)
 SELECT refs.open_id,coalesce(nullif(p.name,''),nullif(a.name,''),'') FROM refs
 LEFT JOIN directory_sources d ON d.tenant_key=$1 AND d.app_id=$2 AND d.visible
 LEFT JOIN directory_people p ON (p.tenant_key,p.app_id,p.snapshot_version,p.open_id)=($1,$2,d.active_version,refs.open_id)
 LEFT JOIN accounts a ON (a.tenant_key,a.app_id,a.open_id)=($1,$2,refs.open_id)`, s.cfg.TenantKey, s.cfg.AppID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		if name = strings.TrimSpace(name); name != "" {
			names[id] = name
		}
	}
	return names, rows.Err()
}
