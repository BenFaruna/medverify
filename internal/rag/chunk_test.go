package rag_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"

	"github.com/benfaruna/medverify/internal/rag"
)

func fixtureDocument() rag.Document {
	return rag.Document{
		ProductID:   1,
		DocumentID:  10,
		ProductName: "Paracetamol Tablets BP 500 mg",
		NRN:         "04-0858",
		SourceURL:   "https://example.test/leaflets/paracetamol.pdf",
	}
}

func fixtureSections() []rag.Section {
	return []rag.Section{
		{
			ID:      1,
			Code:    "4.2",
			Heading: "Posology and method of administration",
			Text: "Adults and children over 12 years: 500 mg to 1000 mg every 4 to 6 hours as required. " +
				"Do not exceed 4000 mg in 24 hours. The dose should be reduced in patients with hepatic impairment.",
		},
		{
			ID:      2,
			Code:    "4.4",
			Heading: "Special warnings and precautions for use",
			Text: "Care is advised in the treatment of patients with renal or hepatic impairment. " +
				"Patients should be advised not to take other paracetamol-containing products concurrently. " +
				"Severe skin reactions have been reported rarely.",
		},
	}
}

func TestChunksStayInsideTheirSectionAndKeepCitationData(t *testing.T) {
	chunks := rag.ChunkDocument(fixtureDocument(), fixtureSections(), rag.Options{Model: "test-model"})
	require.Len(t, chunks, 2)

	for i, chunk := range chunks {
		assert.Equal(t, i, chunk.Index, "chunk indexes are contiguous per document")
		assert.NotZero(t, chunk.SectionID)
		assert.NotZero(t, chunk.DocumentID)
		assert.NotEmpty(t, chunk.Text)
		assert.NotEmpty(t, chunk.Hash)
		assert.Positive(t, chunk.Tokens)
	}

	assert.Equal(t, "4.2", chunks[0].SectionCode)
	assert.Equal(t, "4.4", chunks[1].SectionCode)
	assert.Contains(t, chunks[0].Text, "Adults and children over 12 years")
	assert.NotContains(t, chunks[0].Text, "Care is advised")
	assert.NotContains(t, chunks[1].Text, "Adults and children")
}

func TestEmbeddingTextCarriesContextButDisplayTextDoesNot(t *testing.T) {
	chunks := rag.ChunkDocument(fixtureDocument(), fixtureSections(), rag.Options{Model: "test-model"})
	require.NotEmpty(t, chunks)

	for _, chunk := range chunks {
		assert.Contains(t, chunk.EmbeddingText, "Paracetamol Tablets BP 500 mg (04-0858)")
		assert.NotContains(t, chunk.Text, "Paracetamol Tablets BP 500 mg (04-0858)",
			"the context header must not be quoted back to the user")
		assert.NotContains(t, chunk.Text, "https://")
	}
}

func TestChunkingRespectsTheSizeLimitAndNeverSplitsMidSentence(t *testing.T) {
	long := strings.Repeat("This paragraph describes a warning about the medicine and its use in adults. ", 40)
	sections := []rag.Section{{ID: 1, Code: "4.4", Heading: "Warnings", Text: long}}
	// The source has no blank lines, so the chunker must fall back to sentences.
	chunks := rag.ChunkDocument(fixtureDocument(), sections, rag.Options{
		TargetTokens: 120, MaxTokens: 150, Overlap: 20, Model: "test-model",
	})
	require.Greater(t, len(chunks), 1)

	for _, chunk := range chunks {
		assert.LessOrEqual(t, rag.EstimateTokens(chunk.Text), 150, "chunk %d exceeds the max", chunk.Index)
		assert.True(t, strings.HasSuffix(chunk.Text, "."),
			"chunk %d should end on a sentence boundary: %q", chunk.Index, tail(chunk.Text))
	}
}

func TestConsecutiveChunksOverlap(t *testing.T) {
	paragraphs := []string{
		"First paragraph explains what the medicine contains and who it is for.",
		"Second paragraph lists the warnings that apply to most patients.",
		"Third paragraph describes storage conditions and the shelf life.",
	}
	text := strings.Join(paragraphs, "\n\n")
	chunks := rag.ChunkDocument(fixtureDocument(), []rag.Section{{ID: 1, Text: text}}, rag.Options{
		TargetTokens: 30, MaxTokens: 40, Overlap: 20, Model: "test-model",
	})
	require.Greater(t, len(chunks), 1)

	// The second chunk repeats the paragraph the first chunk ended with, so a
	// quote that straddles the boundary is never lost.
	assert.True(t, strings.HasPrefix(chunks[1].Text, "Second paragraph"),
		"second chunk should start at the overlap, starts %q", head(chunks[1].Text))
	assert.Contains(t, chunks[0].Text, "Second paragraph")
	assert.Contains(t, chunks[1].Text, "Third paragraph")
}

func TestChunkHashesChangeWithTheModel(t *testing.T) {
	a := rag.ChunkDocument(fixtureDocument(), fixtureSections(), rag.Options{Model: "model-a"})
	b := rag.ChunkDocument(fixtureDocument(), fixtureSections(), rag.Options{Model: "model-b"})
	require.Len(t, a, len(b))

	for i := range a {
		assert.Equal(t, a[i].Text, b[i].Text)
		assert.NotEqual(t, a[i].Hash, b[i].Hash,
			"a new embedding model must invalidate the stored chunk hash")
	}
}

func TestEmptySectionsAreSkipped(t *testing.T) {
	chunks := rag.ChunkDocument(fixtureDocument(), []rag.Section{
		{ID: 1, Text: "   "},
		{ID: 2, Text: "Real content."},
	}, rag.Options{Model: "test-model"})
	require.Len(t, chunks, 1)
	assert.Equal(t, int64(2), chunks[0].SectionID)
}

func TestChunkingIsDeterministic(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		text := rapid.StringMatching(`[A-Za-z0-9 .,\n]{0,400}`).Draw(t, "text")
		sections := []rag.Section{{ID: 1, Code: "4.4", Heading: "Warnings", Text: text}}
		opts := rag.Options{TargetTokens: 40, MaxTokens: 60, Overlap: 10, Model: "test-model"}

		first := rag.ChunkDocument(fixtureDocument(), sections, opts)
		second := rag.ChunkDocument(fixtureDocument(), sections, opts)
		require.Len(t, second, len(first))
		for i := range first {
			if first[i].Text != second[i].Text || first[i].Hash != second[i].Hash {
				t.Fatalf("chunk %d differs between runs", i)
			}
			if first[i].Tokens > 60 {
				t.Fatalf("chunk %d has %d tokens, over the limit", i, first[i].Tokens)
			}
		}
	})
}

func tail(s string) string {
	const limit = 60
	if len(s) <= limit {
		return s
	}
	return s[len(s)-limit:]
}

func head(s string) string {
	const limit = 60
	if len(s) <= limit {
		return s
	}
	return s[:limit]
}
