package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode"
)

// Embedder turns text into vectors for one fixed model. Documents and queries
// have separate methods because some providers embed them with different
// instructions; callers must not mix the two modes.
type Embedder interface {
	EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
	Model() string
	Dim() int
}

// ErrEmbeddingMismatch is returned when a provider answers with a vector that
// does not match the configured dimension or batch.
var ErrEmbeddingMismatch = errors.New("embedding response does not match the configured model")

// FakeEmbedder is a deterministic, dependency-free embedder for tests and for
// local development without a provider key. It is lexical, not semantic: it
// hashes words into dimensions, so only tests that rely on shared words are
// meaningful. It must never be used to build a production index.
type FakeEmbedder struct {
	dim   int
	model string
}

// NewFakeEmbedder builds a fake embedder. A non-positive dimension means 1024.
func NewFakeEmbedder(dim int, model string) *FakeEmbedder {
	if dim <= 0 {
		dim = 1024
	}
	if model == "" {
		model = "fake-embed-v1"
	}
	return &FakeEmbedder{dim: dim, model: model}
}

// Model implements Embedder.
func (f *FakeEmbedder) Model() string { return f.model }

// Dim implements Embedder.
func (f *FakeEmbedder) Dim() int { return f.dim }

// EmbedDocuments implements Embedder.
func (f *FakeEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		out[i] = f.vector(text)
	}
	return out, nil
}

// EmbedQuery implements Embedder.
func (f *FakeEmbedder) EmbedQuery(_ context.Context, text string) ([]float32, error) {
	return f.vector(text), nil
}

// vector hashes each word and its immediate successor into the fixed space and
// normalizes the result. Identical text always yields an identical vector.
func (f *FakeEmbedder) vector(text string) []float32 {
	vec := make([]float32, f.dim)
	words := strings.Fields(strings.ToLower(text))
	for i, word := range words {
		word = strings.TrimFunc(word, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
		if word == "" {
			continue
		}
		add(vec, word, 1)
		if i+1 < len(words) {
			next := strings.TrimFunc(strings.ToLower(words[i+1]), func(r rune) bool {
				return !unicode.IsLetter(r) && !unicode.IsDigit(r)
			})
			if next != "" {
				add(vec, word+" "+next, 0.5)
			}
		}
	}
	normalize(vec)
	return vec
}

func add(vec []float32, token string, weight float32) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(token))
	sum := h.Sum64()
	index := int(sum % uint64(len(vec)))
	if sum&(1<<63) != 0 {
		vec[index] -= weight
		return
	}
	vec[index] += weight
}

func normalize(vec []float32) {
	var norm float64
	for _, value := range vec {
		norm += float64(value) * float64(value)
	}
	if norm == 0 {
		vec[0] = 1
		return
	}
	scale := float32(1 / math.Sqrt(norm))
	for i := range vec {
		vec[i] *= scale
	}
}

// HTTPOptions configures an OpenAI-compatible embeddings endpoint. The provider
// choice is still an open decision, so the client is generic: any service that
// accepts POST {base}/embeddings with {"model", "input"} works.
type HTTPOptions struct {
	BaseURL   string
	APIKey    string
	Model     string
	Dimension int
	BatchSize int
	Timeout   time.Duration
	Client    *http.Client
}

// HTTPEmbedder calls an OpenAI-compatible embeddings endpoint.
type HTTPEmbedder struct {
	baseURL   string
	apiKey    string
	model     string
	dim       int
	batchSize int
	client    *http.Client
}

// NewHTTPEmbedder validates the configuration and builds the client. It fails
// rather than guessing a dimension: a wrong dimension would be rejected by the
// database on every write.
func NewHTTPEmbedder(opts HTTPOptions) (*HTTPEmbedder, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("rag: embeddings base URL is required")
	}
	if strings.TrimSpace(opts.Model) == "" {
		return nil, fmt.Errorf("rag: embeddings model is required")
	}
	if opts.Dimension <= 0 {
		return nil, fmt.Errorf("rag: embeddings dimension must be positive")
	}
	batch := opts.BatchSize
	if batch <= 0 {
		batch = 64
	}
	client := opts.Client
	if client == nil {
		if opts.Timeout <= 0 {
			opts.Timeout = 30 * time.Second
		}
		client = &http.Client{Timeout: opts.Timeout}
	}
	return &HTTPEmbedder{
		baseURL:   baseURL,
		apiKey:    opts.APIKey,
		model:     opts.Model,
		dim:       opts.Dimension,
		batchSize: batch,
		client:    client,
	}, nil
}

// Model implements Embedder.
func (h *HTTPEmbedder) Model() string { return h.model }

// Dim implements Embedder.
func (h *HTTPEmbedder) Dim() int { return h.dim }

// EmbedDocuments implements Embedder, batching requests to keep each call small.
func (h *HTTPEmbedder) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += h.batchSize {
		end := min(start+h.batchSize, len(texts))
		vectors, err := h.embed(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vectors...)
	}
	return out, nil
}

// EmbedQuery implements Embedder.
func (h *HTTPEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	vectors, err := h.embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 {
		return nil, fmt.Errorf("%w: expected 1 query vector, got %d", ErrEmbeddingMismatch, len(vectors))
	}
	return vectors[0], nil
}

func (h *HTTPEmbedder) embed(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{
		"model":           h.model,
		"input":           texts,
		"encoding_format": "float",
	})
	if err != nil {
		return nil, fmt.Errorf("rag: encode embeddings request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("rag: build embeddings request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if h.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+h.apiKey)
	}

	response, err := h.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("rag: embeddings request failed: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// Read a bounded slice for diagnostics; never include the API key.
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return nil, fmt.Errorf("rag: embeddings endpoint returned %d: %s", response.StatusCode, strings.TrimSpace(string(detail)))
	}

	var decoded struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<20)).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("rag: decode embeddings response: %w", err)
	}
	if len(decoded.Data) != len(texts) {
		return nil, fmt.Errorf("%w: asked for %d vectors, got %d", ErrEmbeddingMismatch, len(texts), len(decoded.Data))
	}

	out := make([][]float32, len(texts))
	for _, item := range decoded.Data {
		if item.Index < 0 || item.Index >= len(texts) {
			return nil, fmt.Errorf("%w: vector index %d out of range", ErrEmbeddingMismatch, item.Index)
		}
		if len(item.Embedding) != h.dim {
			return nil, fmt.Errorf("%w: expected %d dimensions, got %d", ErrEmbeddingMismatch, h.dim, len(item.Embedding))
		}
		out[item.Index] = item.Embedding
	}
	for i, vector := range out {
		if vector == nil {
			return nil, fmt.Errorf("%w: missing vector at index %d", ErrEmbeddingMismatch, i)
		}
	}
	return out, nil
}
