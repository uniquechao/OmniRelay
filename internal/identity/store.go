package identity

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Service) storeLogin(ctx context.Context, state, browser, verifier, returnPath string) error {
	if _, err := s.pool.Exec(ctx, `
DELETE FROM oauth_login_transactions
WHERE state_digest IN (
    SELECT state_digest FROM oauth_login_transactions
    WHERE expires_at <= CURRENT_TIMESTAMP
    ORDER BY expires_at LIMIT 256 FOR UPDATE SKIP LOCKED
)
`); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `
INSERT INTO oauth_login_transactions (state_digest, browser_digest, tenant_key, app_id, redirect_uri, pkce_verifier, return_path, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, CURRENT_TIMESTAMP + make_interval(secs => $8))
`, digest(state), digest(browser), s.cfg.TenantKey, s.cfg.AppID, s.callback, verifier, returnPath, loginTTL.Seconds())
	return err
}

func (s *Service) consumeLogin(ctx context.Context, state, browser string) (string, string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback(ctx)
	var verifier, returnPath string
	err = tx.QueryRow(ctx, `
DELETE FROM oauth_login_transactions
WHERE state_digest = $1 AND browser_digest = $2 AND tenant_key = $3 AND app_id = $4
  AND redirect_uri = $5 AND expires_at > clock_timestamp()
RETURNING pkce_verifier, return_path
`, digest(state), digest(browser), s.cfg.TenantKey, s.cfg.AppID, s.callback).Scan(&verifier, &returnPath)
	if err != nil {
		return "", "", err
	}
	// A rejected completion rolls back the delete so the browser can retry.
	if !s.oauth.allowCompletion(time.Now()) {
		return "", "", errOAuthRateLimited
	}
	return verifier, returnPath, tx.Commit(ctx)
}

// ResolveFeishuAccount resolves a verified Feishu identity to a preprovisioned,
// active account and locks it against deactivation until the transaction ends.
func ResolveFeishuAccount(ctx context.Context, tx pgx.Tx, tenantKey, appID, openID string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
SELECT id FROM accounts WHERE tenant_key = $1 AND app_id = $2 AND open_id = $3 AND active
FOR NO KEY UPDATE
`, tenantKey, appID, openID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotAdmitted
	}
	return id, err
}

func (s *Service) createSession(ctx context.Context, p *Principal, token, previousToken string) error {
	// Clear expired rows before taking the account lock shared with deactivation.
	if _, err := s.pool.Exec(ctx, `
DELETE FROM web_sessions
WHERE token_digest IN (
    SELECT token_digest FROM web_sessions
    WHERE idle_expires_at <= CURRENT_TIMESTAMP
    ORDER BY idle_expires_at LIMIT 256 FOR UPDATE SKIP LOCKED
)
`); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	p.AccountID, err = ResolveFeishuAccount(ctx, tx, p.TenantKey, p.AppID, p.OpenID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
UPDATE accounts SET name = $2, avatar_url = $3, updated_at = CURRENT_TIMESTAMP WHERE id = $1
`, p.AccountID, p.Name, p.AvatarURL); err != nil {
		return err
	}
	if previousToken != "" {
		if _, err := tx.Exec(ctx, "DELETE FROM web_sessions WHERE token_digest = $1", digest(previousToken)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO web_sessions (token_digest, account_id, expires_at, idle_expires_at)
VALUES ($1, $2, CURRENT_TIMESTAMP + make_interval(secs => $3), CURRENT_TIMESTAMP + make_interval(secs => $4))
`, digest(token), p.AccountID, s.cfg.SessionTTL.Seconds(), s.cfg.IdleTTL.Seconds()); err != nil {
		return err
	}
	if s.cfg.Audit != nil {
		if err := s.cfg.Audit(ctx, tx, *p, "identity.login"); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Service) loadSession(ctx context.Context, token string, refresh bool) (Principal, error) {
	const fields = `account.id, account.tenant_key, account.app_id, account.open_id, account.name, account.avatar_url, session.auth_method,
		EXISTS (SELECT 1 FROM account_passwords WHERE account_id=account.id)`
	const conditions = `session.token_digest=$1 AND account.id=session.account_id AND account.active
		AND account.tenant_key=$2 AND account.app_id=$3
		AND session.expires_at>clock_timestamp() AND session.idle_expires_at>clock_timestamp()`
	args := []any{digest(token), s.cfg.TenantKey, s.cfg.AppID}
	query := `SELECT ` + fields + ` FROM web_sessions AS session, accounts AS account WHERE ` + conditions
	if refresh {
		query = `UPDATE web_sessions AS session
			SET idle_expires_at=LEAST(session.expires_at, clock_timestamp()+make_interval(secs => $4))
			FROM accounts AS account WHERE ` + conditions + ` RETURNING ` + fields
		args = append(args, s.cfg.IdleTTL.Seconds())
	}
	var p Principal
	err := s.pool.QueryRow(ctx, query, args...).Scan(
		&p.AccountID, &p.TenantKey, &p.AppID, &p.OpenID, &p.Name, &p.AvatarURL, &p.AuthMethod, &p.CanChangePassword)
	return p, err
}

func (s *Service) revokeSession(ctx context.Context, p Principal, token string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	res, err := tx.Exec(ctx, "DELETE FROM web_sessions WHERE token_digest = $1 AND account_id = $2", digest(token), p.AccountID)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 1 && s.cfg.Audit != nil {
		if err := s.cfg.Audit(ctx, tx, p, "identity.logout"); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
