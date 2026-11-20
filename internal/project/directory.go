package project

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

var projectStatuses = []string{"planned", "active", "paused", "blocked", "completed", "archived"}

const profileColumns = `m.project_id, coalesce(p.name,m.project_id), coalesce(p.status,'active'),
	coalesce(p.project_type,''), coalesce(b.business_timezone,'Asia/Shanghai'),
	coalesce(p.background,''), coalesce(p.version,0)`

func (s *Service) directory(ctx context.Context, actor identity.Principal, filter url.Values, page, size int) (ProjectPage, error) {
	result := ProjectPage{Items: []Project{}, Page: page, PageSize: size}
	const visible = ` FROM project_members m
		JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(m.tenant_key,m.app_id,m.account_id)
		LEFT JOIN project_profiles p ON (p.tenant_key,p.app_id,p.project_id)=(m.tenant_key,m.app_id,m.project_id)
		LEFT JOIN project_task_boards b ON (b.tenant_key,b.app_id,b.project_id)=(m.tenant_key,m.app_id,m.project_id)
		WHERE (m.tenant_key,m.app_id,m.account_id)=($1,$2,$3) AND a.open_id=$4 AND a.active AND m.role<>'revoked'
		AND (strpos(lower(coalesce(p.name,m.project_id)),lower($5))>0 OR strpos(lower(m.project_id),lower($5))>0)
		AND ($6='' OR coalesce(p.status,'active')=$6)`
	if status := filter.Get("status"); status != "" && !slices.Contains(projectStatuses, status) {
		return result, errInput
	}
	args := []any{actor.TenantKey, actor.AppID, actor.AccountID, actor.OpenID, strings.TrimSpace(filter.Get("q")), filter.Get("status")}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT count(*)"+visible, args...).Scan(&result.Total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+profileColumns+visible+
			" ORDER BY lower(coalesce(p.name,m.project_id)), m.project_id LIMIT $7 OFFSET $8", append(args, size, (page-1)*size)...)
		if err != nil {
			return err
		}
		result.Items, err = pgx.CollectRows(rows, pgx.RowToStructByPos[Project])
		return err
	})
	return result, err
}

func (s *Service) overview(ctx context.Context, actor identity.Principal, id string) (panelView, error) {
	k := scope(actor, id)
	var project Project
	err := s.pool.QueryRow(ctx, `SELECT $3::text, coalesce(p.name,$3), coalesce(p.status,'active'),
		coalesce(p.project_type,''), coalesce(b.business_timezone,'Asia/Shanghai'),
		coalesce(p.background,''), coalesce(p.version,0)
		FROM (SELECT 1) row_anchor
		LEFT JOIN project_profiles p ON (p.tenant_key,p.app_id,p.project_id)=($1,$2,$3)
		LEFT JOIN project_task_boards b ON (b.tenant_key,b.app_id,b.project_id)=($1,$2,$3)`, k.args()...).
		Scan(&project.ID, &project.Name, &project.Status, &project.Type, &project.Timezone, &project.Background, &project.Version)
	if err != nil {
		return panelView{}, err
	}
	nodes, err := s.milestones(ctx, k, time.Now())
	return panelView{Project: id, Panel: "overview", Data: map[string]any{
		"workspace": map[string]any{"project": project}, "milestones": map[string]any{"items": nodes},
	}}, err
}

func (s *Service) setProfile(w http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
	var in struct {
		Name       string `json:"name"`
		Status     string `json:"status"`
		Type       string `json:"project_type"`
		Background string `json:"background"`
		Version    *int64 `json:"expected_version"`
	}
	if !decodeLimit(w, r, &in, 32<<10) || in.Version == nil || *in.Version < 0 ||
		strings.TrimSpace(in.Name) == "" || !textLimit(in.Name, 200) ||
		!slices.Contains(projectStatuses, in.Status) || !textLimit(in.Type, 60) || !textLimit(in.Background, 4000) {
		return nil, errInput
	}
	k, ctx := scope(actor, r.PathValue("projectID")), r.Context()
	var version int64
	err := s.roles.WithOrganizationMutation(ctx, func(tx pgx.Tx) error {
		if err := s.requireSettingsMutation(ctx, tx, actor, k.project); err != nil {
			return err
		}
		args := k.args(strings.TrimSpace(in.Name), in.Status, strings.TrimSpace(in.Type), in.Background)
		var err error
		if *in.Version == 0 {
			err = tx.QueryRow(ctx, `INSERT INTO project_profiles VALUES($1,$2,$3,$4,$5,$6,$7,1)
				ON CONFLICT DO NOTHING RETURNING version`, args...).Scan(&version)
		} else {
			err = tx.QueryRow(ctx, `UPDATE project_profiles SET name=$4,status=$5,project_type=$6,background=$7,version=version+1
				WHERE (tenant_key,app_id,project_id,version)=($1,$2,$3,$8) RETURNING version`, append(args, *in.Version)...).Scan(&version)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return errConflict
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO audit_events
			(tenant_key,app_id,actor_account_id,action,resource_type,resource_id,result,change_version)
			VALUES($1,$2,$4,'project.profile.update','project',$3,'success',$5)`, k.args(actor.AccountID, version)...)
		return err
	})
	return map[string]any{"project_id": k.project, "version": version}, err
}
