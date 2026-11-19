package directory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

const route = "/api/v1/system/directory"

type authorizeSync func(context.Context, pgx.Tx, identity.Principal) (bool, error)

var errSyncForbidden = errors.New("directory sync forbidden")

func (s *Service) Register(mux *http.ServeMux, auth func(http.ResponseWriter, *http.Request, identity.SessionPolicy) (identity.Principal, bool),
	check func(context.Context, identity.Principal, string, string) (bool, error), syncPermission authorizeSync) {
	secured := func(action string, handler func(http.ResponseWriter, *http.Request, identity.Principal)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			p, ok := auth(w, r, identity.ValidateSession)
			if !ok {
				return
			}
			if !s.sameSource(p) {
				respond(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
				return
			}
			allowed, err := check(r.Context(), p, action, "")
			if err != nil {
				unavailable(w)
				return
			}
			if !allowed {
				respond(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
				return
			}
			handler(w, r, p)
		}
	}
	mux.HandleFunc("GET "+route+"/departments", secured("directory.read", s.getDepartments))
	mux.HandleFunc("GET "+route+"/people", secured("directory.read", s.getPeople))
	mux.HandleFunc("GET "+route+"/sync-runs", secured("directory.read", s.getRuns))
	mux.HandleFunc("GET "+route+"/sync-runs/{syncID}", secured("directory.read", s.getRun))
	mux.HandleFunc("POST "+route+"/sync-runs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := auth(w, r, identity.RefreshSessionWithCSRF)
		if ok {
			s.postRun(w, r, actor, syncPermission)
		}
	})
}

func respond(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func unavailable(w http.ResponseWriter) {
	respond(w, http.StatusServiceUnavailable, map[string]string{"error": "directory_unavailable"})
}

type run struct {
	ID                 int64         `json:"id"`
	Trigger            string        `json:"trigger"`
	State              string        `json:"state"`
	CreatedAt          time.Time     `json:"created_at"`
	StartedAt          *time.Time    `json:"started_at"`
	FinishedAt         *time.Time    `json:"finished_at"`
	DepartmentCount    int           `json:"department_count"`
	PeopleCount        int           `json:"people_count"`
	MissingPeopleCount int           `json:"missing_people_count"`
	OutsideScopeCount  int           `json:"outside_scope_count"`
	NewPeopleCount     int           `json:"new_people_count"`
	ChangedPeopleCount int           `json:"changed_people_count"`
	FailureReason      string        `json:"failure_reason,omitempty"`
	Scope              *scopeSummary `json:"scope"`
}

const runFields = `id,trigger,state,created_at,started_at,finished_at,department_count,people_count,
	missing_people_count,outside_scope_count,new_people_count,changed_people_count,failure_reason,scope`

func scanRun(row pgx.Row) (run, error) {
	var item run
	err := row.Scan(&item.ID, &item.Trigger, &item.State, &item.CreatedAt, &item.StartedAt, &item.FinishedAt,
		&item.DepartmentCount, &item.PeopleCount, &item.MissingPeopleCount, &item.OutsideScopeCount, &item.NewPeopleCount,
		&item.ChangedPeopleCount, &item.FailureReason, &item.Scope)
	item.CreatedAt = item.CreatedAt.UTC()
	normalizeTimes(item.StartedAt, item.FinishedAt)
	return item, err
}

func normalizeTimes(times ...*time.Time) {
	for _, value := range times {
		if value != nil {
			*value = value.UTC()
		}
	}
}

type sourceStatus struct {
	Version     *int64        `json:"snapshot_version"`
	Scope       *scopeSummary `json:"scope"`
	Visible     bool          `json:"visible"`
	LastSuccess *time.Time    `json:"last_success_at"`
	NextRun     *time.Time    `json:"next_run_at"`
	Interval    int64         `json:"interval_seconds"`
}

func (s *Service) status(ctx context.Context, tx pgx.Tx) (sourceStatus, error) {
	status := sourceStatus{Interval: int64(s.cfg.Interval / time.Second)}
	err := tx.QueryRow(ctx, `SELECT CASE WHEN src.visible THEN src.active_version END,src.visible,src.last_success_at,src.next_run_at,
		CASE WHEN src.visible THEN history.scope END
		FROM directory_sources src LEFT JOIN directory_sync_runs history
		ON (history.tenant_key,history.app_id,history.id)=(src.tenant_key,src.app_id,src.active_version)
		WHERE src.tenant_key=$1 AND src.app_id=$2`, s.cfg.TenantKey, s.cfg.AppID).Scan(&status.Version, &status.Visible,
		&status.LastSuccess, &status.NextRun, &status.Scope)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	normalizeTimes(status.LastSuccess, status.NextRun)
	return status, err
}

func listPage(r *http.Request) (int, int, bool) {
	q := r.URL.Query()
	limit, offset := 50, 0
	var err error
	if q.Has("limit") {
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil || limit < 1 || limit > 200 {
			return 0, 0, false
		}
	}
	if q.Has("offset") {
		offset, err = strconv.Atoi(q.Get("offset"))
		if err != nil || offset < 0 || offset > 1000000 {
			return 0, 0, false
		}
	}
	return limit, offset, true
}

func badQuery(w http.ResponseWriter) {
	respond(w, http.StatusBadRequest, map[string]string{"error": "invalid_directory_query"})
}

func (s *Service) openRead(w http.ResponseWriter, r *http.Request) (pgx.Tx, sourceStatus, bool) {
	ctx := r.Context()
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		unavailable(w)
		return nil, sourceStatus{}, false
	}
	status, err := s.status(ctx, tx)
	if err != nil {
		_ = tx.Rollback(ctx)
		unavailable(w)
		return nil, sourceStatus{}, false
	}
	if r.URL.Query().Has("snapshot_version") {
		version, err := strconv.ParseInt(r.URL.Query().Get("snapshot_version"), 10, 64)
		if err != nil || version <= 0 {
			_ = tx.Rollback(ctx)
			badQuery(w)
			return nil, sourceStatus{}, false
		}
		if status.Version == nil || *status.Version != version {
			_ = tx.Rollback(ctx)
			respond(w, http.StatusConflict, map[string]string{"error": "directory_snapshot_changed"})
			return nil, sourceStatus{}, false
		}
	}
	return tx, status, true
}

