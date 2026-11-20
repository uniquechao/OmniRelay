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
			if r.URL.Query().Get("fetch_child") != "false" {
				t.Error("child list must request direct children")
			}
			if r.URL.Query().Get("page_token") == "" {
				data = `{"items":[{"open_department_id":"b","parent_department_id":"a","name":"B"}],"has_more":true,"page_token":"next/+"}`
			} else if r.URL.Query().Get("page_token") == "next/+" {
				data = `{"items":[{"open_department_id":"c","parent_department_id":"a","name":"C"}],"has_more":false}`
			}
		case "/open-apis/contact/v3/departments/b/children", "/open-apis/contact/v3/departments/c/children":
			data = `{"items":[],"has_more":false}`
		case "/open-apis/contact/v3/users/find_by_department":
			switch r.URL.Query().Get("department_id") {
			case "a":
				if r.URL.Query().Get("page_token") == "" {
					data = `{"items":[{"open_id":"ou_shared","name":"Shared","department_ids":["a","b","outside"]}],"has_more":true,"page_token":"people-next"}`
				} else if r.URL.Query().Get("page_token") == "people-next" {
					data = `{"items":[{"open_id":"ou_second","name":"Shared","department_ids":["a"]}],"has_more":false}`
				}
			case "b":
				data = `{"items":[{"open_id":"ou_shared","name":"Shared"}],"has_more":false}`
			case "c":
				data = `{"items":[],"has_more":false}`
			}
		case "/open-apis/contact/v3/users/ou_direct":
			data = `{"user":{"open_id":"ou_direct","name":"Direct","department_ids":["outside"]}}`
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.URL.Query().Get("department_id_type") != "open_department_id" ||
			((strings.Contains(r.URL.Path, "/users/") || strings.HasSuffix(r.URL.Path, "/scopes")) &&
				r.URL.Query().Get("user_id_type") != "open_id") {
			t.Errorf("incorrect identity fields: %s", r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("unexpected authorization %q", r.Header.Get("Authorization"))
		}
		fmt.Fprintf(w, `{"code":0,"data":%s}`, data)
	})
	result, err := service.collect(context.Background(), func(scope, int, int) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.People) != 3 || len(result.Departments) != 3 || result.Departments["a"].ParentID != "" || result.Departments["b"].ParentID != "a" {
		t.Fatalf("unexpected snapshot %+v", result)
	}
	if len(result.Members["ou_shared"]) != 2 || len(result.Members["ou_second"]) != 1 || len(result.Members["ou_direct"]) != 0 {
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
	}, nil)
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
	if _, err := service.collect(context.Background(), func(scope, int, int) error { return nil }, nil); err == nil {
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
				_, err := service.collect(context.Background(), func(scope, int, int) error { return nil }, nil)
				if err == nil {
					t.Fatal("malformed page was accepted as an empty page")
				}
			})
		}
	}
}

func TestContactActivationStatus(t *testing.T) {
	for _, tc := range []struct {
		payload     string
		want        string
		unactivated bool
	}{
		{`{}`, "unknown", false},
		{`{"status":null}`, "unknown", false},
		{`{"status":{}}`, "unknown", false},
		{`{"status":{"is_resigned":false}}`, "unknown", false},
		{`{"status":{"is_activated":true}}`, "active", false},
		{`{"status":{"is_activated":false}}`, "inactive", true},
		{`{"status":{"is_activated":true,"is_exited":true}}`, "inactive", false},
		{`{"status":{"is_activated":false,"is_exited":true}}`, "inactive", false},
		{`{"status":{"is_activated":false,"is_frozen":true}}`, "inactive", false},
		{`{"status":{"is_activated":false,"is_unjoin":true}}`, "inactive", false},
		{`{"status":{"is_activated":false,"is_resigned":true}}`, "resigned", false},
	} {
		var person contactPerson
		if err := json.Unmarshal([]byte(tc.payload), &person); err != nil {
			t.Fatal(err)
		}
		if got, unactivated := person.status(); got != tc.want || unactivated != tc.unactivated {
			t.Errorf("status from %s = %s, unactivated=%t; want %s, unactivated=%t", tc.payload, got, unactivated, tc.want, tc.unactivated)
		}
	}
}

