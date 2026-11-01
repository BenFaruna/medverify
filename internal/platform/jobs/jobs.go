// Package jobs wires the Postgres-backed job queue (River) used for slow work
// such as ingestion and extraction. Phase 1 only ships a no-op job that proves
// the API can enqueue and the worker can execute.
package jobs

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/benfaruna/medverify/internal/platform/config"
)

// Job kinds. One constant per worker so enqueue sites never hardcode strings.
const (
	KindNoop = "noop"
)

// NoopArgs is the payload of the no-op job.
type NoopArgs struct {
	Message string `json:"message"`
}

// Kind identifies the job to River.
func (NoopArgs) Kind() string { return KindNoop }

// NoopWorker logs and completes. It exists to prove queue wiring end to end.
type NoopWorker struct {
	river.WorkerDefaults[NoopArgs]

	Logger *slog.Logger
}

// Work implements river.Worker.
func (w *NoopWorker) Work(ctx context.Context, job *river.Job[NoopArgs]) error {
	logger := w.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	logger.InfoContext(ctx, "noop job completed",
		slog.Int64("job_id", job.ID),
		slog.String("message", job.Args.Message),
	)
	return nil
}

// NewWorkers registers every worker this build knows about.
func NewWorkers(logger *slog.Logger) *river.Workers {
	workers := river.NewWorkers()
	river.AddWorker(workers, &NoopWorker{Logger: logger})
	return workers
}

// NewClient builds a River client. Callers decide whether to Start it: the API
// only enqueues, the worker executes.
func NewClient(pool *pgxpool.Pool, logger *slog.Logger, workers *river.Workers, cfg config.Jobs) (*river.Client[pgx.Tx], error) {
	if pool == nil {
		return nil, fmt.Errorf("job client requires a database pool")
	}

	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = 1
	}

	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Logger:  logger,
		Workers: workers,
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: concurrency},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create job client: %w", err)
	}
	return client, nil
}

// EnqueueNoop inserts a no-op job. It is the wiring path used by the API and by
// the integration test.
func EnqueueNoop(ctx context.Context, client *river.Client[pgx.Tx], message string) (int64, error) {
	result, err := client.Insert(ctx, NoopArgs{Message: message}, nil)
	if err != nil {
		return 0, fmt.Errorf("enqueue noop job: %w", err)
	}
	return result.Job.ID, nil
}

// Direction selects the migration direction.
type Direction string

// Migration directions.
const (
	DirectionUp   Direction = "up"
	DirectionDown Direction = "down"
)

// Migrate applies or reverts River's own schema. MedVerify migrations stay in
// the migrations package; this one only manages the river_* tables.
func Migrate(ctx context.Context, url string, direction Direction) error {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return fmt.Errorf("connect for job migrations: %w", err)
	}
	defer pool.Close()

	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return fmt.Errorf("create job migrator: %w", err)
	}

	opts := &rivermigrate.MigrateOpts{}
	var migrateErr error
	switch direction {
	case DirectionUp:
		_, migrateErr = migrator.Migrate(ctx, rivermigrate.DirectionUp, opts)
	case DirectionDown:
		// Down migrations run one step at a time by default; revert them all.
		for {
			result, err := migrator.Migrate(ctx, rivermigrate.DirectionDown, &rivermigrate.MigrateOpts{MaxSteps: 1})
			if err != nil {
				migrateErr = err
				break
			}
			if len(result.Versions) == 0 {
				break
			}
		}
	default:
		return fmt.Errorf("unknown migration direction %q", direction)
	}
	if migrateErr != nil {
		return fmt.Errorf("migrate job schema: %w", migrateErr)
	}
	return nil
}
