package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/directory"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

const exchangeOperation = "oauth.identity"
const scopeOperation = "directory.scope"
const sampleOperation = "directory.sample"

var candidateID = regexp.MustCompile("^[0-9a-f]{32}$")

type candidateRequest struct {
	Version                                int
	Method, Scene, Run, Attempt, Operation string
	Expires                                int64
	Resources                              map[string]string
	Payload                                map[string]string
	Candidate                              json.RawMessage
}

type candidateReply struct {
	Ready     bool                      `json:"ready,omitempty"`
	Attempt   string                    `json:"attempt,omitempty"`
	Finished  bool                      `json:"finished,omitempty"`
	Component string                    `json:"component,omitempty"`
	Error     string                    `json:"error,omitempty"`
	Identity  *identity.Principal       `json:"identity,omitempty"`
	Sample    *directory.SampleSnapshot `json:"sample,omitempty"`
}

// 通过受控服务的私有 Unix 套接字按连接启动。标准输入输出仅传送协议消息。
func candidateOperation(ctx context.Context, pool *pgxpool.Pool, key []byte, productionOrigin string, input io.Reader, output io.Writer, transport http.RoundTripper) error {
	var req candidateRequest
	decoder := json.NewDecoder(io.LimitReader(input, 65537))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || req.Version != 1 {
		return errors.New("受控请求无效")
	}
	origin := os.Getenv("OMNIRELAY_CANDIDATE_ORIGIN")
	creds, ok, err := newWebSetupStore(pool, key).load(ctx)
	if err != nil || !ok {
		return errors.New("网页配置的应用身份不可用")
	}
	provider, err := identity.NewSetupOAuth(creds.AppID, creds.AppSecret, origin, transport)
	if err != nil || provider.CallbackURL() == productionOrigin+"/auth/feishu/callback" {
		return errors.New("验收回调无效")
	}
	callback := provider.CallbackURL()
	bound := req.Resources["callback"] == callback && req.Resources["identity"] != "" && req.Resources["appID"] == creds.AppID && req.Resources["tenantKey"] == creds.TenantKey
	selected := directory.SampleSelection{Department: req.Resources["department"], Person: req.Resources["person"], Group: req.Resources["group"]}
	hasSample := selected.Department != "" || selected.Person != "" || selected.Group != ""
	attemptDir := os.Getenv("OMNIRELAY_CANDIDATE_ATTEMPTS")
	info, statErr := os.Stat(attemptDir)
	bound = bound && filepath.IsAbs(attemptDir) && statErr == nil && info.IsDir() && info.Mode().Perm()&0077 == 0
	if req.Method == "readiness" {
		return json.NewEncoder(output).Encode(candidateReply{Ready: bound && (req.Scene == "OAuth" || req.Scene == "目录" && hasSample)})
	}
	if req.Method != "execute" || !candidateID.MatchString(req.Run) || !candidateID.MatchString(req.Attempt) || !bound || time.Now().Unix() >= req.Expires {
		return errors.New("运行绑定无效")
	}
	marker, err := os.OpenFile(filepath.Join(attemptDir, req.Run+"-"+req.Attempt), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("运行尝试已消费或无法登记")
	}
	if err = marker.Close(); err != nil {
		return errors.New("运行尝试登记失败")
	}
	ctx, cancel := context.WithDeadline(ctx, time.Unix(req.Expires, 0))
	defer cancel()
	executable, err := os.Open("/proc/self/exe")
	if err != nil {
		return err
	}
	defer executable.Close()
	digest := sha256.New()
	if _, err = io.Copy(digest, executable); err != nil {
		return err
	}
	reply := candidateReply{Attempt: req.Attempt, Finished: true, Component: "omnirelay@sha256:" + hex.EncodeToString(digest.Sum(nil)), Error: "unavailable"}
	switch req.Operation {
	case exchangeOperation:
		if len(req.Payload) == 2 && req.Payload["code"] != "" && req.Payload["verifier"] != "" {
			p, err := provider.Resolve(ctx, req.Payload["code"], req.Payload["verifier"])
			if errors.Is(err, identity.ErrNotAdmitted) || err == nil && (p.OpenID != req.Resources["identity"] || p.TenantKey != creds.TenantKey) {
				reply.Error = "denied"
			} else if err == nil {
				reply.Identity = &p
				reply.Error = ""
			}
		}
	case scopeOperation, sampleOperation:
		scopeOnly := req.Operation == scopeOperation
		expected := req.Payload["scope"]
		if hasSample && ((scopeOnly && len(req.Payload) == 0) || (!scopeOnly && len(req.Payload) == 1 && len(expected) == 64)) {
			s, err := directory.ReadSample(ctx, pool, directory.Config{AppID: creds.AppID, TenantKey: creds.TenantKey, AppSecret: creds.AppSecret, HTTPClient: &http.Client{Transport: transport}}, selected, expected)
			if directory.SampleDenied(err) {
				reply.Error = "denied"
			} else if err == nil {
				reply.Sample = &s
				reply.Error = ""
			}
		}
	}
	return json.NewEncoder(output).Encode(reply)
}
