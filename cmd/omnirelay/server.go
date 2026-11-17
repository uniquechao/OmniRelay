package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/directory"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/migrations"
)

func newBackend(ctx context.Context, pool *pgxpool.Pool, cfg configuration, transport http.RoundTripper) (http.Handler, *directory.Service, error) {
	roles := access.New(pool, cfg.tenantKey, cfg.appID)
	if err := roles.Bootstrap(ctx, cfg.bootstrap); err != nil {
		if errors.Is(err, access.ErrBootstrapRequired) {
			return nil, nil, errors.New("set OMNIRELAY_BOOTSTRAP_OPEN_ID to the first administrator's verified Feishu Open ID")
		}
		return nil, nil, errors.New("administrator bootstrap failed")
	}
	directoryService, err := directory.New(pool, directory.Config{
		AppID: cfg.appID, AppSecret: cfg.appSecret, TenantKey: cfg.tenantKey, Interval: cfg.syncInterval, PageBudget: cfg.pageBudget,
		HTTPClient: &http.Client{Transport: transport, Timeout: 12 * time.Second},
	})
	if err != nil {
		return nil, nil, err
	}
	identityService, err := identity.New(pool, identity.Config{
		AppID: cfg.appID, AppSecret: cfg.appSecret, TenantKey: cfg.tenantKey, PublicOrigin: cfg.publicOrigin,
		Transport: transport, Audit: roles.AuditIdentity,
	})
	if err != nil {
		return nil, nil, err
	}
	mux := http.NewServeMux()
	identityService.Register(mux)
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
	return secureHeaders(mux), directoryService, nil
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
	var loginStarts, loginCallbacks requestWindow
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		var limiter *requestWindow
		var limit int
		switch r.URL.Path {
		case "/auth/feishu/start":
			limiter, limit = &loginStarts, 120
		case "/auth/feishu/callback":
			limiter, limit = &loginCallbacks, 240
		}
		if limiter != nil && !limiter.allow(time.Now(), limit) {
			w.Header().Set("Retry-After", strconv.Itoa(60))
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
