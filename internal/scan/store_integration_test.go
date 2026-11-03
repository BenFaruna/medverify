//go:build integration

package scan_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/logging"
	"github.com/benfaruna/medverify/internal/platform/testkit"
	"github.com/benfaruna/medverify/internal/safety"
	"github.com/benfaruna/medverify/internal/scan"
	"github.com/benfaruna/medverify/internal/storage"
	"github.com/benfaruna/medverify/internal/verify"
	"github.com/benfaruna/medverify/internal/vision"
	"github.com/benfaruna/medverify/migrations"
)

func TestCreateStoresScanImagesAndOnlyCoarseLocation(t *testing.T) {
	pipeline := newPipeline(t)
	ctx := context.Background()

	image := readImage(t, "normal.jpg")
	scanRow, created, err := pipeline.service.Create(ctx, scan.CreateRequest{
		ClientKey:    "default",
		RequestHash:  "hash-1",
		LocationCell: mustCell(t, "6.453123", "3.394321"),
		Images:       [][]byte{image},
	})
	require.NoError(t, err)
	require.True(t, created)
	assert.Equal(t, scan.StatusQueued, scanRow.Status)
	require.Len(t, scanRow.Images, 1)
	assert.Contains(t, scanRow.Images[0].StorageKey, "scans/"+scanRow.ID.String()+"/")
	assert.NotContains(t, scanRow.Images[0].StorageKey, "photo.jpg")
	assert.Equal(t, "6.5,3.4", scanRow.LocationCell)

	// The raw coordinates must not appear anywhere in the scan row.
	var rowJSON string
	require.NoError(t, pipeline.pool.QueryRow(ctx, `SELECT row_to_json(s)::text FROM scans s WHERE id = $1`, scanRow.ID).Scan(&rowJSON))
	assert.NotContains(t, rowJSON, "6.453123")
	assert.NotContains(t, rowJSON, "3.394321")

	// The stored object is the re-encoded JPEG, not the original upload.
	stored, err := pipeline.objects.Get(ctx, scanRow.Images[0].StorageKey)
	require.NoError(t, err)
	assert.NotContains(t, string(stored), "Exif")
	assert.NotContains(t, string(stored), "GPS")

	// Nothing personal appears in the logs either.
	assert.NotContains(t, pipeline.logs.String(), "6.453123")
	assert.NotContains(t, pipeline.logs.String(), "3.394321")
	assert.NotContains(t, pipeline.logs.String(), "photo.jpg")
	require.Len(t, pipeline.enqueuer.IDs(), 1)
}

func TestProcessCompletesWithFakeProvider(t *testing.T) {
	pipeline := newPipeline(t)
	ctx := context.Background()

	created := createScan(t, pipeline, "hash-2")
	require.NoError(t, pipeline.service.Process(ctx, created.ID.String()))

	finished, err := pipeline.service.Get(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, scan.StatusCompleted, finished.Status)
	require.NotNil(t, finished.Result)
	assert.Equal(t, "fake", finished.Result.Provider)
	assert.JSONEq(t, "[]", string(finished.Result.Reasons))
	assert.Nil(t, finished.Result.Verdict)
	assert.NotNil(t, finished.Result.Extracted)
	assert.NotNil(t, finished.CompletedAt)
	// No matcher is configured here, so verification is explicitly empty
	// rather than missing.
	assert.JSONEq(t, "{}", string(finished.Result.Features))

	var extracted vision.LabelData
	require.NoError(t, json.Unmarshal(finished.Result.Extracted, &extracted))
	require.NotNil(t, extracted.NRN.Value)
	assert.Equal(t, "01-2345", *extracted.NRN.Value)
	require.NotNil(t, extracted.ExpiryDate.Value)
	assert.Equal(t, "2028-01", extracted.ExpiryDate.Value.Format())
	assert.Equal(t, "fake", extracted.Provider)
	assert.Equal(t, "fake", extracted.PromptVersion)

	view := finished.View()
	assert.Equal(t, scan.StatusCompleted, view.Status)
	assert.Nil(t, view.Verdict)
	assert.JSONEq(t, "[]", string(view.Reasons))
	assert.JSONEq(t, "[]", string(view.Flags), "no flags is an empty list, not null")
	assert.NotNil(t, view.Extracted)
}

