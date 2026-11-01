package registry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// ErrTooManyPages is returned when a PDF exceeds the configured page cap.
var ErrTooManyPages = errors.New("pdf exceeds the configured page limit")

// commandRunner runs an external program and returns its stdout. Tests swap it
// for a fake so the extractor logic is covered without poppler installed.
type commandRunner interface {
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		return nil, fmt.Errorf("%s: %w: %s", name, err, message)
	}
	return out, nil
}

// PDFExtractor extracts a text layer with poppler's pdftotext. A scanned
// leaflet yields empty text, which the pipeline records as scanned_needs_ocr.
type PDFExtractor struct {
	pdftotext string
	pdfinfo   string
	maxPages  int
	runner    commandRunner
}

// NewPDFExtractor builds an extractor. Empty binary names fall back to the
// poppler tools on PATH.
func NewPDFExtractor(pdftotext, pdfinfo string, maxPages int) *PDFExtractor {
	if pdftotext == "" {
		pdftotext = "pdftotext"
	}
	if pdfinfo == "" {
		pdfinfo = "pdfinfo"
	}
	if maxPages <= 0 {
		maxPages = 100
	}
	return &PDFExtractor{
		pdftotext: pdftotext,
		pdfinfo:   pdfinfo,
		maxPages:  maxPages,
		runner:    execRunner{},
	}
}

// Extract reads the PDF's text layer.
func (e *PDFExtractor) Extract(ctx context.Context, path string) (Extraction, error) {
	pages := 0
	if e.pdfinfo != "" {
		count, err := e.pageCount(ctx, path)
		if err != nil {
			return Extraction{}, err
		}
		if count > e.maxPages {
			return Extraction{Pages: count, Method: "pdftotext"}, fmt.Errorf(
				"%w: %d pages, limit %d", ErrTooManyPages, count, e.maxPages,
			)
		}
		pages = count
	}

	out, err := e.runner.Output(ctx, e.pdftotext, "-layout", "-enc", "UTF-8", path, "-")
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Extraction{}, fmt.Errorf("pdftotext timed out: %w", ctx.Err())
		}
		return Extraction{}, fmt.Errorf("extract pdf text: %w", err)
	}
	if pages == 0 {
		pages = strings.Count(string(out), "\f")
		if !strings.HasSuffix(string(out), "\f") {
			pages++
		}
	}

	text := string(out)
	return Extraction{
		Text:    text,
		Pages:   pages,
		Method:  "pdftotext",
		Scanned: strings.TrimSpace(normalizePageBreaks(text)) == "",
	}, nil
}

var pagesPattern = regexp.MustCompile(`(?m)^Pages:\s+(\d+)\s*$`)

func (e *PDFExtractor) pageCount(ctx context.Context, path string) (int, error) {
	out, err := e.runner.Output(ctx, e.pdfinfo, path)
	if err != nil {
		return 0, fmt.Errorf("read pdf page count: %w", err)
	}
	match := pagesPattern.FindSubmatch(out)
	if match == nil {
		return 0, fmt.Errorf("read pdf page count: pdfinfo output had no Pages line")
	}
	pages, err := strconv.Atoi(string(match[1]))
	if err != nil {
		return 0, fmt.Errorf("read pdf page count: %w", err)
	}
	return pages, nil
}

// normalizePageBreaks turns form feeds into newlines for emptiness checks.
func normalizePageBreaks(text string) string {
	return strings.ReplaceAll(text, "\f", "\n")
}
