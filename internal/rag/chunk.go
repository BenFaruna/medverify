// Package rag chunks leaflet sections, embeds them, and retrieves the passages
// that answer a question about one specific product.
package rag

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"
)

// Chunking defaults. A chunk is a few paragraphs of one section, sized so that
// a retrieval result is readable on a phone without truncation.
const (
	DefaultTargetTokens = 400
	DefaultMaxTokens    = 500
	DefaultOverlap      = 50

	tokensPerWord = 4 // rough English ratio: 4/3 tokens per word
)

// Document identifies the leaflet a set of sections belongs to. ProductName and
// NRN are used only in the embedding header; they never appear in the stored
// display text.
type Document struct {
	ProductID   int64
	DocumentID  int64
	ProductName string
	NRN         string
	SourceURL   string
	PageStart   int
}

// Section is one leaflet section as extracted in phase 02. Code may be empty
// for a document that could not be split into recognised sections.
type Section struct {
	ID        int64
	Code      string
	Heading   string
	Text      string
	PageStart int
}

// Options controls chunk sizing. The zero value uses the defaults.
type Options struct {
	TargetTokens int
	MaxTokens    int
	Overlap      int
	// Model is part of the chunk hash so a new embedding model re-embeds every
	// chunk instead of silently reusing vectors from another space.
	Model string
}

func (o Options) withDefaults() Options {
	if o.TargetTokens <= 0 {
		o.TargetTokens = DefaultTargetTokens
	}
	if o.MaxTokens <= 0 {
		o.MaxTokens = DefaultMaxTokens
	}
	if o.Overlap < 0 {
		o.Overlap = DefaultOverlap
	}
	if o.Overlap == 0 {
		o.Overlap = DefaultOverlap
	}
	if o.Overlap >= o.TargetTokens {
		o.Overlap = o.TargetTokens / 4
	}
	return o
}

// Chunk is one retrievable passage. Text is what a user sees and what a
// citation quotes; EmbeddingText adds the product and section context that
// makes a bare paragraph findable.
type Chunk struct {
	DocumentID    int64
	SectionID     int64
	SectionCode   string
	Heading       string
	PageStart     int
	Index         int
	Text          string
	EmbeddingText string
	Hash          string
	Tokens        int
}

// ChunkDocument splits sections into bounded, overlapping chunks. Sections keep
// their order and are never merged, so a chunk cannot quote two sections as if
// they were one. The output is deterministic for identical input.
func ChunkDocument(doc Document, sections []Section, opts Options) []Chunk {
	opts = opts.withDefaults()

	header := contextHeader(doc)
	chunks := make([]Chunk, 0, len(sections))
	for _, section := range sections {
		text := strings.TrimSpace(section.Text)
		if text == "" {
			continue
		}
		for _, part := range splitSection(text, opts) {
			chunk := Chunk{
				DocumentID:    doc.DocumentID,
				SectionID:     section.ID,
				SectionCode:   section.Code,
				Heading:       section.Heading,
				PageStart:     section.PageStart,
				Index:         len(chunks),
				Text:          part,
				EmbeddingText: header + "\n\n" + part,
				Tokens:        EstimateTokens(part),
			}
			chunk.Hash = chunkHash(chunk.EmbeddingText, opts.Model)
			chunks = append(chunks, chunk)
		}
	}
	return chunks
}

// contextHeader is prepended only to the embedding text. It is not quoted back
// to the user: the citation already names the product and section.
func contextHeader(doc Document) string {
	name := strings.TrimSpace(doc.ProductName)
	nrn := strings.TrimSpace(doc.NRN)
	switch {
	case name != "" && nrn != "":
		return fmt.Sprintf("%s (%s)", name, nrn)
	case name != "":
		return name
	case nrn != "":
		return nrn
	default:
		return "Leaflet"
	}
}

