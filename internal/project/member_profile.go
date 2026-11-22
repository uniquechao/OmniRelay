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
	if strings.TrimSpace(q) == "" || !textLimit(q, 200) || page < 1 || page > 100000 {
		return nil, errInput
	}
	if err := s.require(ctx, actor, "project.settings", k.project); err != nil {
		return nil, err
	}
	rows, err := s.reader().Query(ctx, `SELECT jsonb_build_object('account_id',a.id,'display_name',coalesce(nullif(a.name,''),a.id),'version',coalesce(m.version,0))
 FROM accounts a LEFT JOIN project_members m ON (m.tenant_key,m.app_id,m.account_id)=(a.tenant_key,a.app_id,a.id) AND m.project_id=$3
 WHERE a.tenant_key=$1 AND a.app_id=$2 AND a.active AND (m.role IS NULL OR m.role='revoked')
 AND strpos(lower(a.name||' '||a.id),lower($4))>0 ORDER BY a.name,a.id LIMIT 25 OFFSET $5`, k.args(strings.TrimSpace(q), (page-1)*25)...)
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