func TestProcessStoresRegistryVerification(t *testing.T) {
	pipeline := newPipeline(t)
	ctx := context.Background()
	pipeline.matcher = &fakeMatcher{result: verify.MatchResult{
		Outcome: verify.CandidateOnly,
		Fields:  map[string]verify.FieldComparison{},
		Candidates: []verify.ProductRef{{
			SourceID: "p1", NRN: "01-2345", Name: "Paracetamol Tablets 500 mg",
			Status: "Active", Score: 0.91,
		}},
		Dataset: verify.DatasetInfo{ConfigVersion: verify.ConfigVersion, Products: 198},
	}}
	pipeline.rebuild(t)

	created := createScan(t, pipeline, "hash-verify")
	require.NoError(t, pipeline.service.Process(ctx, created.ID.String()))

	finished, err := pipeline.service.Get(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, scan.StatusCompleted, finished.Status)
	require.NotNil(t, finished.Result.Features, "verification must be stored on the result row")

	var features verify.MatchResult
	require.NoError(t, json.Unmarshal(finished.Result.Features, &features))
	assert.Equal(t, verify.CandidateOnly, features.Outcome)
	require.Len(t, features.Candidates, 1)
	assert.Equal(t, "01-2345", features.Candidates[0].NRN)
	assert.Equal(t, verify.ConfigVersion, features.Dataset.ConfigVersion)

	// The API view exposes the same explainable result.
	view := finished.View()
	require.NotNil(t, view.Features)
	assert.Contains(t, string(view.Features), "candidate_only")
	assert.Equal(t, 1, pipeline.matcher.(*fakeMatcher).calls)
}

func TestProcessStoresSourcedSafetyFlags(t *testing.T) {
	pipeline := newPipeline(t)
	ctx := context.Background()

	issued := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	// A flag cites a real watchlist row, so the foreign key must resolve.
	var (
		sourceID         int64
		watchlistEntryID int64
	)
	require.NoError(t, pipeline.pool.QueryRow(ctx, `
		INSERT INTO watchlist_sources (issuing_body, title, source_url, issued_at)
		VALUES ('NAFDAC', 'Public alert on a test batch', 'https://example.test/nafdac/alerts/2026-09-01', '2026-09-01')
		RETURNING id`).Scan(&sourceID))
	require.NoError(t, pipeline.pool.QueryRow(ctx, `
		INSERT INTO watchlist_entries (source_id, kind, nrn, reason, reviewed_by)
		SELECT id, 'alert', 'TT4-7001', 'Synthetic fixture entry', 'Test Reviewer'
		FROM watchlist_sources WHERE source_url = 'https://example.test/nafdac/alerts/2026-09-01'
		RETURNING id`).Scan(&watchlistEntryID))

	pipeline.safety = &fakeSafety{flags: []safety.Flag{{
		Type:       safety.WatchlistProduct,
		Severity:   safety.SeverityHigh,
		ReasonCode: safety.ReasonWatchlistAlert,
		Evidence: map[string]string{
			"issuing_body": "NAFDAC",
			"issued_at":    "2026-09-01",
			"source_url":   "https://example.test/nafdac/alerts/2026-09-01",
		},
		Source: safety.Source{
			Kind:             safety.SourceWatchlistEntry,
			WatchlistEntryID: watchlistEntryID,
			IssuingBody:      "NAFDAC",
			Title:            "Public alert on a test batch",
			URL:              "https://example.test/nafdac/alerts/2026-09-01",
			IssuedAt:         &issued,
		},
	}}}
	pipeline.rebuild(t)

	created := createScan(t, pipeline, "hash-safety")
	require.NoError(t, pipeline.service.Process(ctx, created.ID.String()))
	assert.Equal(t, 1, pipeline.safety.(*fakeSafety).calls)

	finished, err := pipeline.service.Get(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, scan.StatusCompleted, finished.Status, "a rejected flag write must not leave the scan unfinished")
	require.Len(t, finished.Flags, 1)
	assert.Equal(t, safety.WatchlistProduct, finished.Flags[0].Type)
	assert.Equal(t, watchlistEntryID, finished.Flags[0].Source.WatchlistEntryID)
	assert.Equal(t, "https://example.test/nafdac/alerts/2026-09-01", finished.Flags[0].Source.URL)
	assert.Equal(t, "2026-09-01", finished.Flags[0].Evidence["issued_at"])

	view := finished.View()
	var flagsView []map[string]any
	require.NoError(t, json.Unmarshal(view.Flags, &flagsView))
	require.Len(t, flagsView, 1)
	assert.Equal(t, "watchlist_product", flagsView[0]["type"])
	assert.Equal(t, "high", flagsView[0]["severity"])
	assert.Equal(t, "watchlist_alert", flagsView[0]["reason_code"])

	var stored int
	require.NoError(t, pipeline.pool.QueryRow(ctx,
		`SELECT count(*) FROM safety_flags WHERE scan_id = $1`, created.ID).Scan(&stored))
	assert.Equal(t, 1, stored)

	// A retry replaces the findings instead of appending to them.
	_, err = pipeline.pool.Exec(ctx, `UPDATE scans SET status = 'processing', completed_at = NULL WHERE id = $1`, created.ID)
	require.NoError(t, err)
	replaced := []safety.Flag{{
		Type: safety.MissingIngredient, Severity: safety.SeverityMedium,
		ReasonCode: safety.ReasonMissingIngredient,
		Evidence:   map[string]string{"ingredient": "caffeine"},
		Source:     safety.Source{Kind: safety.SourceRegistry, NRN: "01-2345"},
	}}
	updated, err := pipeline.store.FinishScan(ctx, created.ID, scan.StatusCompleted, scan.Result{
		Extracted: finished.Result.Extracted,
		Features:  finished.Result.Features,
		Flags:     replaced,
	})
	require.NoError(t, err)
	require.True(t, updated)

	again, err := pipeline.service.Get(ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, again.Flags, 1)
	assert.Equal(t, safety.MissingIngredient, again.Flags[0].Type)
	assert.Equal(t, "caffeine", again.Flags[0].Evidence["ingredient"])
}

