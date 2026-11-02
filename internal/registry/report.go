package registry

import (
	"context"
	"fmt"
	"time"
)

// IngredientCount is one normalized ingredient name and how many product rows
// use it.
type IngredientCount struct {
	Name  string
	Count int64
	Known bool
}

// SyncRunReport is one row of sync history for the ingestion report.
type SyncRunReport struct {
	ID          int64
	StartedAt   time.Time
	FinishedAt  *time.Time
	Status      string
	RowsSeen    int32
	RowsAdded   int32
	RowsChanged int32
	RowsFailed  int32
	Error       string
}

// IngestionReport summarises what the local registry currently contains. It is
// diagnostic only: it never declares a medicine safe or genuine.
type IngestionReport struct {
	Products            int64
	Ingredients         int64
	ProductsWithLeaflet int64
	Sections            int64
	LeafletStatuses     map[string]int64
	SyncRuns            []SyncRunReport
	UnknownIngredients  []IngredientCount
	IngredientNamesSeen int
}

// BuildIngestionReport gathers counts, recent sync runs, leaflet statuses, and
// the most common ingredient names that are not yet in the synonym table.
func (s *Store) BuildIngestionReport(ctx context.Context, recentRuns, ingredientLimit, unknownLimit int) (IngestionReport, error) {
	if recentRuns <= 0 {
		recentRuns = 10
	}
	if ingredientLimit <= 0 {
		ingredientLimit = 1000
	}
	if unknownLimit <= 0 {
		unknownLimit = 50
	}

	report := IngestionReport{LeafletStatuses: make(map[string]int64)}

	var err error
	if report.Products, err = s.queries.CountProducts(ctx); err != nil {
		return IngestionReport{}, fmt.Errorf("count products: %w", err)
	}
	if report.Ingredients, err = s.queries.CountProductIngredients(ctx); err != nil {
		return IngestionReport{}, fmt.Errorf("count product ingredients: %w", err)
	}
	if report.ProductsWithLeaflet, err = s.queries.CountProductsWithLeafletURL(ctx); err != nil {
		return IngestionReport{}, fmt.Errorf("count products with leaflet URL: %w", err)
	}
	if report.Sections, err = s.queries.CountSMPCSections(ctx); err != nil {
		return IngestionReport{}, fmt.Errorf("count leaflet sections: %w", err)
	}

	statuses, err := s.queries.CountSMPCDocumentsByStatus(ctx)
	if err != nil {
		return IngestionReport{}, fmt.Errorf("count leaflet statuses: %w", err)
	}
	for _, status := range statuses {
		report.LeafletStatuses[status.Status] = status.Count
	}

	runs, err := s.queries.ListRecentSyncRuns(ctx, int32(recentRuns))
	if err != nil {
		return IngestionReport{}, fmt.Errorf("list recent sync runs: %w", err)
	}
	for _, run := range runs {
		item := SyncRunReport{
			ID:          run.ID,
			StartedAt:   run.StartedAt,
			FinishedAt:  run.FinishedAt,
			Status:      run.Status,
			RowsSeen:    run.RowsSeen,
			RowsAdded:   run.RowsAdded,
			RowsChanged: run.RowsChanged,
			RowsFailed:  run.RowsFailed,
		}
		if run.Error != nil {
			item.Error = *run.Error
		}
		report.SyncRuns = append(report.SyncRuns, item)
	}

	names, err := s.queries.ListIngredientNameCounts(ctx, int32(ingredientLimit))
	if err != nil {
		return IngestionReport{}, fmt.Errorf("list ingredient names: %w", err)
	}
	report.IngredientNamesSeen = len(names)
	for _, name := range names {
		known := KnownIngredient(name.NameNorm)
		if known {
			continue
		}
		if len(report.UnknownIngredients) < unknownLimit {
			report.UnknownIngredients = append(report.UnknownIngredients, IngredientCount{
				Name:  name.NameNorm,
				Count: name.Count,
			})
		}
	}
	return report, nil
}
