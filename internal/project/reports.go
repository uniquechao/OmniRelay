package project

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

type memberReport struct {
	Account string    `json:"account_id"`
	Person  string    `json:"person_name"`
	Date    string    `json:"work_date"`
	Note    string    `json:"note"`
	State   string    `json:"submission_state"`
	Version int64     `json:"version"`
	Updated time.Time `json:"updated_at"`
}

func validReportDate(value string) bool {
	day, err := time.Parse(time.DateOnly, value)
	return err == nil && day.Year() > 0 && day.Format(time.DateOnly) == value
}

// Build one authorized report relation for the list and the briefing snapshot.
const authorizedReports = ` FROM project_member_reports r
	JOIN project_members m USING (tenant_key,app_id,project_id,account_id)
	JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(m.tenant_key,m.app_id,m.account_id)
	WHERE (r.tenant_key,r.app_id,r.project_id)=($1,$2,$3) AND m.role<>'revoked' AND a.active
	AND (r.account_id=$4 OR $5::boolean)
	AND (nullif($6,'') IS NULL OR r.work_date >= nullif($6,'')::date)
	AND (nullif($7,'') IS NULL OR r.work_date <= nullif($7,'')::date)
	AND strpos(lower(coalesce(a.name,'')||' '||r.note),lower($8))>0`

const reportRevision = `md5(coalesce(string_agg(jsonb_build_array(r.account_id,r.work_date,r.version)::text,',' ORDER BY r.work_date,r.account_id),''))`

func (s *Service) reportArgs(ctx context.Context, actor identity.Principal, id, from, to, search string) ([]any, bool, error) {
	if (from != "" && !validReportDate(from)) || (to != "" && !validReportDate(to)) || (from != "" && to != "" && from > to) {
		return nil, false, errInput
	}
	manage, err := s.allowed(ctx, actor, "project.settings", id)
	return scope(actor, id).args(actor.AccountID, manage, from, to, search), manage, err
}

func (s *Service) reports(ctx context.Context, actor identity.Principal, id string, query url.Values) (panelView, error) {
	page, size, err := panelPagination(query)
	if err != nil {
		return panelView{}, err
	}
	args, manage, err := s.reportArgs(ctx, actor, id, query.Get("date_from"), query.Get("date_to"), query.Get("q"))
	if err != nil {
		return panelView{}, err
	}
	items := []memberReport{}
	var total int
	var revision string
	err = s.withReportSnapshot(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT count(*),"+reportRevision+authorizedReports, args...).Scan(&total, &revision); err != nil {
			return err
		}
		if expected := query.Get("revision"); expected != "" && expected != revision {
			return errConflict
		}
		rows, err := tx.Query(ctx, `SELECT r.account_id,coalesce(nullif(a.name,''),m.member_id),
			r.work_date::text,r.note,'submitted',r.version,r.updated_at`+authorizedReports+
			" ORDER BY r.work_date DESC,r.account_id LIMIT $9 OFFSET $10", append(args, size, (page-1)*size)...)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, pgx.RowToStructByPos[memberReport])
		return err
	})
	if err != nil {
		return panelView{}, err
	}
	if manage {
		if err := s.require(ctx, actor, "project.settings", id); err != nil {
			return panelView{}, err
		}
	}
	return listPanel(id, "reports", items, total, map[string]any{"revision": revision}), nil
}

func (s *Service) submissionCount(ctx context.Context, actor identity.Principal, id, date string) (int, string, error) {
	args, manage, err := s.reportArgs(ctx, actor, id, date, date, "")
	if err != nil {
		return 0, "", err
	}
	var count int
	var revision string
	err = s.pool.QueryRow(ctx, "SELECT count(*),"+reportRevision+authorizedReports, args...).Scan(&count, &revision)
	if err != nil {
		return 0, "", err
	}
	if count == 0 {
		return 0, "", errUnavailable
	}
	if manage {
		if err := s.require(ctx, actor, "project.settings", id); err != nil {
			return 0, "", err
		}
	}
	return count, "local:" + revision, nil
}

func (s *Service) saveReport(w http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
	var in struct {
		Note    string `json:"note"`
		Version *int64 `json:"expected_version"`
	}
	date := r.PathValue("date")
	if !validReportDate(date) || !decodeLimit(w, r, &in, 64<<10) || in.Version == nil || *in.Version < 0 ||
		strings.TrimSpace(in.Note) == "" || !textLimit(in.Note, 10000) {
		return nil, errInput
	}
	k, ctx := scope(actor, r.PathValue("projectID")), r.Context()
	result := memberReport{Account: actor.AccountID, Date: date, Note: strings.TrimSpace(in.Note), State: "submitted"}
	err := s.roles.WithOrganizationMutation(ctx, func(tx pgx.Tx) error {
		allowed, err := s.roles.CheckInOrganizationMutation(ctx, tx, actor, "project.comment", k.project)
		if err != nil {
			return err
		}
		if !allowed {
			return errDenied
		}
		args := k.args(actor.AccountID, date, result.Note)
		if *in.Version == 0 {
			err = tx.QueryRow(ctx, `INSERT INTO project_member_reports
				(tenant_key,app_id,project_id,account_id,work_date,note,version)
				VALUES($1,$2,$3,$4,$5::text::date,$6,1) ON CONFLICT DO NOTHING RETURNING version,updated_at`, args...).
				Scan(&result.Version, &result.Updated)
		} else {
			err = tx.QueryRow(ctx, `UPDATE project_member_reports SET note=$6,version=version+1,updated_at=clock_timestamp()
				WHERE (tenant_key,app_id,project_id,account_id,work_date,version)=($1,$2,$3,$4,$5::text::date,$7)
				RETURNING version,updated_at`, append(args, *in.Version)...).Scan(&result.Version, &result.Updated)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return errConflict
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO audit_events
			(tenant_key,app_id,actor_account_id,action,resource_type,resource_id,result,change_version)
			VALUES($1,$2,$4,'project.report.submit','project',$3,'success',$5)`, k.args(actor.AccountID, result.Version)...)
		return err
	})
	return result, err
}
