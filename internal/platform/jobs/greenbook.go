package jobs

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/riverqueue/river"

	"github.com/benfaruna/medverify/internal/platform/config"
	"github.com/benfaruna/medverify/internal/registry"
)

// KindGreenbookSync is the job that refreshes the local Greenbook copy.
const KindGreenbookSync = "greenbook_sync"

// GreenbookSyncArgs is the payload of the nightly sync job. Trigger is free
// text recorded in sync_runs logs so an operator can tell scheduled and manual
// runs apart.
type GreenbookSyncArgs struct {
	Trigger string `json:"trigger,omitempty"`
}

// Kind identifies the job to River.
func (GreenbookSyncArgs) Kind() string { return KindGreenbookSync }

// GreenbookRunner is the sync half of registry.Syncer, kept as an interface so
// the worker can be tested without a database or network.
type GreenbookRunner interface {
	Sync(context.Context, registry.SyncOptions) (registry.SyncSummary, error)
}

// GreenbookSyncWorker runs one full sync. River retries a failed job; an
// anomaly abort surfaces as an error so the run is visible in both sync_runs
// and the job queue.
type GreenbookSyncWorker struct {
	river.WorkerDefaults[GreenbookSyncArgs]

	Runner GreenbookRunner
	Logger *slog.Logger
}

// Work implements river.Worker.
func (w *GreenbookSyncWorker) Work(ctx context.Context, job *river.Job[GreenbookSyncArgs]) error {
	logger := w.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if w.Runner == nil {
		return fmt.Errorf("greenbook sync worker has no runner")
	}

	summary, err := w.Runner.Sync(ctx, registry.SyncOptions{Trigger: job.Args.Trigger})
	if err != nil {
		logger.ErrorContext(ctx, "greenbook sync job failed",
			slog.Int64("job_id", job.ID),
			slog.Int64("run_id", summary.RunID),
			slog.String("status", summary.Status),
			slog.Any("err", err),
		)
		return err
	}

	logger.InfoContext(ctx, "greenbook sync job finished",
		slog.Int64("job_id", job.ID),
		slog.Int64("run_id", summary.RunID),
		slog.Int("rows_seen", summary.RowsSeen),
		slog.Int("rows_added", summary.RowsAdded),
		slog.Int("rows_changed", summary.RowsChanged),
		slog.Int("rows_failed", summary.RowsFailed),
	)
	return nil
}

// GreenbookPeriodicJob returns the nightly sync schedule, or nil when the kill
// switch is off or no interval is configured. The worker only registers this
// when it owns a syncer; the API and admin clients would otherwise enqueue work
// they cannot execute.
func GreenbookPeriodicJob(cfg config.Greenbook) []*river.PeriodicJob {
	if !cfg.Enabled || cfg.SyncInterval <= 0 {
		return nil
	}
	return []*river.PeriodicJob{
		river.NewPeriodicJob(
			river.PeriodicInterval(cfg.SyncInterval),
			func() (river.JobArgs, *river.InsertOpts) {
				return GreenbookSyncArgs{Trigger: "nightly"}, nil
			},
			&river.PeriodicJobOpts{ID: KindGreenbookSync},
		),
	}
}
