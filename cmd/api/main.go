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
	"github.com/benfaruna/medverify/internal/platform/jobs"
	"github.com/benfaruna/medverify/internal/platform/logging"
	"github.com/benfaruna/medverify/internal/scan"
	"github.com/benfaruna/medverify/internal/storage"
	"github.com/benfaruna/medverify/internal/vision"
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

	// The API only enqueues work. An insert-only client (no workers, not
	// started) leaves execution to the worker process and can insert job kinds
	// registered there.
	jobClient, err := jobs.NewClient(pool, logger, nil, cfg.Jobs)
	if err != nil {
		return err
	}

	scanService, err := scan.NewService(scan.ServiceOptions{
		Store:     scan.NewStore(pool),
		Objects:   store,
		Provider:  vision.NewFake(),
		Enqueuer:  jobs.NewScanEnqueuer(jobClient),
		Logger:    logger,
		MaxImages: cfg.Scan.MaxImages,
		Limits: scan.ImageLimits{
			MaxBytes:  cfg.Scan.MaxImageBytes,
			MaxPixels: cfg.Scan.MaxPixels,
		},
		MinConfidence: cfg.Scan.MinConfidence,
		PollInterval:  cfg.Scan.PollInterval,
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
		APIKey:         cfg.API.Key,
		Scan: scan.NewHTTPHandler(scan.HTTPOptions{
			Service:         scanService,
			Logger:          logger,
			MaxImages:       cfg.Scan.MaxImages,
			MaxImageBytes:   cfg.Scan.MaxImageBytes,
			MaxRequestBytes: cfg.Scan.MaxRequestBytes,
			SyncWait:        cfg.Scan.SyncWait,
		}),
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
