package main

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/agent/dsh"
	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/feishu"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/orchestrator/group"
	"github.com/uniquechao/OmniRelay/internal/project"
	"github.com/uniquechao/OmniRelay/internal/store/postgres"
)

func groupWorker(pool *pgxpool.Pool, cfg configuration, roles *access.Service, source *feishu.ChatSource, mux *http.ServeMux, identities *identity.Service, bot *postgres.FeishuBot, projects *project.Service) (func(context.Context), error) {
	endpoint := os.Getenv("OMNIRELAY_AGENT_RUNTIME_URL")
	if endpoint == "" {
		return nil, nil
	}

	certs, err := dsh.TLS(os.Getenv("OMNIRELAY_AGENT_CA"), os.Getenv("OMNIRELAY_AGENT_CERT"), os.Getenv("OMNIRELAY_AGENT_KEY"))
	if err != nil {
		return nil, err
	}
	client, err := dsh.NewClient(endpoint, certs)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(endpoint)
	svc := &group.Service{DB: pool, Roles: roles, Projects: projects, Tenant: cfg.tenantKey, App: cfg.appID, Owner: u.Hostname(), Execute: client.Execute, Channels: map[string]group.Channel{}}
	svc.RegisterWorkbench(mux, identities.AuthenticateWorkbench)
	if os.Getenv("OMNIRELAY_AGENT_FEISHU_ENABLED") != "0" && cfg.botSendEnabled && cfg.appSecret != "" {
		svc.Channels["feishu"] = group.Channel{Resolve: postgres.FeishuGroupScope(cfg.credentialsGeneration), Reply: func(ctx context.Context, e group.Envelope, bot, text, id string) group.Receipt {
			var r chat.DeliveryResult
			switch e.Mode {
			case group.PrivateReply:
				r = source.ReplyPrivate(ctx, e.Chat, bot, e.Message, text, id)
			case group.GroupNotice, group.PersonNotice:
				r = source.Notify(ctx, e.Mode == group.PersonNotice, e.Recipient, bot, text, id)
			case group.GroupReply:
				r = source.ReplyTo(ctx, e.Chat, bot, e.Message, text, id)
			default:
				return group.Receipt{State: "failed", Reason: "unknown_mode"}
			}
			return group.Receipt{State: r.State, MessageID: r.MessageID, Reason: r.Reason}
		}}
		svc.RegisterNotices(mux, identities.AuthenticateWorkbench, "feishu", func(ctx context.Context) (int64, error) { status, err := bot.Status(ctx); return status.Version, err })
	}
	addr := os.Getenv("OMNIRELAY_AGENT_LISTEN")
	if addr == "" {
		addr = ":8444"
	}
	if _, err = net.ResolveTCPAddr("tcp", addr); err != nil {
		return nil, err
	}
	return func(ctx context.Context) {
		// Web setup can rebuild workers. Bind and close within their lifetime.
		for ctx.Err() == nil {
			l, err := net.Listen("tcp", addr)
			if err != nil {
				slog.Error("Agent private listener unavailable")
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
				}
				continue
			}
			server := &http.Server{Handler: svc.Tools(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 12 * time.Second}
			stopped := make(chan struct{})
			run, cancel := context.WithCancel(ctx)
			go func() { defer close(stopped); _ = server.Serve(tls.NewListener(l, certs)); cancel() }()
			svc.Run(run)
			cancel()
			_ = server.Close()
			<-stopped
		}
	}, nil
}