func TestProcessFailsWhenTheSafetyCheckerFails(t *testing.T) {
	pipeline := newPipeline(t)
	ctx := context.Background()
	pipeline.safety = &fakeSafety{err: errors.New("watchlist unavailable")}
	pipeline.rebuild(t)

	created := createScan(t, pipeline, "hash-safety-fail")
	err := pipeline.service.Process(ctx, created.ID.String())
	require.Error(t, err)

	failed, err := pipeline.service.Get(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, scan.StatusFailed, failed.Status)
}

func TestProcessMarksFailedThenRetrySucceeds(t *testing.T) {
	pipeline := newPipeline(t)
	pipeline.provider.Errors = []error{errors.New("provider unavailable")}
	ctx := context.Background()

	created := createScan(t, pipeline, "hash-3")
	require.Error(t, pipeline.service.Process(ctx, created.ID.String()))

	failed, err := pipeline.service.Get(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, scan.StatusFailed, failed.Status)
	require.NotNil(t, failed.Result)
	assert.Contains(t, failed.Result.Error, "provider unavailable")

	require.NoError(t, pipeline.service.Process(ctx, created.ID.String()), "a retry must requeue a failed scan")

	retried, err := pipeline.service.Get(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, scan.StatusCompleted, retried.Status)
	assert.Equal(t, 2, pipeline.provider.CallsCount())
}

func TestProcessUnreadableNeverGuesses(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		provider *vision.Fake
	}{
		{"provider says unreadable", &vision.Fake{Errors: []error{vision.ErrUnreadable}}},
		{"low confidence", &vision.Fake{Results: []vision.Result{{Provider: "fake", Confidence: 0.2}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pipeline := newPipeline(t)
			pipeline.provider = tc.provider
			pipeline.rebuild(t)

			created := createScan(t, pipeline, "hash-4")
			require.NoError(t, pipeline.service.Process(context.Background(), created.ID.String()))

			got, err := pipeline.service.Get(context.Background(), created.ID)
			require.NoError(t, err)
			assert.Equal(t, scan.StatusUnreadable, got.Status)
			assert.Nil(t, got.Result.Verdict)
			assert.Nil(t, got.Result.Extracted)
		})
	}
}

func TestIdempotencyReplaysSameScanAndRejectsDifferentBody(t *testing.T) {
	pipeline := newPipeline(t)
	ctx := context.Background()

	request := scan.CreateRequest{
		ClientKey:      "default",
		IdempotencyKey: "idem-12345678",
		RequestHash:    "same-body-hash",
		Images:         [][]byte{readImage(t, "normal.jpg")},
	}
	first, created, err := pipeline.service.Create(ctx, request)
	require.NoError(t, err)
	require.True(t, created)

	second, created, err := pipeline.service.Create(ctx, request)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.ID, second.ID)

	request.RequestHash = "different-body-hash"
	_, _, err = pipeline.service.Create(ctx, request)
	assert.ErrorIs(t, err, scan.ErrIdempotencyReused)

	assert.Equal(t, 1, countScans(t, pipeline.pool))
	assert.Equal(t, 1, len(pipeline.enqueuer.IDs()), "a replay must not enqueue a second job")
}

