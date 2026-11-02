package registry

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"golang.org/x/text/unicode/norm"

	"github.com/benfaruna/medverify/internal/platform/db/dbgen"
)

// SearchResult is one trigram match with its similarity score.
type SearchResult struct {
	Product Product
	Score   float64
}

// GetByNRN looks up a product by its normalized NAFDAC number. It returns
// found=false when no product matches; that is a data state, not proof that a
// medicine is fake.
func (s *Store) GetByNRN(ctx context.Context, rawNRN string) (Product, bool, error) {
	nrn := NormalizeNRN(rawNRN)
	if nrn == "" {
		return Product{}, false, nil
	}
	row, err := s.queries.GetProductByNRN(ctx, optional(nrn))
	if errors.Is(err, pgx.ErrNoRows) {
		return Product{}, false, nil
	}
	if err != nil {
		return Product{}, false, fmt.Errorf("lookup product by NRN: %w", err)
	}
	return productFromRow(row), true, nil
}

// GetBySourceID looks up a product by its Greenbook identifier.
func (s *Store) GetBySourceID(ctx context.Context, sourceID string) (Product, bool, error) {
	row, err := s.queries.GetProductBySourceID(ctx, sourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Product{}, false, nil
	}
	if err != nil {
		return Product{}, false, fmt.Errorf("lookup product by source ID: %w", err)
	}
	return productFromRow(row), true, nil
}

// SearchByName runs the pg_trgm similarity search. Empty queries return no
// results rather than the whole table.
func (s *Store) SearchByName(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	query = normalizeSearchQuery(query)
	if query == "" || limit <= 0 {
		return nil, nil
	}
	rows, err := s.queries.SearchProductsByName(ctx, dbgen.SearchProductsByNameParams{
		Query:    query,
		RowLimit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("search products by name: %w", err)
	}
	results := make([]SearchResult, 0, len(rows))
	for _, row := range rows {
		results = append(results, SearchResult{
			Product: productFromRow(row.Product),
			Score:   row.Score,
		})
	}
	return results, nil
}

// ListByIngredient returns products whose normalized ingredient matches.
func (s *Store) ListByIngredient(ctx context.Context, rawName string, limit int) ([]Product, error) {
	name := NormalizeIngredient(rawName)
	if name == "" || limit <= 0 {
		return nil, nil
	}
	rows, err := s.queries.ListProductsByIngredient(ctx, dbgen.ListProductsByIngredientParams{
		NameNorm: name,
		RowLimit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list products by ingredient: %w", err)
	}
	products := make([]Product, 0, len(rows))
	for _, row := range rows {
		products = append(products, productFromRow(row))
	}
	return products, nil
}

// Ingredients loads the normalized ingredient rows for one product ID.
func (s *Store) Ingredients(ctx context.Context, productID int64) ([]Ingredient, error) {
	rows, err := s.queries.ListProductIngredients(ctx, productID)
	if err != nil {
		return nil, fmt.Errorf("list product ingredients: %w", err)
	}
	ingredients := make([]Ingredient, 0, len(rows))
	for _, row := range rows {
		ingredient := Ingredient{
			NameRaw:     row.NameRaw,
			NameNorm:    row.NameNorm,
			StrengthRaw: deref(row.StrengthRaw),
			Strength: Strength{
				Value:   row.StrengthValue,
				Unit:    deref(row.StrengthUnit),
				PerUnit: deref(row.PerUnit),
			},
		}
		ingredients = append(ingredients, ingredient)
	}
	return ingredients, nil
}

func productFromRow(row dbgen.Product) Product {
	return Product{
		ID:                   row.ID,
		SourceID:             row.SourceID,
		NRN:                  deref(row.Nrn),
		Name:                 row.Name,
		Category:             deref(row.Category),
		Status:               row.Status,
		Applicant:            deref(row.Applicant),
		Manufacturer:         deref(row.Manufacturer),
		ManufacturerSourceID: deref(row.ManufacturerSourceID),
		CompositionRaw:       deref(row.CompositionRaw),
		Form:                 deref(row.Form),
		Route:                deref(row.Route),
		StrengthRaw:          deref(row.StrengthRaw),
		PackSize:             deref(row.PackSize),
		ATCCode:              deref(row.AtcCode),
		ApprovedAt:           row.ApprovedAt,
		ExpiresAt:            row.ExpiresAt,
		SmpcURL:              deref(row.SmpcUrl),
		Raw:                  row.Raw,
		FetchedAt:            row.FetchedAt,
	}
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// normalizeSearchQuery prepares a product-name query for pg_trgm without
// applying ingredient salt stripping, which would change words like "sodium".
func normalizeSearchQuery(query string) string {
	query = norm.NFKC.String(query)
	query = strings.ToLower(query)
	return strings.Join(strings.Fields(query), " ")
}