// splitSection breaks one section into overlapping pieces of at most
// opts.MaxTokens. It prefers paragraph boundaries, then sentence boundaries, and
// only splits inside a sentence when a single sentence is longer than a whole
// chunk.
func splitSection(text string, opts Options) []string {
	pieces := sectionPieces(text, opts.MaxTokens)

	var (
		out    []string
		buffer []string
		tokens int
		fresh  bool // buffer holds at least one piece not carried over as overlap
	)
	flush := func() {
		if !fresh {
			// The buffer is pure overlap, so a flush would emit a duplicate
			// fragment. Drop it and let the next piece start a clean chunk.
			buffer, tokens = nil, 0
			return
		}
		out = append(out, strings.Join(buffer, "\n\n"))
		buffer = trailingOverlap(buffer, opts.Overlap)
		tokens = 0
		for _, piece := range buffer {
			tokens += EstimateTokens(piece)
		}
		fresh = false
	}

	for _, piece := range pieces {
		size := EstimateTokens(piece)
		if len(buffer) > 0 && tokens+size > opts.MaxTokens {
			flush()
			// The overlap yields to the size cap rather than producing a chunk
			// over the limit.
			if len(buffer) > 0 && tokens+size > opts.MaxTokens {
				buffer, tokens = nil, 0
			}
		}
		buffer = append(buffer, piece)
		tokens += size
		fresh = true
	}
	if len(buffer) > 0 {
		out = append(out, strings.Join(buffer, "\n\n"))
	}
	return out
}

// sectionPieces returns paragraph groups and sentences that each fit inside one
// chunk. Oversized paragraphs are reduced to sentences; an oversized sentence is
// split on word boundaries as a last resort.
func sectionPieces(text string, maxTokens int) []string {
	paragraphs := splitParagraphs(text)
	pieces := make([]string, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		if EstimateTokens(paragraph) <= maxTokens {
			pieces = append(pieces, paragraph)
			continue
		}
		for _, sentence := range splitSentences(paragraph) {
			if EstimateTokens(sentence) <= maxTokens {
				pieces = append(pieces, sentence)
				continue
			}
			pieces = append(pieces, splitOversized(sentence, maxTokens)...)
		}
	}
	return pieces
}

func splitParagraphs(text string) []string {
	raw := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n")
	out := make([]string, 0, len(raw))
	for _, paragraph := range raw {
		// A single newline is a line wrap inside a paragraph, not a boundary.
		trimmed := strings.TrimSpace(strings.Join(strings.Fields(paragraph), " "))
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// splitSentences keeps the terminating punctuation with the sentence. It only
// splits when punctuation is followed by whitespace and an uppercase letter or
// a digit, so abbreviations such as "e.g." stay inside their sentence.
func splitSentences(text string) []string {
	var (
		out   []string
		start int
		runes = []rune(text)
	)
	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case '.', '!', '?':
			next := i + 1
			if next >= len(runes) || !unicode.IsSpace(runes[next]) {
				continue
			}
			for next < len(runes) && unicode.IsSpace(runes[next]) {
				next++
			}
			if next >= len(runes) || (!unicode.IsUpper(runes[next]) && !unicode.IsDigit(runes[next])) {
				continue
			}
			sentence := strings.TrimSpace(string(runes[start:next]))
			if sentence != "" {
				out = append(out, sentence)
			}
			start = next
			i = next - 1
		}
	}
	if tail := strings.TrimSpace(string(runes[start:])); tail != "" {
		out = append(out, tail)
	}
	return out
}

// splitOversized breaks a sentence longer than a chunk on word boundaries. This
// only happens for pathological input such as a flattened table.
func splitOversized(text string, maxTokens int) []string {
	words := strings.Fields(text)
	var out []string
	var current []string
	for _, word := range words {
		current = append(current, word)
		if EstimateTokens(strings.Join(current, " ")) >= maxTokens {
			out = append(out, strings.Join(current, " "))
			current = nil
		}
	}
	if len(current) > 0 {
		out = append(out, strings.Join(current, " "))
	}
	return out
}

// trailingOverlap returns whole trailing pieces that fit inside the overlap
// budget, in their original order. Splitting only between pieces keeps the
// overlap on sentence boundaries.
func trailingOverlap(buffer []string, budget int) []string {
	if budget <= 0 || len(buffer) == 0 {
		return nil
	}
	total := 0
	start := len(buffer)
	for i := len(buffer) - 1; i >= 0; i-- {
		size := EstimateTokens(buffer[i])
		if total+size > budget {
			break
		}
		total += size
		start = i
	}
	return append([]string(nil), buffer[start:]...)
}

// EstimateTokens approximates token count without a tokenizer. It is used only
// for chunk sizing, never for billing or limits.
func EstimateTokens(text string) int {
	words := len(strings.Fields(text))
	if words == 0 {
		return 0
	}
	return (words*tokensPerWord + 2) / 3
}

func chunkHash(text, model string) string {
	sum := sha256.Sum256([]byte(model + "\x00" + text))
	return hex.EncodeToString(sum[:])
}
