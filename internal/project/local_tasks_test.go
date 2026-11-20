package project

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

func TestLocalTaskHTTPContracts(t *testing.T) {
	pool := testProjectPool(t, "g1_local_tasks")
	if _, err := pool.Exec(t.Context(), "INSERT INTO accounts(id,tenant_key,app_id,open_id,name,active) VALUES('relay-user','tenant','app','ou_reader','真实用户',true)"); err != nil {
		t.Fatal(err)
	}
	roles := access.New(pool, "tenant", "app")
	roles.ProjectAccess = access.LocalProjects{Pool: pool}
	if err := roles.Bootstrap(t.Context(), testActor.OpenID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), "INSERT INTO accounts(id,tenant_key,app_id,open_id,name,active) VALUES('second','tenant','app','ou_second','第二人',true)"); err != nil {
		t.Fatal(err)
	}
	secondActor := identity.Principal{AccountID: "second", OpenID: "ou_second", TenantKey: "tenant", AppID: "app"}
	actor := testActor
	groupSource := &assignmentSource{}
	svc := Service{pool: pool, roles: roles, chat: chat.New(pool, "tenant", "app", 1, groupSource, nil, roles.Check, chat.ReceiptSources{})}
	mux := http.NewServeMux()
	svc.Register(mux, func(w http.ResponseWriter, r *http.Request, p identity.SessionPolicy) (identity.Principal, bool) {
		if r.Method != "GET" && p != identity.RefreshSessionWithCSRF {
			t.Error("写操作缺少会话校验")
		}
		return actor, true
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
	actor = secondActor
	request("PUT", "a/members/second/access", `{"role":"manager"}`, 404)
	actor = testActor
	request("POST", base, `{"title":"尚未授权"}`, 404)
	request("PUT", "a/members/relay-user/access", `{"role":"manager","member_id":"u"}`, 200)
	request("PUT", "b/members/relay-user/access", `{"role":"manager","member_id":"u"}`, 200)
	request("PUT", "a/members/second/access", `{"role":"reader","member_id":"v"}`, 200)
	request("PUT", "a/members/relay-user/access", `{"role":"revoked"}`, 409)
	actor = secondActor
	request("PUT", "a/members/second/access", `{"role":"manager"}`, 404)
	request("PUT", "b/members/second/access", `{"role":"manager"}`, 404)
	actor = testActor
	request("PUT", "a/task-tags/urgent", `{"name":"紧急"}`, 200)
	request("GET", "a/members", "", 200)
	first := create(base, `{"title":"合同草案","owner_member_id":"u","tag_ids":["urgent"],"priority":"P0","due_at":"2026-11-22"}`)
	record := request("GET", base+"/"+first, "", 200)
	if record["created_by"] != "relay-user" || record["updated_by_name"] != "真实用户" || record["created_at"] == nil {
		t.Fatal(record)
	}
	m := create("a/milestones", fmt.Sprintf(`{"title":"验收","task_id":%q}`, first))
	foreign := create("b/milestones", `{"title":"其他项目"}`)
	request("GET", base+"?milestone_id="+foreign, "", 400)
	request("POST", base, fmt.Sprintf(`{"title":"跨项目","milestone_id":%q}`, foreign), 400)
	request("POST", "b/milestones", fmt.Sprintf(`{"title":"跨项目节点","task_id":%q}`, first), 400)
	second := create(base, fmt.Sprintf(`{"title":"合同复核","owner_member_id":"u","milestone_id":%q}`, m))
	create(base, fmt.Sprintf(`{"title":"合同复核","owner_member_id":"v","milestone_id":%q}`, m))
	create(base, fmt.Sprintf(`{"title":"评审","owner_member_id":"u","milestone_id":%q}`, m))
	other := create("b/tasks", fmt.Sprintf(`{"title":"合同复核","owner_member_id":"u","milestone_id":%q}`, foreign))
	projectBoard := func(path, filter string) taskBoard {
		raw := request("GET", path+"?q=合同&owner_member_id=u"+filter, "", 200)["data"]
		b, _ := json.Marshal(raw)
		var got taskBoard
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	board := func(filter string) taskBoard { return projectBoard(base, filter) }
	if b := projectBoard("b/tasks", "&milestone_id="+foreign); b.Total != 1 || b.Columns[0].Items[0].ID != other {
		t.Fatal("另一项目联合筛选结果不符")
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
	afterConflict := board("&milestone_id=" + m)
	if !reflect.DeepEqual(got, afterConflict) {
		t.Fatal("移动失败改变了看板")
	}
	count := 0
	for _, column := range afterConflict.Columns {
		for _, item := range column.Items {
			if item.ID == first {
				count++
				if item.Milestone == nil || *item.Milestone != m || item.Status != "todo" {
					t.Fatal("修改归属后卡片内容错误")
				}
			}
		}
	}
	if count != 1 {
		t.Fatalf("移动失败后找到 %d 张同一任务的卡片", count)
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
	detailPath := base + "/" + second
	updateBody := `{"expected_task_version":1,"request_key":"save","title":"新标题","owner_member_id":"u","participant_ids":["v"],"acceptance":"交付完整"}`
	saved := request("PUT", detailPath, updateBody, 200)
	if !reflect.DeepEqual(saved, request("PUT", detailPath, updateBody, 200)) {
		t.Fatal("保存回执不一致")
	}
	request("PUT", detailPath, strings.Replace(updateBody, "新标题", "另一标题", 1), 409)
	request("PUT", detailPath, strings.Replace(updateBody, "save", "other", 1), 409)
	for _, body := range []string{
		`{"expected_task_version":2,"title":"非法字段","actor_id":"代写"}`,
		`{"expected_task_version":2,"title":"无此人","owner_member_id":"foreign"}`,
		`{"expected_task_version":2,"title":"无标签","tag_ids":["foreign"]}`,
		`{"expected_task_version":2,"title":"受阻","status":"blocked"}`,
		`{"expected_task_version":2,"title":"取消","status":"cancelled"}`,
	} {
		request("PUT", detailPath, body, 400)
	}
	if detail := request("GET", detailPath, "", 200); detail["version"] != float64(2) {
		t.Fatal("非法命令改变版本")
	}
	comments := detailPath + "/comments"
	cbody := `{"expected_version":2,"request_key":"c1","body":"第一条"}`
	for i := range codes {
		wg.Add(1)
		go func() { defer wg.Done(); codes[i], results[i] = call("POST", comments, cbody) }()
	}
	wg.Wait()
	if codes != [2]int{200, 200} || results[0] != results[1] {
		t.Fatalf("重复评论 %v %v", codes, results)
	}
	request("POST", comments, strings.Replace(cbody, "第一条", "不同正文", 1), 409)
	request("POST", comments, `{"expected_version":2,"request_key":"c2","body":"第二条"}`, 200)
	page := request("GET", detailPath+"/activity?kind=comment&limit=1", "", 200)
	event := page["items"].([]any)[0].(map[string]any)
	if event["actor_id"] != "relay-user" || event["actor_name"] != "真实用户" || event["body"] != "第二条" || event["created_at"] == nil {
		t.Fatal(event)
	}
	request("POST", comments, `{"expected_version":2,"request_key":"c3","body":"新插入"}`, 200)
	older := request("GET", detailPath+"/activity?kind=comment&limit=1&cursor="+url.QueryEscape(page["next_cursor"].(string)), "", 200)
	if older["next_cursor"] != nil || older["items"].([]any)[0].(map[string]any)["body"] != "第一条" {
		t.Fatal(older)
	}
	history := request("GET", detailPath+"/activity?kind=change", "", 200)["items"].([]any)
	if len(history) != 2 || history[0].(map[string]any)["after"].(map[string]any)["标题"] != "新标题" {
		t.Fatal(history)
	}
	source := "a/sources/document/doc1"
	request("PUT", source, `{"title":"敏感标题","body":"敏感正文","reader_account_ids":["relay-user"]}`, 200)
	request("PUT", detailPath+"/evidence/document/doc1", "", 200)
	request("GET", source+"/tasks", "", 200)
	_, shownEvidence := call("GET", detailPath+"/evidence", "")
	if !strings.Contains(shownEvidence, "敏感正文") {
		t.Fatal(shownEvidence)
	}
	actor = secondActor
	request("GET", source+"/tasks", "", 404)
	request("PUT", source+"/readers", `{"reader_account_ids":["second"]}`, 404)
	actor = testActor
	request("PUT", source+"/readers", `{"reader_account_ids":[]}`, 200)
	request("GET", source+"/tasks", "", 404)
	code, hidden := call("GET", detailPath+"/evidence", "")
	if code != 200 || strings.Contains(hidden, "敏感") {
		t.Fatal(hidden)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO feishu_bot_configs(tenant_key,app_id,credentials_generation,enabled) VALUES('tenant','app',1,true);
 INSERT INTO feishu_chat_conversations(tenant_key,app_id,chat_id,kind) VALUES('tenant','app','oc_test','group');
 INSERT INTO project_chat_links(tenant_key,app_id,chat_id,project_id) VALUES('tenant','app','oc_test','a');
 INSERT INTO feishu_chat_messages(tenant_key,app_id,message_id,chat_id,sent_at,updated_at,body,visible_in_chat) VALUES('tenant','app','msg1','oc_test',now(),now(),'受控群消息',true)`); err != nil {
		t.Fatal(err)
	}
	request("PUT", "a/sources/message/msg1", `{"chat_id":"oc_test","reader_account_ids":["relay-user"]}`, 200)
	request("PUT", detailPath+"/evidence/message/msg1", "", 200)
	checkMessage := func(want bool) {
		t.Helper()
		status, body := call("GET", detailPath+"/evidence", "")
		if status != 200 || strings.Contains(body, "受控群消息") != want {
			t.Fatal(status, body)
		}
	}
	checkMessage(true)
	groupSource.denied.Store(true)
	checkMessage(false)
	groupSource.denied.Store(false)
	if _, err := pool.Exec(t.Context(), "UPDATE feishu_chat_messages SET deleted=true,body='' WHERE message_id='msg1'"); err != nil {
		t.Fatal(err)
	}
	checkMessage(false)
	fb := request("POST", detailPath+"/update-suggestions", `{"task_version":2,"request_key":"f1","proposed_status":"blocked","note":"等待评审"}`, 200)
	reviewPath := "/task-update-suggestions/" + fb["id"].(string) + "/review"
	review := `{"expected_version":1,"action":"accept","reason":"已核对"}`
	request("POST", "b"+reviewPath, review, 404)
	request("POST", "a"+reviewPath, review, 200)
	request("POST", "a"+reviewPath, review, 200)
	request("POST", "a"+reviewPath, strings.Replace(review, "已核对", "另一理由", 1), 409)
	updated := request("GET", detailPath, "", 200)
	if updated["status"] != "blocked" || updated["version"] != float64(3) || updated["blocked_reason"] != "等待评审" {
		t.Fatal(updated)
	}

	// 经撤权的请求即使有旧回执，也必须重新走动作授权。
	actor = secondActor
	request("PUT", detailPath, updateBody, 404)

	request("POST", comments, cbody, 404)
	request("PUT", detailPath, strings.Replace(updateBody, "新标题", "越权标题", 1), 404)
	request("POST", "a"+reviewPath, review, 404)
	actor = testActor
	request("PUT", "a/members/second/access", `{"role":"member"}`, 200)
	actor = secondActor
	actions := request("GET", detailPath+"/actions", "", 200)
	if actions["edit_fields"] != false || actions["update_progress"] != true || actions["comment"] != true {
		t.Fatal(actions)
	}
	progress := `{"expected_task_version":3,"request_key":"progress","title":"新标题","owner_member_id":"u","participant_ids":["v"],"acceptance":"交付完整","status":"in_progress"}`
	request("PUT", detailPath, progress, 200)
	actor = testActor
	request("PUT", "a/members/second/access", `{"role":"revoked"}`, 200)
	actor = secondActor
	request("PUT", detailPath, progress, 404)
	actor = testActor
	suggestion := request("POST", detailPath+"/update-suggestions", `{"task_version":4,"request_key":"f2","proposed_status":"done","note":"已核查"}`, 200)
	// 两个不同的保存命令从同一版本开始，只有一个可以提交。
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := fmt.Sprintf(`{"expected_task_version":4,"request_key":"race%d","title":"并发%d","owner_member_id":"u"}`, i, i)
			codes[i], _ = call("PUT", detailPath, body)
		}()
	}
	wg.Wait()
	if !(codes == [2]int{200, 409} || codes == [2]int{409, 200}) {
		t.Fatal(codes)
	}
	staleReview := "a/task-update-suggestions/" + suggestion["id"].(string) + "/review"
	request("POST", staleReview, review, 409)
	request("POST", staleReview, strings.Replace(review, "accept", "reject", 1), 200)
	if task := request("GET", detailPath, "", 200); task["version"] != float64(5) {
		t.Fatal(task)
	}

}
func TestTaskBusinessClock(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	ny, _ := time.LoadLocation("America/New_York")
	cases := []struct {
		instant, due, status, label string
		zone                        *time.Location
		warning                     string
	}{
		{"2026-12-30T15:59:59Z", "2027-01-03", "todo", "2027/1/3", sh, ""},
		{"2026-12-30T16:00:00Z", "2027-01-03", "todo", "2027/1/3", sh, "临期"},
		{"2026-12-31T16:00:00Z", "2026-12-31T16:30:00Z", "todo", "1/1", sh, "今日到期"},
		{"2026-12-31T16:00:00Z", "2026-12-30", "todo", "2026/12/30", sh, "已逾期"},
		{"2026-12-31T16:00:00Z", "2027-01-01", "done", "1/1", sh, ""},
		{"2026-12-31T16:00:00Z", "2027-01-01", "cancelled", "1/1", sh, ""},
		{"2026-12-31T16:00:00Z", "", "todo", "", sh, ""},
		{"2026-10-30T05:00:00Z", "2026-11-02", "todo", "11/2", ny, "临期"},
	}
	for _, c := range cases {
		instant, _ := time.Parse(time.RFC3339, c.instant)
		task := storedTask{Status: c.status}
		if c.due != "" {
			task.Due = &c.due
		}
		if err := task.present(instant.In(c.zone), 1); err != nil || task.DueLabel != c.label || task.DueAlertLabel != c.warning || task.DueAlert != (c.warning != "") {
			t.Errorf("业务日边界 %v 得到 %v", c, task)
		}
	}
}
