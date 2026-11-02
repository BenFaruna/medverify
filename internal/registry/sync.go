package registry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/benfaruna/medverify/internal/platform/clock"
	"github.com/benfaruna/medverify/internal/platform/config"
)

// ErrAnomaly marks a sync that stopped before writing because the guard
// considered the result suspicious. The run row records the reasons.
var ErrAnomaly = errors.New("greenbook sync aborted by anomaly guard")

// PageSource is the page-fetching half of Fetcher, kept as an interface so the
// sync can be tested with fixtures.
type PageSource interface {
	FetchProducts(context.Context, PageRequest) ([]byte, error)
}

// SyncerOptions configures one sync.
type SyncerOptions struct {
	Source     PageSource
	Store      *Store
	Normalizer *Normalizer
	Policy     AnomalyPolicy
	PageSize   int
	Clock      clock.Clock
	Logger     *slog.Logger
}

// Syncer fetches, validates, and persists Greenbook products.
type Syncer struct {
	source     PageSource
	store      *Store
	normalizer *Normalizer
	policy     AnomalyPolicy
	pageSize   int
	clock      clock.Clock
	logger     *slog.Logger
}

// NewSyncer builds a syncer from explicit dependencies.
func NewSyncer(opts SyncerOptions) (*Syncer, error) {
	if opts.Source == nil {
		return nil, fmt.Errorf("greenbook syncer requires a page source")
	}
	if opts.Store == nil {
		return nil, fmt.Errorf("greenbook syncer requires a store")
	}
	if opts.PageSize <= 0 {
		opts.PageSize = 100
	}
	if opts.Normalizer == nil {
		opts.Normalizer = NewNormalizer()
	}
	if opts.Clock == nil {
		opts.Clock = clock.System{}
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	return &Syncer{
		source:     opts.Source,
		store:      opts.Store,
		normalizer: opts.Normalizer,
		policy:     opts.Policy,
		pageSize:   opts.PageSize,
		clock:      opts.Clock,
		logger:     opts.Logger,
	}, nil
}

// NewSyncerFromConfig wires the live fetcher, Postgres store, and anomaly
// policy from application configuration. Both the worker and the admin command
// use it so a manual sync cannot quietly differ from the nightly one.
func NewSyncerFromConfig(pool *pgxpool.Pool, cfg config.Greenbook, logger *slog.Logger) (*Syncer, error) {
	if pool == nil {
		return nil, fmt.Errorf("greenbook syncer requires a database pool")
	}
	fetcher, err := NewFetcher(cfg, logger)
	if err != nil {
		return nil, err
	}
	return NewSyncer(SyncerOptions{
		Source: fetcher,
		Store:  NewStore(pool),
		Policy: AnomalyPolicy{
			MaxDropPercent:         cfg.AnomalyMaxDropPercent,
			MaxParseFailurePercent: cfg.AnomalyMaxParseFailurePercent,
		},
		PageSize: cfg.PageSize,
		Logger:   logger,
	})
}

// SyncOptions controls one sync invocation.
type SyncOptions struct {
	// OverrideAnomaly lets an operator commit a run the guard would abort.
	OverrideAnomaly bool
	// Trigger is free text recorded in logs ("nightly", "admin").
	Trigger string
}

// RowFailure records a product the sync could not write. Duplicate NRNs are
// the expected case: two Greenbook records can reuse one number, and the
// database keeps NRN unique. The run continues so one conflict cannot block
// the rest of the dataset.
type RowFailure struct {
	SourceID string
	NRN      string
	Name     string
	Err      string
}

// SyncSummary is returned to the worker and the admin command.
type SyncSummary struct {
	RunID              int64
	Status             string
	RowsSeen           int
	RowsAdded          int
	RowsChanged        int
	RowsFailed         int
	RowFailures        []RowFailure
	Anomaly            Anomaly
	AnomalyOverridden  bool
	UnknownIngredients map[string]int
	StartedAt          time.Time
	FinishedAt         time.Time
}

// Sync performs a full unfiltered sweep. It fetches and parses every page
// first, runs the anomaly guard, and only then writes products. A suspicious
// sweep therefore leaves the previous dataset untouched.
func (s *Syncer) Sync(ctx context.Context, opts SyncOptions) (SyncSummary, error) {
	startedAt := s.clock.Now().UTC()
	runID, err := s.store.StartSyncRun(ctx, startedAt)
	if err != nil {
		return SyncSummary{}, err
	}
	summary := SyncSummary{RunID: runID, StartedAt: startedAt, Status: "failed"}
	s.logger.InfoContext(ctx, "greenbook sync started",
		slog.Int64("run_id", runID),
		slog.String("trigger", opts.Trigger),
	)

	finish := func(result SyncResult) error {
		result.Status = summary.Status
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		return s.store.FinishSyncRun(finishCtx, runID, s.clock.Now().UTC(), result)
	}
	fail := func(cause error) (SyncSummary, error) {
		summary.Status = "failed"
		summary.FinishedAt = s.clock.Now().UTC()
		finishErr := finish(SyncResult{
			RowsSeen:    summary.RowsSeen,
			RowsAdded:   summary.RowsAdded,
			RowsChanged: summary.RowsChanged,
			RowsFailed:  summary.RowsFailed,
			Error:       cause.Error(),
		})
		return summary, errors.Join(cause, finishErr)
	}

	previousRows, hasPrevious, err := s.store.PreviousSyncRows(ctx)
	if err != nil {
		return fail(err)
	}

	var products []Product
	start := 0
	total := 0
	for {
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}

		body, err := s.source.FetchProducts(ctx, PageRequest{Start: start, Length: s.pageSize})
		if err != nil {
			return fail(fmt.Errorf("fetch greenbook page at %d: %w", start, err))
		}
		page, err := ParsePage(body, s.clock.Now().UTC(), s.normalizer)
		if err != nil {
			return fail(fmt.Errorf("parse greenbook page at %d: %w", start, err))
		}
		if start == 0 {
			total = page.RecordsTotal
		}

		rows := len(page.Products) + len(page.RowErrors)
		summary.RowsSeen += rows
		summary.RowsFailed += len(page.RowErrors)
		for _, product := range page.Products {
			if product.SourceID == "" || product.Name == "" || product.Status == "" {
				summary.RowsFailed++
				continue
			}
			products = append(products, product)
		}

		if rows == 0 {
			break
		}
		start += rows
		if total > 0 && start >= total {
			break
		}
		if total == 0 && rows < s.pageSize {
			break
		}
	}

	summary.Anomaly = CheckAnomaly(
		previousRows, hasPrevious, len(products), summary.RowsSeen, summary.RowsFailed, s.policy,
	)
	if summary.Anomaly.Aborted() && !opts.OverrideAnomaly {
		summary.Status = "aborted"
		summary.FinishedAt = s.clock.Now().UTC()
		summary.UnknownIngredients = s.normalizer.Unknown()
		finishErr := finish(SyncResult{
			RowsSeen:   summary.RowsSeen,
			RowsFailed: summary.RowsFailed,
			Error:      summary.Anomaly.Error(),
		})
		s.logger.WarnContext(ctx, "greenbook sync aborted by anomaly guard",
			slog.Int64("run_id", runID),
			slog.String("reasons", summary.Anomaly.Error()),
		)
		if finishErr != nil {
			return summary, errors.Join(fmt.Errorf("%w: %s", ErrAnomaly, summary.Anomaly.Error()), finishErr)
		}
		return summary, fmt.Errorf("%w: %s", ErrAnomaly, summary.Anomaly.Error())
	}
	if summary.Anomaly.Aborted() {
		summary.AnomalyOverridden = true
		s.logger.WarnContext(ctx, "greenbook sync anomaly overridden by operator",
			slog.Int64("run_id", runID),
			slog.String("reasons", summary.Anomaly.Error()),
		)
	}

	for _, product := range products {
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		change, err := s.store.UpsertProduct(ctx, product)
		if err != nil {
			if !isDuplicateKey(err) {
				return fail(err)
			}
			// A duplicate NRN is a source-data conflict, not a reason to
			// abandon the other ~9,000 products. Keep the first record, log the
			// loser with enough detail to investigate, and move on.
			summary.RowsFailed++
			failure := RowFailure{
				SourceID: product.SourceID,
				NRN:      product.NRN,
				Name:     product.Name,
				Err:      err.Error(),
			}
			summary.RowFailures = append(summary.RowFailures, failure)
			s.logger.WarnContext(ctx, "greenbook product skipped: duplicate key",
				slog.Int64("run_id", runID),
				slog.String("source_id", product.SourceID),
				slog.String("nrn", product.NRN),
				slog.String("name", product.Name),
				slog.Any("err", err),
			)
			continue
		}
		switch {
		case change.Inserted:
			summary.RowsAdded++
		case change.Changed:
			summary.RowsChanged++
		}
	}

	summary.Status = "succeeded"
	summary.FinishedAt = s.clock.Now().UTC()
	summary.UnknownIngredients = s.normalizer.Unknown()

	runError := rowFailureSummary(summary.RowFailures)
	if summary.AnomalyOverridden {
		if runError != "" {
			runError += "; "
		}
		runError += "overridden: " + summary.Anomaly.Error()
	}
	finishErr := finish(SyncResult{
		RowsSeen:    summary.RowsSeen,
		RowsAdded:   summary.RowsAdded,
		RowsChanged: summary.RowsChanged,
		RowsFailed:  summary.RowsFailed,
		Error:       runError,
	})
	if finishErr != nil {
		return summary, finishErr
	}

	s.logger.InfoContext(ctx, "greenbook sync finished",
		slog.Int64("run_id", runID),
		slog.Int("rows_seen", summary.RowsSeen),
		slog.Int("rows_added", summary.RowsAdded),
		slog.Int("rows_changed", summary.RowsChanged),
		slog.Int("rows_failed", summary.RowsFailed),
		slog.Int("unknown_ingredients", len(summary.UnknownIngredients)),
	)
	return summary, nil
}

// uniqueViolation is PostgreSQL SQLSTATE 23505. The only expected source of it
// during a greenbook upsert is the nullable unique NRN index.
const uniqueViolation = "23505"

func isDuplicateKey(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}

// rowFailureSummary is persisted in sync_runs.error so an operator can see why
// rows were skipped even after the process exits. Keep it short; the full list
// is logged per row.
func rowFailureSummary(failures []RowFailure) string {
	if len(failures) == 0 {
		return ""
	}
	const shown = 5
	var b strings.Builder
	fmt.Fprintf(&b, "%d product(s) skipped by duplicate key: ", len(failures))
	for i, failure := range failures {
		if i >= shown {
			break
		}
		if i > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "source_id=%s nrn=%q name=%q error=%s",
			failure.SourceID, failure.NRN, failure.Name, shortError(failure.Err))
	}
	if len(failures) > shown {
		fmt.Fprintf(&b, "; and %d more", len(failures)-shown)
	}
	return b.String()
}

func shortError(message string) string {
	const limit = 200
	if len(message) <= limit {
		return message
	}
	return message[:limit] + "..."
}
