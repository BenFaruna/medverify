package registry

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/benfaruna/medverify/internal/platform/db/dbgen"
)

// ListProductsForLeafletSync returns products whose leaflet is missing,
// previously failed, or older than the product record itself.
func (s *Store) ListProductsForLeafletSync(ctx context.Context, limit int) ([]LeafletTarget, error) {
	rows, err := s.queries.ListProductsForLeafletSync(ctx, int32(limit))
	if err != nil {
		return nil, fmt.Errorf("list products for leaflet sync: %w", err)
	}
	targets := make([]LeafletTarget, 0, len(rows))
	for _, row := range rows {
		if row.SmpcUrl == nil || *row.SmpcUrl == "" {
			continue
		}
		targets = append(targets, LeafletTarget{
			ProductID: row.ID,
			SourceID:  row.SourceID,
			URL:       *row.SmpcUrl,
		})
	}
	return targets, nil
}

// GetSMPCDocument loads the current document row for a product URL.
func (s *Store) GetSMPCDocument(ctx context.Context, productID int64, sourceURL string) (SMPCDocument, bool, error) {
	row, err := s.queries.GetSMPCDocument(ctx, dbgen.GetSMPCDocumentParams{
		ProductID: productID,
		SourceUrl: sourceURL,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return SMPCDocument{}, false, nil
	}
	if err != nil {
		return SMPCDocument{}, false, fmt.Errorf("get leaflet document: %w", err)
	}
	return smpcDocumentFromRow(row), true, nil
}

// SaveSMPCDocument inserts or replaces the document row and returns its ID.
func (s *Store) SaveSMPCDocument(ctx context.Context, doc SMPCDocument) (int64, error) {
	row, err := s.queries.UpsertSMPCDocument(ctx, dbgen.UpsertSMPCDocumentParams{
		ProductID:        doc.ProductID,
		SourceUrl:        doc.SourceURL,
		Sha256:           optional(doc.SHA256),
		StorageKey:       optional(doc.StorageKey),
		FetchedAt:        doc.FetchedAt,
		ExtractionMethod: optional(doc.ExtractionMethod),
		Status:           doc.Status,
		Error:            optional(doc.Error),
	})
	if err != nil {
		return 0, fmt.Errorf("save leaflet document for product %d: %w", doc.ProductID, err)
	}
	return row.ID, nil
}

// ReplaceSMPCSections swaps a document's sections in one transaction so a
// failed extraction never leaves a half-written section list.
func (s *Store) ReplaceSMPCSections(ctx context.Context, documentID int64, sections []SMPCSection) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin leaflet sections transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := dbgen.New(tx)
	if err := q.DeleteSMPCSections(ctx, documentID); err != nil {
		return fmt.Errorf("delete old leaflet sections: %w", err)
	}
	for _, section := range sections {
		params := dbgen.InsertSMPCSectionParams{
			DocumentID:  documentID,
			SectionCode: optional(section.Code),
			Heading:     optional(section.Heading),
			Text:        section.Text,
		}
		if section.PageStart > 0 {
			page := int32(section.PageStart)
			params.PageStart = &page
		}
		if err := q.InsertSMPCSection(ctx, params); err != nil {
			return fmt.Errorf("insert leaflet section %q: %w", section.Code, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit leaflet sections: %w", err)
	}
	return nil
}

func smpcDocumentFromRow(row dbgen.SmpcDocument) SMPCDocument {
	doc := SMPCDocument{
		ID:        row.ID,
		ProductID: row.ProductID,
		SourceURL: row.SourceUrl,
		FetchedAt: row.FetchedAt,
		Status:    row.Status,
	}
	if row.Sha256 != nil {
		doc.SHA256 = *row.Sha256
	}
	if row.StorageKey != nil {
		doc.StorageKey = *row.StorageKey
	}
	if row.ExtractionMethod != nil {
		doc.ExtractionMethod = *row.ExtractionMethod
	}
	if row.Error != nil {
		doc.Error = *row.Error
	}
	return doc
}