func (s *Service) getDepartments(w http.ResponseWriter, r *http.Request, _ identity.Principal) {
	q := r.URL.Query()
	parent := q.Get("parent_id")
	if parent == "0" {
		parent = ""
	}
	if parent != "" && !validID(parent) {
		badQuery(w)
		return
	}
	limit, offset, ok := listPage(r)
	if !ok {
		badQuery(w)
		return
	}
	tx, status, ok := s.openRead(w, r)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	body := struct {
		sourceStatus
		Items json.RawMessage `json:"items"`
		Total int             `json:"total"`
	}{sourceStatus: status, Items: json.RawMessage("[]")}
	if status.Visible {
		err := tx.QueryRow(r.Context(), `WITH matched AS (
			SELECT id,COALESCE(parent_id,'') AS parent_id,name,path,has_children FROM departments
			WHERE tenant_key=$1 AND app_id=$2 AND snapshot_version=$3 AND parent_id IS NOT DISTINCT FROM NULLIF($4,'')
		), page AS (
			SELECT * FROM matched ORDER BY lower(name),id LIMIT $5 OFFSET $6
		)
		SELECT COALESCE(jsonb_agg(page ORDER BY lower(name),id),'[]'),(SELECT count(*) FROM matched) FROM page`,
			s.cfg.TenantKey, s.cfg.AppID, status.Version, parent, limit, offset).Scan(&body.Items, &body.Total)
		if err != nil {
			unavailable(w)
			return
		}
	}
	respond(w, http.StatusOK, body)
}

