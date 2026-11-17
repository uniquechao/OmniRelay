package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/migrations"
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
	if len(args) == 1 {
		command = args[0]
	}
	if len(args) > 1 || (command != "serve" && command != "migrate") {
		return errors.New("usage: omnirelay [serve|migrate]")
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
	handler, directory, err := newBackend(startup, pool, cfg, nil)
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
	directoryStopped := make(chan struct{})
	go func() {
		defer close(directoryStopped)
		directory.Run(background)
	}()
	serverDone := make(chan error, 1)
	go func() {
		slog.Info("OmniRelay HTTP backend starting")
		serverDone <- server.ListenAndServe()
	}()
	select {
	case err := <-serverDone:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("HTTP listener failed on %s", cfg.listenAddr)
		}
	case <-ctx.Done():
	}
	cancelBackground()
	shutdown, stopShutdown := context.WithTimeout(context.Background(), 15*time.Second)
	defer stopShutdown()
	if err := server.Shutdown(shutdown); err != nil {
		_ = server.Close()
		return errors.New("HTTP shutdown deadline exceeded")
	}
	select {
	case <-directoryStopped:
		return nil
	case <-shutdown.Done():
		return errors.New("directory shutdown deadline exceeded")
	}
}
