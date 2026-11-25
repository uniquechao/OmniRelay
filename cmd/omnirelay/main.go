package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/feishu"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/migrations"
	"golang.org/x/term"
)

var errWebSetupSaved = errors.New("web setup saved")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(os.Args) == 2 && os.Args[1] == feishu.WorkerCommand {
		if os.Getenv("OMNIRELAY_CANDIDATE") != "" || feishu.ServeSDKWorker(ctx) != nil {
			os.Exit(1)
		}
		return
	}
	for {
		err := run(ctx, os.Args[1:])
		if errors.Is(err, errWebSetupSaved) && ctx.Err() == nil {
			continue
		}
		if err != nil && !errors.Is(err, errWebSetupSaved) {
			slog.Error("OmniRelay startup or shutdown failed", "error", err.Error())
			os.Exit(1)
		}
		return
	}
}

func run(ctx context.Context, args []string) error {
	command := "serve"
	if len(args) > 0 {
		command = args[0]
	}
	candidate := os.Getenv("OMNIRELAY_CANDIDATE")
	if candidate != "" && (candidate != "1" || command != "serve" && command != "migrate") {
		return errors.New("候选模式或命令无效")
	}
	if command == "setup-token" && len(args) == 1 {
		key, err := parseSetupKey(os.Getenv("OMNIRELAY_SETUP_KEY"))
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(os.Stdout, setupToken(key))
		return err
	}
	valid := (command == "serve" || command == "migrate") && len(args) <= 1 ||
		command == "password" && len(args) == 3 || command == "create-local-admin" && len(args) == 1
	if !valid && !(command == "candidate-operation" && len(args) == 1) {
		return errors.New("usage: omnirelay [serve|migrate|setup-token|create-local-admin|password <account-id> <username>]")
	}
	cfg, err := readConfig(os.Getenv, (command == "serve" || command == "candidate-operation") && candidate == "")
	if err != nil {
		return err
	}
	dbCfg, err := pgxpool.ParseConfig(cfg.databaseURL)
	if err != nil {
		return errors.New("invalid PostgreSQL connection configuration")
	}
	if dbCfg.ConnConfig.TLSConfig == nil {
		return errors.New("PostgreSQL TLS configuration is required")
	}
	for _, fallback := range dbCfg.ConnConfig.Fallbacks {
		if fallback.TLSConfig == nil {
			return errors.New("plaintext PostgreSQL fallbacks are unsupported")
		}
	}
	dbCfg.MaxConns = 12
	dbCfg.ConnConfig.ConnectTimeout = 5 * time.Second

	startup, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(startup, dbCfg)
	if err != nil {
		return errors.New("could not initialize PostgreSQL pool")
	}
	defer pool.Close()
	if err := pool.Ping(startup); err != nil {
		return errors.New("PostgreSQL is unavailable or failed TLS verification")
	}
	if command == "candidate-operation" {
		if len(cfg.setupKey) == 0 {
			return errors.New("需要网页配置的应用身份")
		}
		return candidateOperation(startup, pool, cfg.setupKey, cfg.publicOrigin, os.Stdin, os.Stdout, nil)
	}
	if candidate != "" {
		if err := checkCandidateDatabase(startup, pool, command); err != nil {
			return err
		}
	}
	if command == "migrate" {
		if err := migrations.Apply(startup, pool); err != nil {
			return errors.New("PostgreSQL migration failed; inspect migration role and schema state")
		}
		slog.Info("PostgreSQL schema is current")
		return nil
	}
	if err := migrations.Check(startup, pool); err != nil {
		return errors.New("required PostgreSQL schema missing or changed; run omnirelay migrate for this release")
	}
	if command == "password" || command == "create-local-admin" {
		return runPasswordCommand(startup, pool, args)
	}
	var handler http.Handler
	var workers *backendWorkers
	var reload chan struct{}
	if candidate != "" {
		if addr := os.Getenv("OMNIRELAY_LISTEN_ADDR"); addr != "" {
			cfg.listenAddr = addr
		}
		handler = candidateHandler(pool)
		if os.Getenv("OMNIRELAY_CANDIDATE_ORIGIN") != "" {
			handler = candidateWeb(ctx, pool, handler, candidateBridge)
		}
	} else if cfg.appID == "" {
		store := newWebSetupStore(pool, cfg.setupKey)
		credentials, configured, err := store.load(startup)
		if err != nil {
			return fmt.Errorf("could not load saved web setup: %w", err)
		}
		if configured {
			cfg.appID, cfg.appSecret = credentials.AppID, credentials.AppSecret
			cfg.tenantKey, cfg.bootstrap = credentials.TenantKey, credentials.Bootstrap
		} else {
			reload = make(chan struct{}, 1)
			handler = newWebSetupHandler(store, cfg.publicOrigin, nil, func() {
				select {
				case reload <- struct{}{}:
				default:
				}
			})
			slog.Info("OmniRelay web setup is available")
		}
	}
	if handler == nil {
		handler, workers, err = newBackend(startup, pool, cfg, nil)
		if err != nil {
			return err
		}
	}

	server := &http.Server{
		Addr: cfg.listenAddr, Handler: handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10,
	}
	background, cancelBackground := context.WithCancel(ctx)
	defer cancelBackground()
	workersStopped := make(chan struct{})
	go func() {
		defer close(workersStopped)
		if workers != nil {
			workers.Run(background)
		}
	}()
	serverDone := make(chan error, 1)
	go func() {
		if cfg.tlsCertFile != "" {
			slog.Info("OmniRelay HTTPS backend starting")
			serverDone <- server.ListenAndServeTLS(cfg.tlsCertFile, cfg.tlsKeyFile)
			return
		}
		slog.Info("OmniRelay HTTP backend starting; password endpoints require TLS")
		serverDone <- server.ListenAndServe()
	}()
	var serveErr error
	select {
	case err := <-serverDone:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = fmt.Errorf("HTTP listener failed on %s", cfg.listenAddr)
		}
	case <-ctx.Done():
	case <-reload:
		serveErr = errWebSetupSaved
	}
	cancelBackground()
	shutdown, stopShutdown := context.WithTimeout(context.Background(), 15*time.Second)
	defer stopShutdown()
	if err := server.Shutdown(shutdown); err != nil {
		_ = server.Close()
		serveErr = errors.Join(serveErr, errors.New("HTTP shutdown deadline exceeded"))
	}
	select {
	case <-workersStopped:
		return serveErr
	case <-shutdown.Done():
		return errors.Join(serveErr, errors.New("background worker shutdown deadline exceeded"))
	}
}