func TestConcurrentIdenticalRequestsCreateOneScan(t *testing.T) {
	pipeline := newPipeline(t)
	request := scan.CreateRequest{
		ClientKey:      "default",
		IdempotencyKey: "concurrent-key-1",
		RequestHash:    "concurrent-hash",
		Images:         [][]byte{readImage(t, "normal.jpg")},
	}

	var wg sync.WaitGroup
	ids := make([]uuid.UUID, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			created, _, err := pipeline.service.Create(context.Background(), request)
			ids[index] = created.ID
			errs[index] = err
		}(i)
	}
	wg.Wait()

	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	assert.Equal(t, ids[0], ids[1])
	assert.Equal(t, 1, countScans(t, pipeline.pool))
}

func TestClaimAndFinishGuardsPreventDoubleProcessing(t *testing.T) {
	pipeline := newPipeline(t)
	ctx := context.Background()
	created := createScan(t, pipeline, "hash-5")

	claimed, err := pipeline.store.ClaimScan(ctx, created.ID)
	require.NoError(t, err)
	assert.True(t, claimed)

	again, err := pipeline.store.ClaimScan(ctx, created.ID)
	require.NoError(t, err)
	assert.False(t, again, "two workers must not claim the same scan")

	finished, err := pipeline.store.FinishScan(ctx, created.ID, scan.StatusCompleted, scan.Result{Reasons: []byte("[]")})
	require.NoError(t, err)
	assert.True(t, finished)

	overwrite, err := pipeline.store.FinishScan(ctx, created.ID, scan.StatusFailed, scan.Result{Reasons: []byte("[]")})
	require.NoError(t, err)
	assert.False(t, overwrite, "a second terminal write must not overwrite the first")

	got, err := pipeline.store.GetScan(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, scan.StatusCompleted, got.Status)
}

type testPipeline struct {
	pool     *pgxpool.Pool
	store    *scan.Store
	objects  *storage.Fake
	provider *vision.Fake
	recorder vision.Recorder
	matcher  scan.RegistryMatcher
	safety   scan.SafetyChecker
	enqueuer *fakeEnqueuer
	service  *scan.Service
	logs     *bytes.Buffer
}

// fakeMatcher stands in for registry verification so the pipeline test never
// needs a seeded registry.
type fakeMatcher struct {
	result verify.MatchResult
	err    error
	calls  int
}

func (f *fakeMatcher) Match(context.Context, vision.LabelData) (verify.MatchResult, error) {
	f.calls++
	return f.result, f.err
}

// fakeSafety stands in for the phase 06 checker.
type fakeSafety struct {
	flags []safety.Flag
	err   error
	calls int
}

func (f *fakeSafety) Check(context.Context, verify.MatchResult, vision.LabelData) ([]safety.Flag, error) {
	f.calls++
	return f.flags, f.err
}

func newPipeline(t *testing.T) *testPipeline {
	t.Helper()

	ctx := context.Background()
	databaseURL := testkit.Postgres(t)
	require.NoError(t, migrations.Up(ctx, databaseURL))

	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	logs := &bytes.Buffer{}
	logger, _ := logging.New(logging.Options{Level: slog.LevelDebug, Writer: logs})

	pipeline := &testPipeline{
		pool:     pool,
		store:    scan.NewStore(pool),
		objects:  &storage.Fake{BucketExists: true, Objects: map[string][]byte{}},
		provider: vision.NewFake(),
		enqueuer: &fakeEnqueuer{},
		logs:     logs,
	}
	pipeline.rebuildWithLogger(t, logger)
	return pipeline
}

func (p *testPipeline) rebuild(t *testing.T) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	p.rebuildWithLogger(t, logger)
}

