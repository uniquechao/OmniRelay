package project

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

type dailyBrief struct {
	ID        string     `json:"id"`
	Date      string     `json:"business_date"`
	Title     string     `json:"title,omitempty"`
	Body      string     `json:"body,omitempty"`
	Version   int64      `json:"version"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// A project-authored brief has one current version per date. Delivery runs have
// different authorship and visibility and are queried separately.
func (k taskScope) authoredBriefs(ctx context.Context, db taskReader, from, to, query string, page int64, directory bool) (any, error) {
	if !validReportRange(from, to) {
		return nil, errInput
	}
	var total int
	var items []struct {
		Date    string    `json:"business_date"`
		Title   string    `json:"title"`
		Excerpt string    `json:"excerpt"`
		Version int64     `json:"version"`
		Updated time.Time `json:"updated_at"`
	}
	err := db.QueryRow(ctx, `WITH matched AS (
		SELECT business_date,title,left(body,180) AS excerpt,version,updated_at FROM project_daily_briefs WHERE `+projectWhere+`
		AND (nullif($4,'') IS NULL OR business_date>=nullif($4,'')::date)
		AND (nullif($5,'') IS NULL OR business_date<=nullif($5,'')::date)
		AND strpos(lower(title||' '||body),lower($6))>0
	), page AS (SELECT * FROM matched ORDER BY business_date DESC LIMIT 25 OFFSET $7)
	SELECT (SELECT count(*) FROM matched),coalesce(jsonb_agg(page ORDER BY business_date DESC),'[]'::jsonb) FROM page`,
		k.args(from, to, strings.TrimSpace(query), (page-1)*25)...).Scan(&total, &items)
	out := map[string]any{"source": "authored", "items": items, "page": page, "total": total}
	if err != nil || !directory {
		return out, err
	}
	var count int
	var recent []struct {
		Date    string `json:"business_date"`
		Title   string `json:"title"`
		Version int64  `json:"version"`
	}
	err = db.QueryRow(ctx, `WITH recent AS (
	   SELECT business_date,title,version FROM project_daily_briefs WHERE `+projectWhere+` ORDER BY business_date DESC LIMIT 5)
	   SELECT (SELECT count(*) FROM project_daily_briefs WHERE `+projectWhere+`),
	     coalesce(jsonb_agg(recent ORDER BY business_date DESC),'[]'::jsonb) FROM recent`, k.args()...).Scan(&count, &recent)
	out["project_directory"] = map[string]any{"scope": "catalog_metadata_only", "total": count, "recent": recent}
	return out, err
}

func (k taskScope) dailyBrief(ctx context.Context, db taskReader, date string) (dailyBrief, error) {
	b := dailyBrief{ID: date, Date: date}
	if !validReportDate(date) {
		return b, errInput
	}
	err := db.QueryRow(ctx, `SELECT title,body,version,updated_at FROM project_daily_briefs WHERE `+projectWhere+
		` AND business_date=$4::text::date`, k.args(date)...).Scan(&b.Title, &b.Body, &b.Version, &b.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, nil
	}
	return b, err
}

func (k taskScope) saveDailyBrief(ctx context.Context, tx pgx.Tx, actor identity.Principal, date, title, body string, expected int64) (any, error) {
	title, body = strings.TrimSpace(title), strings.TrimSpace(body)
	if !validReportDate(date) || title == "" || utf8.RuneCountInString(title) > 200 || body == "" {
		return nil, errInput
	}
	p, err := k.profile(ctx, tx)
	if err != nil {
		return nil, err
	}
	if p.Status == "archived" {
		return nil, errConflict
	}
	var version int64
	args := k.args(date, title, body, actor.AccountID)
	if expected == 0 {
		err = tx.QueryRow(ctx, `INSERT INTO project_daily_briefs(tenant_key,app_id,project_id,business_date,title,body,updated_by,version)
			VALUES($1,$2,$3,$4::text::date,$5,$6,$7,1) ON CONFLICT DO NOTHING RETURNING version`, args...).Scan(&version)
	} else {
		err = tx.QueryRow(ctx, `UPDATE project_daily_briefs SET title=$5,body=$6,updated_by=$7,updated_at=now(),version=version+1
			WHERE `+projectWhere+` AND business_date=$4::text::date AND version=$8 RETURNING version`, append(args, expected)...).Scan(&version)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errConflict
	}
	if err != nil {
		return nil, err
	}
	err = k.auditProfile(ctx, tx, actor, "project.briefing.save", version, map[string]string{"business_date": date})
	return map[string]any{"business_date": date, "version": version}, err
}

// Each day has one list entry. Legacy generation receipts remain visible only to their owner.
const briefIndexSQL = `WITH briefs AS (
	SELECT business_date,business_date::text AS id,title,left(body,180) AS excerpt,updated_at FROM project_daily_briefs WHERE ` + projectWhere + `
	UNION ALL
	SELECT r.business_date,r.id,'' AS title,'' AS excerpt,r.generated_at FROM project_briefing_runs r
	WHERE (r.tenant_key,r.app_id,r.project_id,r.account_id)=($1,$2,$3,$4)
	AND NOT EXISTS (SELECT FROM project_daily_briefs b WHERE (b.tenant_key,b.app_id,b.project_id,b.business_date)=
		(r.tenant_key,r.app_id,r.project_id,r.business_date))
) `

type briefSummary struct {
	ID      string    `json:"id"`
	Date    string    `json:"business_date"`
	Title   string    `json:"title,omitempty"`
	Excerpt string    `json:"excerpt,omitempty"`
	Updated time.Time `json:"updated_at"`
}

func (k taskScope) latestBriefSummary(ctx context.Context, db taskReader, account string) (*briefSummary, error) {
	var b briefSummary
	err := db.QueryRow(ctx, briefIndexSQL+`SELECT id,business_date::text,title,excerpt,updated_at FROM briefs ORDER BY business_date DESC LIMIT 1`,
		k.args(account)...).Scan(&b.ID, &b.Date, &b.Title, &b.Excerpt, &b.Updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &b, err
}

func (s *Service) listBriefings(_ http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
	ctx, k := r.Context(), scope(actor, r.PathValue("projectID"))
	page, size, err := panelPagination(r.URL.Query())
	if err != nil {
		return nil, err
	}
	var total int
	var summaries []briefSummary
	err = s.pool.QueryRow(ctx, briefIndexSQL+`, page AS (
		SELECT * FROM briefs ORDER BY business_date DESC LIMIT $5 OFFSET $6
	) SELECT (SELECT count(*) FROM briefs),coalesce(jsonb_agg(page ORDER BY business_date DESC),'[]'::jsonb) FROM page`,
		k.args(actor.AccountID, size, (page-1)*size)...).Scan(&total, &summaries)
	if err != nil {
		return nil, err
	}
	items := make([]any, 0, len(summaries))
	for _, entry := range summaries {
		if entry.Title != "" {
			items = append(items, entry)
			continue
		}
		run, err := s.visibleBriefing(ctx, actor, k.project, entry.ID)
		if err != nil {
			return nil, err
		}
		items = append(items, run)
	}
	return map[string]any{"items": items, "total": total}, nil
}

func (s *Service) briefingDetail(w http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
	date := r.PathValue("runID")
	if !validReportDate(date) {
		return s.latestBriefing(w, r, actor)
	}
	b, err := scope(actor, r.PathValue("projectID")).dailyBrief(r.Context(), s.pool, date)
	if err != nil {
		return nil, err
	}
	if b.Version == 0 {
		return nil, errDenied
	}
	return map[string]any{"items": []dailyBrief{b}, "total": 1}, nil
}
