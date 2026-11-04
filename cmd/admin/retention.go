package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/benfaruna/medverify/internal/platform/config"
	"github.com/benfaruna/medverify/internal/platform/db"
	"github.com/benfaruna/medverify/internal/retention"
	"github.com/benfaruna/medverify/internal/storage"
)

// newSweeper opens the pool and object store needed by the retention commands.
// The caller closes the returned pool.
func newSweeper(ctx context.Context, cfg config.Config) (*retention.Sweeper, func(), error) {
	pool, err := db.Open(cfg.Database)
	if err != nil {
		return nil, nil, err
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
		pool.Close()
		return nil, nil, err
	}
	return retention.NewSweeper(retention.NewStore(pool), objects, slog.Default()), pool.Close, nil
}

func retentionOptions(cfg config.Config) retention.Options {
	return retention.Options{
		ImageRetention:        time.Duration(cfg.Retention.ImageDays) * 24 * time.Hour,
		FlaggedImageRetention: time.Duration(cfg.Retention.FlaggedImageDays) * 24 * time.Hour,
		BatchSize:             cfg.Retention.BatchSize,
	}
}

func retentionCommand(ctx context.Context, cfg config.Config, args []string) error {
	if len(args) == 0 || args[0] != "sweep" {
		return fmt.Errorf("retention requires the action: sweep")
	}

	sweeper, closePool, err := newSweeper(ctx, cfg)
	if err != nil {
		return err
	}
	defer closePool()

	report, sweepErr := sweeper.Sweep(ctx, retentionOptions(cfg), time.Now())
	_, _ = fmt.Fprintf(os.Stdout,
		"deleted %d images and %d expired conversations; %d deletes failed\n",
		report.ImagesDeleted, report.ConversationsDeleted, report.FailedDeletes)
	return sweepErr
}

func scanCommand(ctx context.Context, cfg config.Config, args []string) error {
	if len(args) < 2 || args[0] != "delete" {
		return fmt.Errorf("scan requires: delete <scan-id> --confirm")
	}
	scanID, err := uuid.Parse(args[1])
	if err != nil {
		return fmt.Errorf("scan delete: invalid scan id: %w", err)
	}

	confirmed := false
	for _, arg := range args[2:] {
		if arg == "--confirm" {
			confirmed = true
			continue
		}
		return fmt.Errorf("unknown scan delete option %q", arg)
	}
	if !confirmed {
		return fmt.Errorf("scan delete removes the scan and its images; pass --confirm to proceed")
	}

	sweeper, closePool, err := newSweeper(ctx, cfg)
	if err != nil {
		return err
	}
	defer closePool()

	report, err := sweeper.Erase(ctx, scanID)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(os.Stdout,
		"erased scan %s and %d images; its conversation, result, and flags were removed too\n",
		scanID, report.ImagesDeleted)
	return nil
}
