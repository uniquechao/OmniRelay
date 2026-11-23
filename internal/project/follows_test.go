package project

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

func TestPersonalProjectFollows(t *testing.T) {
	pool := testProjectPool(t, "personal_project_follows")
	exec := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO accounts(id,tenant_key,app_id,open_id) VALUES
		('relay-user','tenant','app','ou_reader'),('other','tenant','app','ou_other');
		INSERT INTO project_members(tenant_key,app_id,project_id,account_id,member_id,role) VALUES
		('tenant','app','legacy','relay-user','reader','reader'),
		('tenant','app','legacy','other','other','reader'),
		('tenant','app','private','relay-user','reader','manager')`)
	roles := access.New(pool, "tenant", "app")
	roles.ProjectAccess = access.LocalProjects{Pool: pool}
	svc := New(pool, roles, nil, nil, &projectChat{}, true)
	actor := testActor
	mux := http.NewServeMux()
	svc.Register(mux, func(_ http.ResponseWriter, r *http.Request, policy identity.SessionPolicy) (identity.Principal, bool) {
		if r.Method != "GET" && policy != identity.RefreshSessionWithCSRF {
			t.Fatal("follow mutation missing CSRF policy")
		}
		return actor, true
	})
	request := func(method, path string, status int, dst any) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, "/api/v1/"+path, nil))
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		if dst != nil {
			if err := json.Unmarshal(w.Body.Bytes(), dst); err != nil {
				t.Fatal(err)
			}
		}
	}
	check := func(name string) {
		t.Helper()
		var list struct{ Items []projectShortcut }
		request("GET", "me/followed-projects", 200, &list)
		if name == "" && len(list.Items) == 0 && list.Items != nil {
			return
		}
		if len(list.Items) != 1 || list.Items[0] != (projectShortcut{ID: "legacy", Name: name}) {
			t.Fatalf("shortcuts for %s: %+v, want %q", actor.AccountID, list.Items, name)
		}
	}
	const follow = "projects/legacy/follow"
	check("")
	request("PUT", follow, 200, nil)
	request("PUT", follow, 200, nil)
	check("legacy")
	var page ProjectPage
	request("GET", "projects?q=legacy", 200, &page)
	if len(page.Items) != 1 || !page.Items[0].Followed {
		t.Fatal(page.Items)
	}
	var overview struct {
		Data struct {
			Workspace struct{ Project directoryProject }
		}
	}
	request("GET", "projects/legacy/overview", 200, &overview)
	if !overview.Data.Workspace.Project.Followed {
		t.Fatal("overview missing follow state")
	}
	actor = identity.Principal{TenantKey: "tenant", AppID: "app", AccountID: "other", OpenID: "ou_other"}
	check("")
	request("PUT", "projects/private/follow", 404, nil)
	request("DELETE", follow, 200, nil)
	actor = testActor
	check("legacy")
	exec(`INSERT INTO project_profiles(tenant_key,app_id,project_id,name,status,version) VALUES ('tenant','app','legacy','归档项目','archived',1)`)
	check("归档项目")
	request("DELETE", follow, 200, nil)
	request("DELETE", follow, 200, nil)
	check("")
	request("PUT", follow, 200, nil)
	exec(`UPDATE accounts SET active=false WHERE id='relay-user'`)
	check("")
	exec(`UPDATE accounts SET active=true WHERE id='relay-user';
		UPDATE project_members SET role='revoked' WHERE account_id='relay-user' AND project_id='legacy'`)
	check("")
	request("PUT", follow, 404, nil)
}
