package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
	"github.com/uniquechao/OmniRelay/internal/store/postgres"
	"github.com/uniquechao/OmniRelay/internal/system"
)

func runCandidateMessages(ctx context.Context, pool *pgxpool.Pool, bridge, pod string) {
	app, tenant := os.Getenv("OMNIRELAY_FEISHU_APP_ID"), os.Getenv("OMNIRELAY_FEISHU_TENANT_KEY")
	if app == "" || tenant == "" || pod == "" {
		return
	}
	roles := access.New(pool, tenant, app)
	roles.ProjectAccess = access.LocalProjects{Pool: pool}
	bot := postgres.NewFeishuBot(pool, system.Config{AppID: app, TenantKey: tenant}, 1, 30*time.Second, nil, nil)
	inbox, _ := postgres.NewFeishuInbox(bot, 1000)
	archive := chat.New(pool, tenant, app, 1, nil, nil, roles.Check, chat.ReceiptSources{})
	store := postgres.NewCandidateMessages(inbox, archive.AcceptEvent, false)
	timer := time.NewTimer(0)
	defer timer.Stop()
	var current string
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		run, err := loadWebRun(bridge, pod)
		delay := 2 * time.Second
		if err == nil && run.MessageProject != "" {
			call, stop := context.WithDeadline(ctx, time.Unix(run.Expires, 0))
			if current != run.Run {
				if err = bot.Activate(call); err == nil {
					current = run.Run
				}
			}
			if err == nil && pullCandidateMessage(call, store, roles, run) {
				delay = 200 * time.Millisecond
			}
			stop()
		}
		timer.Reset(delay)
	}
}

func pullCandidateMessage(ctx context.Context, store *postgres.CandidateMessages, roles *access.Service, run webRun) bool {
	reply, err := run.call(ctx, orchestrator.CandidateClaim, nil)
	item := reply.Message
	if err != nil || reply.Error != "" || item == nil || item.Run != run.Run ||
		item.Project != run.MessageProject || item.Expires.Unix() != run.Expires {
		return false
	}
	if current, err := loadWebRun(run.bridge, run.Pod); err != nil || current != run {
		return false
	}
	authorize := func(ctx context.Context, tx pgx.Tx, p identity.Principal, project string) (bool, error) {
		return roles.CheckInOrganizationMutation(ctx, tx, p, "project.read", project)
	}
	if err := store.AcceptCandidateMessage(ctx, *item, authorize); err != nil {
		return false
	}
	reply, err = run.call(ctx, orchestrator.CandidateAck, map[string]string{
		"source": item.Source, "lease": item.Lease, "revision": strconv.Itoa(item.Revision),
	})
	return err == nil && reply.Error == ""
}

func registerCandidateReplies(mux *http.ServeMux, pool *pgxpool.Pool,
	auth func(http.ResponseWriter, *http.Request, identity.SessionPolicy) (identity.Principal, bool),
	roles *access.Service, run webRun,
) {
	type work struct {
		MessageID string `json:"message_id"`
		ChatID    string `json:"chat_id"`
		Kind      string `json:"chat_kind"`
		WorkID    string `json:"work_id"`
		AccountID string `json:"account_id"`
		State     string `json:"state"`
	}
	const source = `SELECT e.message_id,e.chat_id,e.chat_kind,w.id,w.account_id,w.state
		FROM feishu_candidate_message_receipts r
		JOIN feishu_inbound_events e ON (e.tenant_id,e.app_id,e.message_id)=(r.tenant_key,r.app_id,r.source_id)
		JOIN feishu_inbound_work w ON (w.app_id,w.tenant_id,w.message_id)=(e.app_id,e.tenant_id,e.message_id)
		WHERE (r.run_id,r.tenant_key,r.app_id,w.account_id)=($1,$2,$3,$4)`
	scan := func(row pgx.CollectableRow) (work, error) {
		var v work
		err := row.Scan(&v.MessageID, &v.ChatID, &v.Kind, &v.WorkID, &v.AccountID, &v.State)
		return v, err
	}
	listOrReply := func(w http.ResponseWriter, r *http.Request) {
		policy := identity.ValidateSession
		action := "project.read"
		if r.Method == http.MethodPost {
			policy = identity.RefreshSessionWithCSRF
		}
		send := r.Pattern == "POST /api/v1/candidate/messages/{messageID}/reply"
		if send {
			action = "project.comment"
		}
		actor, ok := auth(w, r, policy)
		if !ok {
			return
		}
		ctx, cancel := context.WithDeadline(r.Context(), time.Unix(run.Expires, 0))
		defer cancel()
		if current, err := loadWebRun(run.bridge, run.Pod); err != nil || current != run {
			http.Error(w, "验收运行未开放", http.StatusForbidden)
			return
		}
		if ok, err := roles.Check(ctx, actor, action, run.MessageProject); err != nil || !ok {
			http.Error(w, "验收项目权限已失效", http.StatusForbidden)
			return
		}
		key := []any{run.Run, actor.TenantKey, actor.AppID, actor.AccountID}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			rows, err := pool.Query(ctx, source+` ORDER BY e.received_at DESC,e.message_id DESC LIMIT 100`, key...)
			if err == nil {
				var items []work
				items, err = pgx.CollectRows(rows, scan)
				if err == nil {
					_ = json.NewEncoder(w).Encode(items)
					return
				}
			}
			http.Error(w, "候选入站账本暂不可用", http.StatusServiceUnavailable)
			return
		}
		id := r.PathValue("messageID")
		var state string
		if err := pool.QueryRow(ctx, `SELECT state FROM (`+source+` AND e.message_id=$5) AS received`, append(key, id)...).Scan(&state); err != nil || state != "candidate" {
			http.NotFound(w, r)
			return
		}
		op := orchestrator.CandidateReceipt
		payload := map[string]string{"source": id}
		if send {
			op = orchestrator.CandidateReply
			var body struct{ Text string }
			d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
			d.DisallowUnknownFields()
			if d.Decode(&body) != nil || d.Decode(new(any)) != io.EOF || body.Text == "" || len(body.Text) > 8192 {
				http.Error(w, "无效的回复正文", http.StatusBadRequest)
				return
			}
			payload["text"] = body.Text
		}
		reply, err := run.call(ctx, op, payload)
		if err != nil || reply.Error != "" || reply.Delivery == nil {
			http.Error(w, "回复未获准或回执待核对", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(reply.Delivery)
	}
	mux.HandleFunc("GET /api/v1/candidate/messages", listOrReply)
	mux.HandleFunc("POST /api/v1/candidate/messages/{messageID}/reply", listOrReply)
	mux.HandleFunc("POST /api/v1/candidate/messages/{messageID}/receipt", listOrReply)
}
