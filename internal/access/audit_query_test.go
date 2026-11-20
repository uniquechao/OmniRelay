package access

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"testing"
)

func TestAuditQueryContract(t *testing.T) {
	ctx, pool := testPool(t)
	service := New(pool, "tenant", "app")
	if err := service.Bootstrap(ctx, "ou_admin"); err != nil {
		t.Fatal(err)
	}
	admin := principal(t, ctx, pool, "tenant", "app", "ou_admin")
	member := addAccount(t, ctx, pool, "tenant", "app", "ou_member")
	foreign := addAccount(t, ctx, pool, "elsewhere", "app", "ou_foreign")
	mux := testMux(service, admin, member)
	const path = "/api/v1/system/audit-events?"
	insert := `INSERT INTO audit_events (tenant_key,app_id,actor_account_id,action,resource_type,resource_id,result,change_version,created_at)
		SELECT $1,'app',$2,CASE WHEN i=1 THEN 'identity.login' ELSE 'account.enable' END,'account',
		'样例-'||i,CASE WHEN i=1 THEN 'denied' ELSE 'success' END,1,'2025-01-01' FROM generate_series(1,$3::int) i`
	seed := func(tenant, actor string, n int) {
		t.Helper()
		if _, err := pool.Exec(ctx, insert, tenant, actor, n); err != nil {
			t.Fatal(err)
		}
	}
	seed("tenant", admin.AccountID, 10001)
	seed("elsewhere", foreign.AccountID, 5)
	get := func(query string) auditQueryPage {
		t.Helper()
		r := request(mux, http.MethodGet, path+query, admin, "")
		assertStatus(t, r, 200)
		var page auditQueryPage
		if err := json.Unmarshal(r.Body.Bytes(), &page); err != nil || page.Items == nil {
			t.Fatalf("分页响应无效 %v", err)
		}
		return page
	}

	const query = "q=样例&sort=time&direction=desc&limit=100"
	page := get(query)
	if len(page.Items) != 100 || page.Items[0].Kind != "account" {
		t.Fatal("首页未按时间和编号倒序")
	}
	firstCursor := page.NextCursor
	if _, err := pool.Exec(ctx, `INSERT INTO audit_events (tenant_key,app_id,actor_account_id,action,resource_type,resource_id,result,change_version)
		VALUES ('tenant','app',$1,'account.enable','account','样例-10002','success',1)`, admin.AccountID); err != nil {
		t.Fatal(err)
	}
	var last int64
	for n := 0; n <= 100; n++ {
		want := 100
		if n == 100 {
			want = 1
		}
		if len(page.Items) != want || page.Total != 10001 {
			t.Fatalf("第 %d 页条数 %d，总数 %d", n+1, len(page.Items), page.Total)
		}
		for i, e := range page.Items {
			wantTarget := fmt.Sprintf("样例-%d", 10001-100*n-i)
			if e.ResourceID != wantTarget || (last != 0 && e.ID >= last) {
				t.Fatalf("第 %d 页记录错序 %+v", n+1, e)
			}
			last = e.ID
		}
		if n < 100 {
			if page.NextCursor == "" {
				t.Fatal("续页缺少游标")
			}
			page = get(query + "&cursor=" + page.NextCursor)
		} else if page.NextCursor != "" {
			t.Fatal("末页仍有游标")
		}
	}
	if fresh := get(query); fresh.Total != 10002 {
		t.Fatal("刷新未看见新事件")
	}
	for _, sort := range []string{"id", "time", "action", "actor", "target", "result"} {
		for _, direction := range []string{"asc", "desc"} {
			q := "sort=" + sort + "&direction=" + direction + "&limit=1&q=" + url.QueryEscape("样例-")
			p := get(q)
			next := get(q + "&cursor=" + p.NextCursor)
			if len(next.Items) != 1 || p.Items[0].ID == next.Items[0].ID {
				t.Fatalf("排序续页错误 %s", q)
			}
		}
	}
	for _, q := range []string{"q=登录", "q=" + strconv.FormatInt(last, 10) + "&kind=login", "q=样例&kind=login&result=denied"} {
		p := get(q)
		if p.Total != 1 || len(p.Items) != 1 || p.Items[0].ID != last {
			t.Fatalf("深页命中错误 %s", q)
		}
	}
	if p := get("q=%25_"); p.Total != 0 || len(p.Items) != 0 || p.NextCursor != "" {
		t.Fatal("空结果不正确")
	}
	for _, q := range []string{"limit=101", "limit=0", "sort=details", "direction=sideways", "kind=other", "result=other", "q=%00", "q=%ff", "q=%zz", "sort=time&sort=id", "cursor=no", "sort=time&cursor=1", "sort=id&cursor=" + firstCursor, query + "&cursor=" + firstCursor + "1"} {
		assertStatus(t, request(mux, http.MethodGet, path+q, admin, ""), 400)
	}
	var denied string
	for _, q := range []string{query, "q=不存在", "cursor=broken", query + "&cursor=" + firstCursor} {
		r := request(mux, http.MethodGet, path+q, member, "")
		assertStatus(t, r, 403)
		if denied != "" && r.Body.String() != denied {
			t.Fatal("拒绝响应随查询变化")
		}
		var fields map[string]any
		if err := json.Unmarshal(r.Body.Bytes(), &fields); err != nil || len(fields) != 1 || fields["error"] == nil {
			t.Fatal("拒绝响应泄露分页信息")
		}
		denied = r.Body.String()
	}
	p := get("limit=1")
	id, err := strconv.ParseInt(p.NextCursor, 10, 64)
	if err != nil || id != p.Items[0].ID || get("limit=1&cursor=" + p.NextCursor).Items[0].ID >= id {
		t.Fatal("旧编号游标不兼容")
	}
}

