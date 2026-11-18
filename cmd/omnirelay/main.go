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
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/migrations"
	"golang.org/x/term"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		slog.Error("OmniRelay startup or shutdown failed", "error", err.Error())
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	command := "serve"
	if len(args) > 0 {
		command = args[0]
	}
	valid := (command == "serve" || command == "migrate") && len(args) <= 1 ||
		command == "password" && len(args) == 3
	if !valid {
		return errors.New("usage: omnirelay [serve|migrate|password <account-id> <username>]")
	}
	cfg, err := readConfig(os.Getenv, command == "serve")
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
	if command == "password" {
		return setAccountPassword(startup, pool, args[1], args[2])
	}
	handler, workers, err := newBackend(startup, pool, cfg, nil)
	if err != nil {
		return err
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
		workers.Run(background)
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

func setAccountPassword(ctx context.Context, pool *pgxpool.Pool, accountID, username string) error {
	tenant := strings.TrimSpace(os.Getenv("OMNIRELAY_FEISHU_TENANT_KEY"))
	app := strings.TrimSpace(os.Getenv("OMNIRELAY_FEISHU_APP_ID"))
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
	if err := identity.ProvisionPassword(ctx, pool, tenant, app, accountID, username, string(password)); err != nil {
		return errors.New("password provisioning failed; verify the active account, username and password requirements")
	}
	slog.Info("Account password provisioned; existing sessions revoked")
	return nil
}
