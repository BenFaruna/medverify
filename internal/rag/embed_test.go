package rag_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/rag"
)

func TestFakeEmbedderIsStableAndNormalized(t *testing.T) {
	embedder := rag.NewFakeEmbedder(64, "fake-test")
	ctx := context.Background()

	first, err := embedder.EmbedDocuments(ctx, []string{"Paracetamol 500 mg", "Ibuprofen 200 mg"})
	require.NoError(t, err)
	require.Len(t, first, 2)
	assert.Len(t, first[0], 64)
	assert.NotEqual(t, first[0], first[1])

	again, err := embedder.EmbedDocuments(ctx, []string{"Paracetamol 500 mg"})
	require.NoError(t, err)
	assert.Equal(t, first[0], again[0], "identical text must give an identical vector")

	query, err := embedder.EmbedQuery(ctx, "Paracetamol 500 mg")
	require.NoError(t, err)
	assert.Equal(t, first[0], query)

	var sum float64
	for _, value := range first[0] {
		sum += float64(value) * float64(value)
	}
	assert.InDelta(t, 1, sum, 1e-5)
}

func TestHTTPEmbedderBatchesAndValidatesDimensions(t *testing.T) {
	var batches []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/embeddings", r.URL.Path)
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))

		var body struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "test-embed", body.Model)
		batches = append(batches, len(body.Input))

		data := make([]map[string]any, len(body.Input))
		for i := range body.Input {
			data[i] = map[string]any{"index": i, "embedding": []float32{0.1, 0.2, 0.3, 0.4}}
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": data}))
	}))
	defer server.Close()

	embedder, err := rag.NewHTTPEmbedder(rag.HTTPOptions{
		BaseURL:   server.URL + "/v1",
		APIKey:    "test-key",
		Model:     "test-embed",
		Dimension: 4,
		BatchSize: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, "test-embed", embedder.Model())
	assert.Equal(t, 4, embedder.Dim())

	vectors, err := embedder.EmbedDocuments(context.Background(), []string{"a", "b", "c"})
	require.NoError(t, err)
	require.Len(t, vectors, 3)
	assert.Equal(t, []int{2, 1}, batches)

	query, err := embedder.EmbedQuery(context.Background(), "a")
	require.NoError(t, err)
	assert.Len(t, query, 4)
}

func TestHTTPEmbedderRejectsWrongDimension(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"index": 0, "embedding": []float32{0.1, 0.2}}},
		})
	}))
	defer server.Close()

	embedder, err := rag.NewHTTPEmbedder(rag.HTTPOptions{
		BaseURL: server.URL, Model: "test-embed", Dimension: 4,
	})
	require.NoError(t, err)

	_, err = embedder.EmbedQuery(context.Background(), "a")
	require.ErrorIs(t, err, rag.ErrEmbeddingMismatch)
}

func TestHTTPEmbedderRequiresConfiguration(t *testing.T) {
	_, err := rag.NewHTTPEmbedder(rag.HTTPOptions{Model: "m", Dimension: 4})
	require.Error(t, err, "a missing base URL must fail at startup")

	_, err = rag.NewHTTPEmbedder(rag.HTTPOptions{BaseURL: "https://example.test", Model: "m"})
	require.Error(t, err, "a missing dimension must fail at startup")
}
