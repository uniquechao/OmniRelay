// Package feishu adapts Feishu platform APIs to OmniRelay services.
package feishu

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/uniquechao/OmniRelay/internal/system"
)

const (
	tokenURL = "https://accounts.feishu.cn/oauth/v3/token"
	botURL   = "https://open.feishu.cn/open-apis/bot/v3/info"
)

type BotCheck struct {
	appID, secret string
	client        *http.Client
}

func NewBotCheck(appID, secret string, transport http.RoundTripper) *BotCheck {
	return &BotCheck{appID: appID, secret: secret, client: &http.Client{
		Transport: transport,
		Timeout:   8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

func (b *BotCheck) TenantToken(ctx context.Context) (string, system.CheckError) {
	if b.appID == "" || b.secret == "" {
		return "", system.CredentialsMissing
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {b.appID}, "client_secret": {b.secret}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", system.ProviderUnavailable
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var body struct {
		Code  *int   `json:"code"`
		Token string `json:"access_token"`
	}
	status, err := b.request(req, &body)
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return "", system.CredentialsRejected
	}
	if body.Code != nil && (status == http.StatusOK || status == http.StatusBadRequest) {
		switch *body.Code {
		case 20002, 20009, 20048, 20069, 20130, 20140:
			return "", system.CredentialsRejected
		}
	}
	if err != nil || status != http.StatusOK || body.Code == nil || *body.Code != 0 || body.Token == "" {
		return "", system.ProviderUnavailable
	}
	return body.Token, ""
}

func (b *BotCheck) BotInfo(ctx context.Context, token string) (system.BotInfo, system.CheckError) {
	if b.secret == "" || token == "" {
		return system.BotInfo{}, system.CredentialsMissing
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, botURL, nil)
	if err != nil {
		return system.BotInfo{}, system.ProviderUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+token)
	var body struct {
		Code *int `json:"code"`
		Bot  struct {
			Name      string `json:"app_name"`
			OpenID    string `json:"open_id"`
			Activated *int   `json:"activate_status"`
		} `json:"bot"`
	}
	status, err := b.request(req, &body)
	if status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound {
		return system.BotInfo{}, system.BotUnavailable
	}
	if err != nil || status != http.StatusOK || body.Code == nil {
		return system.BotInfo{}, system.ProviderUnavailable
	}
	switch *body.Code {
	case 0:
	case 99991400, 99991401:
		return system.BotInfo{}, system.ProviderUnavailable
	default:
		return system.BotInfo{}, system.BotUnavailable
	}
	if body.Bot.OpenID == "" || body.Bot.Name == "" || (body.Bot.Activated != nil && *body.Bot.Activated != 2) {
		return system.BotInfo{}, system.BotUnavailable
	}
	// Provider-controlled display fields must never echo credentials.
	if strings.Contains(body.Bot.Name, b.secret) || strings.Contains(body.Bot.Name, token) ||
		strings.Contains(body.Bot.OpenID, b.secret) || strings.Contains(body.Bot.OpenID, token) ||
		strings.ContainsRune(body.Bot.Name, 0) || strings.ContainsRune(body.Bot.OpenID, 0) {
		return system.BotInfo{}, system.BotUnavailable
	}
	return system.BotInfo{Name: body.Bot.Name, OpenID: body.Bot.OpenID}, ""
}

func (b *BotCheck) request(req *http.Request, target any) (int, error) {
	resp, err := b.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(target)
	return resp.StatusCode, err
}
