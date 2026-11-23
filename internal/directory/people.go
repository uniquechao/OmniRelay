package directory

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/uniquechao/OmniRelay/internal/identity"
)

var ErrResourcePeopleForbidden = errors.New("resource people read forbidden")

// ResourcePersonRef is a reference from an authorized resource payload. OpenID
// must belong to this app. AccountID is used when the source has a trusted local
// mapping instead. Name is the display name already visible in that payload.
type ResourcePersonRef struct {
	GroupMember bool
	OpenID      string
	AccountID   string
	Name        string
}

// ResourcePeopleReader checks current resource and field access, then returns
// the visible person references for that conversation or task, in display order.
type ResourcePeopleReader func(context.Context, identity.Principal, string) ([]ResourcePersonRef, bool, error)

type PersonProjection struct {
	Resigned          bool       `json:"resigned,omitempty"`
	PersonKey         string     `json:"person_key,omitempty"`
	Name              string     `json:"name"`
	Source            string     `json:"source,omitempty"`
	OrganizationPaths [][]string `json:"organization_paths,omitempty"`
}

// OrganizationPathsSQL projects department names for the visible directory_people row dp.
// Callers must check directory.read before including this projection.
const OrganizationPathsSQL = `COALESCE((SELECT jsonb_agg(d.path ORDER BY d.path,d.id)
	FROM department_memberships m JOIN departments d
	ON (d.tenant_key,d.app_id,d.snapshot_version,d.id)=(m.tenant_key,m.app_id,m.snapshot_version,m.department_id)
	WHERE (m.tenant_key,m.app_id,m.snapshot_version,m.open_id)=(dp.tenant_key,dp.app_id,dp.snapshot_version,dp.open_id)
),'[]'::jsonb)`

// ReadResourcePeople is called by conversation and task readers using their own
// authorizer. Use PersonKey as the avatar seed, falling back to Name for color
// only. The result preserves source order, including separate name-only people.
func (s *Service) ReadResourcePeople(ctx context.Context, actor identity.Principal, resourceID string,
	read ResourcePeopleReader, checkDirectory func(context.Context, identity.Principal, string, string) (bool, error),
) ([]PersonProjection, error) {
	if !s.sameSource(actor) || actor.AccountID == "" || actor.OpenID == "" || resourceID == "" || read == nil {
		return nil, ErrResourcePeopleForbidden
	}
	refs, allowed, err := read(ctx, actor, resourceID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrResourcePeopleForbidden
	}

	result := make([]PersonProjection, len(refs))
	openIDs, accountIDs := make([]string, len(refs)), make([]string, len(refs))
	resolve := false
	for i, ref := range refs {
		if validID(ref.OpenID) {
			openIDs[i] = ref.OpenID
		}
		accountIDs[i] = ref.AccountID
		resolve = resolve || openIDs[i] != "" || accountIDs[i] != ""
		result[i].Name = strings.TrimSpace(ref.Name)
		if result[i].Name == "" {
			result[i].Name = "未知人员"
		}
	}
	if !resolve {
		return result, nil
	}

	directoryAllowed := false
	if checkDirectory != nil {
		directoryAllowed, err = checkDirectory(ctx, actor, "directory.read", "")
		if err != nil {
			return nil, err
		}
	}

	// The active version, names and paths are read in one database statement.
	rows, err := s.db.Query(ctx, `WITH visible_snapshot AS (
		SELECT active_version FROM directory_sources
		WHERE tenant_key=$1 AND app_id=$2 AND visible
	), refs AS (
		SELECT ref.position, COALESCE(NULLIF(ref.open_id,''), a.open_id) AS open_id
		FROM unnest($4::text[], $5::text[]) WITH ORDINALITY ref(open_id, account_id, position)
		LEFT JOIN accounts a ON a.tenant_key=$1 AND a.app_id=$2 AND a.id=ref.account_id AND ref.open_id=''
	)
	SELECT refs.position, COALESCE(`+identity.FeishuPersonKeySQL("refs.open_id")+`,''),
		CASE WHEN $3 THEN COALESCE(dp.name,'') ELSE '' END, CASE WHEN $3 THEN `+OrganizationPathsSQL+` ELSE '[]' END,
		EXISTS (SELECT 1 FROM visible_snapshot) AND COALESCE(dp.directory_status='resigned', k.resigned, r.resigned_at IS NOT NULL)
	FROM refs
	LEFT JOIN directory_people dp ON (dp.tenant_key,dp.app_id,dp.snapshot_version,dp.open_id)
		= ($1,$2,(SELECT active_version FROM visible_snapshot),refs.open_id)
	LEFT JOIN feishu_chat_sender_checks k ON (k.tenant_key,k.app_id,k.open_id)=($1,$2,refs.open_id)
	LEFT JOIN directory_roster r ON (r.tenant_key,r.app_id,r.open_id)=($1,$2,refs.open_id)`,
		s.cfg.TenantKey, s.cfg.AppID, directoryAllowed, openIDs, accountIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var position int
		var key, name string
		var paths json.RawMessage
		var resigned bool
		if err := rows.Scan(&position, &key, &name, &paths, &resigned); err != nil {
			return nil, err
		}
		person := &result[position-1]
		person.Resigned = refs[position-1].GroupMember && resigned
		if key != "" {
			person.PersonKey, person.Source = key, "feishu"
		}
		if name != "" {
			person.Name = name
		}
		if err := json.Unmarshal(paths, &person.OrganizationPaths); err != nil {
			return nil, err
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
