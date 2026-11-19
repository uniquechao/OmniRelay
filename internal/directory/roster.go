package directory

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// Verify historical absences only after all directory pages have been read,
// while the collector still has its original token and request budget.
func (s *Service) verifyDepartures(ctx context.Context, f *collector, result *snapshot) error {
	entireOrg := result.Scope.summary().EntireOrg
	rows, err := s.db.Query(ctx, `SELECT r.open_id FROM directory_roster r
		JOIN directory_sources src USING(tenant_key,app_id)
		WHERE r.tenant_key=$1 AND r.app_id=$2 AND src.active_scope_hash=$3
			AND r.resigned_at IS NULL AND ($4 OR r.last_scope_hash=$3)
		ORDER BY r.open_id`, s.cfg.TenantKey, s.cfg.AppID, result.Scope.hash(), entireOrg)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, present := result.People[id]; present {
			continue
		}
		if result.MissingExplicit[id] {
			result.Departures = append(result.Departures, id)
			continue
		}
		if !validOpenID(id) {
			continue
		}
		person, err := f.readPerson(ctx, id)
		var upstream upstreamError
		switch {
		case userDoesNotExist(err), err == nil && entireOrg && person.Status.Resigned:
			result.Departures = append(result.Departures, id)
		case err == nil:
		case errors.As(err, &upstream) && upstream.code == 41050 &&
			(upstream.status == http.StatusOK || upstream.status == http.StatusBadRequest || upstream.status == http.StatusForbidden) &&
			!entireOrg:
			// A person may have moved outside the app's current department scope.
		default:
			return err
		}
	}
	return nil
}

func (s *Service) updateRoster(ctx context.Context, tx pgx.Tx, runID int64, result snapshot) error {
	tenant, app, hash := s.cfg.TenantKey, s.cfg.AppID, result.Scope.hash()
	_, err := tx.Exec(ctx, `INSERT INTO directory_roster (tenant_key,app_id,open_id,last_scope_hash)
		SELECT tenant_key,app_id,open_id,$4 FROM directory_people
		WHERE tenant_key=$1 AND app_id=$2 AND snapshot_version=$3 AND directory_status<>'resigned'
		ON CONFLICT (tenant_key,app_id,open_id) DO UPDATE SET
			last_scope_hash=EXCLUDED.last_scope_hash,resigned_at=NULL,resigned_run_id=NULL
		WHERE (directory_roster.last_scope_hash,directory_roster.resigned_at)
			IS DISTINCT FROM (EXCLUDED.last_scope_hash,NULL::timestamptz)`, tenant, app, runID, hash)
	if err != nil || len(result.Departures) == 0 {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE directory_roster r SET resigned_at=statement_timestamp(),resigned_run_id=$3
		FROM directory_sources src
		WHERE (r.tenant_key,r.app_id)=(src.tenant_key,src.app_id)
			AND r.tenant_key=$1 AND r.app_id=$2 AND src.active_scope_hash=$4
			AND r.open_id=ANY($5::text[]) AND r.resigned_at IS NULL`, tenant, app, runID, hash, result.Departures)
	return err
}
