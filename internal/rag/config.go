package rag

import (
	"fmt"
	"strings"
	"time"

	"github.com/benfaruna/medverify/internal/platform/config"
)

// NewEmbedder builds the configured embedder. The provider choice is still an
// open decision, so "fake" is local-only and "http" targets any OpenAI-compatible
// endpoint rather than a named vendor.
func NewEmbedder(cfg config.Embeddings) (Embedder, error) {
	if cfg.Dimension != VectorDimension {
		return nil, fmt.Errorf("%w: EMBEDDINGS_DIMENSION is %d, the smpc_chunks column is %d",
			ErrEmbeddingMismatch, cfg.Dimension, VectorDimension)
	}
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	switch provider {
	case "", "fake":
		model := strings.TrimSpace(cfg.Model)
		if model == "" {
			model = "fake-embed-v1"
		}
		return NewFakeEmbedder(cfg.Dimension, model), nil
	case "http":
		timeout := cfg.Timeout
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		return NewHTTPEmbedder(HTTPOptions{
			BaseURL:   cfg.BaseURL,
			APIKey:    cfg.APIKey,
			Model:     cfg.Model,
			Dimension: cfg.Dimension,
			BatchSize: cfg.BatchSize,
			Timeout:   timeout,
		})
	default:
		return nil, fmt.Errorf("rag: unknown embeddings provider %q", cfg.Provider)
	}
}
