package directory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

type Config struct {
	AppID      string
	AppSecret  string
	TenantKey  string
	Interval   time.Duration
	PageBudget int
	HTTPClient *http.Client
	BaseURL    string
}

type Service struct {
	db     *pgxpool.Pool
	cfg    Config
	client *http.Client
	wake   chan struct{}
}

func New(pool *pgxpool.Pool, cfg Config) (*Service, error) {
	if pool == nil || cfg.AppID == "" || cfg.TenantKey == "" {
		return nil, errors.New("directory requires a database and Feishu app and tenant configuration")
	}
	if cfg.Interval < 0 || cfg.PageBudget < 0 {
		return nil, errors.New("directory interval and page budget must be positive")
	}
	if cfg.Interval == 0 {
		cfg.Interval = time.Hour
	}
	if cfg.PageBudget == 0 {
		cfg.PageBudget = 1000
	}
	if cfg.BaseURL != "" {
		base, err := url.Parse(cfg.BaseURL)
		if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" ||
			base.User != nil || base.RawQuery != "" || base.Fragment != "" || strings.Trim(base.Path, "/") != "" {
			return nil, errors.New("invalid directory Feishu base URL")
		}
		cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	}
	client := http.Client{Timeout: 15 * time.Second}
	if cfg.HTTPClient != nil {
		client = *cfg.HTTPClient
		if client.Timeout <= 0 || client.Timeout > 15*time.Second {
			client.Timeout = 15 * time.Second
		}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Service{db: pool, cfg: cfg, client: &client, wake: make(chan struct{}, 1)}, nil
}

func (s *Service) sameSource(p identity.Principal) bool {
	return p.AppID == s.cfg.AppID && p.TenantKey == s.cfg.TenantKey
}

const leaseSeconds = 345
const runTimeout = 5 * time.Minute

var errLostClaim = errors.New("directory synchronization lease lost")

type claim struct{ id, version int64 }

func (s *Service) sourceLock(ctx context.Context, tx pgx.Tx) (bool, bool, error) {
	_, err := tx.Exec(ctx, `INSERT INTO directory_sources (tenant_key,app_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`,
		s.cfg.TenantKey, s.cfg.AppID)
	if err != nil {
		return false, false, err
	}
	var due, initial bool
	err = tx.QueryRow(ctx, `SELECT next_run_at <= clock_timestamp(), active_version IS NULL
		FROM directory_sources WHERE tenant_key=$1 AND app_id=$2 FOR UPDATE`,
		s.cfg.TenantKey, s.cfg.AppID).Scan(&due, &initial)
	return due, initial, err
}

func (s *Service) claimNext(ctx context.Context) (claim, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return claim{}, err
	}
	defer tx.Rollback(ctx)
	due, initial, err := s.sourceLock(ctx, tx)
	if err != nil {
		return claim{}, err
	}
	var id int64
	var state string
	var expired bool
	err = tx.QueryRow(ctx, `SELECT id,state,COALESCE(lease_until <= clock_timestamp(),false)
		FROM directory_sync_runs WHERE tenant_key=$1 AND app_id=$2 AND state IN ('queued','running')`,
		s.cfg.TenantKey, s.cfg.AppID).Scan(&id, &state, &expired)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return claim{}, err
	}
	if state == "running" {
		if !expired {
			return claim{}, nil
		}
		// Keep interrupted attempts in history and apply the same restart backoff
		// used for a failed request before assigning a fresh run ID.
		if err = s.finishFailed(ctx, tx, id, "worker lease expired"); err != nil {
			return claim{}, err
		}
		return claim{}, tx.Commit(ctx)
	}
	if id == 0 {
		if !due {
			return claim{}, nil
		}
		trigger := "scheduled"
		if initial {
			trigger = "initial"
		}
		err = tx.QueryRow(ctx, `INSERT INTO directory_sync_runs (tenant_key,app_id,trigger,state)
			VALUES ($1,$2,$3,'queued') RETURNING id`, s.cfg.TenantKey, s.cfg.AppID, trigger).Scan(&id)
		if err != nil {
			return claim{}, err
		}
	}
	job := claim{id: id}
	err = tx.QueryRow(ctx, `UPDATE directory_sync_runs SET state='running', claim_version=claim_version+1,
		started_at=clock_timestamp(),lease_until=clock_timestamp()+($2::int * interval '1 second')
		WHERE id=$1 RETURNING claim_version`, id, leaseSeconds).Scan(&job.version)
	if err != nil {
		return claim{}, err
	}
	return job, tx.Commit(ctx)
}