func TestAuditQueryWebSample(t *testing.T) {
	ctx, pool := testPool(t)
	service := New(pool, "tenant", "app")
	if err := service.Bootstrap(ctx, "ou_admin"); err != nil {
		t.Fatal(err)
	}
	admin := principal(t, ctx, pool, "tenant", "app", "ou_admin")
	mux := testMux(service, admin)
	_, err := pool.Exec(ctx, `INSERT INTO accounts(id,tenant_key,app_id,open_id) VALUES ('user-2','tenant','app','actor');
		INSERT INTO audit_events(id,tenant_key,app_id,actor_account_id,action,resource_type,resource_id,result,change_version,created_at,correlation_id)
		OVERRIDING SYSTEM VALUE SELECT 9007199254740992+i,'tenant','app','user-2','identity.login','account','user-2','success',1,
		'2026-11-19 00:00:00Z'::timestamptz+i*interval '1 hour','example-'||(i+1) FROM generate_series(0,1)i`)
	if err != nil {
		t.Fatal(err)
	}
	var sample struct {
		Query                 map[string]any
		Response, Last, Empty auditQueryPage
	}
	data, err := os.ReadFile("../../tests/fixtures/audit-query.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &sample); err != nil {
		t.Fatal(err)
	}
	q := url.Values{}
	for k, v := range sample.Query {
		q.Set(k, fmt.Sprint(v))
	}
	check := func(want auditQueryPage) {
		t.Helper()
		r := request(mux, http.MethodGet, "/api/v1/system/audit-events?"+q.Encode(), admin, "")
		assertStatus(t, r, 200)
		var got auditQueryPage
		if err := json.Unmarshal(r.Body.Bytes(), &got); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("接口与前端样例不一致：%s", r.Body.String())
		}
	}
	check(sample.Response)
	q.Set("cursor", sample.Response.NextCursor)
	check(sample.Last)
	q.Del("cursor")
	q.Set("q", "无匹配")
	check(sample.Empty)
}
