package system

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/uniquechao/OmniRelay/internal/identity"
)

type readPermissions func() (bool, error)

func (p readPermissions) Check(_ context.Context, _ identity.Principal, action, scope string) (bool, error) {
	if action != "system.read" || scope != "" {
		return false, errors.New("unexpected permission")
	}
	return p()
}

type checkStore struct{ Store }

func (checkStore) Check(context.Context, string) (Check, error) {
	return Check{Results: []Result{
		{Name: "event_subscription", State: "passed"},
		{Name: tenantTokenCheck, State: "passed"},
		{Name: botInfoCheck, State: "passed"},
	}}, nil
}

func TestConnectionSettingsContract(t *testing.T) {
	var allowed bool
	var permissionErr error
	permissions := readPermissions(func() (bool, error) { return allowed, permissionErr })
	mux := http.NewServeMux()
	New(checkStore{}, nil).Register(mux, func(w http.ResponseWriter, r *http.Request, policy identity.SessionPolicy) (identity.Principal, bool) {
		if policy != identity.ValidateSession {
			t.Fatalf("unexpected session policy: %v", policy)
		}
		if r.Header.Get("Cookie") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return identity.Principal{}, false
		}
		return identity.Principal{}, true
	}, permissions)
	get := func(path string, signedIn bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if signedIn {
			req.AddCookie(&http.Cookie{Name: "session", Value: "fixture"})
		}
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, req)
		return res
	}
	const path = "/api/v1/system/connections"
	if res := get(path, false); res.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous request: %d %s", res.Code, res.Body)
	}
	if res := get(path, true); res.Code != http.StatusForbidden {
		t.Fatalf("forbidden request: %d %s", res.Code, res.Body)
	}
	permissionErr = errors.New("authorization unavailable")
	if res := get(path, true); res.Code != http.StatusServiceUnavailable || !strings.Contains(res.Body.String(), `"service_unavailable"`) {
		t.Fatalf("unavailable request: %d %s", res.Code, res.Body)
	}
	permissionErr = nil
	allowed = true
	if res := get(path, true); res.Code != http.StatusOK || strings.TrimSpace(res.Body.String()) !=
		`{"items":[{"id":"omnigrid","state":"not_configured"},{"id":"deepseek_harness","state":"not_configured"}]}` {
		t.Fatalf("unconfigured response: %d %s", res.Code, res.Body)
	}
	if res := get(path+"/unknown", true); res.Code != http.StatusNotFound {
		t.Fatalf("unknown connection: %d %s", res.Code, res.Body)
	}
	res := get(basePath+"/checks/fixture", true)
	var check Check
	if res.Code != http.StatusOK || json.NewDecoder(res.Body).Decode(&check) != nil || !slices.EqualFunc(check.Results,
		[]Result{{Name: tenantTokenCheck, State: "passed"}, {Name: botInfoCheck, State: "passed"}},
		func(a, b Result) bool { return a.Name == b.Name && a.State == b.State }) {
		t.Fatalf("unexpected check response: %d %+v", res.Code, check)
	}
}
