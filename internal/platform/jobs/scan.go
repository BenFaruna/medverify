package jobs

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// KindProcessScan is the job that extracts a queued scan.
const KindProcessScan = "process_scan"

// ProcessScanArgs is the payload of an extraction job.
type ProcessScanArgs struct {
	ScanID string `json:"scan_id"`
}

// Kind identifies the job to River.
func (ProcessScanArgs) Kind() string { return KindProcessScan }

// ProcessScanRunner is the pipeline half of scan.Service, kept as an interface
// so the worker can be tested without a database or storage.
type ProcessScanRunner interface {
	Process(ctx context.Context, scanID string) error
}

// ProcessScanWorker runs one extraction attempt. Returning an error makes River
// retry; the scan service turns a retry into failed -> queued -> processing.
type ProcessScanWorker struct {
	river.WorkerDefaults[ProcessScanArgs]

	Runner ProcessScanRunner
	Logger *slog.Logger
}

// Work implements river.Worker.
func (w *ProcessScanWorker) Work(ctx context.Context, job *river.Job[ProcessScanArgs]) error {
	logger := w.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if w.Runner == nil {
		return fmt.Errorf("process scan worker has no runner")
	}

	if err := w.Runner.Process(ctx, job.Args.ScanID); err != nil {
		logger.ErrorContext(ctx, "process scan job failed",
			slog.Int64("job_id", job.ID),
			slog.String("scan_id", job.Args.ScanID),
			slog.Any("err", err),
		)
		return err
	}

	logger.InfoContext(ctx, "process scan job finished",
		slog.Int64("job_id", job.ID),
		slog.String("scan_id", job.Args.ScanID),
	)
	return nil
}

// EnqueueScan inserts an extraction job. The attempt cap keeps a permanently
// broken scan from retrying forever.
func EnqueueScan(ctx context.Context, client *river.Client[pgx.Tx], scanID string) (int64, error) {
	result, err := client.Insert(ctx, ProcessScanArgs{ScanID: scanID}, &river.InsertOpts{MaxAttempts: 3})
	if err != nil {
		return 0, fmt.Errorf("enqueue process scan job: %w", err)
	}
	return result.Job.ID, nil
}

// ScanEnqueuer adapts a River client to scan.Enqueuer without making the scan
// package depend on River.
type ScanEnqueuer struct {
	Client *river.Client[pgx.Tx]
}

// NewScanEnqueuer builds the adapter.
func NewScanEnqueuer(client *river.Client[pgx.Tx]) *ScanEnqueuer {
	return &ScanEnqueuer{Client: client}
}

// EnqueueScan implements scan.Enqueuer.
func (e *ScanEnqueuer) EnqueueScan(ctx context.Context, scanID string) error {
	if e == nil || e.Client == nil {
		return fmt.Errorf("scan enqueuer has no job client")
	}
	_, err := EnqueueScan(ctx, e.Client, scanID)
	return err
}