func TestCollectRejectsInconsistentPages(t *testing.T) {
	for _, tc := range []struct {
		name     string
		children string
		people   string
	}{
		{"child parent mismatch", `{"items":[{"open_department_id":"b","parent_department_id":"other","name":"B"}],"has_more":false}`, ""},
		{"empty intermediate page", `{"items":[],"has_more":true,"page_token":"next"}`, ""},
		{"person listed under wrong department", "", `{"items":[{"open_id":"ou_one","name":"One","department_ids":["outside"]}],"has_more":false}`},
		{"reported membership missing from direct pages", `{"items":[{"open_department_id":"b","parent_department_id":"0","name":"B"}],"has_more":false}`, `{"items":[{"open_id":"ou_one","name":"One","department_ids":["0","b"]}],"has_more":false}`},
		{"invalid membership id", "", `{"items":[{"open_id":"ou_one","name":"One","department_ids":["0","bad/id"]}],"has_more":false}`},
		{"employee id in open id field", "", `{"items":[{"open_id":"employee123","name":"One"}],"has_more":false}`},
		{"repeated person with different continuation", "", `{"items":[{"open_id":"ou_one","name":"One"}],"has_more":true,"page_token":"next"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := fixtureService(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/oauth/v3/token":
					fmt.Fprint(w, `{"code":0,"access_token":"token"}`)
					return
				case contacts + "/scopes":
					fmt.Fprint(w, `{"code":0,"data":{"department_ids":["0"],"has_more":false}}`)
					return
				case contacts + "/departments/0/children":
					if r.URL.Query().Has("page_token") || tc.children == "" {
						fmt.Fprint(w, `{"code":0,"data":{"items":[],"has_more":false}}`)
						return
					}
					fmt.Fprintf(w, `{"code":0,"data":%s}`, tc.children)
				case contacts + "/departments/b/children":
					fmt.Fprint(w, `{"code":0,"data":{"items":[],"has_more":false}}`)
				case contacts + "/users/find_by_department":
					if r.URL.Query().Get("department_id") == "b" {
						fmt.Fprint(w, `{"code":0,"data":{"items":[],"has_more":false}}`)
						return
					}
					if r.URL.Query().Has("page_token") {
						fmt.Fprint(w, `{"code":0,"data":{"items":[{"open_id":"ou_one","name":"One"}],"has_more":false}}`)
						return
					}
					fmt.Fprintf(w, `{"code":0,"data":%s}`, tc.people)
				default:
					t.Errorf("unexpected request %s", r.URL)
					w.WriteHeader(http.StatusNotFound)
				}
			})
			if _, err := service.collect(context.Background(), func(scope, int, int) error { return nil }, nil); err == nil {
				t.Fatal("inconsistent directory was accepted")
			}
		})
	}
}

func TestContactErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		status, code int
		denied       bool
	}{
		{http.StatusBadRequest, 40003, false},
		{http.StatusForbidden, 99991400, false},
		{http.StatusForbidden, 99991401, false},
		{http.StatusBadRequest, 20009, true},
		{http.StatusBadRequest, 20069, true},
		{http.StatusBadRequest, 40004, true},
		{http.StatusBadRequest, 41050, true},
		{http.StatusBadRequest, 99991672, true},
		{http.StatusForbidden, 0, true},
	} {
		err := upstreamError{status: tc.status, code: tc.code}
		if err.denied() != tc.denied {
			t.Errorf("HTTP %d code %d denied = %t, want %t", tc.status, tc.code, err.denied(), tc.denied)
		}
	}
}

func TestReadScopeExpandsUserGroups(t *testing.T) {
	service := fixtureService(t, func(w http.ResponseWriter, r *http.Request) {
		var data string
		switch r.URL.Path {
		case contacts + "/scopes":
			data = `{"department_ids":["a"],"user_ids":["ou_direct"],"group_ids":["g_one"],"has_more":false}`
		case contacts + "/group/g_one/member/simplelist":
			if r.URL.Query().Get("member_id_type") != "open_id" {
				t.Error("group members must use stable open identifiers")
			}
			switch r.URL.Query().Get("member_type") {
			case "department":
				data = `{"memberlist":[{"member_id":"a","member_type":"department"},{"member_id":"b","member_type":"department"}],"has_more":false}`
			case "user":
				if r.URL.Query().Get("page_token") == "" {
					data = `{"memberlist":[{"member_id":"ou_direct","member_type":"user"}],"has_more":true,"page_token":"next"}`
				} else {
					data = `{"memberlist":[{"member_id":"ou_group","member_type":"user"}],"has_more":false}`
				}
			default:
				t.Error("missing group member type")
			}
		default:
			t.Error("unexpected request path")
		}
		fmt.Fprintf(w, `{"code":0,"data":%s}`, data)
	})
	f := collector{service: service, token: "fixture", left: 20}
	got, err := f.readScope(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Departments, ",") != "a,b" || strings.Join(got.People, ",") != "ou_direct,ou_group" {
		t.Fatalf("group scope was not expanded and deduplicated: %+v", got)
	}
}

func TestDirectoryRetainsPermissionCodeWithStructuredError(t *testing.T) {
	service := fixtureService(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"code":99991672,"error":{"permission_violations":[]},"msg":"Access denied"}`)
	})
	var result any
	err := service.requestOnce(t.Context(), http.MethodGet, service.cfg.BaseURL, "fixture", nil, &result)
	failure, ok := err.(upstreamError)
	if !ok || failure.code != 99991672 || !failure.denied() {
		t.Fatalf("structured permission error lost its code: %v", err)
	}
}
