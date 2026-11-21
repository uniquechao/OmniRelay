package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/directory"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

const candidateBridge = "/run/omnirelay-candidate"

type webRun struct {
	bridge               string
	Run, Credential, Pod string
	Expires              int64
	MessageProject       string
}

func loadWebRun(bridge, pod string) (webRun, error) {
	raw, err := os.ReadFile(filepath.Join(bridge, "web.json"))
	if err != nil {
		return webRun{}, err
	}
	var run webRun
	if json.Unmarshal(raw, &run) != nil || pod == "" || run.Pod != pod || run.Expires <= time.Now().Unix() || len(run.Credential) < 32 {
		return webRun{}, errors.New("验收运行未开放")
	}
	run.bridge = bridge
	return run, nil
}

func (run webRun) call(ctx context.Context, op string, payload map[string]string) (candidateReply, error) {
	dial := net.Dialer{Timeout: 3 * time.Second}
	conn, err := dial.DialContext(ctx, "unix", filepath.Join(run.bridge, "service.sock"))
	if err != nil {
		return candidateReply{}, errors.New("运行服务不可用")
	}
	defer conn.Close()
	deadline := time.Now().Add(45 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	conn.SetDeadline(deadline)
	request := map[string]any{"version": 1, "run": run.Run, "credential": run.Credential, "operation": op, "payload": payload}
	var reply candidateReply
	if json.NewEncoder(conn).Encode(request) != nil || json.NewDecoder(io.LimitReader(conn, 2<<20)).Decode(&reply) != nil || !reply.Finished || reply.Component == "" {
		return candidateReply{}, errors.New("运行服务未确认完成")
	}
	return reply, nil
}

func (run webRun) resolve(ctx context.Context, code, verifier string) (identity.Principal, error) {
	reply, err := run.call(ctx, exchangeOperation, map[string]string{"code": code, "verifier": verifier})
	if err != nil {
		return identity.Principal{}, err
	}
	if reply.Error == "denied" {
		return identity.Principal{}, identity.ErrNotAdmitted
	}
	if reply.Error != "" || reply.Identity == nil {
		return identity.Principal{}, errors.New("换码服务不可用")
	}
	return *reply.Identity, nil
}

func (run webRun) sample(ctx context.Context, expectedScope string) (directory.SampleSnapshot, error) {
	op := sampleOperation
	payload := map[string]string{}
	if expectedScope == "" {
		op = scopeOperation
	} else {
		payload["scope"] = expectedScope
	}
	reply, err := run.call(ctx, op, payload)
	if err != nil {
		return directory.SampleSnapshot{}, err
	}
	if reply.Error == "denied" {
		return directory.SampleSnapshot{}, directory.SampleReadDenied()
	}
	if reply.Error != "" || reply.Sample == nil {
		return directory.SampleSnapshot{}, errors.New("读取目录样本失败")
	}
	return *reply.Sample, nil
}

func candidateWeb(ctx context.Context, pool *pgxpool.Pool, probes http.Handler, bridge string) http.Handler {
	var mu sync.Mutex
	var stopped chan struct{}
	var current webRun
	var app http.Handler
	var cancel context.CancelFunc
	pod := os.Getenv("OMNIRELAY_CANDIDATE_POD")
	origin := os.Getenv("OMNIRELAY_CANDIDATE_ORIGIN")
	go runCandidateMessages(ctx, pool, bridge, pod)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			probes.ServeHTTP(w, r)
			return
		}
		if "https://"+strings.ToLower(r.Host) != origin {
			http.Error(w, "候选入口不匹配", 421)
			return
		}
		mu.Lock()
		run, err := loadWebRun(bridge, pod)
		if err != nil {
			mu.Unlock()
			http.Error(w, "验收运行未开放", 503)
			return
		}
		if current != run {
			if cancel != nil {
				cancel()
				<-stopped
			}
			app = nil
			cfg := configuration{
				publicOrigin:          origin,
				appID:                 os.Getenv("OMNIRELAY_FEISHU_APP_ID"),
				tenantKey:             os.Getenv("OMNIRELAY_FEISHU_TENANT_KEY"),
				bootstrap:             os.Getenv("OMNIRELAY_BOOTSTRAP_OPEN_ID"),
				credentialsGeneration: 1,
				botHeartbeatTTL:       30 * time.Second,
				candidateOAuth:        &identity.CandidateOAuth{Run: run.Run, StateKey: run.Credential, Resolve: run.resolve},
				candidateRead:         run.sample,
				candidateMessageRun:   &run,
			}
			_, err = pool.Exec(r.Context(), "UPDATE directory_sources SET visible=false,next_run_at=clock_timestamp() WHERE tenant_key=$1 AND app_id=$2", cfg.tenantKey, cfg.appID)
			var workers *backendWorkers
			if err == nil {
				app, workers, err = newBackend(r.Context(), pool, cfg, nil)
			}
			if err == nil {
				current = run
				var workerCtx context.Context
				workerCtx, cancel = context.WithDeadline(ctx, time.Unix(run.Expires, 0))
				stopped = make(chan struct{})
				go func(done chan struct{}) { defer close(done); workers.directory.Run(workerCtx) }(stopped)
			}
		}
		handler := app
		mu.Unlock()
		if handler == nil {
			http.Error(w, "候选服务未就绪", 503)
			return
		}
		handler.ServeHTTP(w, r)
	})
}