func runPasswordCommand(ctx context.Context, pool *pgxpool.Pool, args []string) error {
	tenant := strings.TrimSpace(os.Getenv("OMNIRELAY_FEISHU_TENANT_KEY"))
	app := strings.TrimSpace(os.Getenv("OMNIRELAY_FEISHU_APP_ID"))
	if tenant == "" && app == "" && strings.TrimSpace(os.Getenv("OMNIRELAY_SETUP_MODE")) == "true" {
		key, err := parseSetupKey(os.Getenv("OMNIRELAY_SETUP_KEY"))
		if err != nil {
			return err
		}
		credentials, configured, err := newWebSetupStore(pool, key).load(ctx)
		if err != nil || !configured {
			return errors.New("complete web setup before provisioning an account password")
		}
		tenant, app = credentials.TenantKey, credentials.AppID
	}
	if tenant == "" || app == "" {
		return errors.New("the Feishu app ID and tenant key are required")
	}
	var password []byte
	var err error
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, "New password: ")
		password, err = term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
	} else {
		password, err = io.ReadAll(io.LimitReader(os.Stdin, 257))
	}
	defer clear(password)
	if err != nil {
		return errors.New("could not read the password")
	}
	if args[0] == "create-local-admin" {
		id, err := access.New(pool, tenant, app).CreateLocalAdmin(ctx, string(password))
		if err != nil {
			return fmt.Errorf("create local admin: %w", err)
		}
		slog.Info("Local administrator created", "account_id", id)
		return nil
	}
	if err := identity.ProvisionPassword(ctx, pool, tenant, app, args[1], args[2], string(password)); err != nil {
		return errors.New("password provisioning failed; verify the active account, username and password requirements")
	}
	slog.Info("Account password provisioned; existing sessions revoked")
	return nil
}
