// Command api serves MedVerify NG's HTTP API.
//
// Process modes: cmd/api serves HTTP, cmd/worker runs background jobs, and
// cmd/admin holds one-off commands. They share the internal packages and the
// same database.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/benfaruna/medverify/internal/platform/config"
	"github.com/benfaruna/medverify/internal/platform/db"
	"github.com/benfaruna/medverify/internal/platform/httpx"
	"github.com/benfaruna/medverify/internal/platform/logging"
	"github.com/benfaruna/medverify/internal/storage"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "api: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger, _ := logging.New(logging.Options{
		Level:   cfg.LogLevelValue(),
		Service: "medverify-api",
		Secrets: cfg.Secrets(),
	})
	slog.SetDefault(logger)

	logger.Info("starting api", slog.String("env", string(cfg.Env)))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer pool.Close()

	store, err := storage.NewS3(storage.Options{
		Endpoint:  cfg.Storage.Endpoint,
		AccessKey: cfg.Storage.AccessKey,
		SecretKey: cfg.Storage.SecretKey,
		Bucket:    cfg.Storage.Bucket,
		Region:    cfg.Storage.Region,
		UseSSL:    cfg.Storage.UseSSL,
	})
	if err != nil {
		return err
	}

	handler := NewHandler(Options{
		Logger: logger,
		Dependencies: []Dependency{
			{Name: "database", Check: func(ctx context.Context) error { return db.Ping(ctx, pool) }},
			{Name: "storage", Check: store.Ping},
		},
		ReadyTimeout:   cfg.HTTP.RequestTimeout,
		RequestTimeout: cfg.HTTP.RequestTimeout,
		MaxBodyBytes:   cfg.HTTP.MaxBodyBytes,
	})

	server := httpx.NewServer(httpx.ServerOptions{
		Addr:            cfg.HTTP.Addr,
		Handler:         handler,
		Logger:          logger,
		ReadTimeout:     cfg.HTTP.ReadTimeout,
		WriteTimeout:    cfg.HTTP.WriteTimeout,
		IdleTimeout:     cfg.HTTP.IdleTimeout,
		ShutdownTimeout: cfg.HTTP.ShutdownTimeout,
	})

	if err := server.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	logger.Info("api stopped")
	return nil
}
