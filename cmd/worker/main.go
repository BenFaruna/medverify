// Command worker runs MedVerify NG's background jobs.
//
// It shares the API's database and internal packages, and stops gracefully: on
// SIGTERM it stops fetching new work, lets running jobs finish within the
// configured shutdown timeout, then releases the database pool.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/benfaruna/medverify/internal/platform/config"
	"github.com/benfaruna/medverify/internal/platform/db"
	"github.com/benfaruna/medverify/internal/platform/jobs"
	"github.com/benfaruna/medverify/internal/platform/logging"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "worker: "+err.Error())
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
		Service: "medverify-worker",
		Secrets: cfg.Secrets(),
	})
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer pool.Close()

	client, err := jobs.NewClient(pool, logger, jobs.NewWorkers(logger), cfg.Jobs)
	if err != nil {
		return err
	}

	if err := client.Start(ctx); err != nil {
		return fmt.Errorf("start job client: %w", err)
	}
	logger.Info("worker started", slog.Int("concurrency", cfg.Jobs.Concurrency))

	<-ctx.Done()
	logger.Info("worker shutting down", slog.Duration("grace_period", cfg.Jobs.ShutdownTimeout))

	stopCtx, cancel := context.WithTimeout(context.Background(), cfg.Jobs.ShutdownTimeout)
	defer cancel()
	if err := client.Stop(stopCtx); err != nil {
		return fmt.Errorf("stop job client: %w", err)
	}

	logger.Info("worker stopped")
	return nil
}
