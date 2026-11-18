package directory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fixtureService(t *testing.T, serve http.HandlerFunc) *Service {
	t.Helper()
	server := httptest.NewServer(serve)
	t.Cleanup(server.Close)
	return &Service{cfg: Config{AppID: "app", AppSecret: "secret", PageBudget: 30, BaseURL: server.URL}, client: server.Client()}
}

func TestCollectRetainsOnlyVerifiedHierarchyAndDeduplicatesPeople(t *testing.T) {
	service := fixtureService(t, func(w http.ResponseWriter, r *http.Request) {
		data := ""
		switch r.URL.Path {
		case "/oauth/v3/token":
			fmt.Fprint(w, `{"code":0,"access_token":"token"}`)
			return
		case "/open-apis/contact/v3/scopes":
			if r.URL.Query().Get("page_token") == "" {
				data = `{"department_ids":["a"],"has_more":true,"page_token":"next"}`
			} else {
				data = `{"user_ids":["ou_direct"],"has_more":false}`
			}
		case "/open-apis/contact/v3/departments/a":
			data = `{"department":{"open_department_id":"a","parent_department_id":"outside","name":"A"}}`
		case "/open-apis/contact/v3/departments/a/children":
			data = `{"items":[{"open_department_id":"b","parent_department_id":"a","name":"B"}],"has_more":false}`
		case "/open-apis/contact/v3/departments/b/children":
			data = `{"items":[],"has_more":false}`
		case "/open-apis/contact/v3/users/find_by_department":
			data = `{"items":[{"open_id":"ou_shared","name":"Shared","department_ids":["a","b","outside"]}],"has_more":false}`
		case "/open-apis/contact/v3/users/ou_direct":
			data = `{"user":{"open_id":"ou_direct","name":"Direct","department_ids":["outside"]}}`
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("unexpected authorization %q", r.Header.Get("Authorization"))
		}
		fmt.Fprintf(w, `{"code":0,"data":%s}`, data)
	})
	result, err := service.collect(context.Background(), func(scope, int, int) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(result.People) != 2 || len(result.Departments) != 2 || result.Departments["a"].ParentID != "" || result.Departments["b"].ParentID != "a" {
		t.Fatalf("unexpected snapshot %+v", result)
	}
	if len(result.Members["ou_shared"]) != 2 || len(result.Members["ou_direct"]) != 0 {
		t.Fatalf("unexpected memberships %+v", result.Members)
	}
}

func TestCollectRejectsRepeatedScopePage(t *testing.T) {
	service := fixtureService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/v3/token" {
			fmt.Fprint(w, `{"code":0,"access_token":"token"}`)
			return
		}
		fmt.Fprint(w, `{"code":0,"data":{"department_ids":["a"],"has_more":true,"page_token":"same"}}`)
	})
	_, err := service.collect(context.Background(), func(scope, int, int) error {
		t.Error("incomplete permission scope reached persistence")
		return nil
	})
	if err == nil {
		t.Fatal("expected incomplete snapshot error")
	}
}

func TestCollectRejectsScopeChangesDuringRead(t *testing.T) {
	scopeReads := 0
	service := fixtureService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/v3/token" {
			fmt.Fprint(w, `{"code":0,"access_token":"token"}`)
			return
		}
		scopeReads++
		if scopeReads == 1 {
			fmt.Fprint(w, `{"code":0,"data":{"has_more":false}}`)
			return
		}
		fmt.Fprint(w, `{"code":0,"data":{"department_ids":["new-root"],"has_more":false}}`)
	})
	if _, err := service.collect(context.Background(), func(scope, int, int) error { return nil }); err == nil {
		t.Fatal("expected changing authorization scope to discard the snapshot")
	}
}

func TestReadScopeRecognizesPermissionDenialInBadRequest(t *testing.T) {
	service := fixtureService(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"code":41050,"msg":"no user authority error"}`)
	})
	f := collector{service: service, token: "test", left: 1}
	_, err := f.readScope(context.Background())
	upstream, ok := err.(upstreamError)
	if !ok || !upstream.denied() {
		t.Fatalf("permission denial would leave stale directory visible: %v", err)
	}
}

func TestCollectRejectsMissingOrNullPageItems(t *testing.T) {
	for _, endpoint := range []string{"children", "find_by_department"} {
		for _, items := range []string{"", `,"items":null`} {
			t.Run(endpoint+items, func(t *testing.T) {
				service := fixtureService(t, func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/oauth/v3/token":
						fmt.Fprint(w, `{"code":0,"access_token":"token"}`)
					case contacts + "/scopes":
						fmt.Fprint(w, `{"code":0,"data":{"department_ids":["0"],"has_more":false}}`)
					case contacts + "/departments/0/children", contacts + "/users/find_by_department":
						field := `,"items":[]`
						if strings.HasSuffix(r.URL.Path, endpoint) {
							field = items
						}
						fmt.Fprintf(w, `{"code":0,"data":{"has_more":false%s}}`, field)
					default:
						t.Errorf("unexpected request %s", r.URL)
						w.WriteHeader(http.StatusBadRequest)
					}
				})
				_, err := service.collect(context.Background(), func(scope, int, int) error { return nil })
				if err == nil {
					t.Fatal("malformed page was accepted as an empty page")
				}
			})
		}
	}
}

func TestMissingContactActivationIsUnknown(t *testing.T) {
	for _, tc := range []struct {
		payload string
		want    string
	}{
		{`{}`, "unknown"},
		{`{"status":null}`, "unknown"},
		{`{"status":{}}`, "unknown"},
		{`{"status":{"is_resigned":false}}`, "unknown"},
		{`{"status":{"is_activated":true}}`, "active"},
		{`{"status":{"is_activated":false}}`, "inactive"},
		{`{"status":{"is_activated":true,"is_exited":true}}`, "resigned"},
	} {
		var person contactPerson
		if err := json.Unmarshal([]byte(tc.payload), &person); err != nil {
			t.Fatal(err)
		}
		if got := person.status(); got != tc.want {
			t.Errorf("status from %s = %s, want %s", tc.payload, got, tc.want)
		}
	}
}
