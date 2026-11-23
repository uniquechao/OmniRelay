package project

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/uniquechao/OmniRelay/internal/identity"
)

// The manager sees only enabled candidate identities, never account credentials or system roles.
func (s *Service) findMemberCandidates(ctx context.Context, k taskScope, actor identity.Principal, q string, page int) (any, error) {
	if !textLimit(q, 200) || page < 1 || page > 100000 {
		return nil, errInput
	}
	if err := s.require(ctx, actor, "project.settings", k.project); err != nil {
		return nil, err
	}
	rows, err := s.reader().Query(ctx, groupPeople+`, candidates AS (SELECT jsonb_build_object(`+userChoiceJSON+`,'version',coalesce(m.version,0)) AS item, `+userChoiceSearch+` AS searchable, 1 AS rank
 FROM accounts a `+userChoiceJoins+` LEFT JOIN project_members m ON (m.tenant_key,m.app_id,m.account_id)=(a.tenant_key,a.app_id,a.id) AND m.project_id=$3
 WHERE a.tenant_key=$1 AND a.app_id=$2 AND a.active AND (m.role IS NULL OR m.role='revoked')
 AND ($4<>'' OR NOT EXISTS (SELECT FROM project_chat_assignments c WHERE c.tenant_key=$1 AND c.app_id=$2 AND c.project_id=$3))
 AND NOT EXISTS (SELECT FROM people p WHERE p.account=a.id)
 UNION ALL SELECT jsonb_build_object('account_id',CASE WHEN role='revoked' THEN account ELSE id END,'display_name',display_name,'avatar_url',avatar,
 'username',username,'version',version),lower(concat_ws(' ',display_name,username,open_id,account)),0 FROM people WHERE role IS NULL OR role='revoked')
 SELECT item FROM candidates WHERE strpos(searchable,lower($4))>0 ORDER BY rank,item->>'display_name',item->>'account_id' LIMIT 25 OFFSET $5`, k.args(strings.TrimSpace(q), (page-1)*25)...)
	if err != nil {
		return nil, err
	}
	return collectToolItems(rows)
}
func (s *Service) memberCandidates(_ http.ResponseWriter, r *http.Request, a identity.Principal) (any, error) {
	page := 1
	if r.URL.Query().Has("page") {
		var err error
		page, err = strconv.Atoi(r.URL.Query().Get("page"))
		if err != nil {
			return nil, errInput
		}
	}
	return s.findMemberCandidates(r.Context(), scope(a, r.PathValue("projectID")), a, r.URL.Query().Get("q"), page)
}
func (s *Service) saveMemberProfile(w http.ResponseWriter, r *http.Request, a identity.Principal) (any, error) {
	var in memberChange
	if !decodeLimit(w, r, &in, 32<<10) || in.Expected == nil {
		return nil, errInput
	}
	return s.changeMember(r.Context(), scope(a, r.PathValue("projectID")), a, r.PathValue("accountID"), in)
}
