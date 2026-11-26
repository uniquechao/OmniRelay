package project

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

// PortfolioProject is the project scope available to one enabled account.
type PortfolioProject struct {
	ID     string `json:"project_id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

const memberPortfolio = `SELECT p.project_id,p.name,p.status FROM project_members m
	JOIN project_profiles p USING(tenant_key,app_id,project_id)
	JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(m.tenant_key,m.app_id,m.account_id)
	WHERE (m.tenant_key,m.app_id,m.account_id)=($1,$2,$3) AND a.active AND m.role<>'revoked'`

func VisiblePortfolio(ctx context.Context, tx pgx.Tx, tenant, app, account string) ([]PortfolioProject, error) {
	rows, err := tx.Query(ctx, memberPortfolio+" ORDER BY p.name,p.project_id", tenant, app, account)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[PortfolioProject])
}

type portfolioBlocker struct {
	ID     string `json:"task_id"`
	Title  string `json:"title"`
	Reason string `json:"reason"`
}
type portfolioSummary struct {
	Project  PortfolioProject   `json:"project"`
	Blockers []portfolioBlocker `json:"blockers"`
}

func PortfolioSummary(ctx context.Context, tx pgx.Tx, p identity.Principal) (any, error) {
	rows, err := tx.Query(ctx, `WITH visible AS (`+memberPortfolio+`)
	SELECT v.project_id,v.name,v.status,coalesce(jsonb_agg(jsonb_build_object('task_id',t.id,'title',t.title,'reason',t.blocked_reason)
	ORDER BY t.id) FILTER(WHERE t.id IS NOT NULL),'[]'::jsonb)
	FROM visible v LEFT JOIN project_tasks t ON (t.tenant_key,t.app_id,t.project_id)=($1,$2,v.project_id)
	AND t.deleted_at IS NULL AND t.status='blocked' GROUP BY v.project_id,v.name,v.status ORDER BY v.name,v.project_id`,
		p.TenantKey, p.AppID, p.AccountID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (portfolioSummary, error) {
		var item portfolioSummary
		err := r.Scan(&item.Project.ID, &item.Project.Name, &item.Project.Status, &item.Blockers)
		return item, err
	})
}
