package drive

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/migrations"
)

type testSource struct {
	denied     map[string]bool
	err        error
	attachment func() io.Reader
	text       string
}

func (s *testSource) CheckShare(_ context.Context, r Ref, _ string) error {
	if s.denied[r.MessageID] {
		return ErrRevoked
	}
	return nil
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
	return Snapshot{Name: "已同步文档", Content: []byte(s.text)}, nil
}
func TestFileArchiveAccess(t *testing.T) {
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
 INSERT INTO project_chat_links VALUES('t','a','p','g1'),('t','a','p','g2');
 INSERT INTO feishu_chat_messages(tenant_key,app_id,message_id,chat_id,sent_at,updated_at,body,message_type)
 VALUES('t','a','m1','g1',now(),now(),'{"text":"https://example.feishu.cn/docx/Token"}','text'),
 ('t','a','m2','g2',now(),now(),'{"text":"https://example.feishu.cn/docx/Token"}','text'),
 ('t','a','m3','g1',now(),now(),'{"file_key":"file1","file_name":"空.pdf"}','file')`)
	src := &testSource{denied: map[string]bool{}, text: "第一版"}
	s := New(pool, "t", "a", 1, src)
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
	mux := http.NewServeMux()
	s.Register(mux, func(http.ResponseWriter, *http.Request, identity.SessionPolicy) (identity.Principal, bool) {
		return identity.Principal{TenantKey: "t", AppID: "a", AccountID: "alice", OpenID: "ou_alice"}, true
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
	_, docs := page("?category=docs")
	if len(docs) != 1 || len(docs[0].Sources) != 2 || docs[0].Size != nil || docs[0].SizeSource != "unknown" {
		t.Fatal(docs)
	}
	path := "/" + strconv.FormatInt(docs[0].ID, 10)
	history := path + "/versions/" + strconv.FormatInt(*docs[0].Version, 10) + "/content"
	if get(history, 200) != "第一版" {
		t.Fatal("历史正文不符")
	}
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
	if get(path+"/object", 200) != "第二版" || get(history, 200) != "第一版" {
		t.Fatal("版本恢复失败")
	}
	src.denied["m2"] = true
	_, docs = page("?category=docs")
	if len(docs) != 1 || len(docs[0].Sources) != 1 || docs[0].Sources[0].MessageID != "m1" {
		t.Fatal(docs)
	}
	exec("UPDATE project_members SET role='revoked'")
	if total, _ := page(""); total != 0 {
		t.Fatal(total)
	}
	get(history, 404)
	get(path+"/object", 404)
	exec("UPDATE project_members SET role='reader'")
	exec("DELETE FROM project_chat_links WHERE chat_id='g1'")
	if total, _ := page(""); total != 0 {
		t.Fatal(total)
	}
	get(path+"/versions", 404)
	get(history, 404)
	get(path+"/object", 404)
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
	exec("DELETE FROM feishu_file_projects")
	get(object, 404)
	if total, _ := page(""); total != 0 {
		t.Fatal(total)
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
