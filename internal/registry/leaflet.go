package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/benfaruna/medverify/internal/platform/clock"
	"github.com/benfaruna/medverify/internal/platform/config"
	"github.com/benfaruna/medverify/internal/storage"
)

// Leaflet document statuses. Only "ok" means a text layer was extracted and
// split into sections; the others are terminal states the report surfaces.
const (
	LeafletStatusOK              = "ok"
	LeafletStatusScannedNeedsOCR = "scanned_needs_ocr"
	LeafletStatusFailed          = "failed"
	LeafletStatusTooLarge        = "too_large"
)

// LeafletTarget is one product whose leaflet should be refreshed.
type LeafletTarget struct {
	ProductID int64
	SourceID  string
	URL       string
}

// SMPCDocument is the pipeline's view of one leaflet download.
type SMPCDocument struct {
	ID               int64
	ProductID        int64
	SourceURL        string
	SHA256           string
	StorageKey       string
	FetchedAt        *time.Time
	ExtractionMethod string
	Status           string
	Error            string
}

// SMPCSection is one numbered section of a leaflet.
type SMPCSection struct {
	Code      string
	Heading   string
	Text      string
	PageStart int
}

// LeafletSource is the download half of Fetcher, kept as an interface so the
// pipeline is testable without HTTP.
type LeafletSource interface {
	FetchLeaflet(context.Context, string) (*Leaflet, error)
}

// TextExtractor turns a PDF file into text.
type TextExtractor interface {
	Extract(context.Context, string) (Extraction, error)
}

// Extraction is the result of reading a PDF's text layer.
type Extraction struct {
	Text    string
	Pages   int
	Method  string
	Scanned bool
}

// LeafletRepository is the persistence side of the pipeline. Store implements
// it; tests use an in-memory fake.
type LeafletRepository interface {
	ListProductsForLeafletSync(ctx context.Context, limit int) ([]LeafletTarget, error)
	GetLeafletTarget(ctx context.Context, productID int64) (LeafletTarget, bool, error)
	GetSMPCDocument(ctx context.Context, productID int64, sourceURL string) (SMPCDocument, bool, error)
	SaveSMPCDocument(ctx context.Context, doc SMPCDocument) (int64, error)
	ReplaceSMPCSections(ctx context.Context, documentID int64, sections []SMPCSection) error
}

// LeafletSummary reports one pipeline run.
type LeafletSummary struct {
	Candidates      int
	Downloaded      int
	Skipped         int
	OK              int
	ScannedNeedsOCR int
	TooLarge        int
	Failed          int
	Sections        int
}

// LeafletOptions configures the pipeline.
type LeafletOptions struct {
	Source    LeafletSource
	Store     LeafletRepository
	Objects   storage.Store
	Extractor TextExtractor
	Clock     clock.Clock
	Logger    *slog.Logger
	MaxBytes  int64
	MaxPages  int
	ExtractTO time.Duration
}

// LeafletPipeline downloads, extracts, and stores leaflets.
type LeafletPipeline struct {
	source    LeafletSource
	store     LeafletRepository
	objects   storage.Store
	extractor TextExtractor
	clock     clock.Clock
	logger    *slog.Logger
	maxBytes  int64
	maxPages  int
	extractTO time.Duration
}

// NewLeafletPipeline validates and builds the pipeline.
func NewLeafletPipeline(opts LeafletOptions) (*LeafletPipeline, error) {
	if opts.Source == nil {
		return nil, fmt.Errorf("leaflet pipeline requires a source")
	}
	if opts.Store == nil {
		return nil, fmt.Errorf("leaflet pipeline requires a store")
	}
	if opts.Objects == nil {
		return nil, fmt.Errorf("leaflet pipeline requires object storage")
	}
	if opts.Extractor == nil {
		return nil, fmt.Errorf("leaflet pipeline requires a text extractor")
	}
	if opts.Clock == nil {
		opts.Clock = clock.System{}
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = 50 << 20
	}
	if opts.MaxPages <= 0 {
		opts.MaxPages = 100
	}
	if opts.ExtractTO <= 0 {
		opts.ExtractTO = 60 * time.Second
	}
	return &LeafletPipeline{
		source:    opts.Source,
		store:     opts.Store,
		objects:   opts.Objects,
		extractor: opts.Extractor,
		clock:     opts.Clock,
		logger:    opts.Logger,
		maxBytes:  opts.MaxBytes,
		maxPages:  opts.MaxPages,
		extractTO: opts.ExtractTO,
	}, nil
}

