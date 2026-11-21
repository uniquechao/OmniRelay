package identity

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var previewScopes = map[string]string{
	"docx":    "docx:document:readonly",
	"wiki":    "wiki:node:read",
	"sheets":  "sheets:spreadsheet:read",
	"minutes": "minutes:minutes.transcript:export",
	"base":    "base:app:read base:table:read base:record:retrieve",
	"file":    "drive:file:download",
}

type previewLogin struct {
	Path, Kind, Session, OpenID string
}

type previewGrantKey struct{ session, kind string }

type previewGrant struct {
	openID, token string
	expires       time.Time
}

func previewReturnPath(path string) bool {
	if _, ok := chatTarget(path); ok {
		return true
	}
	base, _, _ := strings.Cut(path, "?")
	parts := strings.Split(base, "/")
	return len(parts) >= 5 && parts[4] == "chats" && projectReturn.MatchString(path)
}

func previewSession(token string) string {
	return base64.RawURLEncoding.EncodeToString(digest(token))
}

func encodePreviewLogin(saved previewLogin) string {
	data, _ := json.Marshal(saved)
	return "preview:" + string(data)
}

func decodePreviewLogin(value string) (previewLogin, bool) {
	var saved previewLogin
	raw, ok := strings.CutPrefix(value, "preview:")
	ok = ok && json.Unmarshal([]byte(raw), &saved) == nil
	return saved, ok
}

func (s *Service) completePreview(w http.ResponseWriter, r *http.Request, query url.Values, verifier string, saved previewLogin) {
	p, token, ok := s.authenticate(w, r, ValidateSession)
	if !ok {
		return
	}
	ctx := r.Context()
	if p.OpenID != saved.OpenID || previewSession(token) != saved.Session || previewScopes[saved.Kind] == "" || !previewReturnPath(saved.Path) {
		writeFailure(w, http.StatusForbidden, "invalid_preview_session")
		return
	}
	target := saved.Path
	if s.cfg.Access != nil {
		allowed, err := s.cfg.Access(ctx, p)
		if err != nil {
			redirect(w, "/", http.StatusSeeOther)
			return
		}
		target = s.returnForAccess(ctx, p, allowed, saved.Path)
		if target != saved.Path {
			redirect(w, target, http.StatusSeeOther)
			return
		}
	}
	finish := func(result string) {
		redirect(w, strings.Replace(target, "/#", "/?document_preview="+result+"#", 1), http.StatusSeeOther)
	}
	if query.Get("error") == "access_denied" && len(query["code"]) == 0 {
		finish("cancelled")
		return
	}
	code := query.Get("code")
	if len(query["error"]) != 0 || len(query["code"]) != 1 || code == "" || len(code) > 1024 || s.cfg.AppSecret == "" {
		finish("unavailable")
		return
	}
	var credential feishuUserToken
	actor, err := s.feishuIdentityWithToken(ctx, code, verifier, &credential)
	if err != nil || actor.OpenID != p.OpenID || actor.TenantKey != p.TenantKey || credential.ExpiresIn <= 0 {
		finish("unavailable")
		return
	}
	if current, err := s.loadSession(ctx, token, ValidateSession); err != nil || current.OpenID != p.OpenID || current.AccountID != p.AccountID {
		writeFailure(w, http.StatusForbidden, "invalid_preview_session")
		return
	}
	s.savePreviewGrant(previewGrantKey{saved.Session, saved.Kind}, previewGrant{
		openID: p.OpenID, token: credential.AccessToken, expires: time.Now().Add(time.Duration(min(credential.ExpiresIn, 3600)) * time.Second),
	})
	finish("authorized")
}

func (s *Service) savePreviewGrant(key previewGrantKey, grant previewGrant) {
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	if s.previewGrants == nil {
		s.previewGrants = make(map[previewGrantKey]previewGrant)
	}
	now := time.Now()
	var oldest previewGrantKey
	var expiry time.Time
	for candidate, entry := range s.previewGrants {
		if !entry.expires.After(now) {
			delete(s.previewGrants, candidate)
		} else if expiry.IsZero() || entry.expires.Before(expiry) {
			oldest, expiry = candidate, entry.expires
		}
	}
	if _, present := s.previewGrants[key]; !present && len(s.previewGrants) >= 2048 {
		delete(s.previewGrants, oldest)
	}
	s.previewGrants[key] = grant
}

// DocumentPreviewToken retrieves a grant for the session and principal already authenticated by the reader.
func (s *Service) DocumentPreviewToken(r *http.Request, p Principal, kind string) string {
	token := cookieSecret(r, s.cookieName(sessionCookie))
	if token == "" || p.TenantKey != s.cfg.TenantKey || p.AppID != s.cfg.AppID || p.OpenID == "" {
		return ""
	}
	key := previewGrantKey{previewSession(token), kind}
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	grant := s.previewGrants[key]
	if !grant.expires.After(time.Now()) {
		delete(s.previewGrants, key)
		return ""
	}
	if grant.openID != p.OpenID {
		return ""
	}
	return grant.token
}

func (s *Service) clearPreviewGrants(token string) {
	session := previewSession(token)
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	for key := range s.previewGrants {
		if key.session == session {
			delete(s.previewGrants, key)
		}
	}
}
