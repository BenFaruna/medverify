// Command admin holds one-off operational commands: migrations, storage
// bootstrap, and manual job enqueueing.
//
// Usage:
//
//	medverify-admin migrate up|down|status
//	medverify-admin storage init
//	medverify-admin job noop [message]
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/benfaruna/medverify/internal/platform/config"
	"github.com/benfaruna/medverify/internal/platform/db"
	"github.com/benfaruna/medverify/internal/platform/jobs"
	"github.com/benfaruna/medverify/internal/platform/logging"
	"github.com/benfaruna/medverify/internal/registry"
	"github.com/benfaruna/medverify/internal/storage"
	"github.com/benfaruna/medverify/migrations"
)

const usage = `usage: medverify-admin <command>

commands:
  migrate up            apply pending migrations (app schema, then job schema)
  migrate down          revert the most recent migration
  migrate status        list migrations and whether they are applied
  storage init          create the configured bucket if missing
  job noop [message]    enqueue a no-op job for the worker
  sync greenbook [--override-anomaly]
                        run a full Greenbook sync now
  sync leaflets [--limit N]
                        download, extract, and split pending leaflets
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "admin: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("missing command")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger, _ := logging.New(logging.Options{
		Level:   cfg.LogLevelValue(),
		Service: "medverify-admin",
		Secrets: cfg.Secrets(),
		Writer:  os.Stderr,
	})
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch args[0] {
	case "migrate":
		return migrate(ctx, cfg, args[1:])
	case "storage":
		return storageCommand(ctx, cfg, args[1:])
	case "job":
		return jobCommand(ctx, cfg, args[1:])
	case "sync":
		return syncCommand(ctx, cfg, args[1:])
	case "help", "-h", "--help":
		_, _ = fmt.Fprint(os.Stdout, usage)
		return nil
	default:
		_, _ = fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func migrate(ctx context.Context, cfg config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("migrate requires one of: up, down, status")
	}

	switch args[0] {
	case "up":
		if err := migrations.Up(ctx, cfg.Database.URL); err != nil {
			return err
		}
		if err := jobs.Migrate(ctx, cfg.Database.URL, jobs.DirectionUp); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(os.Stdout, "migrations applied")
		return nil

	case "down":
		if err := jobs.Migrate(ctx, cfg.Database.URL, jobs.DirectionDown); err != nil {
			return err
		}
		if err := migrations.Down(ctx, cfg.Database.URL); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(os.Stdout, "migrations reverted one step")
		return nil

	case "status":
		statuses, err := migrations.Status(ctx, cfg.Database.URL)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "VERSION\tSTATE\tNAME")
		for _, status := range statuses {
			_, _ = fmt.Fprintf(w, "%d\t%s\t%s\n", status.Source.Version, status.State, status.Source.Path)
		}
		return w.Flush()

	default:
		return fmt.Errorf("unknown migrate action %q", args[0])
	}
}

func storageCommand(ctx context.Context, cfg config.Config, args []string) error {
	if len(args) == 0 || args[0] != "init" {
		return fmt.Errorf("storage requires the action: init")
	}

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

	if err := store.EnsureBucket(ctx); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(os.Stdout, "bucket %q ready\n", cfg.Storage.Bucket)
	return nil
}

func jobCommand(ctx context.Context, cfg config.Config, args []string) error {
	if len(args) == 0 || args[0] != "noop" {
		return fmt.Errorf("job requires the kind: noop")
	}

	message := "phase 1 wiring check"
	if len(args) > 1 {
		message = args[1]
	}

	pool, err := db.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer pool.Close()

	client, err := jobs.NewClient(pool, slog.Default(), jobs.NewWorkers(slog.Default()), cfg.Jobs)
	if err != nil {
		return err
	}

	insertCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	id, err := jobs.EnqueueNoop(insertCtx, client, message)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(os.Stdout, "enqueued noop job %d\n", id)
	return nil
}

func syncCommand(ctx context.Context, cfg config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("sync requires a source: greenbook or leaflets")
	}
	switch args[0] {
	case "greenbook":
		return syncGreenbook(ctx, cfg, args[1:])
	case "leaflets":
		return syncLeaflets(ctx, cfg, args[1:])
	default:
		return fmt.Errorf("unknown sync source %q", args[0])
	}
}

func syncGreenbook(ctx context.Context, cfg config.Config, args []string) error {
	override := false
	for _, arg := range args {
		switch arg {
		case "--override-anomaly":
			override = true
		default:
			return fmt.Errorf("unknown sync option %q", arg)
		}
	}

	pool, err := db.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer pool.Close()

	syncer, err := registry.NewSyncerFromConfig(pool, cfg.Greenbook, slog.Default())
	if err != nil {
		return err
	}

	summary, syncErr := syncer.Sync(ctx, registry.SyncOptions{
		Trigger:         "admin",
		OverrideAnomaly: override,
	})
	printSyncSummary(summary)
	return syncErr
}

func syncLeaflets(ctx context.Context, cfg config.Config, args []string) error {
	limit := 500
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--limit":
			if i+1 >= len(args) {
				return fmt.Errorf("--limit requires a number")
			}
			parsed, err := strconv.Atoi(args[i+1])
			if err != nil || parsed <= 0 {
				return fmt.Errorf("--limit must be a positive integer")
			}
			limit = parsed
			i++
		default:
			return fmt.Errorf("unknown sync option %q", args[i])
		}
	}

	pool, err := db.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer pool.Close()

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

	pipeline, err := registry.NewLeafletPipelineFromConfig(pool, cfg.Greenbook, objects, slog.Default())
	if err != nil {
		return err
	}

	summary, runErr := pipeline.Run(ctx, limit)
	printLeafletSummary(summary)
	return runErr
}

func printSyncSummary(summary registry.SyncSummary) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(w, "run\t%d\n", summary.RunID)
	_, _ = fmt.Fprintf(w, "status\t%s\n", summary.Status)
	_, _ = fmt.Fprintf(w, "rows seen\t%d\n", summary.RowsSeen)
	_, _ = fmt.Fprintf(w, "rows added\t%d\n", summary.RowsAdded)
	_, _ = fmt.Fprintf(w, "rows changed\t%d\n", summary.RowsChanged)
	_, _ = fmt.Fprintf(w, "rows failed\t%d\n", summary.RowsFailed)
	if summary.AnomalyOverridden {
		_, _ = fmt.Fprintf(w, "anomaly\t%s\n", summary.Anomaly.Error())
	}
	_, _ = fmt.Fprintf(w, "unknown ingredient names\t%d\n", len(summary.UnknownIngredients))
	_ = w.Flush()
}

func printLeafletSummary(summary registry.LeafletSummary) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(w, "candidates\t%d\n", summary.Candidates)
	_, _ = fmt.Fprintf(w, "downloaded\t%d\n", summary.Downloaded)
	_, _ = fmt.Fprintf(w, "skipped (unchanged)\t%d\n", summary.Skipped)
	_, _ = fmt.Fprintf(w, "ok\t%d\n", summary.OK)
	_, _ = fmt.Fprintf(w, "scanned (needs OCR)\t%d\n", summary.ScannedNeedsOCR)
	_, _ = fmt.Fprintf(w, "too large\t%d\n", summary.TooLarge)
	_, _ = fmt.Fprintf(w, "failed\t%d\n", summary.Failed)
	_, _ = fmt.Fprintf(w, "sections stored\t%d\n", summary.Sections)
	_ = w.Flush()
}
