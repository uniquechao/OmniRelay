package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCandidateOperation(t *testing.T) {
	dsn := os.Getenv("OMNIRELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("需要隔离数据库")
	}
	ctx := t.Context()
	pool := backendTestPool(t, ctx, dsn)
	key := make([]byte, 32)
	rand.Read(key)
	store := newWebSetupStore(pool, key)
	creds := setupCredentials{AppID: "cli_example", AppSecret: "private-fixture-secret", TenantKey: "tenant_fixture", Bootstrap: "ou_dedicated"}
	if saved, err := store.save(ctx, creds); err != nil || !saved {
		t.Fatal("测试网页配置写入失败")
	}
	const origin = "https://acceptance.example.test"
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OMNIRELAY_CANDIDATE_ATTEMPTS", dir)
	t.Setenv("OMNIRELAY_CANDIDATE_ORIGIN", origin)
	callback := origin + "/auth/feishu/callback"
	req := candidateRequest{Version: 1, Method: "execute", Operation: exchangeOperation, Run: strings.Repeat("1", 32), Expires: time.Now().Unix() + 60,
		Resources: map[string]string{"appID": creds.AppID, "tenantKey": creds.TenantKey, "identity": creds.Bootstrap, "callback": callback, "person": "ou_dedicated"},
		Payload:   map[string]string{"code": "test-code", "verifier": "test-verifier"}}
	calls := 0
	transport := setupProviderFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		var data string
		switch r.URL.Path {
		case "/open-apis/authen/v2/oauth/token":
			var body map[string]string
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["redirect_uri"] != callback || body["client_secret"] != creds.AppSecret || body["code_verifier"] != "test-verifier" {
				t.Fatal("受控换码参数错误")
			}
			data = `{"code":0,"token_type":"Bearer","access_token":"test-platform-token"}`
		case "/open-apis/authen/v1/user_info":
			if r.Header.Get("Authorization") != "Bearer test-platform-token" {
				t.Fatal("身份读取凭据错误")
			}
			data = `{"code":0,"data":{"open_id":"ou_dedicated","tenant_key":"tenant_fixture"}}`
		case "/oauth/v3/token":
			data = `{"code":0,"access_token":"test-platform-token"}`
		case "/open-apis/contact/v3/scopes":
			data = `{"code":0,"data":{"department_ids":["outside"],"user_ids":["ou_dedicated","ou_outside"],"group_ids":["outside"],"has_more":false}}`
		case "/open-apis/contact/v3/users/ou_dedicated":
			data = `{"code":0,"data":{"user":{"open_id":"ou_dedicated","name":"测试人员","department_ids":["outside"]}}}`
		default:
			t.Fatal("发生越界读取")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(data)), Header: make(http.Header)}, nil
	})
	invoke := func(fresh bool) (candidateReply, error) {
		if fresh {
			var id [16]byte
			rand.Read(id[:])
			req.Attempt = hex.EncodeToString(id[:])
		}
		raw, _ := json.Marshal(req)
		var out bytes.Buffer
		err := candidateOperation(ctx, pool, key, "https://production.example.test", bytes.NewReader(raw), &out, transport)
		var reply candidateReply
		if err == nil {
			for _, secret := range []string{creds.AppSecret, "test-platform-token", "test-code", "test-verifier", "outside"} {
				if strings.Contains(out.String(), secret) {
					t.Fatal("响应包含凭据或样本外数据")
				}
			}
			err = json.Unmarshal(out.Bytes(), &reply)
		}
		return reply, err
	}
	req.Resources["callback"] = "https://production.example.test/auth/feishu/callback"
	if _, err := invoke(true); err == nil || calls != 0 {
		t.Fatal("接受了未绑定回调")
	}
	req.Resources["callback"] = callback
	reply, err := invoke(true)
	if err != nil || reply.Identity == nil || reply.Identity.OpenID != creds.Bootstrap || !strings.HasPrefix(reply.Component, "omnirelay@sha256:") || calls != 2 {
		t.Fatal("受控换码未返回绑定身份和版本")
	}
	if _, err = invoke(false); err == nil || calls != 2 {
		t.Fatal("重复尝试触发换码")
	}
	req.Payload = nil
	req.Operation = scopeOperation
	reply, err = invoke(true)
	if err != nil || reply.Sample == nil {
		t.Fatal("受限范围读取失败")
	}
	req.Operation = sampleOperation
	req.Payload = map[string]string{"scope": reply.Sample.ScopeDigest()}
	reply, err = invoke(true)
	if err != nil || reply.Sample == nil || len(reply.Sample.People) != 1 || len(reply.Sample.People["ou_dedicated"].Departments) != 0 {
		t.Fatal("指定人员样本读取失败")
	}
	req.Payload["scope"] = strings.Repeat("f", 64)
	reply, err = invoke(true)
	if err != nil || reply.Error != "denied" || reply.Sample != nil {
		t.Fatal("范围改变后仍可发布")
	}
}