func (s *Service) Run(ctx context.Context) {
	poll := time.NewTicker(2 * time.Second)
	defer poll.Stop()
	for {
		job, err := s.claimNext(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Error("claim directory synchronization", "error", err)
		} else if err == nil && job.id != 0 {
			s.runClaim(ctx, job)
		}
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
		case <-s.wake:
		}
	}
}

func (s *Service) runClaim(ctx context.Context, job claim) {
	workCtx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	result, err := s.collect(workCtx, func(authorization scope, departments, people int) error {
		return s.recordProgress(workCtx, job, authorization, departments, people)
	})
	if err == nil {
		err = s.publish(workCtx, job, result)
		if err != nil && !errors.Is(err, errLostClaim) && ctx.Err() == nil {
			slog.Error("publish directory snapshot", "run_id", job.id, "error", err)
			err = errors.New("could not publish directory snapshot")
		}
	}
	if err == nil || errors.Is(err, errLostClaim) || ctx.Err() != nil {
		return
	}
	var upstream upstreamError
	revoked := errors.As(err, &upstream) && upstream.denied()
	finishCtx, finishCancel := context.WithTimeout(ctx, 10*time.Second)
	defer finishCancel()
	if err := s.failClaim(finishCtx, job, err.Error(), revoked); err != nil && !errors.Is(err, errLostClaim) {
		slog.Error("record directory synchronization failure", "run_id", job.id, "error", err)
	}
}

func (s *Service) lockClaim(ctx context.Context, tx pgx.Tx, job claim) error {
	if _, _, err := s.sourceLock(ctx, tx); err != nil {
		return err
	}
	var live bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM directory_sync_runs
		WHERE tenant_key=$1 AND app_id=$2 AND id=$3 AND claim_version=$4 AND state='running'
			AND lease_until > clock_timestamp())`, s.cfg.TenantKey, s.cfg.AppID, job.id, job.version).Scan(&live)
	if err == nil && !live {
		return errLostClaim
	}
	return err
}

func (s *Service) recordProgress(ctx context.Context, job claim, authorization scope, depts, people int) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.lockClaim(ctx, tx, job); err != nil {
		return err
	}
	// A newly observed scope can remove data anywhere in the graph. Until a
	// complete replacement is verified, no data from the old scope is served.
	_, err = tx.Exec(ctx, `UPDATE directory_sources SET visible=false WHERE tenant_key=$1 AND app_id=$2
		AND visible AND active_scope_hash IS DISTINCT FROM $3`, s.cfg.TenantKey, s.cfg.AppID, authorization.hash())
	if err != nil {
		return err
	}
	scopeJSON, _ := json.Marshal(authorization.summary())
	_, err = tx.Exec(ctx, `UPDATE directory_sync_runs SET scope=$2,
		department_count=$3,people_count=$4 WHERE id=$1`, job.id, scopeJSON, depts, people)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) finishFailed(ctx context.Context, tx pgx.Tx, id int64, reason string) error {
	_, err := tx.Exec(ctx, `UPDATE directory_sync_runs SET state='failed', finished_at=clock_timestamp(),
		lease_until=NULL, failure_reason=$2 WHERE id=$1`, id, reason)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE directory_sources SET failures=LEAST(failures+1,10),
		next_run_at=clock_timestamp() + LEAST($3::double precision, 15 * (1 << LEAST(failures,8))) * interval '1 second'
		WHERE tenant_key=$1 AND app_id=$2`, s.cfg.TenantKey, s.cfg.AppID, s.cfg.Interval.Seconds())
	return err
}

