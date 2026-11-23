package project

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

var projectStatuses = []string{"planned", "active", "paused", "blocked", "completed", "archived"}

const profileColumns = `coalesce(p.status,'active'), coalesce(p.project_type,''),
	coalesce(b.business_timezone,'Asia/Shanghai'), coalesce(p.background,''), coalesce(p.version,0),
	coalesce(to_char(p.planned_start,'YYYY-MM-DD'),''), coalesce(to_char(p.planned_end,'YYYY-MM-DD'),''), coalesce(b.version,0), p.updated_at, coalesce(c.short_code,'')`

func (s *Service) directory(ctx context.Context, actor identity.Principal, filter url.Values, page, size int) (ProjectPage, error) {
	result := ProjectPage{Items: []directoryProject{}, Page: page, PageSize: size, Types: projectTypeOptions}
	const visible = ` FROM project_members m
 JOIN project_short_codes c USING(tenant_key,app_id,project_id)
		JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(m.tenant_key,m.app_id,m.account_id)
		LEFT JOIN project_profiles p ON (p.tenant_key,p.app_id,p.project_id)=(m.tenant_key,m.app_id,m.project_id)
		LEFT JOIN project_task_boards b ON (b.tenant_key,b.app_id,b.project_id)=(m.tenant_key,m.app_id,m.project_id)
		WHERE (m.tenant_key,m.app_id,m.account_id)=($1,$2,$3) AND a.open_id=$4 AND a.active AND m.role<>'revoked'
		AND (strpos(lower(coalesce(p.name,m.project_id)),lower($5))>0 OR strpos(lower(m.project_id),lower($5))>0 OR strpos(lower(c.short_code),lower($5))>0)
		AND ($6='' OR coalesce(p.status,'active')=$6)`
	if status := filter.Get("status"); status != "" && !slices.Contains(projectStatuses, status) {
		return result, errInput
	}
	order := "p.updated_at DESC NULLS LAST, lower(coalesce(p.name,m.project_id)), m.project_id"
	if filter.Has("sort") || filter.Has("direction") {
		column := map[string]string{
			"name": "lower(coalesce(p.name,m.project_id))", "project_type": "coalesce(p.project_type,'')",
			"status": "coalesce(p.status,'active')", "updated_at": "p.updated_at",
		}[filter.Get("sort")]
		direction := filter.Get("direction")
		if column == "" || (direction != "asc" && direction != "desc") {
			return result, errInput
		}
		order = column + " " + direction + " NULLS LAST, lower(coalesce(p.name,m.project_id)), m.project_id"
	}
	var err error
	result.CanCreate, err = s.roles.Check(ctx, actor, "project.create", "")
	if err != nil {
		return result, err
	}
	args := []any{actor.TenantKey, actor.AppID, actor.AccountID, actor.OpenID, strings.TrimSpace(filter.Get("q")), filter.Get("status")}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT count(*)"+visible, args...).Scan(&result.Total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT m.project_id, coalesce(p.name,m.project_id), "+profileColumns+", m.followed"+visible+
			" ORDER BY "+order+" LIMIT $7 OFFSET $8", append(args, size, (page-1)*size)...)
		if err != nil {
			return err
		}
		result.Items, err = pgx.CollectRows(rows, pgx.RowToStructByPos[directoryProject])
		return err
	})
	return result, err
}

func (s *Service) overview(ctx context.Context, actor identity.Principal, id string) (panelView, error) {
	k := scope(actor, id)
	project, err := k.profile(ctx, s.pool)
	if err != nil {
		return panelView{}, err
	}
	canManage, err := s.roles.Check(ctx, actor, "project.settings", id)
	if err != nil {
		return panelView{}, err
	}
	var followed bool
	err = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT FROM project_members WHERE `+projectWhere+
		` AND account_id=$4 AND role<>'revoked' AND followed)`, k.args(actor.AccountID)...).Scan(&followed)
	if err != nil {
		return panelView{}, err
	}
	brief, err := k.latestBriefSummary(ctx, s.pool, actor.AccountID)
	if err != nil {
		return panelView{}, err
	}
	nodes, err := s.milestones(ctx, k, time.Now())
	return panelView{Project: id, Panel: "overview", Data: map[string]any{
		"workspace": map[string]any{
			"project": directoryProject{Project: project, Followed: followed}, "project_types": projectTypeOptions, "can_manage_profile": canManage && project.Status != "archived",
		},
		"milestones": map[string]any{"items": nodes}, "latest_briefing": brief,
	}}, err
}

func (k taskScope) profile(ctx context.Context, db taskReader) (Project, error) {
	var p Project
	err := db.QueryRow(ctx, `SELECT $3::text, coalesce(p.name,$3), `+profileColumns+`
		FROM (SELECT 1) row_anchor
 LEFT JOIN project_short_codes c ON (c.tenant_key,c.app_id,c.project_id)=($1,$2,$3)
		LEFT JOIN project_profiles p ON (p.tenant_key,p.app_id,p.project_id)=($1,$2,$3)
		LEFT JOIN project_task_boards b ON (b.tenant_key,b.app_id,b.project_id)=($1,$2,$3)`, k.args()...).
		Scan(&p.ID, &p.Name, &p.Status, &p.Type, &p.Timezone, &p.Background, &p.Version,
			&p.PlannedStart, &p.PlannedEnd, &p.BoardVersion, &p.UpdatedAt, &p.ShortCode)
	return p, err
}
