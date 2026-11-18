package identity

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	callbackPath  = "/auth/feishu/callback"
	browserCookie = "__Host-omnirelay_oauth"
	sessionCookie = "__Host-omnirelay_session"
	loginTTL      = 5 * time.Minute
)

var errNotAdmitted = errors.New("account not admitted")

type Principal struct {
	AccountID         string `json:"account_id"`
	TenantKey         string `json:"tenant_key"`
	AppID             string `json:"app_id"`
	OpenID            string `json:"open_id"`
	Name              string `json:"name"`
	AvatarURL         string `json:"avatar_url"`
	AuthMethod        string `json:"auth_method,omitempty"`
	CanChangePassword bool   `json:"can_change_password"`
}

type WorkbenchAccess struct {
	Roles []string        `json:"roles"`
	Admin map[string]bool `json:"admin"`
}

type Config struct {
	AppID        string
	AppSecret    string
	TenantKey    string
	PublicOrigin string
	// Audit, when configured, records a successful login or logout in its transaction.
	Audit func(context.Context, pgx.Tx, Principal, string) error
	// Transport is optional. Feishu endpoints remain fixed.
	Transport  http.RoundTripper
	Access     func(context.Context, Principal) (WorkbenchAccess, error)
	SessionTTL time.Duration
	IdleTTL    time.Duration
}

type Service struct {
	pool     *pgxpool.Pool
	cfg      Config
	callback string
	feishu   *http.Client
}

