package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/directory"
	"github.com/uniquechao/OmniRelay/internal/drive"
	"github.com/uniquechao/OmniRelay/internal/feishu"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
	"github.com/uniquechao/OmniRelay/internal/project"
	"github.com/uniquechao/OmniRelay/internal/routing"
	"github.com/uniquechao/OmniRelay/internal/store/postgres"
	"github.com/uniquechao/OmniRelay/internal/system"
	"github.com/uniquechao/OmniRelay/migrations"
	"github.com/uniquechao/OmniRelay/web"
)

func newBackend(ctx context.Context, pool *pgxpool.Pool, cfg configuration, transport http.RoundTripper) (http.Handler, *backendWorkers, error) {
	return newBackendWithAccess(ctx, pool, cfg, transport, access.New(pool, cfg.tenantKey, cfg.appID))
}

func newBackendWithAccess(ctx context.Context, pool *pgxpool.Pool, cfg configuration, transport http.RoundTripper, roles *access.Service) (http.Handler, *backendWorkers, error) {
	return newBackendWithIntegrations(ctx, pool, cfg, transport, roles, chat.ReceiptSources{})
}

func newBackendWithIntegrations(ctx context.Context, pool *pgxpool.Pool, cfg configuration, transport http.RoundTripper,
	roles *access.Service, receipts chat.ReceiptSources,
) (http.Handler, *backendWorkers, error) {
	if roles.ProjectAccess == nil {
		roles.ProjectAccess = access.LocalProjects{Pool: pool}
	}
	if err := roles.Bootstrap(ctx, cfg.bootstrap); err != nil {
		if errors.Is(err, access.ErrBootstrapRequired) {
			return nil, nil, errors.New("set OMNIRELAY_BOOTSTRAP_OPEN_ID to the first administrator's verified Feishu Open ID")
		}
		return nil, nil, errors.New("administrator bootstrap failed")
	}
	directoryService, err := directory.New(pool, directory.Config{CandidateRead: cfg.candidateRead,
		AppID: cfg.appID, AppSecret: cfg.appSecret, TenantKey: cfg.tenantKey, Interval: cfg.syncInterval, PageBudget: cfg.pageBudget,
		HTTPClient: &http.Client{Transport: transport, Timeout: 12 * time.Second},
	})
	if err != nil {
		return nil, nil, err
	}
	check := feishu.NewBotCheck(cfg.appID, cfg.appSecret, transport)
	chatSource := feishu.NewChatSource(check, cfg.tenantKey)
	chatService := chat.New(pool, cfg.tenantKey, cfg.appID, cfg.credentialsGeneration, chatSource, directoryService, roles.Check, receipts)
	identityService, err := identity.New(pool, identity.Config{Candidate: cfg.candidateOAuth,
		AppID: cfg.appID, AppSecret: cfg.appSecret, TenantKey: cfg.tenantKey, PublicOrigin: cfg.publicOrigin,
		Transport: transport, Audit: roles.AuditIdentity, Access: roles.WorkbenchAccess,
		ResourceRead: func(ctx context.Context, principal identity.Principal, projectID, chatID string) (bool, error) {
			if chatID != "" {
				return chatService.CanReadConversation(ctx, principal, projectID, chatID)
			}
			return roles.Check(ctx, principal, "project.read", projectID)
		},
		OAuthProxyToken: cfg.oauthProxyToken, IdleTTL: cfg.sessionIdleTTL,
	})
	if err != nil {
		return nil, nil, err
	}
	botStore := postgres.NewFeishuBot(pool, system.Config{
		AppID: cfg.appID, TenantKey: cfg.tenantKey, CallbackURL: identityService.CallbackURL(),
		CredentialsConfigured: cfg.appSecret != "",
	}, cfg.credentialsGeneration, cfg.botHeartbeatTTL, roles.AuthorizeBotWrite, roles.AuditBotConfig)
	if err := botStore.Activate(ctx); err != nil {
		if errors.Is(err, system.ErrStaleGeneration) {
			return nil, nil, errors.New("Feishu credentials generation is older than the database generation")
		}
		return nil, nil, errors.New("could not initialize Feishu Bot configuration")
	}
	objects, err := drive.ConfiguredObjects()
	if err != nil {
		return nil, nil, err
	}
	fileService := drive.New(pool, cfg.tenantKey, cfg.appID, cfg.credentialsGeneration, chatSource, roles, objects, chatService.CanReadFileGroup)
	chatService.ConfigureFiles(fileService)
	chatService.ConfigurePreview(chatSource)
	chatService.ConfigureDelivery(chatSource, cfg.botSendEnabled && cfg.appSecret != "")
	projectService := project.New(pool, roles, botStore.Status, chatSource, chatService,
		cfg.botSendEnabled && cfg.appSecret != "")
	chatService.ConfigureBusinessReceipts(projectService)
	projectService.ConfigureFiles(fileService)
	workers := &backendWorkers{directory: directoryService, bot: system.New(botStore, check)}
	mux := http.NewServeMux()
	workers.groupAgent, err = groupWorker(pool, cfg, roles, chatSource, mux, identityService, botStore, projectService)
	if err != nil {
		return nil, nil, err
	}
	var receiver *postgres.CandidateMessages
	if cfg.appSecret != "" {
		workers.chat = chatService
		workers.files = fileService
	}
	if cfg.botLeaseNamespace != "" && cfg.appSecret != "" {
		lease, err := feishu.NewLease(cfg.botLeaseNamespace, cfg.botPodUID)
		if err != nil {
			return nil, nil, err
		}
		executable, err := os.Executable()
		if err != nil {
			return nil, nil, errors.New("could not locate Feishu SDK worker executable")
		}
		inbox, err := postgres.NewFeishuInbox(botStore, 1000)
		if err != nil {
			return nil, nil, err
		}
		if workers.groupAgent != nil && cfg.botSendEnabled && os.Getenv("OMNIRELAY_AGENT_FEISHU_ENABLED") != "0" {
			inbox.EnableGroupAgent()
		}
		connection := feishu.NewConnection(executable, cfg.tenantKey, check, botStore, inbox, cfg.botHeartbeatTTL)
		receiver = postgres.NewCandidateMessages(inbox, chatService.AcceptEvent, cfg.candidateReceiverToken != "")
		connection.SetInboundInterceptor(receiver.Intercept, cfg.candidateReceiverToken != "")
		connection.SetArchive(func(ctx context.Context, event orchestrator.InboundEvent) error {
			err := botStore.AcceptChatArchive(ctx, event, chatService.AcceptEvent)
			if err == nil && event.Archive != nil {
				if event.Archive.Message != nil {
					fileService.NotifyNewMessage()
				}
				if event.Archive.GroupMembersChanged {
					chatService.NotifyFileGroupChange()
				}
			}
			return err
		})
		workers.connection = func(ctx context.Context) { lease.Run(ctx, connection.Run) }
	}
	routing.Gateway{Check: roles.Check}.Register(mux, identityService.AuthenticateWorkbench)
	if run := cfg.candidateMessageRun; run != nil && run.MessageProject != "" {
		registerCandidateReplies(mux, pool, identityService.AuthenticateWorkbench, roles, *run)
	}
	if receiver != nil && cfg.candidateReceiverToken != "" {
		mux.Handle("POST "+candidateReceiverPath, candidateReceiver{
			store: receiver, chat: chatService, source: chatSource, roles: roles,
			token: cfg.candidateReceiverToken, rollback: cfg.candidateReceiverRollback,
		})
	}
	workers.bot.Register(mux, identityService.AuthenticateManagement, roles)
	fileService.Register(mux, identityService.AuthenticateWorkbench)
	chatService.Register(mux, identityService.AuthenticateWorkbench)
	projectService.Register(mux, identityService.AuthenticateWorkbench)
	identityService.Register(mux)
	identityService.RegisterPasswords(mux)
	web.Register(mux)
	roles.Register(mux, identityService.Authenticate)
	directoryService.Register(mux, identityService.Authenticate, roles.Check, func(ctx context.Context, tx pgx.Tx, actor identity.Principal) (bool, error) {
		err := roles.AuthorizeDirectorySync(ctx, tx, actor)
		if errors.Is(err, access.ErrForbidden) {
			return false, nil
		}
		return err == nil, err
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		timeout, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := migrations.Check(timeout, pool); err != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return secureHeaders(mux), workers, nil
}

type backendWorkers struct {
	directory  *directory.Service
	bot        *system.Service
	connection func(context.Context)
	groupAgent func(context.Context)
	chat       *chat.Service
	files      *drive.Service
}

func (w *backendWorkers) Run(ctx context.Context) {
	var running sync.WaitGroup
	if w.groupAgent != nil {
		running.Go(func() { w.groupAgent(ctx) })
	}
	running.Go(func() { w.directory.Run(ctx) })
	running.Go(func() { w.bot.Run(ctx) })
	if w.chat != nil {
		running.Go(func() { w.chat.Run(ctx) })
		running.Go(func() { w.chat.RunDocuments(ctx) })
		running.Go(func() { w.chat.RunFileGroups(ctx) })
	}
	if w.files != nil {
		running.Go(func() { w.files.Run(ctx) })
	}
	if w.connection != nil {
		running.Go(func() { w.connection(ctx) })
	}
	running.Wait()
}

type requestWindow struct {
	mu      sync.Mutex
	started time.Time
	count   int
}

func (l *requestWindow) allow(now time.Time, limit int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.started.IsZero() || now.Before(l.started) || now.Sub(l.started) >= time.Minute {
		l.started, l.count = now, 0
	}
	if l.count >= limit {
		return false
	}
	l.count++
	return true
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
