package identity

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
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

var ErrNotAdmitted = errors.New("account not admitted")

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
	Roles             []string        `json:"roles"`
	Admin             map[string]bool `json:"admin"`
	Permissions       map[string]bool `json:"permissions"`
	Navigation        map[string]bool `json:"navigation"`
	DefaultRoute      string          `json:"default_route"`
	PermissionVersion string          `json:"permission_version"`
}

type CandidateOAuth struct {
	Run      string
	StateKey string
	Resolve  func(context.Context, string, string) (Principal, error)
}

type Config struct {
	Candidate    *CandidateOAuth
	AppID        string
	AppSecret    string
	TenantKey    string
	PublicOrigin string
	// Audit, when configured, records a successful login or logout in its transaction.
	Audit func(context.Context, pgx.Tx, Principal, string) error
	// Transport is optional. Feishu endpoints remain fixed.
	Transport    http.RoundTripper
	Access       func(context.Context, Principal) (WorkbenchAccess, error)
	ResourceRead func(ctx context.Context, principal Principal, projectID, chatID string) (bool, error)
	SessionTTL   time.Duration
	IdleTTL      time.Duration
	// OAuthProxyToken authenticates the TLS upstream that supplies the OAuth source IP.
	OAuthProxyToken string
}

type Service struct {
	pool          *pgxpool.Pool
	cfg           Config
	callback      string
	feishu        *http.Client
	oauth         oauthLimits
	previewMu     sync.Mutex
	previewGrants map[previewGrantKey]previewGrant
}

func New(pool *pgxpool.Pool, cfg Config) (*Service, error) {
	if pool == nil {
		return nil, errors.New("identity database is required")
	}
	if strings.TrimSpace(cfg.AppID) == "" || strings.TrimSpace(cfg.TenantKey) == "" {
		return nil, errors.New("Feishu app ID and tenant key are required")
	}
	return newIdentityService(pool, cfg)
}

