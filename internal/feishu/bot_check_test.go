package feishu

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/uniquechao/OmniRelay/internal/system"
)

type checkTransport func(*http.Request) (*http.Response, error)

func (f checkTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBotCheckProviderFailures(t *testing.T) {
	for _, test := range []struct {
		name     string
		endpoint string
		status   int
		body     string
		want     system.CheckError
	}{
		{"invalid secret", tokenURL, 400, `{"code":20002,"error_description":"private-fixture"}`, system.CredentialsRejected},
		{"token authorization denied", tokenURL, 403, "private-fixture", system.CredentialsRejected},
		{"token rate limited", tokenURL, 429, `{"code":99991400}`, system.ProviderUnavailable},
		{"malformed token response", tokenURL, 200, `{"access_token":"private-token"}`, system.ProviderUnavailable},
		{"empty token", tokenURL, 200, `{"code":0}`, system.ProviderUnavailable},
		{"Bot permission denied", botURL, 200, `{"code":99991672}`, system.BotUnavailable},
		{"Bot not found", botURL, 404, "", system.BotUnavailable},
		{"Bot disabled", botURL, 200, `{"code":0,"bot":{"app_name":"Fixture","open_id":"ou_bot","activate_status":1}}`, system.BotUnavailable},
		{"Bot name echo", botURL, 200, `{"code":0,"bot":{"app_name":"private-token","open_id":"ou_bot"}}`, system.BotUnavailable},
		{"Bot rate limited", botURL, 200, `{"code":99991400}`, system.ProviderUnavailable},
		{"Bot service unavailable", botURL, 503, "private-fixture", system.ProviderUnavailable},
		{"Bot redirect", botURL, 307, "", system.ProviderUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			provider := NewBotCheck("cli_fixture", "private-fixture", checkTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != test.endpoint {
					t.Fatal("unexpected provider URL")
				}
				return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(test.body)),
					Header: http.Header{"Location": {"https://foreign.example/"}}}, nil
			}))
			var got system.CheckError
			if test.endpoint == tokenURL {
				token, failure := provider.TenantToken(context.Background())
				if token != "" {
					t.Fatal("failed request yielded a token")
				}
				got = failure
			} else {
				info, failure := provider.BotInfo(context.Background(), "private-token")
				if info != (system.BotInfo{}) {
					t.Fatal("failed request yielded Bot data")
				}
				got = failure
			}
			if got != test.want || calls != 1 {
				t.Fatalf("got %q with %d calls, want %q with one call", got, calls, test.want)
			}
		})
	}
}

func TestBotCheckTimeoutAndMissingCredentials(t *testing.T) {
	transport := checkTransport(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		if !errors.Is(r.Context().Err(), context.DeadlineExceeded) {
			t.Error("request did not time out")
		}
		return nil, r.Context().Err()
	})
	provider := NewBotCheck("cli_fixture", "private-fixture", transport)
	provider.client.Timeout = 10 * time.Millisecond
	if token, err := provider.TenantToken(context.Background()); token != "" || err != system.ProviderUnavailable {
		t.Fatalf("unexpected token timeout result: %q", err)
	}
	if info, err := provider.BotInfo(context.Background(), "private-token"); info != (system.BotInfo{}) || err != system.ProviderUnavailable {
		t.Fatalf("unexpected Bot timeout result: %q", err)
	}
	missing := NewBotCheck("cli_fixture", "", checkTransport(func(r *http.Request) (*http.Response, error) {
		t.Fatal("missing credentials triggered a provider call")
		return nil, nil
	}))
	if token, err := missing.TenantToken(context.Background()); token != "" || err != system.CredentialsMissing {
		t.Fatalf("unexpected missing credentials result: %q", err)
	}
}
