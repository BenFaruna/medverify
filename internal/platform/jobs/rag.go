package jobs

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/riverqueue/river"

	"github.com/benfaruna/medverify/internal/platform/config"
	"github.com/benfaruna/medverify/internal/rag"
)

// KindRAGIndex is the leaflet indexing job. It is both the backfill and the
// incremental pass: the query selects only documents that are not indexed for
// the current model and file hash.
const KindRAGIndex = "rag_index"

// RAGIndexArgs is the payload of an indexing pass.
type RAGIndexArgs struct {
	Trigger string `json:"trigger,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

// Kind identifies the job to River.
func (RAGIndexArgs) Kind() string { return KindRAGIndex }

// RAGIndexRunner indexes a bounded number of leaflets. It is an interface so
// the worker can be tested without a provider or a database.
type RAGIndexRunner interface {
	IndexLeaflets(ctx context.Context, limit int) (rag.IndexSummary, error)
}

// RAGIndexWorker runs one bounded indexing pass. River retries a failed pass;
// because each document commits atomically and completed documents drop out of
// the selection, a retry resumes rather than redoing the corpus.
type RAGIndexWorker struct {
	river.WorkerDefaults[RAGIndexArgs]

	Runner RAGIndexRunner
	Logger *slog.Logger
}

// Work implements river.Worker.
func (w *RAGIndexWorker) Work(ctx context.Context, job *river.Job[RAGIndexArgs]) error {
	logger := w.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if w.Runner == nil {
		return fmt.Errorf("rag index worker has no runner")
	}

	summary, err := w.Runner.IndexLeaflets(ctx, job.Args.Limit)
	if err != nil {
		logger.ErrorContext(ctx, "leaflet indexing failed",
			slog.Int64("job_id", job.ID),
			slog.String("trigger", job.Args.Trigger),
			slog.Any("err", err),
		)
		return err
	}
	logger.InfoContext(ctx, "leaflet indexing finished",
		slog.Int64("job_id", job.ID),
		slog.String("trigger", job.Args.Trigger),
		slog.Int("documents", summary.Documents),
		slog.Int("chunks", summary.Chunks),
		slog.Int("skipped", summary.Skipped),
	)
	return nil
}

// RAGIndexPeriodicJob schedules an indexing pass when an interval is configured.
// The default interval is zero, which leaves scheduling to the operator.
func RAGIndexPeriodicJob(cfg config.Embeddings) []*river.PeriodicJob {
	if cfg.IndexInterval <= 0 {
		return nil
	}
	return []*river.PeriodicJob{
		river.NewPeriodicJob(
			river.PeriodicInterval(cfg.IndexInterval),
			func() (river.JobArgs, *river.InsertOpts) {
				return RAGIndexArgs{Trigger: "scheduled", Limit: cfg.BackfillLimit}, nil
			},
			&river.PeriodicJobOpts{ID: KindRAGIndex},
		),
	}
}