func New(pool *pgxpool.Pool, cfg Config) (*Service, error) {
	if pool == nil {
		return nil, errors.New("identity database is required")
	}
	if strings.TrimSpace(cfg.AppID) == "" || strings.TrimSpace(cfg.TenantKey) == "" {
		return nil, errors.New("Feishu app ID and tenant key are required")
	}
	origin, err := url.Parse(cfg.PublicOrigin)
	if err != nil || origin.Scheme != "https" || origin.Hostname() == "" || origin.User != nil ||
		(origin.Path != "" && origin.Path != "/") || origin.RawPath != "" ||
		origin.ForceQuery || origin.RawQuery != "" || strings.Contains(cfg.PublicOrigin, "#") {
		return nil, errors.New("identity public origin must be an HTTPS origin")
	}
	host := strings.ToLower(origin.Host)
	if origin.Port() == "443" {
		host = strings.TrimSuffix(host, ":443")
	}
	if strings.HasSuffix(host, ":") || strings.IndexFunc(host, func(r rune) bool { return r > unicode.MaxASCII }) >= 0 {
		return nil, errors.New("identity public origin must use a valid ASCII host")
	}
	if origin.Port() != "" {
		if _, err := net.LookupPort("tcp", origin.Port()); err != nil {
			return nil, errors.New("identity public origin must use a valid HTTPS port")
		}
	}
	cfg.PublicOrigin = "https://" + host
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = 12 * time.Hour
	}
	if cfg.IdleTTL == 0 {
		cfg.IdleTTL = min(30*time.Minute, cfg.SessionTTL)
	}
	if cfg.SessionTTL < time.Second || cfg.IdleTTL < time.Second || cfg.IdleTTL > cfg.SessionTTL {
		return nil, errors.New("invalid identity session duration")
	}
	return &Service{
		pool: pool, cfg: cfg, callback: cfg.PublicOrigin + callbackPath,
		feishu: &http.Client{
			Transport: cfg.Transport,
			Timeout:   10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/feishu/start", s.start)
	mux.HandleFunc("GET "+callbackPath, s.complete)
	mux.HandleFunc("GET /api/v1/me", s.me)
	mux.HandleFunc("POST /auth/logout", s.logout)
}

func (s *Service) CallbackURL() string { return s.callback }

func (s *Service) start(w http.ResponseWriter, r *http.Request) {
	identityHeaders(w)
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeFailure(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query["return_to"]) > 1 {
		writeFailure(w, http.StatusBadRequest, "invalid_return_path")
		return
	}
	returnPath, err := localReturnPath(query.Get("return_to"))
	if err != nil {
		writeFailure(w, http.StatusBadRequest, "invalid_return_path")
		return
	}
	if s.cfg.AppSecret == "" {
		writeFailure(w, http.StatusServiceUnavailable, "login_unavailable")
		return
	}
	state, browser, verifier := newSecret(), newSecret(), newSecret()
	if err := s.storeLogin(r.Context(), state, browser, verifier, returnPath); err != nil {
		writeFailure(w, http.StatusServiceUnavailable, "login_unavailable")
		return
	}
	setCookie(w, browserCookie, browser, int(loginTTL.Seconds()))
	redirect(w, s.authorizationURL(state, verifier), http.StatusFound)
}

func (s *Service) complete(w http.ResponseWriter, r *http.Request) {
	identityHeaders(w)
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeFailure(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	state := query.Get("state")
	browser := cookieSecret(r, browserCookie)
	if err != nil || len(query["state"]) != 1 || !isSecret(state) || browser == "" {
		writeFailure(w, http.StatusBadRequest, "invalid_login_transaction")
		return
	}
	verifier, returnPath, err := s.consumeLogin(r.Context(), state, browser)
	if errors.Is(err, pgx.ErrNoRows) {
		writeFailure(w, http.StatusBadRequest, "invalid_login_transaction")
		return
	}
	if err != nil {
		writeFailure(w, http.StatusServiceUnavailable, "login_unavailable")
		return
	}
	setCookie(w, browserCookie, "", -1)
	if len(query["error"]) != 0 {
		redirect(w, "/#/login?error=feishu", http.StatusSeeOther)
		return
	}
	code := query.Get("code")
	if len(query["code"]) != 1 || code == "" || len(code) > 1024 {
		writeFailure(w, http.StatusBadRequest, "invalid_login_transaction")
		return
	}
	if s.cfg.AppSecret == "" {
		writeFailure(w, http.StatusBadGateway, "feishu_login_unavailable")
		return
	}
	principal, err := s.feishuIdentity(r.Context(), code, verifier)
	if errors.Is(err, errNotAdmitted) {
		writeFailure(w, http.StatusForbidden, "login_denied")
		return
	}
	if err != nil {
		writeFailure(w, http.StatusBadGateway, "feishu_login_unavailable")
		return
	}
	token := newSecret()
	err = s.createSession(r.Context(), principal, token, cookieSecret(r, sessionCookie))
	if errors.Is(err, errNotAdmitted) {
		writeFailure(w, http.StatusForbidden, "login_denied")
		return
	}
	if err != nil {
		writeFailure(w, http.StatusServiceUnavailable, "login_unavailable")
		return
	}
	setCookie(w, sessionCookie, token, int(s.cfg.SessionTTL.Seconds()))
	redirect(w, returnPath, http.StatusSeeOther)
}

// Authenticate validates the current server session and writes a sanitized response
// when authentication fails. Writes require the configured Origin and CSRF token.
func (s *Service) Authenticate(w http.ResponseWriter, r *http.Request, write bool) (Principal, bool) {
	p, _, ok := s.authenticate(w, r, write)
	return p, ok
}

// AuthenticateManagement uses the management API's common authorization errors.
func (s *Service) AuthenticateManagement(w http.ResponseWriter, r *http.Request, write bool) (Principal, bool) {
	identityHeaders(w)
	p, _, status, code := s.authenticateSession(r, write)
	switch status {
	case 0:
		return p, true
	case http.StatusUnauthorized:
		code = "unauthorized"
	case http.StatusForbidden:
		code = "forbidden"
	}
	writeFailure(w, status, code)
	return Principal{}, false
}

func (s *Service) authenticate(w http.ResponseWriter, r *http.Request, write bool) (Principal, string, bool) {
	identityHeaders(w)
	p, token, status, code := s.authenticateSession(r, write)
	if status != 0 {
		writeFailure(w, status, code)
		return Principal{}, "", false
	}
	return p, token, true
}

func (s *Service) authenticateSession(r *http.Request, write bool) (Principal, string, int, string) {
	token := cookieSecret(r, sessionCookie)
	if token == "" {
		return Principal{}, "", http.StatusUnauthorized, "authentication_required"
	}
	validCSRF := true
	if write || (r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions) {
		origins := r.Header.Values("Origin")
		csrf := r.Header.Values("X-CSRF-Token")
		validCSRF = len(origins) == 1 && origins[0] == s.cfg.PublicOrigin && len(csrf) == 1 &&
			hmac.Equal([]byte(csrf[0]), []byte(csrfToken(token)))
	}
	p, err := s.loadSession(r.Context(), token, validCSRF)
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, "", http.StatusUnauthorized, "authentication_required"
	}
	if err != nil {
		return Principal{}, "", http.StatusServiceUnavailable, "authentication_unavailable"
	}
	if !validCSRF {
		return Principal{}, "", http.StatusForbidden, "invalid_csrf"
	}
	return p, token, 0, ""
}

func (s *Service) me(w http.ResponseWriter, r *http.Request) {
	p, token, ok := s.authenticate(w, r, false)
	if !ok {
		return
	}
	var access *WorkbenchAccess
	if s.cfg.Access != nil {
		allowed, err := s.cfg.Access(r.Context(), p)
		if err != nil {
			writeFailure(w, http.StatusServiceUnavailable, "authentication_unavailable")
			return
		}
		access = &allowed
	}
	writeJSON(w, http.StatusOK, struct {
		Principal Principal        `json:"principal"`
		CSRFToken string           `json:"csrf_token"`
		Access    *WorkbenchAccess `json:"access,omitempty"`
	}{Principal: p, CSRFToken: csrfToken(token), Access: access})
}

func (s *Service) logout(w http.ResponseWriter, r *http.Request) {
	p, token, ok := s.authenticate(w, r, true)
	if !ok {
		return
	}
	if err := s.revokeSession(r.Context(), p, token); err != nil {
		writeFailure(w, http.StatusServiceUnavailable, "logout_unavailable")
		return
	}
	setCookie(w, sessionCookie, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

func localReturnPath(raw string) (string, error) {
	if raw == "" {
		return "/", nil
	}
	u, err := url.Parse(raw)
	invalid := func(r rune) bool { return r <= 31 || r == 127 || r == '\\' }
	if err != nil || len(raw) > 2048 || u.IsAbs() || u.Host != "" || !strings.HasPrefix(raw, "/") ||
		!strings.HasPrefix(u.Path, "/") || strings.Contains(u.Path, "//") ||
		strings.IndexFunc(raw, invalid) >= 0 || strings.IndexFunc(u.Path, invalid) >= 0 {
		return "", errors.New("invalid workbench return path")
	}
	return u.String(), nil
}

func newSecret() string {
	var raw [32]byte
	rand.Read(raw[:])
	return base64.RawURLEncoding.EncodeToString(raw[:])
}

func isSecret(value string) bool {
	if len(value) != base64.RawURLEncoding.EncodedLen(32) {
		return false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil && len(raw) == 32
}

func digest(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

// The per-session CSRF value is domain-separated and cannot be used as a cookie.
func csrfToken(session string) string {
	mac := hmac.New(sha256.New, []byte(session))
	mac.Write([]byte("OmniRelay session CSRF v1"))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func cookieSecret(r *http.Request, name string) string {
	cookies := r.CookiesNamed(name)
	if len(cookies) != 1 || !isSecret(cookies[0].Value) {
		return ""
	}
	return cookies[0].Value
}

func setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: maxAge,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

func redirect(w http.ResponseWriter, target string, status int) {
	w.Header().Set("Location", target)
	w.WriteHeader(status)
}

func identityHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func writeFailure(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, struct {
		Error string `json:"error"`
	}{Error: code})
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