func (s *Service) getPeople(w http.ResponseWriter, r *http.Request, _ identity.Principal) {
	q := r.URL.Query()
	dept := q.Get("department_id")
	if dept == "0" {
		dept = ""
	}
	keyword := strings.TrimSpace(q.Get("q"))
	account := q.Get("account_status")
	descendants := false
	if q.Has("include_descendants") {
		var err error
		descendants, err = strconv.ParseBool(q.Get("include_descendants"))
		if err != nil {
			badQuery(w)
			return
		}
	}
	limit, offset, ok := listPage(r)
	if !ok || (dept != "" && !validID(dept)) || len(keyword) > 256 ||
		(account != "" && account != "active" && account != "inactive" && account != "unprovisioned") {
		badQuery(w)
		return
	}
	tx, status, ok := s.openRead(w, r)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	body := struct {
		sourceStatus
		Items json.RawMessage `json:"items"`
		Total int             `json:"total"`
	}{sourceStatus: status, Items: json.RawMessage("[]")}
	if status.Visible {
		err := tx.QueryRow(r.Context(), `WITH RECURSIVE scoped(id) AS (
			SELECT id FROM departments WHERE tenant_key=$1 AND app_id=$2 AND snapshot_version=$3 AND id=$4
			UNION ALL
			SELECT d.id FROM departments d JOIN scoped ON scoped.id=d.parent_id
			WHERE $5 AND d.tenant_key=$1 AND d.app_id=$2 AND d.snapshot_version=$3
		), matched AS (
			SELECT p.open_id,p.name,p.avatar_url,p.directory_status,a.id AS account_id,
				CASE WHEN a.id IS NULL THEN 'unprovisioned' WHEN a.active THEN 'active' ELSE 'inactive' END AS account_status
			FROM directory_people p LEFT JOIN accounts a ON (a.tenant_key,a.app_id,a.open_id)=(p.tenant_key,p.app_id,p.open_id)
			WHERE p.tenant_key=$1 AND p.app_id=$2 AND p.snapshot_version=$3
				AND ($4='' OR EXISTS (SELECT 1 FROM department_memberships m JOIN scoped ON scoped.id=m.department_id
					WHERE (m.tenant_key,m.app_id,m.snapshot_version,m.open_id)=(p.tenant_key,p.app_id,p.snapshot_version,p.open_id)))
				AND ($6='' OR strpos(lower(p.name),lower($6))>0)
		), filtered AS (
			SELECT * FROM matched WHERE $7='' OR account_status=$7
		), page AS (
			SELECT * FROM filtered ORDER BY lower(name),open_id LIMIT $8 OFFSET $9
		), detailed AS (
			SELECT page.*, `+identity.FeishuPersonKeySQL("page.open_id")+` AS person_key,
				COALESCE((SELECT jsonb_agg(jsonb_build_object('id',d.id,'path',d.path) ORDER BY d.path,d.id)
					FROM department_memberships m JOIN departments d
					ON (d.tenant_key,d.app_id,d.snapshot_version,d.id)=(m.tenant_key,m.app_id,m.snapshot_version,m.department_id)
					WHERE m.tenant_key=$1 AND m.app_id=$2 AND m.snapshot_version=$3 AND m.open_id=page.open_id),'[]') AS departments,
				COALESCE((SELECT jsonb_agg(g.role ORDER BY g.role) FROM role_grants g
					WHERE g.tenant_key=$1 AND g.app_id=$2 AND g.account_id=page.account_id AND page.account_status='active'
					AND g.revoked_at IS NULL AND g.project_id IS NULL),'[]') AS platform_roles
			FROM page
		)
		SELECT COALESCE(jsonb_agg(detailed ORDER BY lower(name),open_id),'[]'),(SELECT count(*) FROM filtered) FROM detailed`,
			s.cfg.TenantKey, s.cfg.AppID, status.Version, dept, descendants, keyword, account, limit, offset).Scan(&body.Items, &body.Total)
		if err != nil {
			unavailable(w)
			return
		}
	}
	respond(w, http.StatusOK, body)
}

