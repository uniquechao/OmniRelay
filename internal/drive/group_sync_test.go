package drive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

type diskBucket string

func TestDriveSyncProgress(t *testing.T) {
	ctx, pool := archiveTestDB(t)
	actor := identity.Principal{TenantKey: "t", AppID: "a", AccountID: "alice", OpenID: "ou_alice"}
	readGroup := func(_ context.Context, _ identity.Principal, project, chat string) (bool, error) {
		return project == "p" && chat == "g1", nil
	}
	service := New(pool, "t", "a", 1, &testSource{}, access.New(pool, "t", "a"), testBucket(t), readGroup)
	mux := http.NewServeMux()
	service.Register(mux, func(http.ResponseWriter, *http.Request, identity.SessionPolicy) (identity.Principal, bool) {
		return actor, true
	})
	snapshot := func() syncProgress {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/feishu/files/sync", nil))
		var progress syncProgress
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &progress) != nil {
			t.Fatal(rec.Code, rec.Body.String())
		}
		return progress
	}
	if _, err := pool.Exec(ctx, `INSERT INTO feishu_chat_messages(tenant_key,app_id,message_id,chat_id,sent_at,updated_at,body,message_type)
		VALUES ('t','a','m1','g1',now(),now(),'{"text":"https://example.feishu.cn/docx/D"}','text'),
		       ('t','a','m2','g2',now(),now(),'{"text":"https://example.feishu.cn/docx/E"}','text'),
		       ('t','a','m3','g1',now(),now(),'{"text":"https://example.feishu.cn/wiki/W"}','text')`); err != nil {
		t.Fatal(err)
	}
	if p := snapshot(); p.Groups != 1 || p.Unindexed != 2 {
		t.Fatal(p)
	}
	if _, err := service.index(ctx); err != nil {
		t.Fatal(err)
	}
	if p := snapshot(); p.Pending != 1 || p.UnresolvedWiki != 1 || p.Unindexed != 0 {
		t.Fatal(p)
	}
	if _, err := pool.Exec(ctx, "UPDATE feishu_file_shares SET state='failed' WHERE message_id='m1'"); err != nil {
		t.Fatal(err)
	}
	if p := snapshot(); p.Pending != 0 || p.Attention != 1 {
		t.Fatal(p)
	}
	for range 2 {
		if worked, err := service.refresh(ctx); !worked || err != nil {
			t.Fatal(worked, err)
		}
	}
	if p := snapshot(); p.Complete != 1 || p.Attention != 0 {
		t.Fatal(p)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM project_chat_links WHERE chat_id='g1'"); err != nil {
		t.Fatal(err)
	}
	if p := snapshot(); p != (syncProgress{}) {
		t.Fatal(p)
	}
}

