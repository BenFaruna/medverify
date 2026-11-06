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
	"time"

	"github.com/benfaruna/medverify/internal/llm"
	"github.com/benfaruna/medverify/internal/platform/config"
	"github.com/benfaruna/medverify/internal/platform/db"
	"github.com/benfaruna/medverify/internal/platform/jobs"
	"github.com/benfaruna/medverify/internal/platform/logging"
	"github.com/benfaruna/medverify/internal/rag"
	"github.com/benfaruna/medverify/internal/registry"
	"github.com/benfaruna/medverify/internal/retention"
	"github.com/benfaruna/medverify/internal/safety"
	"github.com/benfaruna/medverify/internal/scan"
	"github.com/benfaruna/medverify/internal/storage"
	"github.com/benfaruna/medverify/internal/verdict"
	"github.com/benfaruna/medverify/internal/verify"
	"github.com/benfaruna/medverify/internal/vision/visioncfg"
	"github.com/benfaruna/medverify/internal/webhook"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "worker: "+err.Error())
		os.Exit(1)
	}
}

// runWebhookDispatcher drains due webhook deliveries until the process stops.
// One failed batch is logged and retried on the next tick rather than killing
// the worker.
func runRetentionSweeper(ctx context.Context, sweeper *retention.Sweeper, options retention.Options, interval time.Duration, logger *slog.Logger) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			passCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			report, err := sweeper.Sweep(passCtx, options, time.Now())
			cancel()
			if err != nil {
				logger.ErrorContext(ctx, "retention sweep failed",
					slog.Int("images_deleted", report.ImagesDeleted),
					slog.Int("failed_deletes", report.FailedDeletes),
					slog.Any("err", err))
				continue
			}
			logger.InfoContext(ctx, "retention sweep complete",
				slog.Int("images_deleted", report.ImagesDeleted),
				slog.Int("conversations_deleted", report.ConversationsDeleted))
		}
	}
}

func runWebhookDispatcher(ctx context.Context, dispatcher *webhook.Dispatcher, interval time.Duration, logger *slog.Logger) {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := dispatcher.RunOnce(ctx); err != nil {
				logger.ErrorContext(ctx, "webhook dispatch pass failed", slog.Any("err", err))
			}
		}
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

	verifyConfig, err := verify.LoadConfig(cfg.Verify.ConfigFile)
	if err != nil {
		return err
	}
	matcher, err := verify.NewMatcher(verify.Options{
		Catalog: registry.NewStore(pool),
		Config:  verifyConfig,
	})
	if err != nil {
		return err
	}

	safetyConfig, err := safety.LoadConfig(cfg.Safety.ConfigFile)
	if err != nil {
		return err
	}
	checker, err := safety.NewChecker(safety.CheckerOptions{
		Watchlist: safety.NewStore(pool),
		Config:    safetyConfig,
	})
	if err != nil {
		return err
	}

	rules, err := verdict.LoadRules(cfg.Verdict.ConfigFile)
	if err != nil {
		return err
	}

	// Webhook delivery lives in the worker: the API only enqueues scans, and a
	// slow customer endpoint must never hold an HTTP request open.
	webhookStore := webhook.NewDBStore(pool)

	scanService, err := scan.NewService(scan.ServiceOptions{
		Store:     scan.NewStore(pool),
		Objects:   objects,
		Provider:  provider,
		Recorder:  llm.NewRecorder(pool),
		Matcher:   matcher,
		Safety:    checker,
		Notifier:  &webhook.Notifier{Store: webhookStore, Logger: logger},
		Rules:     &rules,
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

	// Leaflet indexing is wired even when it is not scheduled, so an operator
	// can enqueue a pass explicitly. The default embeddings provider is the
	// deterministic fake, which is safe locally but useless as a production
	// index; the provider decision is still open.
	embedder, err := rag.NewEmbedder(cfg.Embeddings)
	if err != nil {
		return err
	}
	workerOpts = append(workerOpts, jobs.WithRAGRunner(&leafletIndexer{
		store:    rag.NewStore(pool),
		embedder: embedder,
		delay:    cfg.Embeddings.Delay,
		logger:   logger,
	}))
	clientOpts = append(clientOpts, jobs.WithPeriodicJobs(jobs.RAGIndexPeriodicJob(cfg.Embeddings)))

	// A question about a pack whose leaflet is not stored queues this worker.
	// The pipeline is the same one the admin CLI runs, so a question-triggered
	// download records the same statuses and never replaces a good extraction.
	leafletPipeline, err := registry.NewLeafletPipelineFromConfig(pool, cfg.Greenbook, objects, logger)
	if err != nil {
		return err
	}
	workerOpts = append(workerOpts, jobs.WithLeafletRunner(leafletPipeline))

	// The dispatcher drains due deliveries on its own ticker. It shares the
	// worker's lifetime and stops with it.
	dispatcher := &webhook.Dispatcher{
		Store:  webhookStore,
		Sender: webhook.NewSender(cfg.Webhook.Timeout),
		Logger: logger,
	}
	go runWebhookDispatcher(ctx, dispatcher, cfg.Webhook.Interval, logger)

	// Retention is a scheduled sweep in the worker, not a job row: a missed
	// pass only delays deletion, and the same code is available as an admin
	// command for an operator to run by hand.
	if cfg.Retention.Enabled && cfg.Retention.SweepInterval > 0 {
		sweeper := retention.NewSweeper(retention.NewStore(pool), objects, logger)
		go runRetentionSweeper(ctx, sweeper, retention.Options{
			ImageRetention:        time.Duration(cfg.Retention.ImageDays) * 24 * time.Hour,
			FlaggedImageRetention: time.Duration(cfg.Retention.FlaggedImageDays) * 24 * time.Hour,
			BatchSize:             cfg.Retention.BatchSize,
		}, cfg.Retention.SweepInterval, logger)
	}

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
		slog.String("verify_config_version", verifyConfig.Version),
		slog.String("safety_config_version", safetyConfig.Version),
		slog.String("verdict_config_version", rules.Version),
		slog.String("embeddings_provider", cfg.Embeddings.Provider),
		slog.String("embeddings_model", embedder.Model()),
		slog.Int("embeddings_dimension", embedder.Dim()),
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
