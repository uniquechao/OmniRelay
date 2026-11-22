package identity

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/argon2"
)

var (
	passwordUsername = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{2,63}$`)
	passwordSlots    = make(chan struct{}, 4)
	dummySalt        = newPasswordSalt()
)

func (s *Service) RegisterPasswords(mux *http.ServeMux) {
	mux.HandleFunc("POST /auth/login", requireTLS(s.passwordLogin))
	mux.HandleFunc("POST /auth/password", requireTLS(s.changePassword))
}

func requireTLS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			identityHeaders(w)
			writeFailure(w, http.StatusForbidden, "https_required")
			return
		}
		next(w, r)
	}
}

func newPasswordSalt() []byte {
	salt := make([]byte, 16)
	rand.Read(salt)
	return salt
}

func passwordKey(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, 2, 19*1024, 1, 32)
}

func newPasswordAcceptable(password string) bool {
	return len(password) <= 256 && utf8.ValidString(password) && utf8.RuneCountInString(password) >= 15
}

// ProvisionPassword binds credentials to an existing account in the configured organization.
// Resetting an existing credential invalidates all sessions for the account.
func ProvisionPassword(ctx context.Context, pool *pgxpool.Pool, tenant, app, accountID, username, password string) error {
	username = strings.ToLower(strings.TrimSpace(username))
	if !passwordUsername.MatchString(username) || !newPasswordAcceptable(password) {
		return errors.New("username must be 3–64 characters and password must be 15 characters or more, up to 256 bytes")
	}
	salt := newPasswordSalt()
	key := passwordKey(password, salt)
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `SELECT id FROM accounts WHERE id=$1 AND tenant_key=$2 AND app_id=$3 AND active FOR UPDATE`,
			accountID, tenant, app).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO account_passwords (account_id,tenant_key,app_id,username,salt,password_hash)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (account_id) DO UPDATE SET username=EXCLUDED.username,salt=EXCLUDED.salt,password_hash=EXCLUDED.password_hash`,
			id, tenant, app, username, salt, key); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM web_sessions WHERE account_id=$1`, id)
		return err
	})
}

func readCredentialJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	return decoder.Decode(out) == nil && decoder.Decode(new(any)) == io.EOF
}

func (s *Service) passwordLimit(ctx context.Context, bucket string, limit int) error {
	// Cleanup is bounded so a burst of expired attempts cannot block authentication.
	if _, err := s.pool.Exec(ctx, `DELETE FROM password_attempts
		WHERE (tenant_key,app_id,bucket) IN (
			SELECT tenant_key,app_id,bucket FROM password_attempts WHERE expires_at <= clock_timestamp()
			ORDER BY expires_at LIMIT 32 FOR UPDATE SKIP LOCKED
		)`); err != nil {
		return err
	}
	var count int
	err := s.pool.QueryRow(ctx, `INSERT INTO password_attempts (tenant_key,app_id,bucket,attempts,expires_at)
		VALUES ($1,$2,$3,1,clock_timestamp() + interval '5 minutes')
		ON CONFLICT (tenant_key,app_id,bucket) DO UPDATE SET
			attempts=CASE WHEN password_attempts.expires_at <= clock_timestamp() THEN 1 ELSE LEAST(password_attempts.attempts+1,$4+1) END,
			expires_at=CASE WHEN password_attempts.expires_at <= clock_timestamp() THEN EXCLUDED.expires_at ELSE password_attempts.expires_at END
		RETURNING attempts`, s.cfg.TenantKey, s.cfg.AppID, bucket, limit).Scan(&count)
	if err == nil && count > limit {
		return errPasswordLimited
	}
	return err
}

var errPasswordLimited = errors.New("too many password attempts")

func (s *Service) allowPassword(w http.ResponseWriter, r *http.Request, bucket string, limit int) bool {
	err := s.passwordLimit(r.Context(), bucket, limit)
	switch {
	case errors.Is(err, errPasswordLimited):
		w.Header().Set("Retry-After", "300")
		writeFailure(w, http.StatusTooManyRequests, "password_attempts_limited")
	case err != nil:
		writeFailure(w, http.StatusServiceUnavailable, "login_unavailable")
	default:
		return true
	}
	return false
}

func acquirePasswordSlot(w http.ResponseWriter) bool {
	select {
	case passwordSlots <- struct{}{}:
		return true
	default:
		w.Header().Set("Retry-After", "2")
		writeFailure(w, http.StatusTooManyRequests, "password_attempts_limited")
		return false
	}
}

func (s *Service) passwordLogin(w http.ResponseWriter, r *http.Request) {
	identityHeaders(w)
	origins := r.Header.Values("Origin")
	if len(origins) != 1 || origins[0] != s.cfg.PublicOrigin {
		writeFailure(w, http.StatusForbidden, "invalid_origin")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readCredentialJSON(w, r, &input) || len(input.Password) > 256 || input.Password == "" {
		writeFailure(w, http.StatusBadRequest, "invalid_request")
		return
	}
	username := strings.ToLower(strings.TrimSpace(input.Username))
	if !passwordUsername.MatchString(username) {
		writeFailure(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	// Only consider the address of the actual TCP peer, never client-supplied forwarding headers.
	peer, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !s.allowPassword(w, r, "peer:"+peer, 120) || !s.allowPassword(w, r, "login:"+username, 12) ||
		!acquirePasswordSlot(w) {
		return
	}
	defer func() { <-passwordSlots }()
	var p Principal
	var salt, hash []byte
	err := s.pool.QueryRow(r.Context(), `SELECT a.id,a.tenant_key,a.app_id,a.open_id,a.name,a.avatar_url,c.salt,c.password_hash
		FROM account_passwords c JOIN accounts a ON a.id=c.account_id
		WHERE a.tenant_key=$1 AND a.app_id=$2 AND a.active AND c.tenant_key=$1 AND c.app_id=$2 AND c.username=$3`,
		s.cfg.TenantKey, s.cfg.AppID, username).Scan(&p.AccountID, &p.TenantKey, &p.AppID, &p.OpenID, &p.Name, &p.AvatarURL, &salt, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		salt, hash = dummySalt, make([]byte, 32)
	} else if err != nil {
		writeFailure(w, http.StatusServiceUnavailable, "login_unavailable")
		return
	}
	if subtle.ConstantTimeCompare(passwordKey(input.Password, salt), hash) != 1 || p.AccountID == "" {
		writeFailure(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	s.rotatePasswordSession(w, r, p, hash, nil, nil, username)
}

func (s *Service) changePassword(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.authenticate(w, r, RefreshSessionWithCSRF)
	if !ok {
		return
	}
	var input struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !readCredentialJSON(w, r, &input) || !newPasswordAcceptable(input.NewPassword) ||
		len(input.CurrentPassword) > 256 || input.NewPassword == input.CurrentPassword {
		writeFailure(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if !s.allowPassword(w, r, "change:"+p.AccountID, 10) || !acquirePasswordSlot(w) {
		return
	}
	defer func() { <-passwordSlots }()
	var salt, hash []byte
	err := s.pool.QueryRow(r.Context(), `SELECT salt,password_hash FROM account_passwords WHERE account_id=$1`, p.AccountID).Scan(&salt, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		writeFailure(w, http.StatusForbidden, "password_unavailable")
		return
	}
	if err != nil {
		writeFailure(w, http.StatusServiceUnavailable, "password_unavailable")
		return
	}
	if subtle.ConstantTimeCompare(passwordKey(input.CurrentPassword, salt), hash) != 1 {
		writeFailure(w, http.StatusBadRequest, "invalid_credentials")
		return
	}
	freshSalt := newPasswordSalt()
	s.rotatePasswordSession(w, r, p, hash, freshSalt, passwordKey(input.NewPassword, freshSalt), "")
}

func (s *Service) rotatePasswordSession(w http.ResponseWriter, r *http.Request, p Principal, verified, salt, nextHash []byte, username string) {
	token, previous := newSecret(), cookieSecret(r, s.cookieName(sessionCookie))
	change := nextHash != nil
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var id string
		ctx := r.Context()
		err := tx.QueryRow(ctx, `SELECT id FROM accounts WHERE id=$1 AND tenant_key=$2 AND app_id=$3 AND active FOR UPDATE`,
			p.AccountID, p.TenantKey, p.AppID).Scan(&id)
		if err != nil {
			return err
		}
		var currentHash []byte
		var handle string
		if err := tx.QueryRow(ctx, `SELECT password_hash,username FROM account_passwords WHERE account_id=$1`, id).Scan(&currentHash, &handle); err != nil {
			return err
		}
		if subtle.ConstantTimeCompare(verified, currentHash) != 1 || (!change && handle != username) {
			return pgx.ErrNoRows
		}
		action := "identity.login"
		if change {
			var activeSession bool
			err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM web_sessions WHERE token_digest=$1 AND account_id=$2 AND idle_expires_at>clock_timestamp())`,
				s.sessionDigest(previous), id).Scan(&activeSession)
			if err != nil {
				return err
			}
			if !activeSession {
				return pgx.ErrNoRows
			}
			if _, err := tx.Exec(ctx, `UPDATE account_passwords SET salt=$2,password_hash=$3 WHERE account_id=$1`, id, salt, nextHash); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM web_sessions WHERE account_id=$1`, id); err != nil {
				return err
			}
			action = "identity.password_change"
		} else if previous != "" {
			if _, err := tx.Exec(ctx, `DELETE FROM web_sessions WHERE token_digest=$1`, s.sessionDigest(previous)); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO web_sessions (token_digest, account_id, expires_at, idle_expires_at, auth_method)
			VALUES ($1,$2,CURRENT_TIMESTAMP+make_interval(secs=>$3),CURRENT_TIMESTAMP+make_interval(secs=>$4),'password')`,
			s.sessionDigest(token), id, s.cfg.SessionTTL.Seconds(), s.cfg.IdleTTL.Seconds()); err != nil {
			return err
		}
		if s.cfg.Audit != nil {
			return s.cfg.Audit(ctx, tx, p, action)
		}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeFailure(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	if err != nil {
		writeFailure(w, http.StatusServiceUnavailable, "login_unavailable")
		return
	}
	setCookie(w, s.cookieName(sessionCookie), token, int(s.cfg.SessionTTL.Seconds()))
	w.WriteHeader(http.StatusNoContent)
}
