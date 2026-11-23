package project

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// Only current group snapshots from this project's bot and bindings contribute people.
const groupPeople = `WITH roster AS (
  SELECT person.open_id, max(nullif(g.member_names->>person.open_id,'')) AS name
  FROM project_chat_assignments c JOIN feishu_drive_group_memberships g USING (tenant_key,app_id,chat_id)
  JOIN feishu_bot_configs b USING (tenant_key,app_id)
  CROSS JOIN LATERAL unnest(g.members) person(open_id)
  WHERE c.tenant_key=$1 AND c.app_id=$2 AND c.project_id=$3 AND b.enabled
    AND g.generation=b.credentials_generation AND g.verified_at>now()-interval '26 hours'
  GROUP BY person.open_id
), people AS (
  SELECT 'feishu:'||r.open_id AS id,r.open_id,a.id AS account,
    coalesce(nullif(dp.name,''),r.name,nullif(a.name,''),r.open_id) AS display_name,
    coalesce(nullif(dp.avatar_url,''),a.avatar_url,'') AS avatar,
    coalesce(pw.username,'') AS username, m.role,coalesce(m.version,0) AS version
  FROM roster r LEFT JOIN accounts a ON (a.tenant_key,a.app_id,a.open_id)=($1,$2,r.open_id)
  LEFT JOIN account_passwords pw ON (pw.tenant_key,pw.app_id,pw.account_id)=($1,$2,a.id)
  LEFT JOIN directory_sources ds ON (ds.tenant_key,ds.app_id)=($1,$2) AND ds.visible
  LEFT JOIN directory_people dp ON (dp.tenant_key,dp.app_id,dp.snapshot_version,dp.open_id)=($1,$2,ds.active_version,r.open_id)
  LEFT JOIN directory_roster dr ON (dr.tenant_key,dr.app_id,dr.open_id)=($1,$2,r.open_id)
  LEFT JOIN project_members m ON (m.tenant_key,m.app_id,m.project_id,m.account_id)=($1,$2,$3,a.id)
  WHERE coalesce(a.active,true) AND dr.resigned_at IS NULL AND coalesce(dp.directory_status,'active')<>'resigned'
)`

func (s *Service) groupTaskPeople(ctx context.Context, k taskScope) ([]json.RawMessage, error) {
	rows, err := s.reader().Query(ctx, groupPeople+` SELECT jsonb_build_object('id',id,'display_name',display_name,'avatar_url',avatar,
      'username',username,'active',true,'roster_status','group') FROM people WHERE role IS NULL ORDER BY display_name,id`, k.args()...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[json.RawMessage])
}
