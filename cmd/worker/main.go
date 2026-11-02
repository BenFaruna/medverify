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

	"github.com/benfaruna/medverify/internal/llm"
	"github.com/benfaruna/medverify/internal/platform/config"
	"github.com/benfaruna/medverify/internal/platform/db"
	"github.com/benfaruna/medverify/internal/platform/jobs"
	"github.com/benfaruna/medverify/internal/platform/logging"
	"github.com/benfaruna/medverify/internal/registry"
	"github.com/benfaruna/medverify/internal/scan"
	"github.com/benfaruna/medverify/internal/storage"
	"github.com/benfaruna/medverify/internal/vision/visioncfg"
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

	workerOpts := []jobs.WorkerOption{}
	clientOpts := []jobs.ClientOption{}
	if cfg.Greenbook.Enabled {
		syncer, err := registry.NewSyncerFromConfig(pool, cfg.Greenbook, logger)
		if err != nil {
			return err
		}
		workerOpts = append(workerOpts, jobs.WithGreenbookRunner(syncer))
		clientOpts = append(clientOpts, jobs.WithPeriodicJobs(jobs.GreenbookPeriodicJob(cfg.Greenbook)))
	}

	objects, err := storage.NewS3(storage.Options{
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

	provider, err := visioncfg.New(cfg.Vision)
	if err != nil {
		return err
	}

	scanService, err := scan.NewService(scan.ServiceOptions{
		Store:     scan.NewStore(pool),
		Objects:   objects,
		Provider:  provider,
		Recorder:  llm.NewRecorder(pool),
		Logger:    logger,
		MaxImages: cfg.Scan.MaxImages,
		Limits: scan.ImageLimits{
			MaxBytes:  cfg.Scan.MaxImageBytes,
			MaxPixels: cfg.Scan.MaxPixels,
		},
		MaxImageDimension: cfg.Vision.MaxImageDimension,
		MinConfidence:     cfg.Scan.MinConfidence,
		PollInterval:      cfg.Scan.PollInterval,
	})
	if err != nil {
		return err
	}
	workerOpts = append(workerOpts, jobs.WithScanRunner(scanService))

	client, err := jobs.NewClient(pool, logger, jobs.NewWorkers(logger, workerOpts...), cfg.Jobs, clientOpts...)
	if err != nil {
		return err
	}

	if err := client.Start(ctx); err != nil {
		return fmt.Errorf("start job client: %w", err)
	}
	logger.Info("worker started",
		slog.Int("concurrency", cfg.Jobs.Concurrency),
		slog.Bool("greenbook_enabled", cfg.Greenbook.Enabled),
		slog.Duration("greenbook_interval", cfg.Greenbook.SyncInterval),
		slog.String("vision_provider", cfg.Vision.Provider),
		slog.String("vision_model", cfg.Vision.Model),
	)

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
