// Package retention implements the phase 12 data-retention and erasure rules.
//
// Images are the personal data in a scan. The sweep deletes them after the
// ordinary retention window, keeps them longer for flagged cases so a safety
// review is possible, and never touches the scan row, verdict, or flags. A scan
// can also be erased on request, which deletes its images first and then the
// scan, so its conversations, results, and flags cascade away with it.
package retention

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/benfaruna/medverify/internal/platform/db/dbgen"
	"github.com/benfaruna/medverify/internal/storage"
)

// ErrScanNotFound means the scan to erase does not exist. Erasure is idempotent
// for a caller that retries the same request, so this is reported rather than
// treated as a success.
var ErrScanNotFound = errors.New("scan not found")

// Store reads and updates retention state. It holds no object storage; the
// swiper owns the deletion order so a failed object delete never loses the key.
type Store struct {
	queries *dbgen.Queries
}

// NewStore builds a store around an existing pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{queries: dbgen.New(pool)}
}

// Options is the window applied by one sweep.
type Options struct {
	// ImageRetention is how long an ordinary scan keeps its images.
	ImageRetention time.Duration
	// FlaggedImageRetention is the longer window for a scan with safety flags.
	FlaggedImageRetention time.Duration
	// BatchSize bounds one query. Zero selects a safe default.
	BatchSize int
}

// Report is the outcome of one sweep or erasure.
type Report struct {
	ImagesDeleted        int
	ConversationsDeleted int
	FailedDeletes        int
}

// Sweeper deletes image objects and the rows that point at them.
type Sweeper struct {
	store   *Store
	objects storage.Store
	logger  *slog.Logger
}

// NewSweeper builds a sweeper. The logger may be nil.
func NewSweeper(store *Store, objects storage.Store, logger *slog.Logger) *Sweeper {
	if logger == nil {
		logger = slog.Default()
	}
	return &Sweeper{store: store, objects: objects, logger: logger}
}

// Sweep deletes expired images and expired conversations. It returns an error
// when any object could not be deleted, but it keeps going: one unreachable key
// must not block every other user's retention.
func (s *Sweeper) Sweep(ctx context.Context, options Options, now time.Time) (Report, error) {
	if options.ImageRetention <= 0 || options.FlaggedImageRetention <= 0 {
		return Report{}, fmt.Errorf("retention: image retention windows must be positive")
	}
	if options.FlaggedImageRetention < options.ImageRetention {
		return Report{}, fmt.Errorf("retention: flagged window must not be shorter than the ordinary window")
	}
	batch := options.BatchSize
	if batch <= 0 {
		batch = 500
	}

	var report Report
	var failures []error
	for {
		rows, err := s.store.queries.ListRetentionImages(ctx, dbgen.ListRetentionImagesParams{
			FlaggedCutoff: now.Add(-options.FlaggedImageRetention),
			PlainCutoff:   now.Add(-options.ImageRetention),
			Batch:         int32(batch),
		})
		if err != nil {
			return report, fmt.Errorf("retention: list images: %w", err)
		}
		for _, row := range rows {
			if err := s.objects.Delete(ctx, row.StorageKey); err != nil {
				report.FailedDeletes++
				failures = append(failures, fmt.Errorf("delete object for scan %s: %w", row.ScanID, err))
				s.logger.WarnContext(ctx, "could not delete expired scan image",
					slog.String("scan_id", row.ScanID.String()), slog.Any("err", err))
				continue
			}
			if err := s.store.queries.DeleteScanImageByID(ctx, row.ID); err != nil {
				report.FailedDeletes++
				failures = append(failures, fmt.Errorf("delete image row for scan %s: %w", row.ScanID, err))
				s.logger.WarnContext(ctx, "could not delete expired scan image row",
					slog.String("scan_id", row.ScanID.String()), slog.Any("err", err))
				continue
			}
			report.ImagesDeleted++
		}
		if len(rows) < batch {
			break
		}
	}

	deleted, err := s.store.queries.DeleteExpiredConversations(ctx)
	if err != nil {
		return report, fmt.Errorf("retention: delete expired conversations: %w", err)
	}
	report.ConversationsDeleted = int(deleted)

	if len(failures) > 0 {
		return report, fmt.Errorf("retention: %d deletes failed: %w", len(failures), errors.Join(failures...))
	}
	return report, nil
}

// Erase deletes one scan and its images on request. Images are deleted first:
// if that fails, the scan row stays so its keys are still known and the request
// can be retried. Deleting the row afterwards cascades to results, flags,
// conversations, webhook deliveries, and idempotency records.
func (s *Sweeper) Erase(ctx context.Context, scanID uuid.UUID) (Report, error) {
	keys, err := s.store.queries.ListScanImageKeys(ctx, scanID)
	if err != nil {
		return Report{}, fmt.Errorf("retention: list scan images: %w", err)
	}

	var report Report
	for _, key := range keys {
		if err := s.objects.Delete(ctx, key); err != nil {
			return report, fmt.Errorf("retention: delete object for scan %s: %w", scanID, err)
		}
		report.ImagesDeleted++
	}

	deleted, err := s.store.queries.DeleteScanByID(ctx, scanID)
	if err != nil {
		return report, fmt.Errorf("retention: delete scan: %w", err)
	}
	if deleted == 0 {
		return report, ErrScanNotFound
	}
	s.logger.InfoContext(ctx, "scan erased",
		slog.String("scan_id", scanID.String()),
		slog.Int("images_deleted", report.ImagesDeleted),
	)
	return report, nil
}
