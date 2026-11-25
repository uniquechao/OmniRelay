package access

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

// CreateLocalAdmin is a create-only operation available to the deployment CLI.
// The database enforces a single local account per installation scope.
func (s *Service) CreateLocalAdmin(ctx context.Context, password string) (string, error) {
	var id string
	err := s.WithOrganizationMutation(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO accounts(tenant_key,app_id,name)
			VALUES ($1,$2,'admin') RETURNING id`, s.tenantKey, s.appID).Scan(&id); err != nil {
			return err
		}
		if err := identity.SetAccountPassword(ctx, tx, s.tenantKey, s.appID, id, "admin", password); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_grants(tenant_key,app_id,account_id,role,source)
			VALUES ($1,$2,$3,'system_admin','bootstrap')`, s.tenantKey, s.appID, id); err != nil {
			return err
		}
		version, err := incrementVersion(ctx, tx, id)
		if err != nil {
			return err
		}
		return s.audit(ctx, tx, id, "account.local_admin_create", "account", id, version)
	})
	if err != nil {
		return "", err
	}
	return id, nil
}