func (s *Service) failClaim(ctx context.Context, job claim, reason string, revoked bool) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.lockClaim(ctx, tx, job); err != nil {
		return err
	}
	if revoked {
		_, err = tx.Exec(ctx, `UPDATE directory_sources SET visible=false WHERE tenant_key=$1 AND app_id=$2`,
			s.cfg.TenantKey, s.cfg.AppID)
		if err != nil {
			return err
		}
	}
	if err := s.finishFailed(ctx, tx, job.id, reason); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) publish(ctx context.Context, job claim, result snapshot) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.lockClaim(ctx, tx, job); err != nil {
		return err
	}
	tenant, app := s.cfg.TenantKey, s.cfg.AppID
	batch := &pgx.Batch{}
	for _, entry := range result.Departments {
		var parent *string
		if entry.ParentID != "" {
			parent = &entry.ParentID
		}
		batch.Queue(`INSERT INTO departments (tenant_key,app_id,snapshot_version,id,parent_id,name,path,has_children)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, tenant, app, job.id, entry.ID, parent, entry.Name, entry.Path, entry.HasChildren)
	}
	for _, entry := range result.People {
		batch.Queue(`INSERT INTO directory_people (tenant_key,app_id,snapshot_version,open_id,name,avatar_url,directory_status)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`, tenant, app, job.id, entry.ID, entry.Name, entry.avatarURL(), entry.status())
		for department := range result.Members[entry.ID] {
			batch.Queue(`INSERT INTO department_memberships (tenant_key,app_id,snapshot_version,department_id,open_id)
				VALUES ($1,$2,$3,$4,$5)`, tenant, app, job.id, department, entry.ID)
		}
	}
	if batch.Len() > 0 {
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return fmt.Errorf("stage directory snapshot: %w", err)
		}
	}
	// Recheck after staging in case a slow transaction consumed the lease.
	if err := s.lockClaim(ctx, tx, job); err != nil {
		return err
	}
	scopeJSON, _ := json.Marshal(result.Scope.summary())
	_, err = tx.Exec(ctx, `WITH people AS (
		SELECT p.open_id,p.name,p.avatar_url,p.directory_status,p.snapshot_version=$3 AS incoming,
			(SELECT jsonb_agg(jsonb_build_array(m.department_id,d.path) ORDER BY m.department_id)
				FROM department_memberships m JOIN departments d
					ON (d.tenant_key,d.app_id,d.snapshot_version,d.id)
						=(m.tenant_key,m.app_id,m.snapshot_version,m.department_id)
				WHERE (m.tenant_key,m.app_id,m.snapshot_version,m.open_id)
					=(p.tenant_key,p.app_id,p.snapshot_version,p.open_id)) AS memberships
		FROM directory_people p JOIN directory_sources src
			ON (src.tenant_key,src.app_id)=(p.tenant_key,p.app_id)
		WHERE p.tenant_key=$1 AND p.app_id=$2 AND p.snapshot_version IN (src.active_version,$3)
	), before AS (
		SELECT * FROM people WHERE NOT incoming
	), after AS (
		SELECT * FROM people WHERE incoming
	), delta AS (
		SELECT count(*) FILTER (WHERE after.open_id IS NULL) AS missing,
			count(*) FILTER (WHERE before.open_id IS NULL) AS added,
			count(*) FILTER (WHERE before.open_id IS NOT NULL AND after.open_id IS NOT NULL AND
				(before.name,before.avatar_url,before.directory_status,before.memberships) IS DISTINCT FROM
				(after.name,after.avatar_url,after.directory_status,after.memberships)) AS changed
		FROM before FULL JOIN after USING(open_id)
	)
	UPDATE directory_sync_runs SET state='succeeded',finished_at=clock_timestamp(),lease_until=NULL,
		department_count=$4,people_count=$5,scope=$6,missing_people_count=CASE WHEN (SELECT active_scope_hash FROM directory_sources WHERE tenant_key=$1 AND app_id=$2)=$7 THEN delta.missing ELSE 0 END,
		outside_scope_count=CASE WHEN (SELECT active_scope_hash FROM directory_sources WHERE tenant_key=$1 AND app_id=$2) IS DISTINCT FROM $7 THEN delta.missing ELSE 0 END,
		new_people_count=delta.added,
		changed_people_count=delta.changed FROM delta WHERE id=$3`, tenant, app, job.id, len(result.Departments), len(result.People), scopeJSON, result.Scope.hash())
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE directory_sources SET active_version=$3,active_scope_hash=$4,visible=true,
		last_success_at=clock_timestamp(),failures=0,next_run_at=clock_timestamp()+($5::double precision * interval '1 second')
		WHERE tenant_key=$1 AND app_id=$2`, tenant, app, job.id, result.Scope.hash(), s.cfg.Interval.Seconds())
	if err != nil {
		return err
	}
	for _, table := range []string{"directory_people", "departments"} {
		_, err = tx.Exec(ctx, `DELETE FROM `+table+` WHERE tenant_key=$1 AND app_id=$2 AND snapshot_version<>$3`, tenant, app, job.id)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