func testBucket(t *testing.T) ObjectStore {
	if os.Getenv("OMNIRELAY_TEST_S3") == "1" {
		objects, err := ConfiguredObjects()
		if err != nil || objects == nil {
			t.Fatalf("测试对象存储不可用：%v", err)
		}
		return objects
	}
	return diskBucket(t.TempDir())
}
func (b diskBucket) Put(_ context.Context, key string, body io.ReadSeeker, size int64) error {
	path := filepath.Join(string(b), key)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.CopyN(f, body, size)
	return err
}
func (b diskBucket) Discard(_ context.Context, key string) error {
	err := os.Remove(filepath.Join(string(b), key))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
func (b diskBucket) Open(_ context.Context, key string, size int64) (io.ReadSeekCloser, error) {
	return os.Open(filepath.Join(string(b), key))
}

func TestCurrentGroupAndVersionAccess(t *testing.T) {
	ctx, pool := archiveTestDB(t)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	src := &testSource{denied: map[string]bool{}, text: "甲版"}
	s := New(pool, "t", "a", 1, src, access.New(pool, "t", "a"), testBucket(t), testGroupAccess)
	actor := identity.Principal{TenantKey: "t", AppID: "a", AccountID: "alice", OpenID: "ou_alice"}
	mux := http.NewServeMux()
	s.Register(mux, func(http.ResponseWriter, *http.Request, identity.SessionPolicy) (identity.Principal, bool) {
		return actor, true
	})
	get := func(path string, status int) string {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/feishu/files/"+path, nil))
		if w.Code != status {
			t.Fatalf("%s：%d %s", path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	exec(`INSERT INTO project_members(tenant_key,app_id,project_id,account_id,member_id,role)
    VALUES('t','a','q','alice','n','reader'),('t','a','extra','alice','e','reader');
    DELETE FROM project_chat_links WHERE chat_id='g2'`)
	share := func(msg, chat string) {
		t.Helper()
		exec(`INSERT INTO feishu_chat_messages(tenant_key,app_id,message_id,chat_id,sent_at,updated_at,body,message_type)
        VALUES('t','a',$1,$2,now(),now(),'{"text":"https://example.feishu.cn/docx/D"}','text')`, msg, chat)
		if _, err := s.index(ctx); err != nil {
			t.Fatal(err)
		}
	}
	refresh := func() {
		t.Helper()
		if ok, err := s.refresh(ctx); !ok || err != nil {
			t.Fatal(ok, err)
		}
	}
	file := func(projects ...string) item {
		t.Helper()
		fs, err := s.visible(ctx, actor, 0, nil)
		if err != nil || len(fs) != 1 || !slices.Equal(fs[0].Projects, projects) {
			t.Fatal(fs, err)
		}
		return fs[0]
	}
	share("m1", "g1")
	refresh()
	f := file("p")
	first := *f.Version
	exec(`INSERT INTO feishu_file_manual_projects VALUES('t','a',$1,'extra')`, f.ID)
	if files, err := s.ProjectSources(ctx, actor, "extra"); err != nil || len(files) != 1 || files[0].ID != f.ID {
		t.Fatal(files, err)
	}
	share("m2", "g2")
	file("extra", "p")
	exec(`INSERT INTO project_chat_links VALUES('t','a','q','g2')`)
	if found, err := s.index(ctx); err != nil || !found {
		t.Fatal("绑定后未采集群历史", found, err)
	}
	refresh()
	both := file("extra", "p", "q")
	if both.ID != f.ID || *both.Version != first || len(both.Sources) != 2 {
		t.Fatal(both)
	}
	src.text = "乙版"
	exec("UPDATE feishu_file_shares SET next_at=now() WHERE message_id='m2'")
	refresh()
	second := *file("extra", "p", "q").Version
	if first == second {
		t.Fatal("正文更新未形成新版本")
	}
	// 分享时间与版本同步时间可能顺序相反。
	exec("UPDATE feishu_chat_messages SET sent_at=sent_at-interval '1 day' WHERE message_id='m2'")
	panel, err := s.ProjectSources(ctx, actor, "extra")
	if err != nil || len(panel) != 1 || panel[0].VersionID == nil || *panel[0].VersionID != second || panel[0].MessageID != "m2" {
		t.Fatalf("项目入口的版本和分享来源不一致：%v，%v", panel, err)
	}
	path := fmt.Sprintf("%d/versions/%d", f.ID, second)
	if got := get(path+"/content", 200); got != "乙版" {
		t.Fatal(got)
	}
	src.denied["m2"] = true
	file("extra", "p")
	get(path+"/content", 404)
	result := httptest.NewRecorder()
	mux.ServeHTTP(result, httptest.NewRequest("POST", "/api/v1/feishu/files/"+path+"/previews", nil))
	if result.Code != 404 {
		t.Fatal(result.Code)
	}
	if got := get(fmt.Sprintf("%d/versions/%d/content", f.ID, first), 200); got != "甲版" {
		t.Fatal(got)
	}
	exec("UPDATE project_chat_links SET project_id='q' WHERE chat_id='g1'")
	file("extra", "q")
	if fs, err := s.ProjectSources(ctx, actor, "p"); err != nil || len(fs) != 0 {
		t.Fatal(fs, err)
	}
	// 将已知来源的第一版切回旧分块，验证旧版地址和授权使用同一条读取链。
	exec(`INSERT INTO feishu_file_chunks SELECT file_id,digest,convert_to('甲版','UTF8') FROM feishu_file_versions WHERE id=$1`, first)
	exec(`UPDATE feishu_file_versions SET chunks=ARRAY[digest],object_key=NULL WHERE id=$1`, first)
	if got := get(fmt.Sprintf("%d/versions/%d/content", f.ID, first), 200); got != "甲版" {
		t.Fatal(got)
	}
	exec("DELETE FROM project_chat_links WHERE chat_id='g1'")
	get(fmt.Sprintf("%d/object", f.ID), 404)
}

func TestMediaReferences(t *testing.T) {
	refs := References("post", `{"content":[[{"tag":"img","image_key":"img1"},{"tag":"img","image_key":"img1"},{"tag":"media","file_key":"video1","image_key":"cover"}]]}`)
	if len(refs) != 2 {
		t.Fatal(refs)
	}
	if Format(refs[0].Kind, refs[0].Name).Key != "image" || Format(refs[1].Kind, refs[1].Name).Key != "video" {
		t.Fatal(refs)
	}
}

func testGroupAccess(context.Context, identity.Principal, string, string) (bool, error) {
	return true, nil
}

type resolvedSamples struct{ previewSampleSource }

func (*resolvedSamples) ResolveWiki(context.Context, string) (Ref, error) {
	return Ref{Kind: "docx", Token: "D"}, nil
}

func TestMediaAndWikiSync(t *testing.T) {
	ctx, pool := archiveTestDB(t)
	image := []byte("\x89PNG\r\n\x1a\n图片")
	video := []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom")
	src := &resolvedSamples{previewSampleSource{snapshots: map[string]Snapshot{
		"I": {Name: "项目图片", Content: image}, "V": {Name: "项目视频", Content: video}, "D": {Name: "项目文档", Content: []byte("文档正文")},
	}}}
	allow := true
	s := New(pool, "t", "a", 1, src, access.New(pool, "t", "a"), testBucket(t), func(context.Context, identity.Principal, string, string) (bool, error) { return allow, nil })
	exec := func(q string) {
		t.Helper()
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO feishu_chat_messages(tenant_key,app_id,message_id,chat_id,sent_at,updated_at,body,message_type)
    VALUES('t','a','picture','g1',now(),now(),'{"image_key":"I"}','image'),('t','a','movie','g1',now(),now(),'{"file_key":"V","image_key":"Cover"}','media'),
    ('t','a','wiki','g2',now(),now(),'{"text":"https://test.feishu.cn/wiki/W"}','text'),('t','a','doc','g1',now(),now(),'{"text":"https://test.feishu.cn/docx/D"}','text')`)
	index := func() {
		t.Helper()
		if ok, err := s.index(ctx); !ok || err != nil {
			t.Fatal(ok, err)
		}
	}
	index()
	if ok, err := s.resolveWiki(ctx); !ok || err != nil {
		t.Fatal(ok, err)
	}
	index()
	for range 4 {
		if ok, err := s.refresh(ctx); !ok || err != nil {
			t.Fatal(ok, err)
		}
	}
	actor := identity.Principal{AccountID: "alice", OpenID: "ou_alice", AppID: "a", TenantKey: "t"}
	files, err := s.visible(ctx, actor, 0, nil)
	if err != nil || len(files) != 3 {
		t.Fatal(files, err)
	}
	mux := http.NewServeMux()
	s.Register(mux, func(http.ResponseWriter, *http.Request, identity.SessionPolicy) (identity.Principal, bool) {
		return actor, true
	})
	for _, f := range files {
		if f.Version == nil {
			t.Fatal(f)
		}
		req := httptest.NewRequest("GET", fmt.Sprintf("/api/v1/feishu/files/%d/versions/%d/content", f.ID, *f.Version), nil)
		req.Header.Set("Range", "bytes=0-3")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != 206 || w.Body.Len() != 4 {
			t.Fatal(w.Code, w.Body.String())
		}
		switch f.FormatKey {
		case "docs":
			if len(f.Sources) != 2 {
				t.Fatal(f)
			}
		case "video":
			if w.Header().Get("Content-Type") != "video/mp4" || w.Body.String() != string(video[:4]) {
				t.Fatal(w.Header())
			}
		case "image":
			if w.Header().Get("Content-Type") != "image/png" || w.Body.String() != string(image[:4]) {
				t.Fatal(w.Header())
			}
		default:
			t.Fatal(f)
		}
		allow = false
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		allow = true
		if w.Code != 404 {
			t.Fatal("群成员失权后分段读取仍然成功", w.Code)
		}
	}
	exec("UPDATE feishu_chat_messages SET files_indexed=false")
	index()
	exec("UPDATE feishu_file_shares SET next_at=now()")
	for range 4 {
		if _, err = s.refresh(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var versions, shares, history int
	err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM feishu_file_versions),(SELECT count(*) FROM feishu_file_shares),(SELECT count(*) FROM feishu_file_share_versions)`).Scan(&versions, &shares, &history)
	if err != nil || versions != 3 || shares != 4 || history != 4 {
		t.Fatal(versions, shares, history, err)
	}
}

type interruptedUpload struct {
	ObjectStore
	after func() error
}

func (b interruptedUpload) Put(ctx context.Context, key string, body io.ReadSeeker, size int64) error {
	if err := b.ObjectStore.Put(ctx, key, body, size); err != nil {
		return err
	}
	return b.after()
}

func TestObjectPublicationRecovery(t *testing.T) {
	ctx, pool := archiveTestDB(t)
	src := &testSource{text: "待回收的私有正文", denied: map[string]bool{}}
	bucket := testBucket(t)
	s := New(pool, "t", "a", 1, src, access.New(pool, "t", "a"), bucket, testGroupAccess)
	exec := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO feishu_chat_messages(tenant_key,app_id,message_id,chat_id,sent_at,updated_at,body,message_type)
        VALUES('t','a','m','g1',now(),now(),'{"text":"https://example.feishu.cn/docx/Test"}','text')`)
	if _, err := s.index(ctx); err != nil {
		t.Fatal(err)
	}
	// 上传成功后模拟进程丢失平台响应，队列仍可恢复，外部对象不会作为完整版本发布。
	s.objects = interruptedUpload{bucket, func() error { return ErrUnavailable }}
	if _, err := s.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	var key string
	if err := pool.QueryRow(ctx, `SELECT object_key FROM feishu_sync_objects WHERE NOT published`).Scan(&key); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM feishu_file_share_versions`).Scan(&n); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	exec("UPDATE feishu_sync_objects SET reserved_until=now()-interval '1 second'")
	if err := s.discardAbandoned(ctx); err != nil {
		t.Fatal(err)
	}
	if object, err := bucket.Open(ctx, key, int64(len(src.text))); err == nil {
		object.Close()
		t.Fatal("无引用对象仍可读取")
	}
	// 在保存原件期间改绑；提交后只通过当前归属发布。
	s.objects = interruptedUpload{bucket, func() error {
		exec(`INSERT INTO project_members(tenant_key,app_id,project_id,account_id,member_id,role) VALUES('t','a','q','alice','n','reader');
        UPDATE project_chat_links SET project_id='q' WHERE chat_id='g1'`)
		return nil
	}}
	exec("UPDATE feishu_file_shares SET next_at=now()")
	if _, err := s.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	actor := identity.Principal{AccountID: "alice", OpenID: "ou_alice", AppID: "a", TenantKey: "t"}
	files, err := s.visible(ctx, actor, 0, nil)
	if err != nil || len(files) != 1 || files[0].Version == nil || !slices.Equal(files[0].Projects, []string{"q"}) {
		t.Fatal(files, err)
	}
	exec("UPDATE feishu_sync_objects SET reserved_until=now()-interval '1 day'")
	if err := s.discardAbandoned(ctx); err != nil {
		t.Fatal(err)
	}
	object, err := bucket.Open(ctx, key, int64(len(src.text)))
	if err != nil {
		t.Fatal(err)
	}
	object.Close()
}
