package jobs

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/benfaruna/medverify/internal/registry"
)

// KindLeafletFetch is the on-demand leaflet download. The admin CLI and the
// nightly sweep do the same work in bulk; this job exists so a question about a
// pack whose leaflet is not stored can start that work at once instead of
// waiting for the next sweep.
const KindLeafletFetch = "leaflet_fetch"

// LeafletFetchArgs is the payload of one on-demand fetch. Only ProductID takes
// part in the uniqueness check, so repeated questions about one pack share a
// single queued job while different packs stay independent.
type LeafletFetchArgs struct {
	ProductID int64  `json:"product_id" river:"unique"`
	Trigger   string `json:"trigger,omitempty"`
}

// Kind identifies the job to River.
func (LeafletFetchArgs) Kind() string { return KindLeafletFetch }

// LeafletRunner is the fetch half of registry.LeafletPipeline, kept as an
// interface so the worker can be tested without a database, network, or storage.
type LeafletRunner interface {
	RunProduct(ctx context.Context, productID int64) (registry.LeafletSummary, error)
}

// LeafletFetchWorker downloads one leaflet, records its extraction status, and
// asks for an indexing pass so the stored text becomes searchable. A failed
// download is returned to River, which retries with its own backoff.
type LeafletFetchWorker struct {
	river.WorkerDefaults[LeafletFetchArgs]

	Runner     LeafletRunner
	Logger     *slog.Logger
	IndexLimit int
}

// Work implements river.Worker.
func (w *LeafletFetchWorker) Work(ctx context.Context, job *river.Job[LeafletFetchArgs]) error {
	logger := w.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if w.Runner == nil {
		return fmt.Errorf("leaflet fetch worker has no runner")
	}

	summary, err := w.Runner.RunProduct(ctx, job.Args.ProductID)
	if err != nil {
		logger.ErrorContext(ctx, "leaflet fetch failed",
			slog.Int64("job_id", job.ID),
			slog.Int64("product_id", job.Args.ProductID),
			slog.String("trigger", job.Args.Trigger),
			slog.Any("err", err),
		)
		return err
	}
	logger.InfoContext(ctx, "leaflet fetch finished",
		slog.Int64("job_id", job.ID),
		slog.Int64("product_id", job.Args.ProductID),
		slog.Int("downloaded", summary.Downloaded),
		slog.Int("skipped", summary.Skipped),
		slog.Int("ok", summary.OK),
		slog.Int("sections", summary.Sections),
	)

	// Without this pass the download would sit on file unsearchable until the
	// next scheduled indexing run, and the next question would still find no
	// passage. Indexing has its own retries, so a failure here is logged rather
	// than failing a fetch that already succeeded.
	client := workerClient(ctx)
	if client == nil {
		return nil
	}
	limit := w.IndexLimit
	if limit <= 0 {
		limit = 10
	}
	if _, err := client.Insert(ctx, RAGIndexArgs{Trigger: "leaflet_fetch", Limit: limit}, nil); err != nil {
		logger.WarnContext(ctx, "could not queue leaflet indexing",
			slog.Int64("product_id", job.Args.ProductID),
			slog.Any("err", err),
		)
	}
	return nil
}

// workerClient returns the River client attached to a running worker's context.
// The safe accessor reports an error when none is attached, which happens in
// tests and is a wiring problem elsewhere; either way indexing is best effort,
// so the caller treats a missing client as "skip this step".
func workerClient(ctx context.Context) *river.Client[pgx.Tx] {
	client, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err != nil {
		return nil
	}
	return client
}

// LeafletEnqueuer adapts a River client to assistant.LeafletFetcher without
// making the assistant package depend on River.
type LeafletEnqueuer struct {
	Client *river.Client[pgx.Tx]
}

// NewLeafletEnqueuer builds the adapter.
func NewLeafletEnqueuer(client *river.Client[pgx.Tx]) *LeafletEnqueuer {
	return &LeafletEnqueuer{Client: client}
}

// EnsureLeaflet implements assistant.LeafletFetcher. Queued and running jobs
// for the same product are reused, so asking twice does not download twice.
func (e *LeafletEnqueuer) EnsureLeaflet(ctx context.Context, productID int64) error {
	if e == nil || e.Client == nil {
		return fmt.Errorf("leaflet enqueuer has no job client")
	}
	_, err := e.Client.Insert(ctx,
		LeafletFetchArgs{ProductID: productID, Trigger: "question"},
		&river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true}},
	)
	if err != nil {
		return fmt.Errorf("enqueue leaflet fetch for product %d: %w", productID, err)
	}
	return nil
}
