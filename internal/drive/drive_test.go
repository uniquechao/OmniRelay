package drive

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/migrations"
)

type testSource struct {
	err        error
	attachment func() io.Reader
	text       string
}

func (s *testSource) ReadFile(_ context.Context, r Ref, probe bool) (Snapshot, error) {
	if !probe && s.err != nil {
		return Snapshot{}, s.err
	}
	if r.Kind == "attachment" {
		if s.attachment != nil && !probe {
			return Snapshot{Name: r.Name, Stream: io.NopCloser(s.attachment())}, nil
		}
		return Snapshot{Name: r.Name, Content: []byte{}}, nil
	}
	if r.Kind == "minutes" {
		return Snapshot{Name: "评审纪要", Content: []byte("会议正文")}, nil
	}
	if r.Kind == "base" {
		return Snapshot{Name: "任务登记", Content: []byte(`[{"编号":1}]`)}, nil
	}
	if r.Kind == "file" {
		return Snapshot{Name: "会议附件.xlsx", Content: []byte("表格")}, nil
	}
	return Snapshot{Name: "已同步文档", Content: []byte(s.text)}, nil
}
func archiveTestDB(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("OMNIRELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("需要隔离的 PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	root, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	var schema string
	if err := root.QueryRow(ctx, `SELECT 'files_' || replace(gen_random_uuid()::text,'-','')`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = root.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO feishu_bot_configs(tenant_key,app_id,credentials_generation,enabled) VALUES('t','a',1,true);
 INSERT INTO accounts(id,tenant_key,app_id,open_id) VALUES('alice','t','a','ou_alice');
 INSERT INTO project_members(tenant_key,app_id,project_id,account_id,member_id,role) VALUES('t','a','p','alice','m','reader');
 INSERT INTO feishu_chat_conversations(tenant_key,app_id,chat_id,kind) VALUES('t','a','g1','group'),('t','a','g2','group');
 INSERT INTO feishu_chat_sync(tenant_key,app_id,chat_id) VALUES('t','a','g1'),('t','a','g2');
 INSERT INTO project_chat_links VALUES('t','a','p','g1'),('t','a','p','g2')`)
	return ctx, pool
}

func TestFileArchiveAccess(t *testing.T) {
	ctx, pool := archiveTestDB(t)
	exec := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO feishu_chat_messages(tenant_key,app_id,message_id,chat_id,sent_at,updated_at,body,message_type)
 VALUES('t','a','m1','g1',now()-interval '3 days',now(),'{"text":"https://example.feishu.cn/docx/Token"}','text'),
 ('t','a','m2','g2',now()-interval '1 day',now(),'{"text":"https://example.feishu.cn/docx/Token"}','text'),
 ('t','a','m3','g1',now()-interval '2 days',now(),'{"file_key":"file1","file_name":"空.pdf"}','file')`)
	src := &testSource{text: "第一版"}
	roles := access.New(pool, "t", "a")
	roles.ProjectAccess = access.LocalProjects{Pool: pool}
	s := New(pool, "t", "a", 1, src, roles, testBucket(t), testGroupAccess)
	if worked, err := s.index(ctx); err != nil || !worked {
		t.Fatal(worked, err)
	}
	refresh := func(n int) {
		t.Helper()
		for range n {
			if worked, err := s.refresh(ctx); err != nil || !worked {
				t.Fatal(worked, err)
			}
		}
	}
	refresh(3)
	exec(`UPDATE feishu_file_shares SET synced_at=CASE WHEN message_id='m3' THEN now() ELSE now()-interval '1 hour' END`)
	mux := http.NewServeMux()
	account := "alice"
	s.Register(mux, func(http.ResponseWriter, *http.Request, identity.SessionPolicy) (identity.Principal, bool) {
		return identity.Principal{TenantKey: "t", AppID: "a", AccountID: account, OpenID: "ou_" + account}, true
	})
	get := func(path string, status int) string {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/feishu/files"+path, nil))
		if w.Code != status {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	page := func(q string) (int, []item) {
		t.Helper()
		var v struct {
			Total int
			Items []item
		}
		if err := json.Unmarshal([]byte(get(q, 200)), &v); err != nil {
			t.Fatal(err)
		}
		return v.Total, v.Items
	}
	total, items := page("?sort=size&order=desc&limit=1")
	if total != 2 || len(items) != 1 || items[0].Format != "PDF" || items[0].Size == nil || *items[0].Size != 0 {
		t.Fatal(total, items)
	}
	_, first := page("?limit=1")
	_, next := page("?offset=1&limit=1&sort=shared_at&order=desc")
	_, oldest := page("?limit=1&sort=shared_at&order=asc")
	_, lastSynced := page("?limit=1&sort=updated_at&order=desc")
	if len(first) != 1 || first[0].Category != "docs" || first[0].Sources[0].MessageID != "m2" ||
		len(next) != 1 || next[0].Format != "PDF" || len(oldest) != 1 || oldest[0].ID != next[0].ID ||
		len(lastSynced) != 1 || lastSynced[0].ID != next[0].ID {
		t.Fatalf("share order or sync order changed: %v %v %v %v", first, next, oldest, lastSynced)
	}
	_, docs := page("?category=docs")
	if len(docs) != 1 || len(docs[0].Sources) != 2 || docs[0].Size != nil || docs[0].SizeSource != "unknown" {
		t.Fatal(docs)
	}
	path := "/" + strconv.FormatInt(docs[0].ID, 10)
	t.Run("并发归属和来源定位", func(t *testing.T) {
		exec(`INSERT INTO project_members(tenant_key,app_id,project_id,account_id,member_id,role)
        VALUES('t','a','q','alice','mq','editor'),('t','a','r','alice','mr','manager')`)
		change := func(version int64, add, remove []string) int {
			body, _ := json.Marshal(projectChange{&version, add, remove})
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest("PATCH", "/api/v1/feishu/files"+path+"/projects", strings.NewReader(string(body))))
			return w.Code
		}
		initial := docs[0]
		if status := change(initial.ProjectVersion, []string{"q", "unknown"}, nil); status != 403 {
			t.Fatal(status)
		}
		type result struct {
			project string
			status  int
		}
		done := make(chan result, 2)
		for _, id := range []string{"q", "r"} {
			go func() { done <- result{id, change(initial.ProjectVersion, []string{id}, nil)} }()
		}
		first, second := <-done, <-done
		if first.status != 200 {
			first, second = second, first
		}
		if first.status != 200 || second.status != 409 {
			t.Fatal(first, second)
		}
		total, linked := page("?project_id=" + first.project)
		if total != 1 || linked[0].ID != initial.ID || !slices.Equal(linked[0].Sources, initial.Sources) {
			t.Fatal(linked)
		}
		if total, _ := page("?project_id=p&category=docs"); total != 1 {
			t.Fatal(total)
		}
		for _, src := range linked[0].Sources {
			if src.ContextURL != "/api/v1/feishu/chats/"+src.ChatID+"/messages/"+src.MessageID+"/context" {
				t.Fatal(src)
			}
		}
		// 冲突方重新读取当前版本，保留已经成功的并发修改。
		if status := change(linked[0].ProjectVersion, []string{second.project}, nil); status != 200 {
			t.Fatal(status)
		}
		_, retried := page("?project_id=" + second.project)
		if len(retried) != 1 || !slices.Contains(retried[0].Projects, first.project) || !slices.Equal(retried[0].Sources, initial.Sources) {
			t.Fatal(retried)
		}
		exec(`INSERT INTO accounts(id,tenant_key,app_id,open_id) VALUES('guest','t','a','ou_guest');
		 INSERT INTO project_members(tenant_key,app_id,project_id,account_id,member_id,role)
		 VALUES('t','a','q','guest','gq','manager'),('t','a','r','guest','gr','manager')`)
		func() {
			account = "guest"
			defer func() { account = "alice" }()
			if total, rows := page("?project_id=" + second.project + "&q=已同步文档"); total != 0 || len(rows) != 0 {
				t.Fatal(total, rows)
			}
			get(path, 404)
			get(path+"/object", 404)
		}()
		if status := change(retried[0].ProjectVersion, nil, []string{first.project}); status != 200 {
			t.Fatal(status)
		}
		if total, _ := page("?project_id=" + first.project); total != 0 {
			t.Fatal(total)
		}
		_, remaining := page("?project_id=p&category=docs")
		if len(remaining) != 1 || !slices.Equal(remaining[0].Sources, initial.Sources) {
			t.Fatal(remaining)
		}
		var audits int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM feishu_file_project_audit WHERE file_id=$1 AND project_id=$2 AND actor_id='alice'`, initial.ID, first.project).Scan(&audits); err != nil || audits != 2 {
			t.Fatal(audits, err)
		}
		exec("UPDATE feishu_file_shares SET state='revoked' WHERE message_id IN ('m1','m2')")
		if status := change(remaining[0].ProjectVersion, []string{"q"}, nil); status != 404 {
			t.Fatal(status)
		}
		exec("UPDATE feishu_file_shares SET state='complete' WHERE message_id IN ('m1','m2')")
	})
	history := path + "/versions/" + strconv.FormatInt(*docs[0].Version, 10) + "/content"
	if get(history, 200) != "第一版" {
		t.Fatal("历史正文不符")
	}
	preview := strings.TrimSuffix(history, "/content") + "/previews"
	create := func(status int) string {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/feishu/files"+preview, nil))
		if w.Code != status {
			t.Fatalf("创建预览：%d %s", w.Code, w.Body.String())
		}
		var value struct {
			ID string `json:"task_id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		return value.ID
	}
	taskID := create(202)
	taskPath := preview + "/" + taskID
	for deadline := time.Now().Add(12 * time.Second); ; {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/feishu/files"+taskPath, nil))
		if w.Code == 200 && strings.Contains(w.Body.String(), `"stage":"ready"`) {
			break
		}
		if w.Code != 202 || time.Now().After(deadline) {
			t.Fatalf("查询预览：%d %s", w.Code, w.Body.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(get(taskPath+"/result", 200), "第一版") || create(200) != taskID {
		t.Fatal("版本化缓存读取失败")
	}
	exec(`INSERT INTO accounts(id,tenant_key,app_id,open_id) VALUES('bob','t','a','ou_bob');
	 INSERT INTO project_members(tenant_key,app_id,project_id,account_id,member_id,role) VALUES('t','a','p','bob','b','reader')`)
	account = "bob"
	get(taskPath, 404)
	get(taskPath+"/result", 404)
	account = "alice"
	exec("UPDATE feishu_file_shares SET state='revoked' WHERE message_id IN ('m1','m2')")
	get(taskPath, 403)
	get(taskPath+"/result", 403)
	waiting := previewRequest{reader: identity.Principal{TenantKey: "t", AppID: "a", AccountID: "alice", OpenID: "ou_alice"}, file: docs[0], version: *docs[0].Version, sources: messageIDs(docs[0]), options: previewOptions{Page: 1}}
	if out := s.convertVersion(ctx, waiting); out.Stage != "forbidden" || len(out.Body) != 0 {
		t.Fatal("执行任务时未复核源权限", out.Stage)
	}
	create(403)
	exec("UPDATE feishu_file_shares SET state='complete' WHERE message_id IN ('m1','m2')")
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM feishu_file_versions").Scan(&n); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	exec(`UPDATE feishu_file_shares SET next_at=now() WHERE message_id='m1'`)
	src.err = ErrUnavailable
	refresh(1)
	var state string
	if err := pool.QueryRow(ctx, "SELECT state FROM feishu_file_shares WHERE message_id='m1'").Scan(&state); err != nil || state != "expired" {
		t.Fatal(state, err)
	}
	src.err = ErrPartial
	exec("UPDATE feishu_file_shares SET next_at=now() WHERE message_id='m1'")
	refresh(1)
	if err := pool.QueryRow(ctx, "SELECT state FROM feishu_file_shares WHERE message_id='m1'").Scan(&state); err != nil || state != "partial" {
		t.Fatal(state, err)
	}
	src.err = nil
	src.text = "第二版"
	exec("UPDATE feishu_file_shares SET next_at=now() WHERE message_id='m1'")
	refresh(1)
	if get(path+"/object", 200) != "第二版" || get(history, 200) != "第一版" || !strings.Contains(get(taskPath+"/result", 200), "第一版") {
		t.Fatal("版本恢复失败")
	}
	exec("UPDATE feishu_file_shares SET state='revoked' WHERE message_id='m2'")
	_, docs = page("?category=docs")
	if len(docs) != 1 || len(docs[0].Sources) != 1 || docs[0].Sources[0].MessageID != "m1" {
		t.Fatal(docs)
	}
	alice := identity.Principal{TenantKey: "t", AppID: "a", AccountID: "alice", OpenID: "ou_alice"}
	if sources, err := s.ProjectSources(ctx, alice, "p"); err != nil || len(sources) != 2 ||
		slices.ContainsFunc(sources, func(src ProjectSource) bool { return src.ChatID != "g1" }) {
		t.Fatal(sources, err)
	}
	if sources, err := s.ProjectSources(ctx, alice, "unrelated"); err != nil || len(sources) != 0 {
		t.Fatal(sources, err)
	}
	exec("UPDATE project_members SET role='revoked'")
	if total, _ := page(""); total != 0 {
		t.Fatal(total)
	}
	if sources, err := s.ProjectSources(ctx, alice, "p"); err != nil || len(sources) != 0 {
		t.Fatal(sources, err)
	}
	get(taskPath+"/result", 403)
	get(history, 404)
	get(path+"/object", 404)
	exec("UPDATE project_members SET role='reader'")
	exec("DELETE FROM project_chat_links WHERE chat_id='g1'")
	if total, rows := page(""); total != 2 || slices.ContainsFunc(rows, func(row item) bool { return len(row.InheritedProjects) != 0 }) {
		t.Fatal(total, rows)
	}
	get(path+"/versions", 200)
	get(history, 200)
	get(path+"/object", 200)
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM feishu_file_versions").Scan(&n); err != nil || n != 3 {
		t.Fatal(n, err)
	}
	exec("INSERT INTO project_chat_links VALUES('t','a','p','g1')")
	exec("UPDATE feishu_chat_messages SET deleted=true,body='',search_text='' WHERE message_id='m1'")
	get(history, 404)
	if total, _ := page("?category=docs"); total != 0 {
		t.Fatal(total)
	}
	object := "/" + strconv.FormatInt(items[0].ID, 10) + "/object"
	if get(object, 200) != "" {
		t.Fatal("零字节原件不符")
	}
	payload := strings.Repeat("a", 1<<20) + "末尾"
	src.attachment = func() io.Reader {
		return io.MultiReader(strings.NewReader(payload), iotest.ErrReader(io.ErrUnexpectedEOF))
	}
	exec("UPDATE feishu_file_shares SET next_at=now() WHERE message_id='m3'")
	refresh(1)
	if get(object, 200) != "" {
		t.Fatal("中断的下载覆盖了已保存原件")
	}
	src.attachment = func() io.Reader { return strings.NewReader(payload) }
	exec("UPDATE feishu_file_shares SET next_at=now() WHERE message_id='m3'")
	refresh(1)
	if get(object, 200) != payload {
		t.Fatal("分块原件未完整恢复")
	}
	original := "/" + strconv.FormatInt(items[0].ID, 10)
	oldObject := original + "/versions/" + strconv.FormatInt(*items[0].Version, 10) + "/content"
	if get(oldObject, 200) != "" {
		t.Fatal("更新后应保留零字节历史原件")
	}

	// 同名附件首次采集失败，再经部分覆盖恢复。按文件标识独立记录状态与版本。
	exec(`INSERT INTO feishu_chat_messages(tenant_key,app_id,message_id,chat_id,sent_at,updated_at,body,message_type)
 VALUES('t','a','m5','g1',now(),now(),'{"file_key":"file2","file_name":"空.pdf"}','file')`)
	if worked, err := s.index(ctx); err != nil || !worked {
		t.Fatal(worked, err)
	}
	query := "?category=files&format=pdf&project_id=p&q=空.pdf&sort=size&order=desc"
	src.attachment = func() io.Reader { return strings.NewReader("另一份原件") }
	for _, step := range []struct {
		failure error
		state   string
	}{{ErrUnavailable, "failed"}, {ErrPartial, "partial"}, {nil, "complete"}} {
		src.err = step.failure
		exec("UPDATE feishu_file_shares SET next_at=now() WHERE message_id='m5'")
		refresh(1)
		count, rows := page(query)
		if count != 2 || len(rows) != 2 || rows[0].ID != items[0].ID || rows[0].State != "complete" || rows[0].Size == nil || *rows[0].Size != int64(len(payload)) {
			t.Fatal("同名文件应独立计数并按大小排序", count, rows)
		}
		f := rows[1]
		if f.ID == rows[0].ID || f.Name != "空.pdf" || f.Format != "PDF" || f.State != step.state || len(f.Sources) != 1 || f.Sources[0].MessageID != "m5" || len(f.Projects) != 1 || f.Projects[0] != "p" {
			t.Fatal("同名文件的元数据或重试状态不符", f)
		}
		filePath := "/" + strconv.FormatInt(f.ID, 10) + "/object"
		if step.failure != nil {
			if f.Version != nil || f.Size != nil || f.SizeSource != "unknown" {
				t.Fatal("未完成的原件不应发布版本或大小", f)
			}
			get(filePath, 404)
		} else if f.Version == nil || f.Size == nil || *f.Size != int64(len("另一份原件")) || f.SizeSource != "original" || get(filePath, 200) != "另一份原件" {
			t.Fatal("重试后原件或字节数不符", f)
		}
	}
	exec("UPDATE feishu_file_shares SET state='revoked' WHERE message_id='m3'")
	if total, rows := page(query); total != 1 || len(rows) != 1 || rows[0].Sources[0].MessageID != "m5" {
		t.Fatal("资源撤权后应只剩另一份同名文件", total, rows)
	}
	for _, endpoint := range []string{original, original + "/versions", oldObject, object} {
		get(endpoint, 404)
	}
	exec(`INSERT INTO feishu_chat_messages(tenant_key,app_id,message_id,chat_id,sent_at,updated_at,body,message_type)
 VALUES('t','a','m4','g1',now(),now(),'{"text":"https://example.feishu.cn/file/Sheet"}','text')`)
	if worked, err := s.index(ctx); err != nil || !worked {
		t.Fatal(worked, err)
	}
	refresh(1)
	actor := identity.Principal{TenantKey: "t", AppID: "a", AccountID: "alice", OpenID: "ou_alice"}
	if got := s.CloudFormats(ctx, actor, []string{"Sheet"})["Sheet"].Key; got != "excel" {
		t.Fatal("云附件格式", got)
	}
	exec(`INSERT INTO feishu_chat_messages(tenant_key,app_id,message_id,chat_id,sent_at,updated_at,body,message_type)
 VALUES('t','a','m6','g1',now(),now(),'{"text":"https://example.feishu.cn/minutes/Minute https://example.feishu.cn/base/Base"}','text')`)
	if worked, err := s.index(ctx); err != nil || !worked {
		t.Fatal(worked, err)
	}
	refresh(2)
	for _, sample := range []struct{ category, format, name, body string }{
		{"minutes", "飞书妙记", "评审纪要", "会议正文"},
		{"tasks", "多维表格", "任务登记", `[{"编号":1}]`},
	} {
		total, rows := page("?project_id=p&category=" + sample.category)
		if total != 1 || len(rows) != 1 {
			t.Fatal(sample.category, total, rows)
		}
		f := rows[0]
		if f.Name != sample.name || f.Format != sample.format || f.State != "complete" || f.Version == nil || f.Size != nil || f.SizeSource != "unknown" || get("/"+strconv.FormatInt(f.ID, 10)+"/object", 200) != sample.body {
			t.Fatal("云内容的列表字段或归档内容不符", f)
		}
	}
	exec("DELETE FROM project_chat_links")
	if got := s.CloudFormats(ctx, actor, []string{"Sheet"})["Sheet"].Key; got != "excel" {
		t.Fatal("解绑后应继续显示已授权来源的类型", got)
	}
	if body := get("?project_id=p", 200); !strings.Contains(body, `"projects":{}`) {
		t.Fatal("解绑后仍可见项目选项", body)
	}
	get(object, 404)
	if total, _ := page(""); total != 4 {
		t.Fatal(total)
	}

}

func TestFileArchiveRespectsCandidateOwnership(t *testing.T) {
	ctx, pool := archiveTestDB(t)
	_, err := pool.Exec(ctx, `INSERT INTO feishu_chat_messages(tenant_key,app_id,message_id,chat_id,sent_at,updated_at,body,message_type)
		VALUES('t','a','m1','g1',now(),now(),'{"file_key":"test_file"}','file'),('t','a','m2','g1',now(),now(),'{"file_key":"regular_file"}','file')`)
	if err != nil {
		t.Fatal(err)
	}
	files := New(pool, "t", "a", 1, nil, nil, nil, testGroupAccess)
	if worked, err := files.index(ctx); err != nil || !worked {
		t.Fatal(worked, err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO feishu_candidate_message_runs
		(id,tenant_key,app_id,generation,candidate_sha,candidate_pod,sender_open_id,project_id,group_chat,private_chat,file_token,expires_at,budget,outbound,may_reply,may_reconcile)
		VALUES('run','t','a',1,'sha','pod','ou_alice','p','g1','','test_file',now()+interval '1 hour',0,false,false,false);
		INSERT INTO feishu_candidate_event_owners VALUES('t','a','m1','g1','run')`)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM feishu_file_sources WHERE message_id IN ('m1','m2')`).Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("wrong count of visible production shares: %d, %v", count, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE feishu_chat_messages SET files_indexed=false WHERE message_id='m1'`); err != nil {
		t.Fatal(err)
	}
	if worked, err := files.index(ctx); err != nil || worked {
		t.Fatal(worked, err)
	}
}

func TestFileReferences(t *testing.T) {
	refs := References("post", `{"content":[[{"tag":"a","href":"https://acme.feishu.cn/base/A12?table=1"},{"text":"https://acme.feishu.cn/base/A12 https://acme.larksuite.com/minutes/M1 https://acme.feishu.cn/file/F2"}]]}`)
	if len(refs) != 3 {
		t.Fatal(refs)
	}
	if got := References("text", `{"text":"https://feishu.cn.evil.example/docx/Invalid"}`); len(got) > 0 {
		t.Fatal(got)
	}
	for _, name := range []string{"a.PDF", "a.docx", "a.xlsx", "a.pptx", "a.zip"} {
		format := Format("attachment", name)
		if strings.Contains(format.Label, "其他") {
			t.Fatal(name)
		}
	}
}
