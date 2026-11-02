package safety

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/benfaruna/medverify/internal/platform/db/dbgen"
)

// Store persists the watchlist and reads it for the checker. Only the admin
// import command writes entries; everything else reads.
type Store struct {
	pool    *pgxpool.Pool
	queries *dbgen.Queries
}

// NewStore builds a store around an existing pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, queries: dbgen.New(pool)}
}

// ImportEntry is one validated row from the admin import. Source fields are
// part of the row because a watchlist entry cannot exist without provenance.
type ImportEntry struct {
	Kind             EntryKind
	NRN              string
	ProductName      string
	ProductNameNorm  string
	Manufacturer     string
	ManufacturerNorm string
	IngredientNorm   string
	Reason           string
	EffectiveUntil   *time.Time
	ReviewDueAt      *time.Time
	ReviewedBy       string
	Source           SourceDocument
}

// ImportSummary reports what one import wrote.
type ImportSummary struct {
	Sources int
	Entries int
}

// Import writes validated entries and their source documents atomically. A
// caller that has not validated every row must not call this: the whole import
// commits or nothing does.
func (s *Store) Import(ctx context.Context, entries []ImportEntry) (ImportSummary, error) {
	if len(entries) == 0 {
		return ImportSummary{}, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ImportSummary{}, fmt.Errorf("begin watchlist import: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbgen.New(tx)

	summary := ImportSummary{}
	sourceIDs := make(map[string]int64, len(entries))
	for i, entry := range entries {
		if entry.Source.URL == "" || entry.Source.IssuingBody == "" || entry.Source.Title == "" || entry.Source.IssuedAt.IsZero() {
			return ImportSummary{}, fmt.Errorf("entry %d: source requires a URL, issuing body, title, and issue date", i+1)
		}
		sourceKey := entry.Source.URL + "|" + entry.Source.IssuedAt.Format(time.DateOnly)
		sourceID, ok := sourceIDs[sourceKey]
		if !ok {
			source, err := q.UpsertWatchlistSource(ctx, dbgen.UpsertWatchlistSourceParams{
				IssuingBody: entry.Source.IssuingBody,
				Title:       entry.Source.Title,
				SourceUrl:   entry.Source.URL,
				IssuedAt:    DateOnly(entry.Source.IssuedAt),
			})
			if err != nil {
				return ImportSummary{}, fmt.Errorf("store source %q: %w", entry.Source.URL, err)
			}
			sourceID = source.ID
			sourceIDs[sourceKey] = sourceID
			if source.Inserted {
				summary.Sources++
			}
		}

		inserted, err := q.InsertWatchlistEntry(ctx, dbgen.InsertWatchlistEntryParams{
			SourceID:         sourceID,
			Kind:             string(entry.Kind),
			Nrn:              optional(entry.NRN),
			ProductName:      optional(entry.ProductName),
			ProductNameNorm:  optional(entry.ProductNameNorm),
			Manufacturer:     optional(entry.Manufacturer),
			ManufacturerNorm: optional(entry.ManufacturerNorm),
			IngredientNorm:   optional(entry.IngredientNorm),
			Reason:           entry.Reason,
			EffectiveUntil:   optionalTime(entry.EffectiveUntil),
			ReviewedBy:       entry.ReviewedBy,
			ReviewDueAt:      optionalTime(entry.ReviewDueAt),
		})
		if err != nil {
			return ImportSummary{}, fmt.Errorf("store entry %d: %w", i+1, err)
		}
		summary.Entries += int(inserted)
	}

	if err := tx.Commit(ctx); err != nil {
		return ImportSummary{}, fmt.Errorf("commit watchlist import: %w", err)
	}
	return summary, nil
}

// ProductQuery selects live product entries for one pack: by NRN, by normalized
// name, or both. The checker applies the final matching rules.
type ProductQuery struct {
	NRN      string
	NameNorm string
	Limit    int
	Now      time.Time
}

// ProductEntries returns live product entries that could match. Duplicate hits
// (an entry found by both NRN and name) are returned once.
func (s *Store) ProductEntries(ctx context.Context, query ProductQuery) ([]Entry, error) {
	if query.Limit <= 0 {
		return nil, nil
	}
	today := DateOnly(query.Now)

	byID := make(map[int64]Entry)
	if query.NRN != "" {
		rows, err := s.queries.ListLiveWatchlistEntriesByNRN(ctx, dbgen.ListLiveWatchlistEntriesByNRNParams{
			Nrn:   optional(query.NRN),
			Today: &today,
		})
		if err != nil {
			return nil, fmt.Errorf("lookup watchlist by NRN: %w", err)
		}
		for _, row := range rows {
			entry := entryFromFields(row.WatchlistEntry, row.SourceIssuingBody, row.SourceTitle, row.SourceUrl, row.SourceIssuedAt)
			byID[entry.ID] = entry
		}
	}

	if query.NameNorm != "" {
		rows, err := s.queries.SearchLiveWatchlistEntriesByName(ctx, dbgen.SearchLiveWatchlistEntriesByNameParams{
			Query:    query.NameNorm,
			Today:    &today,
			RowLimit: int32(query.Limit),
		})
		if err != nil {
			return nil, fmt.Errorf("search watchlist by name: %w", err)
		}
		for _, row := range rows {
			entry := entryFromFields(row.WatchlistEntry, row.SourceIssuingBody, row.SourceTitle, row.SourceUrl, row.SourceIssuedAt)
			byID[entry.ID] = entry
		}
	}

	return sortedEntries(byID, query.Limit), nil
}

// IngredientEntries returns live entries whose normalized ingredient matches
// any of names. Names must already be normalized with the shared synonym table.
func (s *Store) IngredientEntries(ctx context.Context, names []string, now time.Time) ([]Entry, error) {
	if len(names) == 0 {
		return nil, nil
	}
	today := DateOnly(now)
	rows, err := s.queries.ListLiveWatchlistEntriesByIngredients(ctx, dbgen.ListLiveWatchlistEntriesByIngredientsParams{
		Ingredients: names,
		Today:       &today,
	})
	if err != nil {
		return nil, fmt.Errorf("lookup watchlist by ingredient: %w", err)
	}
	entries := make([]Entry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, entryFromFields(row.WatchlistEntry, row.SourceIssuingBody, row.SourceTitle, row.SourceUrl, row.SourceIssuedAt))
	}
	return entries, nil
}

// List returns every entry, including inactive ones, for the admin CLI.
func (s *Store) List(ctx context.Context) ([]Entry, error) {
	rows, err := s.queries.ListWatchlistEntries(ctx)
	if err != nil {
		return nil, fmt.Errorf("list watchlist entries: %w", err)
	}
	entries := make([]Entry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, entryFromFields(row.WatchlistEntry, row.SourceIssuingBody, row.SourceTitle, row.SourceUrl, row.SourceIssuedAt))
	}
	return entries, nil
}

