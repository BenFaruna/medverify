//go:build integration

package scan_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/auth"
	"github.com/benfaruna/medverify/internal/platform/config"
	"github.com/benfaruna/medverify/internal/platform/jobs"
	"github.com/benfaruna/medverify/internal/platform/testkit"
	"github.com/benfaruna/medverify/internal/scan"
	"github.com/benfaruna/medverify/internal/storage"
	"github.com/benfaruna/medverify/internal/vision"
	"github.com/benfaruna/medverify/migrations"
)

func TestHTTPUploadToCompletedWithFakeProvider(t *testing.T) {
	pipeline := newHTTPPipeline(t, vision.NewFake(), 10*time.Second)
	ctx := context.Background()

	image, err := os.ReadFile("../../testdata/images/gps.jpg")
	require.NoError(t, err)
	body, contentType := multipartUpload(t, [][]byte{image}, map[string]string{
		"latitude": "6.453123", "longitude": "3.394321", "language": "en",
	})
	request := multipartRequest(body, contentType)
	request.Header.Set(scan.HeaderIdempotencyKey, "http-idem-1")
	recorder := httptest.NewRecorder()
	pipeline.handler.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	assert.True(t, strings.HasPrefix(recorder.Header().Get("Location"), "/v1/scans/"))

	var view map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &view))
	assert.Equal(t, string(scan.StatusCompleted), view["status"])
	assert.Nil(t, view["verdict"], "phase 03 has no verdict yet")
	extracted, ok := view["extracted"].(map[string]any)
	require.True(t, ok, "phase 04 fills extracted label data")
	nrn := extracted["nrn"].(map[string]any)
	assert.Equal(t, "01-2345", nrn["value"])
	assert.Equal(t, []any{}, view["reasons"])
	scanID, ok := view["id"].(string)
	require.True(t, ok)

	// Poll the lookup endpoint.
	getRequest := httptest.NewRequest(http.MethodGet, "/v1/scans/"+scanID, nil)
	getRequest.Header.Set(auth.HeaderAPIKey, testAPIKey)
	getRecorder := httptest.NewRecorder()
	pipeline.handler.ServeHTTP(getRecorder, getRequest)
	require.Equal(t, http.StatusOK, getRecorder.Code)
	require.NoError(t, json.Unmarshal(getRecorder.Body.Bytes(), &view))
	assert.Equal(t, string(scan.StatusCompleted), view["status"])

	// Only the coarse cell is stored, and the object has no EXIF.
	var cell string
	require.NoError(t, pipeline.pool.QueryRow(ctx, `SELECT location_cell FROM scans WHERE id = $1`, scanID).Scan(&cell))
	assert.Equal(t, "6.5,3.4", cell)

	var key string
	require.NoError(t, pipeline.pool.QueryRow(ctx, `SELECT storage_key FROM scan_images WHERE scan_id = $1`, scanID).Scan(&key))
	stored, err := pipeline.objects.Get(ctx, key)
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(stored), "Exif"))
	assert.False(t, strings.Contains(string(stored), "GPS"))

	// A cut-off JPEG is a client error, not a 500.
	corruptBody, corruptType := multipartUpload(t, [][]byte{[]byte{0xFF, 0xD8, 0xFF}}, nil)
	corruptRequest := multipartRequest(corruptBody, corruptType)
	corruptRecorder := httptest.NewRecorder()
	pipeline.handler.ServeHTTP(corruptRecorder, corruptRequest)
	assert.Equal(t, http.StatusBadRequest, corruptRecorder.Code)
	var problem map[string]any
	require.NoError(t, json.Unmarshal(corruptRecorder.Body.Bytes(), &problem))
	assert.Equal(t, "corrupt_image", problem["code"])
}

