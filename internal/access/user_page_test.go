package access

import (
	"encoding/json"
	"fmt"
	"net/url"
	"testing"
)

// 人员、目录归属和角色必须先组合，再统计和截取页面。
func TestUserPageSnapshot(t *testing.T) {
	ctx, pool := testPool(t)
	s := New(pool, "tenant", "app")
	if err := s.Bootstrap(ctx, "ou_admin"); err != nil {
		t.Fatal(err)
	}
	admin := principal(t, ctx, pool, "tenant", "app", "ou_admin")
	local := addAccount(t, ctx, pool, "tenant", "app", "unlinked")
	_ = addAccount(t, ctx, pool, "other", "app", "foreign")
	mux := testMux(s, admin, local)
	_, err := pool.Exec(ctx, `
	INSERT INTO directory_sources(tenant_key,app_id) VALUES ('tenant','app');
	INSERT INTO directory_sync_runs(id,tenant_key,app_id,trigger,state) OVERRIDING SYSTEM VALUE VALUES(17,'tenant','app','initial','succeeded');
	UPDATE directory_sources SET visible=true,active_version=17;
	INSERT INTO departments VALUES ('tenant','app',17,'root',NULL,'研发','{研发}',true),('tenant','app',17,'child','root','一组','{研发,一组}',false);
	INSERT INTO directory_people SELECT 'tenant','app',17,'ou_'||i,'成员'||i,'','active' FROM generate_series(1,1001) i;
	INSERT INTO directory_people VALUES ('tenant','app',17,'ou_admin','管理员','','active');
	INSERT INTO department_memberships SELECT 'tenant','app',17,'child','ou_'||i FROM generate_series(1,1001) i;`)
	if err != nil {
		t.Fatal(err)
	}
	type row struct{ ID, Name, Org, PersonKey string }
	type page struct {
		Items   []row
		Total   int
		Version *int64 `json:"snapshot_version"`
	}
	const path = "/api/v1/system/user-page?limit=10&"
	get := func(q string) page {
		t.Helper()
		r := request(mux, "GET", path+q, admin, "")
		assertStatus(t, r, 200)
		var p page
		if err := json.Unmarshal(r.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := get("offset=0")
	if p.Total != 1003 || len(p.Items) != 10 || *p.Version != 17 {
		t.Fatalf("首页 %+v", p)
	}
	p = get("offset=10&sort=name&direction=asc&department_id=root&status=unprovisioned")
	if p.Total != 1001 || p.Items[0].Name != "成员11" || p.Items[0].Org != "研发 / 一组" {
		t.Fatalf("子部门第二页 %+v", p)
	}
	for term, want := range map[string]int{"成员1001": 1, "系统管理员": 1, "普通用户": 2, "研发 / 一组": 1001, local.AccountID: 1} {
		p = get("offset=0&q=" + url.QueryEscape(term))
		if p.Total != want {
			t.Fatalf("%s 总数 %d", term, p.Total)
		}
	}
	p = get("offset=0&sort=name&direction=desc&department_id=root")
	if p.Items[0].Name != "成员1001" {
		t.Fatal(p.Items)
	}
	for _, query := range []string{"offset=0&snapshot_version=16", "offset=0&snapshot_version=0"} {
		assertStatus(t, request(mux, "GET", path+query, admin, ""), 409)
	}
	assertStatus(t, request(mux, "GET", path+"offset=0", local, ""), 403)
	if _, err = pool.Exec(ctx, "UPDATE directory_sources SET visible=false"); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, request(mux, "GET", path+"offset=0&snapshot_version=17", admin, ""), 409)
	p = get("offset=0&snapshot_version=0")
	if p.Total != 2 || p.Version != nil || p.Items[0].Org != "" {
		t.Fatal(fmt.Sprint(p))
	}
}
