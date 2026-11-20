package drive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

type previewSampleSource struct {
	testSource
	snapshots map[string]Snapshot
}

func (s *previewSampleSource) ReadFile(_ context.Context, r Ref, probe bool) (Snapshot, error) {
	snap, ok := s.snapshots[r.Token]
	if !ok {
		return Snapshot{}, ErrUnavailable
	}
	if probe {
		snap.Content = nil
	}
	return snap, nil
}

func TestPreviewSamples(t *testing.T) {
	ctx, pool := archiveTestDB(t)
	src := &previewSampleSource{snapshots: map[string]Snapshot{}, testSource: testSource{denied: map[string]bool{}}}
	roles := access.New(pool, "t", "a")
	roles.ProjectAccess = access.LocalProjects{Pool: pool}
	s := New(pool, "t", "a", 1, src, roles, testBucket(t), testGroupAccess)
	mux := http.NewServeMux()
	s.Register(mux, func(http.ResponseWriter, *http.Request, identity.SessionPolicy) (identity.Principal, bool) {
		return identity.Principal{TenantKey: "t", AppID: "a", AccountID: "alice", OpenID: "ou_alice"}, true
	})
	request := func(method, path string, codes ...int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, "/api/v1/feishu/files"+path, nil))
		for _, code := range codes {
			if w.Code == code {
				return w
			}
		}
		t.Fatalf("%s %s：%d %s", method, path, w.Code, w.Body.String())
		return nil
	}
	file := func(name string) []byte {
		t.Helper()
		b, err := os.ReadFile("testdata/preview/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	syncFile := func(token, kind, name string, data []byte) item {
		t.Helper()
		src.snapshots[token] = Snapshot{Name: name, Content: data}
		messageType := "file"
		body := map[string]string{"file_key": token, "file_name": name}
		if kind != "attachment" {
			messageType = "text"
			body = map[string]string{"text": "https://example.feishu.cn/" + kind + "/" + token}
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		_, err = pool.Exec(ctx, `INSERT INTO feishu_chat_messages(tenant_key,app_id,message_id,chat_id,sent_at,updated_at,body,message_type)
 VALUES('t','a',$1,'g1',now(),now(),$2,$3) ON CONFLICT DO NOTHING`, token, string(encoded), messageType)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.index(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, "UPDATE feishu_file_shares SET next_at=now() WHERE message_id=$1", token); err != nil {
			t.Fatal(err)
		}
		if worked, err := s.refresh(ctx); err != nil || !worked {
			t.Fatal(worked, err)
		}
		var list struct{ Items []item }
		if err := json.Unmarshal(request("GET", "?q="+url.QueryEscape(name), 200).Body.Bytes(), &list); err != nil || len(list.Items) != 1 || list.Items[0].Version == nil {
			t.Fatal(name, list, err)
		}
		f := list.Items[0]
		if !bytes.Equal(request("GET", fmt.Sprintf("/%d/versions/%d/content", f.ID, *f.Version), 200).Body.Bytes(), data) {
			t.Fatal("版本原件不符", name)
		}
		return f
	}
	preview := func(f item, query, stage string) (string, []byte) {
		t.Helper()
		root := fmt.Sprintf("/%d/versions/%d/previews", f.ID, *f.Version)
		w := request("POST", root+query, 200, 202)
		for deadline := time.Now().Add(12 * time.Second); ; {
			var task struct {
				ID      string `json:"task_id"`
				Version int64  `json:"version_id"`
				Stage   string
			}
			if err := json.Unmarshal(w.Body.Bytes(), &task); err != nil || task.Version != *f.Version || task.ID == "" {
				t.Fatal(f.Name, w.Body.String(), err)
			}
			path := root + "/" + task.ID
			if task.Stage == stage {
				return path, request("GET", path+"/result", 200).Body.Bytes()
			}
			if (task.Stage != "running" && task.Stage != "queued") || time.Now().After(deadline) {
				t.Fatal(f.Name, query, stage, w.Body.String())
			}
			time.Sleep(20 * time.Millisecond)
			w = request("GET", path, 200, 202)
		}
	}
	samples := []struct {
		token, kind, name, query, expected string
		data                               []byte
	}{
		{"Docs", "docx", "评审正文", "?page=2", "正文结论", []byte(strings.Repeat("甲", 4096) + "正文结论")},
		{"Minutes", "minutes", "讨论记录", "?page=2", "会议决议", []byte(strings.Repeat("乙", 4096) + "会议决议")},
		{"Base", "base", "进度登记", "?sheet=1", "验收通过", []byte(`[{"name":"事项","records":[]},{"name":"结果","records":[{"fields":{"状态":"验收通过"}}]}]`)},
		{"Word", "attachment", "评审.docx", "", "负责人", file("sample.docx")},
		{"Excel", "attachment", "明细.xlsx", "?sheet=1&page=2", "53", file("sample.xlsx")},
		{"Slides", "attachment", "结论.pptx", "?page=2", "结论", file("sample.pptx")},
		{"Zip", "attachment", "材料.zip", "", "材料/说明.txt", file("sample.zip")},
	}
	for _, v := range samples {
		f := syncFile(v.token, v.kind, v.name, v.data)
		_, body := preview(f, v.query, "ready")
		if !bytes.Contains(body, []byte(v.expected)) {
			t.Fatal(v.name, string(body))
		}
		if v.token == "Zip" {
			_, nested := preview(f, "?entry=4&sheet=1&page=2", "ready")
			if !bytes.Contains(nested, []byte("53")) {
				t.Fatal("归档内的工作表末页不符", string(nested))
			}
		}
	}
	pdf := file("scan.pdf")
	// 同一文件先后同步需密码、损坏及可读的版本。
	locked := syncFile("Pdf", "attachment", "签字.pdf", file("encrypted.pdf"))
	damaged := syncFile("Pdf", "attachment", "签字.pdf", pdf[:20])
	current := syncFile("Pdf", "attachment", "签字.pdf", pdf)
	for _, v := range []struct {
		f     item
		stage string
	}{{locked, "encrypted"}, {damaged, "failed"}} {
		_, body := preview(v.f, "", v.stage)
		var state struct {
			Stage string
			Body  json.RawMessage
		}
		if err := json.Unmarshal(body, &state); err != nil || state.Stage != v.stage || state.Body != nil {
			t.Fatal(string(body), err)
		}
	}
	path, body := preview(current, "?page=2", "ready")
	img, err := png.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	red, _, blue, _ := img.At(20, 20).RGBA()
	if blue < 0xf000 || red > 0x1000 {
		t.Fatal("第二页像素不符", red, blue)
	}
	src.denied["Pdf"] = true
	request("GET", path, 403)
	request("GET", path+"/result", 403)
	request("GET", fmt.Sprintf("/%d/versions/%d/content", current.ID, *current.Version), 404)
	request("GET", fmt.Sprintf("/%d/object", current.ID), 404)
	var hidden struct {
		Total int
		Items []item
	}
	if err := json.Unmarshal(request("GET", "?q="+url.QueryEscape("签字.pdf"), 200).Body.Bytes(), &hidden); err != nil || hidden.Total != 0 || len(hidden.Items) != 0 {
		t.Fatal("撤权后的搜索结果不符", hidden, err)
	}
	delete(src.denied, "Pdf")
	if !bytes.Equal(request("GET", path+"/result", 200).Body.Bytes(), body) {
		t.Fatal("恢复授权后的版本不符")
	}
	csv := syncFile("Csv", "attachment", "名单.csv", []byte("姓名,项目\n张宁,验收"))
	preview(csv, "", "unsupported")
	large := syncFile("Large", "attachment", "大文档.pdf", append(bytes.Clone(pdf), make([]byte, previewFileLimit+1)...))
	preview(large, "", "over_limit")
}