func TestClientDisconnectDoesNotCancelBackgroundJob(t *testing.T) {
	provider := vision.NewFake()
	provider.Delay = 500 * time.Millisecond
	pipeline := newHTTPPipeline(t, provider, 10*time.Second)

	image, err := os.ReadFile("../../testdata/images/normal.jpg")
	require.NoError(t, err)
	body, contentType := multipartUpload(t, [][]byte{image}, nil)
	request := multipartRequest(body, contentType)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	request = request.WithContext(ctx)

	recorder := httptest.NewRecorder()
	pipeline.handler.ServeHTTP(recorder, request)

	// The client went away during the sync wait; the API answers 202 with the
	// state it had reached, and the job keeps running.
	require.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())
	var view map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &view))
	scanID, ok := view["id"].(string)
	require.True(t, ok)

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer waitCancel()
	final, terminal, err := pipeline.apiService.Wait(waitCtx, mustUUID(t, scanID))
	require.NoError(t, err)
	require.True(t, terminal)
	assert.Equal(t, scan.StatusCompleted, final.Status)
}

type httpPipeline struct {
	pool       *pgxpool.Pool
	objects    *storage.Fake
	apiService *scan.Service
	handler    http.Handler
}

func mustUUID(t *testing.T, raw string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(raw)
	require.NoError(t, err)
	return id
}

// seedAPIClient inserts one client row with a fixed id.
func seedAPIClient(t *testing.T, pool *pgxpool.Pool, id int64) {
	t.Helper()

	_, err := pool.Exec(context.Background(),
		`INSERT INTO api_clients (id, name) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING`,
		id, "integration-client")
	require.NoError(t, err)
}

func newHTTPPipeline(t *testing.T, provider *vision.Fake, syncWait time.Duration) *httpPipeline {
	t.Helper()

	ctx := context.Background()
	databaseURL := testkit.Postgres(t)
	require.NoError(t, migrations.Up(ctx, databaseURL))
	require.NoError(t, jobs.Migrate(ctx, databaseURL, jobs.DirectionUp))

	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	logger := slog.New(slog.DiscardHandler)
	objects := &storage.Fake{BucketExists: true, Objects: map[string][]byte{}}
	store := scan.NewStore(pool)

	// The test key authenticates as client 1, so scans it creates reference a
	// real api_clients row. Seeding it keeps the foreign key honest instead of
	// relaxing it for tests.
	seedAPIClient(t, pool, 1)

	workerService, err := scan.NewService(scan.ServiceOptions{
		Store: store, Objects: objects, Provider: provider, Logger: logger,
		MaxImages: 4, Limits: scan.ImageLimits{MaxBytes: 10 << 20, MaxPixels: 40_000_000},
		MinConfidence: 0.5, PollInterval: 20 * time.Millisecond,
	})
	require.NoError(t, err)
	workerClient, err := jobs.NewClient(pool, logger, jobs.NewWorkers(logger, jobs.WithScanRunner(workerService)), config.Jobs{Concurrency: 2})
	require.NoError(t, err)
	require.NoError(t, workerClient.Start(ctx))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = workerClient.Stop(stopCtx)
	})

	producer, err := jobs.NewClient(pool, logger, nil, config.Jobs{})
	require.NoError(t, err)
	apiService, err := scan.NewService(scan.ServiceOptions{
		Store: store, Objects: objects, Provider: provider, Enqueuer: jobs.NewScanEnqueuer(producer),
		Logger: logger, MaxImages: 4,
		Limits:        scan.ImageLimits{MaxBytes: 10 << 20, MaxPixels: 40_000_000},
		MinConfidence: 0.5, PollInterval: 20 * time.Millisecond,
	})
	require.NoError(t, err)

	impl := scan.NewHTTPHandler(scan.HTTPOptions{
		Service: apiService, Logger: logger, MaxImages: 4,
		MaxImageBytes: 10 << 20, MaxRequestBytes: 45 << 20, SyncWait: syncWait,
	})
	router := chi.NewRouter()
	router.Use(auth.APIKey(&testAPIKeyStore{key: testAPIKey}))
	router.Post("/v1/scans", impl.Create)
	router.Get("/v1/scans/{id}", impl.Get)

	return &httpPipeline{pool: pool, objects: objects, apiService: apiService, handler: router}
}