// Deactivate retires one entry without deleting its history. False means no
// entry had that ID.
func (s *Store) Deactivate(ctx context.Context, id int64) (bool, error) {
	rows, err := s.queries.DeactivateWatchlistEntry(ctx, id)
	if err != nil {
		return false, fmt.Errorf("deactivate watchlist entry %d: %w", id, err)
	}
	return rows == 1, nil
}

// Counts reports total and active entry counts.
func (s *Store) Counts(ctx context.Context) (total, active int, err error) {
	row, err := s.queries.WatchlistCounts(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("count watchlist entries: %w", err)
	}
	return int(row.Total), int(row.Active), nil
}

// Stale returns active entries whose review is overdue, so an operator can see
// a watchlist going out of date before acting on it.
func (s *Store) Stale(ctx context.Context, now time.Time) ([]Entry, error) {
	today := DateOnly(now)
	rows, err := s.queries.ListStaleWatchlistEntries(ctx, &today)
	if err != nil {
		return nil, fmt.Errorf("list stale watchlist entries: %w", err)
	}
	entries := make([]Entry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, entryFromFields(row.WatchlistEntry, row.SourceIssuingBody, row.SourceTitle, row.SourceUrl, row.SourceIssuedAt))
	}
	return entries, nil
}

func entryFromFields(row dbgen.WatchlistEntry, issuingBody, title, sourceURL string, issuedAt time.Time) Entry {
	return Entry{
		ID:               row.ID,
		Kind:             EntryKind(row.Kind),
		NRN:              deref(row.Nrn),
		ProductName:      deref(row.ProductName),
		ProductNameNorm:  deref(row.ProductNameNorm),
		Manufacturer:     deref(row.Manufacturer),
		ManufacturerNorm: deref(row.ManufacturerNorm),
		IngredientNorm:   deref(row.IngredientNorm),
		Reason:           row.Reason,
		EffectiveUntil:   row.EffectiveUntil,
		Active:           row.Active,
		ReviewedBy:       row.ReviewedBy,
		ReviewDueAt:      row.ReviewDueAt,
		CreatedAt:        row.CreatedAt,
		Source: SourceDocument{
			IssuingBody: issuingBody,
			Title:       title,
			URL:         sourceURL,
			IssuedAt:    issuedAt,
		},
	}
}

func sortedEntries(byID map[int64]Entry, limit int) []Entry {
	entries := make([]Entry, 0, len(byID))
	for _, entry := range byID {
		entries = append(entries, entry)
	}
	// Stable order keeps tests and logs deterministic.
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func optionalTime(value *time.Time) *time.Time {
	if value == nil || value.IsZero() {
		return nil
	}
	date := DateOnly(*value)
	return &date
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
