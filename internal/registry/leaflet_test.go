package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/clock"
	"github.com/benfaruna/medverify/internal/storage"
)

type fakeLeafletSource struct {
	bodies map[string]string
	err    error
}

func (f *fakeLeafletSource) FetchLeaflet(_ context.Context, url string) (*Leaflet, error) {
	if f.err != nil {
		return nil, f.err
	}
	body, ok := f.bodies[url]
	if !ok {
		return nil, errors.New("not found")
	}
	return &Leaflet{URL: url, Body: io.NopCloser(strings.NewReader(body)), Size: int64(len(body))}, nil
}

type fakeExtractor struct {
	result Extraction
	err    error
}

func (f *fakeExtractor) Extract(context.Context, string) (Extraction, error) {
	return f.result, f.err
}

type fakeLeafletRepo struct {
	targets  []LeafletTarget
	docs     map[string]SMPCDocument
	sections map[int64][]SMPCSection
	saved    []SMPCDocument
	nextID   int64
}

func newFakeLeafletRepo(targets ...LeafletTarget) *fakeLeafletRepo {
	return &fakeLeafletRepo{
		targets:  targets,
		docs:     make(map[string]SMPCDocument),
		sections: make(map[int64][]SMPCSection),
		nextID:   1,
	}
}

func docKey(productID int64, url string) string {
	return strconv.FormatInt(productID, 10) + "|" + url
}

func (f *fakeLeafletRepo) ListProductsForLeafletSync(context.Context, int) ([]LeafletTarget, error) {
	return f.targets, nil
}

func (f *fakeLeafletRepo) GetSMPCDocument(_ context.Context, productID int64, url string) (SMPCDocument, bool, error) {
	doc, ok := f.docs[docKey(productID, url)]
	return doc, ok, nil
}

func (f *fakeLeafletRepo) SaveSMPCDocument(_ context.Context, doc SMPCDocument) (int64, error) {
	key := docKey(doc.ProductID, doc.SourceURL)
	if existing, ok := f.docs[key]; ok {
		doc.ID = existing.ID
	} else {
		doc.ID = f.nextID
		f.nextID++
	}
	f.docs[key] = doc
	f.saved = append(f.saved, doc)
	return doc.ID, nil
}

func (f *fakeLeafletRepo) ReplaceSMPCSections(_ context.Context, id int64, sections []SMPCSection) error {
	f.sections[id] = sections
	return nil
}

func newTestPipeline(t *testing.T, repo *fakeLeafletRepo, source LeafletSource, extractor TextExtractor, objects storage.Store, maxBytes int64) *LeafletPipeline {
	t.Helper()
	pipeline, err := NewLeafletPipeline(LeafletOptions{
		Source:    source,
		Store:     repo,
		Objects:   objects,
		Extractor: extractor,
		Clock:     &clock.Fixed{Current: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)},
		MaxBytes:  maxBytes,
	})
	require.NoError(t, err)
	return pipeline
}

func TestLeafletPipelineStoresSections(t *testing.T) {
	target := LeafletTarget{ProductID: 1, SourceID: "5576", URL: "https://admin.greenbook.nafdac.gov.ng/a.pdf"}
	repo := newFakeLeafletRepo(target)
	objects := &storage.Fake{}
	source := &fakeLeafletSource{bodies: map[string]string{target.URL: "%PDF-test"}}
	extractor := &fakeExtractor{result: Extraction{
		Text:   "1. NAME OF THE MEDICINAL PRODUCT\nParacetamol\n2. COMPOSITION\n500 mg",
		Method: "pdftotext",
	}}

	summary, err := newTestPipeline(t, repo, source, extractor, objects, 1<<20).Run(context.Background(), 10)
	require.NoError(t, err)
	assert.Equal(t, 1, summary.OK)
	assert.Equal(t, 2, summary.Sections)

	doc := repo.docs[docKey(target.ProductID, target.URL)]
	assert.Equal(t, LeafletStatusOK, doc.Status)
	assert.Equal(t, "pdftotext", doc.ExtractionMethod)
	assert.NotEmpty(t, doc.StorageKey)
	assert.Contains(t, objects.Objects, doc.StorageKey)
	assert.Len(t, repo.sections[doc.ID], 2)
}

