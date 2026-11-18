package identity

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Use the OAuth v3 token endpoint so Feishu enforces PKCE on both legs.
const (
	authorizeEndpoint = "https://accounts.feishu.cn/open-apis/authen/v1/authorize"
	tokenEndpoint     = "https://accounts.feishu.cn/oauth/v3/token"
	userInfoEndpoint  = "https://open.feishu.cn/open-apis/authen/v1/user_info"
)

var errFeishu = errors.New("Feishu OAuth request failed")

func (s *Service) authorizationURL(state, verifier string) string {
	query := url.Values{
		"client_id":             {s.cfg.AppID},
		"response_type":         {"code"},
		"redirect_uri":          {s.callback},
		"target_tenant":         {s.cfg.TenantKey},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(digest(verifier))},
		"code_challenge_method": {"S256"},
	}
	return authorizeEndpoint + "?" + query.Encode()
}

func (s *Service) feishuIdentity(ctx context.Context, code, verifier string) (Principal, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {s.cfg.AppID},
		"client_secret": {s.cfg.AppSecret},
		"redirect_uri":  {s.callback},
		"code":          {code},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Principal{}, errFeishu
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var token struct {
		Code        *int   `json:"code"`
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	status, err := s.feishuJSON(req, &token)
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return Principal{}, ErrNotAdmitted
	}
	if err != nil || token.Code == nil {
		return Principal{}, errFeishu
	}
	switch *token.Code {
	case 20003, 20004, 20008, 20010, 20024, 20049, 20065, 20066, 20071:
		return Principal{}, ErrNotAdmitted
	}
	if status != http.StatusOK || *token.Code != 0 || token.AccessToken == "" || !strings.EqualFold(token.TokenType, "Bearer") {
		return Principal{}, errFeishu
	}

	req, err = http.NewRequestWithContext(ctx, http.MethodGet, userInfoEndpoint, nil)
	if err != nil {
		return Principal{}, errFeishu
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	var user struct {
		Code *int `json:"code"`
		Data struct {
			TenantKey string `json:"tenant_key"`
			AppID     string `json:"app_id"`
			OpenID    string `json:"open_id"`
			Name      string `json:"name"`
			AvatarURL string `json:"avatar_url"`
			Disabled  bool   `json:"disabled"`
			Active    *bool  `json:"active"`
			Status    struct {
				Frozen    bool  `json:"is_frozen"`
				Resigned  bool  `json:"is_resigned"`
				Activated *bool `json:"is_activated"`
			} `json:"status"`
		} `json:"data"`
	}
	status, err = s.feishuJSON(req, &user)
	if err != nil || user.Code == nil {
		return Principal{}, errFeishu
	}
	switch *user.Code {
	case 20008, 20021, 20022, 20023:
		return Principal{}, ErrNotAdmitted
	}
	if status != http.StatusOK || *user.Code != 0 {
		return Principal{}, errFeishu
	}
	data := user.Data
	// The token exchange binds the app because Feishu authenticates the client and
	// rejects codes issued to another app. User info normally omits app_id.
	if data.TenantKey != s.cfg.TenantKey || strings.TrimSpace(data.OpenID) == "" ||
		(data.AppID != "" && data.AppID != s.cfg.AppID) ||
		data.Disabled || (data.Active != nil && !*data.Active) ||
		data.Status.Frozen || data.Status.Resigned || (data.Status.Activated != nil && !*data.Status.Activated) {
		return Principal{}, ErrNotAdmitted
	}
	return Principal{
		TenantKey: data.TenantKey, AppID: s.cfg.AppID, OpenID: data.OpenID,
		Name: data.Name, AvatarURL: httpsAvatar(data.AvatarURL),
	}, nil
}

func (s *Service) feishuJSON(req *http.Request, into any) (int, error) {
	req.Header.Set("Accept", "application/json")
	resp, err := s.feishu.Do(req)
	if err != nil {
		return 0, errFeishu
	}
	defer resp.Body.Close()
	const maxBody = 64 << 10
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(raw) > maxBody || json.Unmarshal(raw, into) != nil {
		return resp.StatusCode, errFeishu
	}
	return resp.StatusCode, nil
}

func httpsAvatar(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return ""
	}
	return u.String()
}
