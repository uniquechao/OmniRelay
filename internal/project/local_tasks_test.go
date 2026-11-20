package project

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

type testTaskGrant struct{}

func (testTaskGrant) ResolveProjectAccess(context.Context, identity.Principal, string) (access.ProjectAccess, error) {
	return access.ProjectAccess{Member: true, Manager: true, CanAssign: true, MembershipGeneration: "1"}, nil
}
func (testTaskGrant) HasProjectAccess(context.Context, identity.Principal) (bool, error) {
	return true, nil
}

func TestLocalTaskHTTPContracts(t *testing.T) {
	pool := testProjectPool(t, "g1_local_tasks")
	if _, err := pool.Exec(t.Context(), "INSERT INTO accounts(id,tenant_key,app_id,open_id,active) VALUES('relay-user','tenant','app','ou_reader',true)"); err != nil {
		t.Fatal(err)
	}
	roles := access.New(pool, "tenant", "app")
	roles.ProjectAccess = testTaskGrant{}
	svc := Service{pool: pool, roles: roles}
	mux := http.NewServeMux()
	svc.Register(mux, func(w http.ResponseWriter, r *http.Request, p identity.SessionPolicy) (identity.Principal, bool) {
		if r.Method != "GET" && p != identity.RefreshSessionWithCSRF {
			t.Error("写操作缺少会话校验")
		}
		return testActor, true
	})
	call := func(method, path, body string) (int, string) {
		r := httptest.NewRequest(method, "/api/v1/projects/"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	request := func(method, path, body string, want int) map[string]any {
		t.Helper()
		code, raw := call(method, path, body)
		if code != want {
			t.Fatalf("%s %s 得到 %d %s", method, path, code, raw)
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(raw), &obj); err != nil {
			t.Fatal(err)
		}
		return obj
	}
	create := func(path, body string) string { return request("POST", path, body, 200)["id"].(string) }
	base := "a/tasks"
	first := create(base, `{"title":"合同草案","owner_member_id":"u","tag_ids":["urgent"],"priority":"P0","due_at":"2026-11-22"}`)
	m := create("a/milestones", fmt.Sprintf(`{"title":"验收","task_id":%q}`, first))
	foreign := create("b/milestones", `{"title":"其他项目"}`)
	request("GET", base+"?milestone_id="+foreign, "", 400)
	request("POST", base, fmt.Sprintf(`{"title":"跨项目","milestone_id":%q}`, foreign), 400)
	request("POST", "b/milestones", fmt.Sprintf(`{"title":"跨项目节点","task_id":%q}`, first), 400)
	second := create(base, fmt.Sprintf(`{"title":"合同复核","owner_member_id":"u","milestone_id":%q}`, m))
	create(base, fmt.Sprintf(`{"title":"合同复核","owner_member_id":"v","milestone_id":%q}`, m))
	create(base, fmt.Sprintf(`{"title":"评审","owner_member_id":"u","milestone_id":%q}`, m))
	board := func(filter string) taskBoard {
		raw := request("GET", base+"?q=合同&owner_member_id=u"+filter, "", 200)["data"]
		b, _ := json.Marshal(raw)
		var got taskBoard
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if board("").Total != 2 {
		t.Fatal("全部筛选不符")
	}
	if got := board("&milestone_id=" + m); got.Total != 1 || got.Columns[0].Items[0].ID != second {
		t.Fatal("指定里程碑筛选不符")
	}
	got := board("&milestone_unassigned=true")
	task := got.Columns[0].Items[0]
	if got.Total != 1 || task.ID != first || task.PriorityLabel != "紧急" || len(task.Tags) != 1 {
		t.Fatal("节点绑定被误作任务归属或投影缺失")
	}
	detail := request("GET", base+"/"+first, "", 200)
	nodes := request("GET", "a/milestones", "", 200)["items"].([]any)
	overview := request("GET", "a/overview", "", 200)["data"].(map[string]any)
	shown := overview["milestones"].(map[string]any)["items"].([]any)
	if !reflect.DeepEqual(shown, nodes) {
		t.Fatal("概览节点与里程碑资源不一致")
	}
	if node := nodes[0].(map[string]any); !reflect.DeepEqual(node["task"], detail) || node["owner_member_id"] != "u" {
		t.Fatal("节点未读取同一正式任务")
	}
	update := func(milestone string, want int) {
		request("PATCH", base+"/"+first+"/milestone", fmt.Sprintf(`{"expected_task_version":1,"milestone_id":%q}`, milestone), want)
	}
	update(foreign, 400)
	update(m, 200)
	if board("&milestone_unassigned=true").Total != 0 {
		t.Fatal("修改归属后筛选未更新")
	}
	got = board("&milestone_id=" + m)
	move := moveCommand{ExpectedTaskVersion: 2, ExpectedBoardVersion: got.Version, TargetColumnID: "in_progress", RequestKey: "r1"}
	path := base + "/" + first + "/move"
	send := func(c moveCommand, want int) map[string]any {
		raw, _ := json.Marshal(c)
		return request("POST", path, string(raw), want)
	}
	stale := move
	stale.ExpectedTaskVersion--
	if got := send(stale, 409)["error"]; got != "task_version_conflict" {
		t.Fatalf("冲突原因错误 %v", got)
	}
	stale = move
	stale.ExpectedBoardVersion--
	if got := send(stale, 409)["error"]; got != "board_version_conflict" {
		t.Fatalf("冲突原因错误 %v", got)
	}
	var codes [2]int
	var results [2]string
	var wg sync.WaitGroup
	body, _ := json.Marshal(move)
	for i := range codes {
		wg.Add(1)
		go func() { defer wg.Done(); codes[i], results[i] = call("POST", path, string(body)) }()
	}
	wg.Wait()
	if codes != [2]int{200, 200} || results[0] != results[1] {
		t.Fatalf("并发重试回执不一致 %v %v", codes, results)
	}
	latest := request("GET", base+"/"+first, "", 200)
	if latest["board_version"] != float64(got.Version+1) || latest["version"] != float64(3) {
		t.Fatal("重试重复修改了版本")
	}
	stale = move
	stale.TargetColumnID = "done"
	if got := send(stale, 409)["error"]; got != "move_request_key_conflict" {
		t.Fatalf("冲突原因错误 %v", got)
	}
	next := move
	next.ExpectedBoardVersion++
	next.ExpectedTaskVersion++
	next.RequestKey = "r2"
	next.TargetColumnID = "done"
	send(next, 200)
	if _, replay := call("POST", path, string(body)); replay != results[0] {
		t.Fatal("后续移动覆盖了原回执")
	}
}
func TestTaskBusinessClock(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	ny, _ := time.LoadLocation("America/New_York")
	cases := []struct {
		instant, due, status, label string
		zone                        *time.Location
		alert                       bool
	}{
		{"2026-12-30T15:59:59Z", "2027-01-03", "todo", "2027/1/3", sh, false},
		{"2026-12-30T16:00:00Z", "2027-01-03", "todo", "2027/1/3", sh, true},
		{"2026-12-31T16:00:00Z", "2026-12-31T16:30:00Z", "todo", "1/1", sh, true},
		{"2026-12-31T16:00:00Z", "2026-12-30", "todo", "2026/12/30", sh, true},
		{"2026-12-31T16:00:00Z", "2027-01-01", "done", "1/1", sh, false},
		{"2026-12-31T16:00:00Z", "2027-01-01", "cancelled", "1/1", sh, false},
		{"2026-12-31T16:00:00Z", "", "todo", "", sh, false},
		{"2026-10-30T05:00:00Z", "2026-11-02", "todo", "11/2", ny, true},
	}
	for _, c := range cases {
		instant, _ := time.Parse(time.RFC3339, c.instant)
		task := storedTask{Status: c.status}
		if c.due != "" {
			task.Due = &c.due
		}
		if err := task.present(instant.In(c.zone), 1); err != nil || task.DueLabel != c.label || task.DueAlert != c.alert {
			t.Errorf("业务日边界 %v 得到 %v", c, task)
		}
	}
}
