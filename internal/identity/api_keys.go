package identity

import (
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const keyPath = "/api/v1/me/api-keys"
const keyPrefix = "omni_pat_"

type keyInfo struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *Service) apiCredentials(r *http.Request, policy SessionPolicy) (Principal, string, int, string) {
	headers := r.Header.Values("Authorization")
	if len(headers) == 0 {
		return s.authenticateSession(r, policy)
	}
	invalid := func() (Principal, string, int, string) {
		return Principal{}, "", http.StatusUnauthorized, "invalid_api_key"
	}
	if r.TLS == nil || len(headers) != 1 || !strings.HasPrefix(r.URL.Path, "/api/v1/") {
		return invalid()
	}
	if _, err := r.Cookie(s.cookieName(sessionCookie)); err == nil {
		return invalid()
	}
	scheme, key, ok := strings.Cut(headers[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || !strings.HasPrefix(key, keyPrefix) || !isSecret(key[len(keyPrefix):]) {
		return invalid()
	}
	var p Principal
	err := s.pool.QueryRow(r.Context(), `SELECT a.id,a.tenant_key,a.app_id,a.open_id,a.name,a.avatar_url
		FROM account_api_keys k JOIN accounts a ON a.id=k.account_id
		WHERE k.secret_digest=$1 AND k.realm=$2 AND k.expires_at>clock_timestamp()
		AND a.active AND a.tenant_key=$3 AND a.app_id=$4`, digest(key), s.cookieName(sessionCookie), s.cfg.TenantKey, s.cfg.AppID).
		Scan(&p.AccountID, &p.TenantKey, &p.AppID, &p.OpenID, &p.Name, &p.AvatarURL)
	if errors.Is(err, pgx.ErrNoRows) {
		return invalid()
	}
	if err != nil {
		return Principal{}, "", http.StatusServiceUnavailable, "authentication_unavailable"
	}
	p.AuthMethod = "api_key"
	return p, "", 0, ""
}

func (s *Service) authenticateAPI(w http.ResponseWriter, r *http.Request, policy SessionPolicy) (Principal, string, bool) {
	identityHeaders(w)
	p, token, status, code := s.apiCredentials(r, policy)
	if status != 0 {
		writeFailure(w, status, code)
		return Principal{}, "", false
	}
	return p, token, true
}

var errKeyLimit = errors.New("active API key limit reached")

// Only an interactive session can issue or revoke personal credentials.
func (s *Service) manageKeys(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.authenticate(w, r, ValidateSession)
	if !ok {
		return
	}
	realm := s.cookieName(sessionCookie)
	ctx := r.Context()
	if r.Method == http.MethodGet {
		rows, err := s.pool.Query(ctx, `SELECT id::text,label,expires_at FROM account_api_keys
			WHERE account_id=$1 AND realm=$2 AND expires_at>now() ORDER BY expires_at DESC,id`, p.AccountID, realm)
		if err == nil {
			defer rows.Close()
			keys, readErr := pgx.CollectRows(rows, pgx.RowToStructByPos[keyInfo])
			if readErr == nil {
				writeJSON(w, http.StatusOK, map[string]any{"items": keys})
				return
			}
		}
		writeFailure(w, 503, "api_keys_unavailable")
		return
	}
	var input struct {
		Label string `json:"label"`
	}
	if r.Method == http.MethodPost {
		if !readCredentialJSON(w, r, &input) {
			writeFailure(w, 400, "invalid_key_label")
			return
		}
		input.Label = strings.TrimSpace(input.Label)
		if input.Label == "" || utf8.RuneCountInString(input.Label) > 80 || strings.IndexFunc(input.Label, unicode.IsControl) >= 0 {
			writeFailure(w, 400, "invalid_key_label")
			return
		}
	}
	var issued string
	var meta keyInfo
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Serialize issuance with account deactivation and concurrent key creation.
		if _, err := ResolveFeishuAccount(ctx, tx, p.TenantKey, p.AppID, p.OpenID); err != nil {
			return err
		}
		action := "identity.api_key_revoke"
		if r.Method == http.MethodDelete {
			res, err := tx.Exec(ctx, `DELETE FROM account_api_keys WHERE account_id=$1 AND realm=$2 AND id::text=$3`, p.AccountID, realm, r.PathValue("id"))
			if err != nil {
				return err
			}
			if res.RowsAffected() == 0 {
				return pgx.ErrNoRows
			}
		} else {
			if _, err := tx.Exec(ctx, `DELETE FROM account_api_keys WHERE account_id=$1 AND realm=$2 AND expires_at<=now()`, p.AccountID, realm); err != nil {
				return err
			}
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM account_api_keys WHERE account_id=$1 AND realm=$2`, p.AccountID, realm).Scan(&count); err != nil {
				return err
			}
			if count >= 10 {
				return errKeyLimit
			}
			issued = keyPrefix + newSecret()
			err := tx.QueryRow(ctx, `INSERT INTO account_api_keys (account_id,realm,label,secret_digest) VALUES ($1,$2,$3,$4)
				RETURNING id::text,label,expires_at`, p.AccountID, realm, input.Label, digest(issued)).Scan(&meta.ID, &meta.Label, &meta.ExpiresAt)
			if err != nil {
				return err
			}
			action = "identity.api_key_create"
		}
		if s.cfg.Audit != nil {
			return s.cfg.Audit(ctx, tx, p, action)
		}
		return nil
	})
	switch {
	case errors.Is(err, ErrNotAdmitted):
		writeFailure(w, 401, "authentication_required")
	case errors.Is(err, pgx.ErrNoRows):
		writeFailure(w, 404, "api_key_not_found")
	case errors.Is(err, errKeyLimit):
		writeFailure(w, 409, "api_key_limit")
	case err != nil:
		writeFailure(w, 503, "api_keys_unavailable")
	case issued != "":
		writeJSON(w, 201, struct {
			keyInfo
			Key string `json:"key"`
		}{meta, issued})
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