func (p *testPipeline) rebuildWithLogger(t *testing.T, logger *slog.Logger) {
	t.Helper()

	service, err := scan.NewService(scan.ServiceOptions{
		Store:         p.store,
		Objects:       p.objects,
		Provider:      p.provider,
		Recorder:      p.recorder,
		Matcher:       p.matcher,
		Safety:        p.safety,
		Enqueuer:      p.enqueuer,
		Logger:        logger,
		MaxImages:     4,
		Limits:        scan.ImageLimits{MaxBytes: 10 << 20, MaxPixels: 40_000_000},
		MinConfidence: 0.5,
		PollInterval:  10 * time.Millisecond,
	})
	require.NoError(t, err)
	p.service = service
}

type fakeEnqueuer struct {
	mu  sync.Mutex
	ids []string
	err error
}

func (f *fakeEnqueuer) EnqueueScan(_ context.Context, scanID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.ids = append(f.ids, scanID)
	return nil
}

func (f *fakeEnqueuer) IDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ids...)
}

func createScan(t *testing.T, pipeline *testPipeline, hash string) scan.Scan {
	t.Helper()

	created, isNew, err := pipeline.service.Create(context.Background(), scan.CreateRequest{
		ClientKey:   "default",
		RequestHash: hash,
		Images:      [][]byte{readImage(t, "normal.jpg")},
	})
	require.NoError(t, err)
	require.True(t, isNew)
	return created
}

func readImage(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../testdata/images/" + name)
	require.NoError(t, err)
	return data
}

func mustCell(t *testing.T, latitude, longitude string) string {
	t.Helper()
	cell, err := scan.ParseLocation(latitude, longitude)
	require.NoError(t, err)
	return cell
}

func countScans(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var count int
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM scans`).Scan(&count))
	return count
}

func TestProcessMergesImagesAndRecordsEveryCall(t *testing.T) {
	pipeline := newPipeline(t)
	ctx := context.Background()

	first := vision.DefaultFakeLabel()
	second := vision.DefaultFakeLabel()
	first.NRN = vision.RawField{Value: ptrTest("01-2345"), Confidence: 0.80, Source: 0}
	second.NRN = vision.RawField{Value: ptrTest("01-9999"), Confidence: 0.88, Source: 0}
	pipeline.provider = &vision.Fake{Results: []vision.Result{
		{Provider: "fake", Model: "fake-v1", PromptVersion: "fake", Confidence: 1, Label: &first},
		{Provider: "fake", Model: "fake-v1", PromptVersion: "fake", Confidence: 1, Label: &second},
	}}
	recorder := &recordingRecorder{}
	pipeline.recorder = recorder
	pipeline.rebuild(t)

	created, isNew, err := pipeline.service.Create(ctx, scan.CreateRequest{
		ClientKey:   "default",
		RequestHash: "merge-hash",
		Images:      [][]byte{readImage(t, "normal.jpg"), readImage(t, "rotated.jpg")},
	})
	require.NoError(t, err)
	require.True(t, isNew)

	require.NoError(t, pipeline.service.Process(ctx, created.ID.String()))
	got, err := pipeline.service.Get(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, scan.StatusCompleted, got.Status, "the readable fields still complete the scan")

	var extracted vision.LabelData
	require.NoError(t, json.Unmarshal(got.Result.Extracted, &extracted))
	assert.Nil(t, extracted.NRN.Value, "a close NRN conflict must not be guessed")
	assert.Contains(t, extracted.Conflicts, "nrn")
	assert.Contains(t, extracted.Unreadable, "nrn")
	assert.Len(t, recorder.calls(), 2)
	for _, call := range recorder.calls() {
		assert.Equal(t, "success", call.Outcome)
		assert.Equal(t, "fake", call.Provider)
		assert.Equal(t, created.ID.String(), call.ScanID)
	}
}

func TestProcessRecordsProviderFailure(t *testing.T) {
	pipeline := newPipeline(t)
	pipeline.provider.Errors = []error{errors.New("provider unavailable")}
	recorder := &recordingRecorder{}
	pipeline.recorder = recorder
	pipeline.rebuild(t)

	created := createScan(t, pipeline, "record-failure")
	require.Error(t, pipeline.service.Process(context.Background(), created.ID.String()))

	calls := recorder.calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "error", calls[0].Outcome)
	assert.Equal(t, "provider_error", calls[0].ErrorCode)
}

type recordingRecorder struct {
	mu    sync.Mutex
	items []vision.CallRecord
}

func (r *recordingRecorder) Record(_ context.Context, call vision.CallRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, call)
	return nil
}

func (r *recordingRecorder) calls() []vision.CallRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]vision.CallRecord(nil), r.items...)
}

func ptrTest(value string) *string { return &value }
