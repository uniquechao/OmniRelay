package project

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
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
	rows, err := s.reader().Query(ctx, systemPeople+` SELECT jsonb_build_object(
		'account_id',coalesce(p.account,'feishu:'||p.open_id),'display_name',p.name,
		'avatar_url',p.avatar,'username',p.username,'version',coalesce(m.version,0))
		FROM system_people p LEFT JOIN project_members m
		ON (m.tenant_key,m.app_id,m.project_id,m.account_id)=($1,$2,$3,p.account)
		WHERE (m.role IS NULL OR m.role='revoked') AND strpos(p.searchable,lower($4))>0
		ORDER BY p.name,p.open_id LIMIT 25 OFFSET $5`, k.args(strings.TrimSpace(q), (page-1)*25)...)
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

func (s *Service) addMembers(w http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
	var in struct {
		ProjectRole    string `json:"project_role"`
		Responsibility string `json:"responsibility"`
		Members        []struct {
			Account string `json:"account_id"`
			Version *int64 `json:"expected_version"`
		} `json:"members"`
	}
	if !decodeLimit(w, r, &in, 256<<10) || !access.IsProjectRole(in.ProjectRole) || !textLimit(in.Responsibility, 4000) || len(in.Members) == 0 {
		return nil, errInput
	}
	ids := make([]string, 0, len(in.Members))
	for _, member := range in.Members {
		if !identifier.MatchString(member.Account) || member.Version == nil || *member.Version < 0 {
			return nil, errInput
		}
		ids = append(ids, member.Account)
	}
	ctx, k := r.Context(), scope(actor, r.PathValue("projectID"))
	err := s.withMutation(ctx, func(tx pgx.Tx) error {
		work := *s
		work.mutation = tx
		if err := work.require(ctx, actor, "project.settings", k.project); err != nil {
			return err
		}
		var candidates int
		err := tx.QueryRow(ctx, systemPeople+` SELECT count(*) FROM system_people p
			LEFT JOIN project_members m ON (m.tenant_key,m.app_id,m.project_id,m.account_id)=($1,$2,$3,p.account)
			WHERE coalesce(p.account,'feishu:'||p.open_id)=ANY($4::text[]) AND (m.role IS NULL OR m.role='revoked')`, k.args(ids)...).Scan(&candidates)
		if err != nil {
			return err
		}
		if candidates != len(ids) {
			return errConflict
		}
		previous := string(access.RoleProjectMember)
		for _, member := range in.Members {
			change := memberChange{Expected: member.Version, ProjectRole: in.ProjectRole, ExpectedProjectRole: &previous, Responsibility: &in.Responsibility}
			if _, err := work.changeMember(ctx, k, actor, member.Account, change); err != nil {
				return err
			}
		}
		return nil
	})
	return map[string]int{"added": len(ids)}, err
}