// NewLeafletPipelineFromConfig wires the live fetcher, Postgres store, poppler
// extractor, and storage from application configuration.
func NewLeafletPipelineFromConfig(pool *pgxpool.Pool, cfg config.Greenbook, objects storage.Store, logger *slog.Logger) (*LeafletPipeline, error) {
	if pool == nil {
		return nil, fmt.Errorf("leaflet pipeline requires a database pool")
	}
	if objects == nil {
		return nil, fmt.Errorf("leaflet pipeline requires object storage")
	}
	fetcher, err := NewFetcher(cfg, logger)
	if err != nil {
		return nil, err
	}
	return NewLeafletPipeline(LeafletOptions{
		Source:    fetcher,
		Store:     NewStore(pool),
		Objects:   objects,
		Extractor: NewPDFExtractor("", "", cfg.MaxPDFPages),
		Logger:    logger,
		MaxBytes:  cfg.MaxPDFBytes,
		MaxPages:  cfg.MaxPDFPages,
	})
}

// Run processes up to limit candidates. One bad leaflet never aborts the run:
// its document row records the failure and the next candidate continues.
func (p *LeafletPipeline) Run(ctx context.Context, limit int) (LeafletSummary, error) {
	targets, err := p.store.ListProductsForLeafletSync(ctx, limit)
	if err != nil {
		return LeafletSummary{}, err
	}

	summary := LeafletSummary{Candidates: len(targets)}
	for _, target := range targets {
		if ctx.Err() != nil {
			return summary, ctx.Err()
		}
		if err := p.process(ctx, target, &summary); err != nil {
			return summary, err
		}
	}
	return summary, nil
}

// RunProduct fetches one product's leaflet and records the outcome. It exists
// for a fetch a person asked for, which must not wait for the next sweep: the
// work is the same process() path, so statuses and the "good extraction is
// never overwritten" rule apply identically.
func (p *LeafletPipeline) RunProduct(ctx context.Context, productID int64) (LeafletSummary, error) {
	target, found, err := p.store.GetLeafletTarget(ctx, productID)
	if err != nil {
		return LeafletSummary{}, err
	}
	if !found {
		// No product or no published leaflet URL: nothing to download. The
		// caller already told the person the leaflet is not on file.
		return LeafletSummary{}, nil
	}
	summary := LeafletSummary{Candidates: 1}
	if err := p.process(ctx, target, &summary); err != nil {
		return summary, err
	}
	return summary, nil
}

