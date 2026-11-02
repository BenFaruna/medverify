package registry

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/benfaruna/medverify/internal/platform/db/dbgen"
)

// Store persists Greenbook products and their ingredients. Every product write
// is one transaction: the product row and its ingredient rows change together.
type Store struct {
	pool    *pgxpool.Pool
	queries *dbgen.Queries
}

// NewStore builds a store around an existing pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, queries: dbgen.New(pool)}
}

// Change reports what an upsert did. Both fields are false when the payload
// hash matched an existing row.
type Change struct {
	Inserted bool
	Changed  bool
}

// SyncResult is the terminal state of one sync run.
type SyncResult struct {
	Status      string
	RowsSeen    int
	RowsAdded   int
	RowsChanged int
	RowsFailed  int
	Error       string
}

// UpsertProduct inserts or updates one product and replaces its ingredients
// when the raw payload changed. It returns no change when the payload hash is
// identical, which is the common case on a nightly sync.
func (s *Store) UpsertProduct(ctx context.Context, product Product) (Change, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Change{}, fmt.Errorf("begin product transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := dbgen.New(tx)
	row, err := q.UpsertProduct(ctx, dbgen.UpsertProductParams{
		SourceID:             product.SourceID,
		Nrn:                  optional(product.NRN),
		Name:                 product.Name,
		Category:             optional(product.Category),
		Status:               product.Status,
		Applicant:            optional(product.Applicant),
		Manufacturer:         optional(product.Manufacturer),
		ManufacturerSourceID: optional(product.ManufacturerSourceID),
		CompositionRaw:       optional(product.CompositionRaw),
		Form:                 optional(product.Form),
		Route:                optional(product.Route),
		StrengthRaw:          optional(product.StrengthRaw),
		PackSize:             optional(product.PackSize),
		AtcCode:              optional(product.ATCCode),
		ApprovedAt:           product.ApprovedAt,
		ExpiresAt:            product.ExpiresAt,
		SmpcUrl:              optional(product.SmpcURL),
		Raw:                  product.Raw,
		PayloadHash:          product.PayloadHash(),
		FetchedAt:            product.FetchedAt,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// The ON CONFLICT ... WHERE payload_hash changed clause matched nothing.
		return Change{}, tx.Commit(ctx)
	}
	if err != nil {
		return Change{}, fmt.Errorf("upsert product %s: %w", product.SourceID, err)
	}

	if err := q.DeleteProductIngredients(ctx, row.ID); err != nil {
		return Change{}, fmt.Errorf("replace ingredients for %s: %w", product.SourceID, err)
	}
	for _, ingredient := range product.Ingredients {
		if err := q.InsertProductIngredient(ctx, dbgen.InsertProductIngredientParams{
			ProductID:     row.ID,
			NameRaw:       ingredient.NameRaw,
			NameNorm:      ingredient.NameNorm,
			StrengthValue: ingredient.Strength.Value,
			StrengthUnit:  optional(ingredient.Strength.Unit),
			PerUnit:       optional(ingredient.Strength.PerUnit),
			StrengthRaw:   optional(ingredient.StrengthRaw),
		}); err != nil {
			return Change{}, fmt.Errorf("insert ingredient %q for %s: %w", ingredient.NameNorm, product.SourceID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return Change{}, fmt.Errorf("commit product %s: %w", product.SourceID, err)
	}
	return Change{Inserted: row.Inserted, Changed: !row.Inserted}, nil
}

// StartSyncRun opens a run row before fetching starts, so a crash leaves a
// visible running or failed run rather than silence.
func (s *Store) StartSyncRun(ctx context.Context, startedAt time.Time) (int64, error) {
	run, err := s.queries.StartSyncRun(ctx, startedAt)
	if err != nil {
		return 0, fmt.Errorf("start sync run: %w", err)
	}
	return run.ID, nil
}

// FinishSyncRun records the terminal state of a run.
func (s *Store) FinishSyncRun(ctx context.Context, id int64, finishedAt time.Time, result SyncResult) error {
	err := s.queries.FinishSyncRun(ctx, dbgen.FinishSyncRunParams{
		ID:          id,
		FinishedAt:  &finishedAt,
		Status:      result.Status,
		RowsSeen:    int32(result.RowsSeen),
		RowsAdded:   int32(result.RowsAdded),
		RowsChanged: int32(result.RowsChanged),
		RowsFailed:  int32(result.RowsFailed),
		Error:       optional(result.Error),
	})
	if err != nil {
		return fmt.Errorf("finish sync run %d: %w", id, err)
	}
	return nil
}

// PreviousSyncRows returns the row count of the last successful run. The bool
// is false when no successful run exists yet.
func (s *Store) PreviousSyncRows(ctx context.Context) (int, bool, error) {
	run, err := s.queries.LastSuccessfulSyncRun(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("load last successful sync run: %w", err)
	}
	return int(run.RowsSeen), true, nil
}

// CountProducts reports how many products are in the local dataset. A zero
// count means verification cannot claim anything about a pack that was not
// found; the match result carries the count so callers can say so.
func (s *Store) CountProducts(ctx context.Context) (int, error) {
	count, err := s.queries.CountProducts(ctx)
	if err != nil {
		return 0, fmt.Errorf("count products: %w", err)
	}
	return int(count), nil
}

// LatestSuccessfulSync reports when the dataset was last fully refreshed. The
// bool is false when no successful sync exists yet.
func (s *Store) LatestSuccessfulSync(ctx context.Context) (time.Time, bool, error) {
	run, err := s.queries.LastSuccessfulSyncRun(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("load last successful sync run: %w", err)
	}
	if run.FinishedAt != nil {
		return *run.FinishedAt, true, nil
	}
	return run.StartedAt, true, nil
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
