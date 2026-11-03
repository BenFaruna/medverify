package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/benfaruna/medverify/internal/rag"
)

// leafletIndexer adapts the RAG store to the job worker's runner interface. It
// only chunks and embeds leaflets; it never touches scans or the registry.
type leafletIndexer struct {
	store    *rag.Store
	embedder rag.Embedder
	delay    time.Duration
	logger   *slog.Logger
}

func (i *leafletIndexer) IndexLeaflets(ctx context.Context, limit int) (rag.IndexSummary, error) {
	if limit <= 0 {
		limit = 50
	}
	return i.store.Index(ctx, i.embedder, rag.IndexOptions{
		Limit:  limit,
		Delay:  i.delay,
		Logger: i.logger,
	})
}