func (p *LeafletPipeline) process(ctx context.Context, target LeafletTarget, summary *LeafletSummary) error {
	logger := p.logger.With(
		slog.Int64("product_id", target.ProductID),
		slog.String("source_id", target.SourceID),
	)
	existing, found, err := p.store.GetSMPCDocument(ctx, target.ProductID, target.URL)
	if err != nil {
		return err
	}

	leaflet, err := p.source.FetchLeaflet(ctx, target.URL)
	if err != nil {
		summary.Failed++
		logger.WarnContext(ctx, "leaflet download failed", slog.Any("err", err))
		if !found || existing.Status != LeafletStatusOK {
			return p.saveStatus(ctx, target, existing, LeafletStatusFailed, err.Error())
		}
		return nil
	}
	defer func() { _ = leaflet.Body.Close() }()

	path, hash, size, err := spoolLeaflet(leaflet.Body, p.maxBytes)
	if err != nil {
		summary.Failed++
		if errors.Is(err, ErrTooLarge) {
			summary.TooLarge++
			return p.saveStatus(ctx, target, existing, LeafletStatusTooLarge, err.Error())
		}
		logger.WarnContext(ctx, "leaflet spool failed", slog.Any("err", err))
		return p.saveStatus(ctx, target, existing, LeafletStatusFailed, err.Error())
	}
	defer func() { _ = os.Remove(path) }()
	summary.Downloaded++

	// The URL content is unchanged. Keep the existing rows and only bump
	// fetched_at so the candidate query stops selecting it every night.
	if found && existing.SHA256 == hash && existing.StorageKey != "" &&
		(existing.Status == LeafletStatusOK || existing.Status == LeafletStatusScannedNeedsOCR) {
		now := p.clock.Now().UTC()
		existing.FetchedAt = &now
		if _, err := p.store.SaveSMPCDocument(ctx, existing); err != nil {
			return err
		}
		summary.Skipped++
		return nil
	}

	key := leafletStorageKey(target.SourceID, hash)
	if err := p.upload(ctx, key, path, size); err != nil {
		summary.Failed++
		logger.WarnContext(ctx, "leaflet upload failed", slog.Any("err", err))
		return p.saveStatus(ctx, target, existing, LeafletStatusFailed, err.Error())
	}

	extractCtx, cancel := context.WithTimeout(ctx, p.extractTO)
	extraction, extractErr := p.extractor.Extract(extractCtx, path)
	cancel()

	doc := SMPCDocument{
		ProductID:        target.ProductID,
		SourceURL:        target.URL,
		SHA256:           hash,
		StorageKey:       key,
		FetchedAt:        timePtr(p.clock.Now().UTC()),
		ExtractionMethod: extraction.Method,
	}
	switch {
	case errors.Is(extractErr, ErrTooManyPages):
		summary.TooLarge++
		doc.Status = LeafletStatusTooLarge
		doc.Error = extractErr.Error()
	case extractErr != nil:
		summary.Failed++
		doc.Status = LeafletStatusFailed
		doc.Error = extractErr.Error()
		logger.WarnContext(ctx, "leaflet extraction failed", slog.Any("err", extractErr))
	case extraction.Scanned || len(strings.TrimSpace(extraction.Text)) == 0:
		summary.ScannedNeedsOCR++
		doc.Status = LeafletStatusScannedNeedsOCR
	default:
		summary.OK++
		doc.Status = LeafletStatusOK
	}

	docID, err := p.store.SaveSMPCDocument(ctx, doc)
	if err != nil {
		return err
	}
	if doc.Status != LeafletStatusOK {
		return p.store.ReplaceSMPCSections(ctx, docID, nil)
	}

	sections := SplitSections(extraction.Text)
	summary.Sections += len(sections)
	if err := p.store.ReplaceSMPCSections(ctx, docID, sections); err != nil {
		return err
	}
	logger.InfoContext(ctx, "leaflet stored",
		slog.String("status", doc.Status),
		slog.Int("sections", len(sections)),
	)
	return nil
}

func (p *LeafletPipeline) upload(ctx context.Context, key, path string, size int64) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open spooled leaflet: %w", err)
	}
	defer func() { _ = file.Close() }()
	return p.objects.Put(ctx, key, file, storage.PutOptions{
		ContentType: "application/pdf",
		Size:        size,
	})
}

func (p *LeafletPipeline) saveStatus(ctx context.Context, target LeafletTarget, existing SMPCDocument, status, message string) error {
	if existing.ID != 0 && existing.Status == LeafletStatusOK {
		// Never throw away a good extraction because of a transient download
		// failure. The next run retries and the log keeps the error visible.
		return nil
	}
	existing.ProductID = target.ProductID
	existing.SourceURL = target.URL
	existing.Status = status
	existing.Error = message
	existing.FetchedAt = timePtr(p.clock.Now().UTC())
	_, err := p.store.SaveSMPCDocument(ctx, existing)
	return err
}

func leafletStorageKey(sourceID, hash string) string {
	return "leaflets/" + sourceID + "/" + hash + ".pdf"
}

// spoolLeaflet streams a response to a temporary file while hashing it. The
// PDF is never held in memory, and a body over the cap is rejected.
func spoolLeaflet(body io.Reader, maxBytes int64) (path, hash string, size int64, err error) {
	file, err := os.CreateTemp("", "medverify-leaflet-*.pdf")
	if err != nil {
		return "", "", 0, fmt.Errorf("create leaflet temp file: %w", err)
	}
	path = file.Name()
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(path)
		}
	}()

	hasher := sha256.New()
	limited := io.LimitReader(body, maxBytes+1)
	size, err = io.Copy(io.MultiWriter(file, hasher), limited)
	if err != nil {
		return "", "", 0, fmt.Errorf("write leaflet temp file: %w", err)
	}
	if size > maxBytes {
		return "", "", 0, fmt.Errorf("%w: leaflet larger than %d bytes", ErrTooLarge, maxBytes)
	}
	return path, hex.EncodeToString(hasher.Sum(nil)), size, nil
}

func timePtr(t time.Time) *time.Time { return &t }
