package access

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

// ReclaimDepartedAccounts runs inside the directory publication's organization
// mutation, after the source and roster changes have been staged.
func (s *Service) ReclaimDepartedAccounts(ctx context.Context, tx pgx.Tx, runID int64) error {
	rows, err := tx.Query(ctx, `SELECT r.open_id, r.resigned_run_id, coalesce(a.id,''), coalesce(a.active,false)
		FROM directory_roster r LEFT JOIN accounts a USING(tenant_key,app_id,open_id)
		WHERE r.tenant_key=$1 AND r.app_id=$2 AND (r.resigned_run_id=$3 OR (r.resigned_at IS NOT NULL AND a.active))
		ORDER BY r.open_id`, s.tenantKey, s.appID, runID)
	if err != nil {
		return err
	}
	type departure struct {
		openID, accountID string
		runID             int64
		active            bool
	}
	departures, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (departure, error) {
		var d departure
		err := row.Scan(&d.openID, &d.runID, &d.accountID, &d.active)
		return d, err
	})
	if err != nil {
		return err
	}
	for _, d := range departures {
		if d.runID == runID {
			if err := s.auditDirectory(ctx, tx, runID, "directory.resign", "directory_person", identity.FeishuPersonKey(d.openID), 0); err != nil {
				return err
			}
		}
		if !d.active {
			continue
		}
		var currentVersion int64
		err := tx.QueryRow(ctx, `SELECT permission_version FROM accounts
			WHERE tenant_key=$1 AND app_id=$2 AND id=$3 AND active FOR NO KEY UPDATE`,
			s.tenantKey, s.appID, d.accountID).Scan(&currentVersion)
		if err != nil {
			return err
		}
		_, version, err := s.changeAccountActive(ctx, tx, d.accountID, false)
		if errors.Is(err, errLastAdmin) {
			if d.runID == runID {
				err = s.auditDirectory(ctx, tx, runID, "account.disable_protected", "account", d.accountID, currentVersion)
				if err != nil {
					return err
				}
			}
			continue
		}
		if err != nil {
			return err
		}
		if err := s.auditDirectory(ctx, tx, runID, "account.disable", "account", d.accountID, version); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) auditDirectory(ctx context.Context, tx pgx.Tx, runID int64, action, resourceType, resourceID string, version int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit_events
		(tenant_key,app_id,directory_sync_run_id,action,resource_type,resource_id,result,change_version)
		VALUES ($1,$2,$3,$4,$5,$6,CASE WHEN $4='account.disable_protected' THEN 'protected' ELSE 'success' END,$7)`,
		s.tenantKey, s.appID, runID, action, resourceType, resourceID, version)
	return err
}