func (s *Service) getRuns(w http.ResponseWriter, r *http.Request, _ identity.Principal) {
	limit, offset, ok := listPage(r)
	if !ok {
		badQuery(w)
		return
	}
	tx, status, ok := s.openRead(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	defer tx.Rollback(ctx)
	body := struct {
		sourceStatus
		Items         []run `json:"items"`
		Total         int   `json:"total"`
		Current       *run  `json:"current"`
		LatestAttempt *run  `json:"latest_attempt"`
	}{sourceStatus: status, Items: []run{}}
	tenant, app := s.cfg.TenantKey, s.cfg.AppID
	from := ` FROM directory_sync_runs WHERE tenant_key=$1 AND app_id=$2`
	row := tx.QueryRow(ctx, `SELECT `+runFields+from+` ORDER BY id DESC LIMIT 1`, tenant, app)
	if item, err := scanRun(row); err == nil {
		body.LatestAttempt = &item
		if item.State == "running" || item.State == "queued" {
			body.Current = &item
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		unavailable(w)
		return
	}
	if err := tx.QueryRow(ctx, `SELECT count(*)`+from, tenant, app).Scan(&body.Total); err != nil {
		unavailable(w)
		return
	}
	rows, err := tx.Query(ctx, `SELECT `+runFields+from+` ORDER BY id DESC LIMIT $3 OFFSET $4`, tenant, app, limit, offset)
	if err != nil {
		unavailable(w)
		return
	}
	defer rows.Close()
	for rows.Next() {
		item, err := scanRun(rows)
		if err != nil {
			unavailable(w)
			return
		}
		body.Items = append(body.Items, item)
	}
	if rows.Err() != nil {
		unavailable(w)
		return
	}
	respond(w, http.StatusOK, body)
}

func (s *Service) getRun(w http.ResponseWriter, r *http.Request, _ identity.Principal) {
	id, err := strconv.ParseInt(r.PathValue("syncID"), 10, 64)
	if err != nil || id <= 0 {
		badQuery(w)
		return
	}
	item, err := scanRun(s.db.QueryRow(r.Context(), `SELECT `+runFields+` FROM directory_sync_runs
		WHERE tenant_key=$1 AND app_id=$2 AND id=$3`, s.cfg.TenantKey, s.cfg.AppID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		respond(w, http.StatusNotFound, map[string]string{"error": "sync_run_not_found"})
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	respond(w, http.StatusOK, item)
}

func (s *Service) postRun(w http.ResponseWriter, r *http.Request, actor identity.Principal, permission authorizeSync) {
	item, coalesced, err := s.enqueue(r.Context(), actor, permission)
	if errors.Is(err, errSyncForbidden) {
		respond(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
	respond(w, http.StatusAccepted, struct {
		run
		Coalesced bool `json:"coalesced"`
	}{run: item, Coalesced: coalesced})
}

func (s *Service) enqueue(ctx context.Context, actor identity.Principal, permission authorizeSync) (run, bool, error) {
	if !s.sameSource(actor) || permission == nil {
		return run{}, false, errSyncForbidden
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return run{}, false, err
	}
	defer tx.Rollback(ctx)
	allowed, err := permission(ctx, tx, actor)
	if err != nil {
		return run{}, false, err
	}
	if !allowed {
		return run{}, false, errSyncForbidden
	}
	if _, _, err := s.sourceLock(ctx, tx); err != nil {
		return run{}, false, err
	}
	tenant, app := s.cfg.TenantKey, s.cfg.AppID
	item, err := scanRun(tx.QueryRow(ctx, `SELECT `+runFields+` FROM directory_sync_runs
		WHERE tenant_key=$1 AND app_id=$2 AND state IN ('queued','running')`, tenant, app))
	if err == nil && item.State == "running" {
		var expired bool
		if err := tx.QueryRow(ctx, `SELECT lease_until <= clock_timestamp() FROM directory_sync_runs WHERE id=$1`, item.ID).Scan(&expired); err != nil {
			return run{}, false, err
		}
		if expired {
			if err := s.finishFailed(ctx, tx, item.ID, "worker lease expired"); err != nil {
				return run{}, false, err
			}
			err = pgx.ErrNoRows
		}
	}
	coalesced := err == nil
	result := "coalesced"
	if errors.Is(err, pgx.ErrNoRows) {
		item, err = scanRun(tx.QueryRow(ctx, `INSERT INTO directory_sync_runs (tenant_key,app_id,trigger,state)
			VALUES ($1,$2,'manual','queued') RETURNING `+runFields, tenant, app))
		result = "requested"
	}
	if err != nil {
		return run{}, false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events (tenant_key,app_id,actor_account_id,action,resource_type,resource_id,result,change_version)
		VALUES ($1,$2,$3,'directory.sync','directory_sync',$4,$5,0)`, tenant, app, actor.AccountID, strconv.FormatInt(item.ID, 10), result)
	if err != nil {
		return run{}, false, err
	}
	return item, coalesced, tx.Commit(ctx)
}