func newIdentityService(pool *pgxpool.Pool, cfg Config) (*Service, error) {
	if c := cfg.Candidate; c != nil && (cfg.AppSecret != "" || len(c.StateKey) < 32 || !regexp.MustCompile("^[a-f0-9]{32}$").MatchString(c.Run) || c.Resolve == nil) {
		return nil, errors.New("候选登录运行绑定无效")
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

func (s *Service) cookieName(name string) string {
	if c := s.cfg.Candidate; c != nil {
		return name + "_candidate_" + c.Run
	}
	return name
}

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/feishu/start", s.start)
	if s.cfg.Candidate == nil {
		mux.HandleFunc("GET /auth/feishu/preview", s.start)
	}
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
	if !s.oauth.allowStart(time.Now(), s.oauthSource(r)) {
		oauthRetry(w, time.Minute)
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
	if s.cfg.AppSecret == "" && s.cfg.Candidate == nil {
		writeFailure(w, http.StatusServiceUnavailable, "login_unavailable")
		return
	}
	var scope string
	if r.URL.Path == "/auth/feishu/preview" {
		scope = previewScopes[query.Get("kind")]
		if scope == "" || len(query["kind"]) != 1 || !previewReturnPath(returnPath) {
			writeFailure(w, http.StatusBadRequest, "invalid_return_path")
			return
		}
		p, token, ok := s.authenticate(w, r, ValidateSession)
		if !ok {
			return
		}
		returnPath = encodePreviewLogin(previewLogin{Path: returnPath, Kind: query.Get("kind"), Session: previewSession(token), OpenID: p.OpenID})
	}
	browser, verifier := newSecret(), newSecret()
	state := s.newOAuthState(time.Now(), browser)
	if err := s.storeLogin(r.Context(), state, browser, verifier, returnPath); err != nil {
		writeFailure(w, http.StatusServiceUnavailable, "login_unavailable")
		return
	}
	setCookie(w, s.cookieName(browserCookie), browser, int(loginTTL.Seconds()))
	redirect(w, s.authorizationURLForScope(state, verifier, scope), http.StatusFound)
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
	browser := cookieSecret(r, s.cookieName(browserCookie))
	now := time.Now()
	var verifier, returnPath string
	if err == nil && len(query["state"]) == 1 && browser != "" {
		verifier, returnPath, err = s.admitCallback(r.Context(), now, state, browser)
	} else {
		err = s.oauth.rejectUnverified(now)
	}
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeFailure(w, http.StatusBadRequest, "invalid_login_transaction")
		return
	case errors.Is(err, errOAuthRateLimited):
		oauthRetry(w, time.Minute)
		return
	case errors.Is(err, errOAuthBusy):
		oauthRetry(w, time.Second)
		return
	}
	if err != nil {
		writeFailure(w, http.StatusServiceUnavailable, "login_unavailable")
		return
	}
	setCookie(w, s.cookieName(browserCookie), "", -1)
	if saved, ok := decodePreviewLogin(returnPath); ok {
		s.completePreview(w, r, query, verifier, saved)
		return
	}
	if len(query["error"]) == 1 && query.Get("error") == "access_denied" && len(query["code"]) == 0 {
		if c := s.cfg.Candidate; c != nil {
			slog.Info("候选 OAuth 回跳", "run", c.Run, "结果", "取消")
		}
		redirectLoginError(w, r, "cancelled")
		return
	}
	code := query.Get("code")
	if len(query["error"]) != 0 || len(query["code"]) != 1 || code == "" || len(code) > 1024 || (s.cfg.AppSecret == "" && s.cfg.Candidate == nil) {
		redirectLoginError(w, r, "unavailable")
		return
	}
	resolve := s.feishuIdentity
	if c := s.cfg.Candidate; c != nil {
		resolve = c.Resolve
	}
	principal, err := resolve(r.Context(), code, verifier)
	if err == nil && (principal.AppID != s.cfg.AppID || principal.TenantKey != s.cfg.TenantKey || principal.OpenID == "") {
		err = ErrNotAdmitted
	}
	if errors.Is(err, ErrNotAdmitted) {
		redirectLoginError(w, r, "denied")
		return
	}
	if err != nil {
		redirectLoginError(w, r, "unavailable")
		return
	}
	token := newSecret()
	err = s.createSession(r.Context(), &principal, token, cookieSecret(r, s.cookieName(sessionCookie)))
	if errors.Is(err, ErrNotAdmitted) {
		redirectLoginError(w, r, "denied")
		return
	}
	if err != nil {
		redirectLoginError(w, r, "unavailable")
		return
	}
	// In-flight transactions can contain paths saved before the allowlist changed.
	target, err := localReturnPath(returnPath)
	if err != nil {
		target = "/"
	}
	if s.cfg.Access != nil {
		allowed, err := s.cfg.Access(r.Context(), principal)
		if errors.Is(err, ErrNotAdmitted) {
			redirectLoginError(w, r, "denied")
			return
		}
		target = "/"
		if err == nil {
			target = s.returnForAccess(r.Context(), principal, allowed, returnPath)
		}
	}
	setCookie(w, s.cookieName(sessionCookie), token, int(s.cfg.SessionTTL.Seconds()))
	if c := s.cfg.Candidate; c != nil {
		slog.Info("候选 OAuth 回跳", "run", c.Run, "结果", "会话已建立")
	}
	redirect(w, target, http.StatusSeeOther)
}

func redirectLoginError(w http.ResponseWriter, r *http.Request, code string) {
	slog.InfoContext(r.Context(), "Feishu OAuth login failed", "code", code)
	redirect(w, "/#/login?error="+code, http.StatusSeeOther)
}

// SessionPolicy specifies whether an authenticated route may extend the idle
// deadline and whether it requires CSRF protection regardless of HTTP method.
type SessionPolicy uint8

const (
	ValidateSession SessionPolicy = iota
	RefreshSession
	RefreshSessionWithCSRF
)

// Authenticate validates the current server session and writes a sanitized response
// when authentication fails. Writes require the configured Origin and CSRF token.
func (s *Service) Authenticate(w http.ResponseWriter, r *http.Request, policy SessionPolicy) (Principal, bool) {
	p, _, ok := s.authenticate(w, r, policy)
	return p, ok
}

// AuthenticateWorkbench 同步约束业务页面的直达 API。
func (s *Service) AuthenticateWorkbench(w http.ResponseWriter, r *http.Request, policy SessionPolicy) (Principal, bool) {
	p, ok := s.Authenticate(w, r, policy)
	if !ok {
		return Principal{}, false
	}
	access, err := s.cfg.Access(r.Context(), p)
	if err != nil || !access.Navigation["workbench"] {
		writeFailure(w, http.StatusForbidden, "forbidden")
		return Principal{}, false
	}
	return p, true
}

// AuthenticateManagement uses the management API's common authorization errors.
func (s *Service) AuthenticateManagement(w http.ResponseWriter, r *http.Request, policy SessionPolicy) (Principal, bool) {
	identityHeaders(w)
	p, _, status, code := s.authenticateSession(r, policy)
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

func (s *Service) authenticate(w http.ResponseWriter, r *http.Request, policy SessionPolicy) (Principal, string, bool) {
	identityHeaders(w)
	p, token, status, code := s.authenticateSession(r, policy)
	if status != 0 {
		writeFailure(w, status, code)
		return Principal{}, "", false
	}
	return p, token, true
}

func (s *Service) authenticateSession(r *http.Request, policy SessionPolicy) (Principal, string, int, string) {
	token := cookieSecret(r, s.cookieName(sessionCookie))
	if token == "" {
		return Principal{}, "", http.StatusUnauthorized, "authentication_required"
	}
	validCSRF := true
	if policy == RefreshSessionWithCSRF || (r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions) {
		origins := r.Header.Values("Origin")
		csrf := r.Header.Values("X-CSRF-Token")
		validCSRF = len(origins) == 1 && origins[0] == s.cfg.PublicOrigin && len(csrf) == 1 &&
			hmac.Equal([]byte(csrf[0]), []byte(csrfToken(token)))
	}
	if !validCSRF || (r.Method == http.MethodGet && r.Header.Get("X-OmniRelay-Activity") == "poll") {
		policy = ValidateSession
	}
	p, err := s.loadSession(r.Context(), token, policy)
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
	p, token, ok := s.authenticate(w, r, ValidateSession)
	if !ok {
		return
	}
	var access *WorkbenchAccess
	if s.cfg.Access != nil {
		allowed, err := s.cfg.Access(r.Context(), p)
		if errors.Is(err, ErrNotAdmitted) {
			writeFailure(w, http.StatusUnauthorized, "authentication_required")
			return
		}
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

const botChatsPath = "/#/feishu/bot-chats"

var chatIdentifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,160}$`)

func matchPathPattern(pattern, path string, valid func(string) bool) bool {
	segments, values := strings.Split(pattern, "/"), strings.Split(path, "/")
	if len(segments) != len(values) {
		return false
	}
	for i, seg := range segments {
		if strings.HasPrefix(seg, ":") {
			if !valid(values[i]) {
				return false
			}
		} else if seg != values[i] {
			return false
		}
	}
	return true
}
func chatTarget(raw string) (string, bool) {
	path := raw
	if raw == "/#/bot-chats" || strings.HasPrefix(raw, "/#/bot-chats/") {
		path = botChatsPath + strings.TrimPrefix(raw, "/#/bot-chats")
	}
	for _, suffix := range []string{"", "/:chat", "/:chat/:message"} {
		if matchPathPattern(botChatsPath+suffix, path, chatIdentifier.MatchString) {
			return path, true
		}
	}
	return "", false
}

// Project return routes stay in the workbench fragment and are authorized again on read.
const projectSegment = `[A-Za-z0-9_-][A-Za-z0-9_.:-]{0,199}`

var projectIDPattern = regexp.MustCompile("^" + projectSegment + "$")

// ValidProjectID 检查项目深链中使用的稳定标识。
func ValidProjectID(id string) bool { return projectIDPattern.MatchString(id) }

var projectReturn = regexp.MustCompile(`^/#/projects(/` + projectSegment + `(/(overview|members|chats|meetings|docs|reports|tasks|feishu|brief)(/` + projectSegment + `){0,3})?)?(\?[a-zA-Z0-9_=%+.&-]*)?$`)

func (a WorkbenchAccess) canReturnTo(path string) bool {
	if _, ok := projectTarget(path); ok {
		return a.Navigation["workbench"]
	}
	if _, ok := chatTarget(path); path == "/#/feishu/drive" || ok {
		return a.Navigation["bot_chats"]
	}
	if strings.HasPrefix(path, "/#/system/") && !a.Navigation["system"] {
		return false
	}
	switch path {
	case "/#/workbench":
		return a.Navigation["workbench"]
	case "/#/system/users":
		return a.Admin["users"]
	case "/#/system/roles":
		return a.Admin["roles"]
	case "/#/system/audit":
		return a.Admin["audit"]
	case "/#/system/feishu-bot":
		return a.Admin["feishu_bot"]
	case "/#/system/settings":
		return a.Admin["settings"]
	default:
		return false
	}
}

func (s *Service) logout(w http.ResponseWriter, r *http.Request) {
	p, token, ok := s.authenticate(w, r, RefreshSessionWithCSRF)
	if !ok {
		return
	}
	if err := s.revokeSession(r.Context(), p, token); err != nil {
		writeFailure(w, http.StatusServiceUnavailable, "logout_unavailable")
		return
	}
	s.clearPreviewGrants(token)
	setCookie(w, s.cookieName(sessionCookie), "", -1)
	w.WriteHeader(http.StatusNoContent)
}

// 返回时重新读取权限，浏览器保存的目标和默认入口都经过同一检查。
func (s *Service) returnForAccess(ctx context.Context, principal Principal, access WorkbenchAccess, raw string) string {
	for _, candidate := range []string{raw, access.DefaultRoute} {
		path, err := localReturnPath(candidate)
		if err != nil || !access.canReturnTo(path) {
			continue
		}
		base, _, _ := strings.Cut(path, "?")
		parts := strings.Split(base, "/")
		var project, chat string
		switch {
		case strings.HasPrefix(base, "/#/projects/"):
			project = parts[3]
			if len(parts) > 5 && parts[4] == "chats" {
				chat = parts[5]
			}
		case strings.HasPrefix(base, botChatsPath+"/"):
			chat = parts[4]
		}
		if project != "" || chat != "" {
			if s.cfg.ResourceRead == nil {
				continue
			}
			allowed, err := s.cfg.ResourceRead(ctx, principal, project, chat)
			if err != nil || !allowed {
				continue
			}
		}
		return path
	}
	return "/"
}

func projectTarget(raw string) (string, bool) {
	if !projectReturn.MatchString(raw) {
		return "", false
	}
	base, query, hasQuery := strings.Cut(raw, "?")
	values, err := url.ParseQuery(query)
	if err != nil {
		return "", false
	}
	parts := strings.Split(base, "/")
	if len(parts) == 4 {
		base += "/overview"
	}
	if len(parts) >= 5 {
		tab, tail := parts[4], parts[5:]
		switch tab {
		case "brief":
			if len(tail) > 1 {
				return "", false
			}
			parts[4] = "reports/brief"
			return strings.Join(parts, "/"), true
		case "reports":
			if len(tail) > 0 && (tail[0] != "brief" || len(tail) > 2) {
				return "", false
			}
		case "chats":
			if len(tail) > 2 {
				return "", false
			}
		case "tasks":
			if len(tail) > 1 {
				if len(tail) != 3 || tail[0] != "source" {
					return "", false
				}
				switch tail[1] {
				case "minutes", "document", "sheets", "bitable", "file":
				case "message":
					if !ValidProjectID(values.Get("chat_id")) {
						return "", false
					}
				default:
					return "", false
				}
			}
		default:
			if len(tail) > 0 {
				return "", false
			}
		}
	}
	if hasQuery {
		base += "?" + query
	}
	return base, true
}

func localReturnPath(raw string) (string, error) {
	if len(raw) > 4096 {
		return "", errors.New("invalid workbench return path")
	}
	if project, ok := projectTarget(raw); ok {
		return project, nil
	}
	if canonical, ok := chatTarget(raw); ok {
		return canonical, nil
	}
	switch raw {
	case "":
		return "/", nil
	case "/", "/#/feishu/drive", "/#/workbench", "/#/system/users", "/#/system/roles", "/#/system/audit", "/#/system/feishu-bot", "/#/system/settings":
		return raw, nil
	case "/#/settings":
		return "/#/system/settings", nil
	case "/#/system/bot":
		return "/#/system/feishu-bot", nil
	default:
		return "", errors.New("invalid workbench return path")
	}
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