func TestLeafletPipelineMarksScannedPDFs(t *testing.T) {
	target := LeafletTarget{ProductID: 2, SourceID: "100", URL: "https://admin.greenbook.nafdac.gov.ng/scan.pdf"}
	repo := newFakeLeafletRepo(target)
	source := &fakeLeafletSource{bodies: map[string]string{target.URL: "%PDF-scan"}}
	extractor := &fakeExtractor{result: Extraction{Scanned: true, Method: "pdftotext"}}

	summary, err := newTestPipeline(t, repo, source, extractor, &storage.Fake{}, 1<<20).Run(context.Background(), 10)
	require.NoError(t, err)
	assert.Equal(t, 1, summary.ScannedNeedsOCR)

	doc := repo.docs[docKey(target.ProductID, target.URL)]
	assert.Equal(t, LeafletStatusScannedNeedsOCR, doc.Status)
	assert.Empty(t, repo.sections[doc.ID])
}

func TestLeafletPipelineRejectsOversizedDownloads(t *testing.T) {
	target := LeafletTarget{ProductID: 3, SourceID: "101", URL: "https://admin.greenbook.nafdac.gov.ng/big.pdf"}
	repo := newFakeLeafletRepo(target)
	source := &fakeLeafletSource{bodies: map[string]string{target.URL: strings.Repeat("x", 100)}}

	summary, err := newTestPipeline(t, repo, source, &fakeExtractor{}, &storage.Fake{}, 10).Run(context.Background(), 10)
	require.NoError(t, err)
	assert.Equal(t, 1, summary.TooLarge)

	doc := repo.docs[docKey(target.ProductID, target.URL)]
	assert.Equal(t, LeafletStatusTooLarge, doc.Status)
}

func TestLeafletPipelineSkipsUnchangedDownload(t *testing.T) {
	target := LeafletTarget{ProductID: 4, SourceID: "102", URL: "https://admin.greenbook.nafdac.gov.ng/same.pdf"}
	body := "%PDF-unchanged"
	sum := sha256.Sum256([]byte(body))
	repo := newFakeLeafletRepo(target)
	repo.docs[docKey(target.ProductID, target.URL)] = SMPCDocument{
		ID:         9,
		ProductID:  target.ProductID,
		SourceURL:  target.URL,
		SHA256:     hex.EncodeToString(sum[:]),
		StorageKey: "leaflets/102/old.pdf",
		Status:     LeafletStatusOK,
	}
	objects := &storage.Fake{}
	source := &fakeLeafletSource{bodies: map[string]string{target.URL: body}}

	summary, err := newTestPipeline(t, repo, source, &fakeExtractor{}, objects, 1<<20).Run(context.Background(), 10)
	require.NoError(t, err)
	assert.Equal(t, 1, summary.Skipped)
	assert.Empty(t, objects.Objects, "an unchanged leaflet must not be uploaded again")

	doc := repo.docs[docKey(target.ProductID, target.URL)]
	assert.Equal(t, int64(9), doc.ID)
	assert.NotNil(t, doc.FetchedAt)
}

func TestLeafletPipelineKeepsGoodDocumentWhenDownloadFails(t *testing.T) {
	target := LeafletTarget{ProductID: 5, SourceID: "103", URL: "https://admin.greenbook.nafdac.gov.ng/flaky.pdf"}
	repo := newFakeLeafletRepo(target)
	repo.docs[docKey(target.ProductID, target.URL)] = SMPCDocument{
		ID:         11,
		ProductID:  target.ProductID,
		SourceURL:  target.URL,
		SHA256:     "abc",
		StorageKey: "leaflets/103/abc.pdf",
		Status:     LeafletStatusOK,
	}
	source := &fakeLeafletSource{err: errors.New("temporary failure")}

	summary, err := newTestPipeline(t, repo, source, &fakeExtractor{}, &storage.Fake{}, 1<<20).Run(context.Background(), 10)
	require.NoError(t, err)
	assert.Equal(t, 1, summary.Failed)

	doc := repo.docs[docKey(target.ProductID, target.URL)]
	assert.Equal(t, LeafletStatusOK, doc.Status, "a transient failure must not replace a good extraction")
	assert.Equal(t, "abc", doc.SHA256)
}
