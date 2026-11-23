package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/migrations"
	"github.com/uniquechao/OmniRelay/internal/webui"
)

const (
	setupCredentialScope = "OmniRelay/Feishu setup credentials/v1"
	setupLoginScope      = "OmniRelay/Feishu setup OAuth request/v1"
	setupBrowserCookie   = "__Host-omnirelay_setup"
	setupLoginTTL        = 5 * time.Minute
)

type setupCredentials struct {
	AppID     string `json:"app_id"`
	AppSecret string `json:"app_secret"`
	TenantKey string `json:"tenant_key"`
	Bootstrap string `json:"admin_open_id"`
}

type setupRequest struct {
	SetupCode string `json:"setup_code"`
	AppID     string `json:"app_id"`
	AppSecret string `json:"app_secret"`
}

type setupLogin struct {
	AppID     string
	AppSecret string
	Verifier  string
}

var setupIdentifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

func validSetupApp(appID, secret string) bool {
	return strings.HasPrefix(appID, "cli_") && len(appID) > len("cli_") && setupIdentifier.MatchString(appID) &&
		secret != "" && len(secret) <= 512 && strings.TrimSpace(secret) == secret &&
		strings.IndexFunc(secret, unicode.IsControl) == -1
}

func (c setupCredentials) valid() bool {
	return validSetupApp(c.AppID, c.AppSecret) && validSetupIdentity(c.TenantKey) && validSetupIdentity(c.Bootstrap)
}

func validSetupIdentity(value string) bool {
	return value != "" && len(value) <= 256 && strings.TrimSpace(value) == value &&
		strings.IndexFunc(value, unicode.IsControl) == -1
}

func setupMAC(key []byte, purpose string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = io.WriteString(mac, purpose)
	return mac.Sum(nil)
}

func newSetupNonce() string {
	var entropy [32]byte
	_, _ = rand.Read(entropy[:])
	return base64.RawURLEncoding.EncodeToString(entropy[:])
}

func setupToken(key []byte) string {
	return base64.RawURLEncoding.EncodeToString(setupMAC(key, "OmniRelay/browser setup code/v1"))
}

type webSetupStore struct {
	pool *pgxpool.Pool
	key  []byte
}

func newWebSetupStore(pool *pgxpool.Pool, key []byte) webSetupStore {
	return webSetupStore{pool: pool, key: key}
}

func (s webSetupStore) aead(scope string) (cipher.AEAD, error) {
	block, err := aes.NewCipher(setupMAC(s.key, scope))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
}

func (s webSetupStore) configured(ctx context.Context) (bool, error) {
	var found bool
	err := s.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM omnirelay_web_setup)").Scan(&found)
	return found, err
}

func (s webSetupStore) load(ctx context.Context) (setupCredentials, bool, error) {
	var encrypted []byte
	err := s.pool.QueryRow(ctx, "SELECT credentials FROM omnirelay_web_setup WHERE singleton").Scan(&encrypted)
	if errors.Is(err, pgx.ErrNoRows) {
		return setupCredentials{}, false, nil
	}
	if err != nil {
		return setupCredentials{}, false, err
	}
	var credentials setupCredentials
	if s.open(setupCredentialScope, encrypted, &credentials) != nil || !credentials.valid() {
		return setupCredentials{}, false, errors.New("cannot decrypt saved web setup configuration")
	}
	return credentials, true, nil
}

func (s webSetupStore) seal(scope string, value any) ([]byte, error) {
	aead, err := s.aead(scope)
	if err != nil {
		return nil, err
	}
	plain, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	return aead.Seal(nil, nil, plain, []byte(scope)), nil
}

func (s webSetupStore) open(scope string, encrypted []byte, destination any) error {
	aead, err := s.aead(scope)
	if err != nil {
		return err
	}
	plain, err := aead.Open(nil, nil, encrypted, []byte(scope))
	if err != nil {
		return err
	}
	defer clear(plain)
	return json.Unmarshal(plain, destination)
}

func (s webSetupStore) save(ctx context.Context, credentials setupCredentials) (bool, error) {
	ciphertext, err := s.seal(setupCredentialScope, credentials)
	if err != nil {
		return false, err
	}
	var saved bool
	err = s.pool.QueryRow(ctx, `WITH inserted AS (
		INSERT INTO omnirelay_web_setup (credentials) VALUES ($1) ON CONFLICT DO NOTHING RETURNING singleton
	), cleared AS (
		DELETE FROM omnirelay_web_setup_logins WHERE EXISTS (SELECT 1 FROM inserted)
	)
	SELECT EXISTS (SELECT 1 FROM inserted)`, ciphertext).Scan(&saved)
	return saved, err
}

func (s webSetupStore) startLogin(ctx context.Context, login setupLogin, state, browser string) error {
	challenge, err := s.seal(setupLoginScope, login)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "DELETE FROM omnirelay_web_setup_logins WHERE expires_at <= now()"); err != nil {
			return err
		}
		stateDigest, browserDigest := sha256.Sum256([]byte(state)), sha256.Sum256([]byte(browser))
		_, err := tx.Exec(ctx, `INSERT INTO omnirelay_web_setup_logins (state_digest,browser_digest,challenge,expires_at)
			VALUES ($1,$2,$3,now()+$4*interval '1 second')`, stateDigest[:], browserDigest[:], challenge, int(setupLoginTTL.Seconds()))
		return err
	})
}

func (s webSetupStore) consumeLogin(ctx context.Context, state, browser string) (setupLogin, error) {
	stateDigest, browserDigest := sha256.Sum256([]byte(state)), sha256.Sum256([]byte(browser))
	var challenge []byte
	err := s.pool.QueryRow(ctx, `DELETE FROM omnirelay_web_setup_logins
		WHERE state_digest=$1 AND browser_digest=$2 AND expires_at>now() RETURNING challenge`, stateDigest[:], browserDigest[:]).Scan(&challenge)
	if err != nil {
		return setupLogin{}, err
	}
	var login setupLogin
	err = s.open(setupLoginScope, challenge, &login)
	return login, err
}

func setupCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: setupBrowserCookie, Value: value, Path: "/", Secure: true,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: maxAge,
	})
}

func newWebSetupHandler(store webSetupStore, origin string,
	transport http.RoundTripper, onSaved func(),
) http.Handler {
	mux := http.NewServeMux()
	webui.RegisterSetup(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		configured, err := store.configured(ctx)
		if configured {
			onSaved()
		}
		if err != nil || configured || migrations.Check(ctx, store.pool) != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /setup", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != origin {
			http.Error(w, "setup_forbidden", http.StatusForbidden)
			return
		}
		contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || contentType != "application/json" {
			http.Error(w, "setup_invalid", http.StatusUnsupportedMediaType)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var request setupRequest
		if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "setup_invalid", http.StatusBadRequest)
			return
		}
		if !hmac.Equal([]byte(request.SetupCode), []byte(setupToken(store.key))) {
			http.Error(w, "setup_forbidden", http.StatusForbidden)
			return
		}
		if !validSetupApp(request.AppID, request.AppSecret) {
			http.Error(w, "setup_invalid", http.StatusBadRequest)
			return
		}
		configured, err := store.configured(r.Context())
		if err != nil {
			http.Error(w, "setup_unavailable", http.StatusServiceUnavailable)
			return
		}
		if configured {
			http.Error(w, "setup_complete", http.StatusConflict)
			return
		}
		provider, err := identity.NewSetupOAuth(request.AppID, request.AppSecret, origin, transport)
		if err != nil {
			http.Error(w, "setup_unavailable", http.StatusServiceUnavailable)
			return
		}
		state, browser, verifier := newSetupNonce(), newSetupNonce(), newSetupNonce()
		login := setupLogin{AppID: request.AppID, AppSecret: request.AppSecret, Verifier: verifier}
		if err := store.startLogin(r.Context(), login, state, browser); err != nil {
			http.Error(w, "setup_unavailable", http.StatusServiceUnavailable)
			return
		}
		setupCookie(w, browser, int(setupLoginTTL.Seconds()))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"authorization_url": provider.AuthorizationURL(state, verifier)})
	})
	var callbacks requestWindow
	mux.HandleFunc("GET /auth/feishu/callback", func(w http.ResponseWriter, r *http.Request) {
		if !callbacks.allow(time.Now(), 240) {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		browser, cookieErr := r.Cookie(setupBrowserCookie)
		if err != nil || cookieErr != nil || len(query["state"]) != 1 ||
			len(query.Get("state")) != 43 || len(browser.Value) != 43 {
			http.Redirect(w, r, "/setup?error=authorization", http.StatusSeeOther)
			return
		}
		login, err := store.consumeLogin(r.Context(), query.Get("state"), browser.Value)
		if err != nil {
			http.Redirect(w, r, "/setup?error=authorization", http.StatusSeeOther)
			return
		}
		setupCookie(w, "", -1)
		if len(query["code"]) != 1 || query.Get("code") == "" || len(query.Get("code")) > 2048 || query.Has("error") {
			http.Redirect(w, r, "/setup?error=cancelled", http.StatusSeeOther)
			return
		}
		provider, err := identity.NewSetupOAuth(login.AppID, login.AppSecret, origin, transport)
		if err != nil {
			http.Redirect(w, r, "/setup?error=unavailable", http.StatusSeeOther)
			return
		}
		principal, err := provider.Resolve(r.Context(), query.Get("code"), login.Verifier)
		credentials := setupCredentials{
			AppID: login.AppID, AppSecret: login.AppSecret, TenantKey: principal.TenantKey, Bootstrap: principal.OpenID,
		}
		if err != nil {
			http.Redirect(w, r, "/setup?error="+setupOAuthFailure(r.Context(), err), http.StatusSeeOther)
			return
		}
		if !credentials.valid() {
			slog.WarnContext(r.Context(), "Feishu setup authorization failed", "stage", "setup_identity_validation")
			http.Redirect(w, r, "/setup?error=identity", http.StatusSeeOther)
			return
		}
		saved, err := store.save(r.Context(), credentials)
		if err != nil {
			http.Redirect(w, r, "/setup?error=unavailable", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/?setup=complete", http.StatusSeeOther)
		if saved {
			onSaved()
		}
	})
	return secureHeaders(mux)
}

func setupOAuthFailure(ctx context.Context, err error) string {
	var failure *identity.OAuthFailure
	if !errors.As(err, &failure) {
		slog.WarnContext(ctx, "Feishu setup authorization failed", "stage", "unknown")
		return "unavailable"
	}
	slog.WarnContext(ctx, "Feishu setup authorization failed", "stage", failure.Stage,
		"http_status", failure.HTTPStatus, "provider_code", failure.ProviderCode)
	if failure.HTTPStatus == 0 || failure.HTTPStatus == http.StatusTooManyRequests || failure.HTTPStatus >= http.StatusInternalServerError {
		return "unavailable"
	}
	switch failure.ProviderCode {
	case 20002, 20048, 20130, 20140:
		return "credentials"
	case 20003, 20004, 20007, 20024:
		return "authorization"
	case 20008, 20009, 20010, 20021, 20022, 20023, 20069:
		return "denied"
	}
	switch failure.Stage {
	case "token_exchange":
		return "token_exchange"
	case "user_info":
		return "user_info"
	case "identity_validation":
		return "identity"
	default:
		return "unavailable"
	}
}
