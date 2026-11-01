//go:build integration

package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/config"
	"github.com/benfaruna/medverify/internal/platform/db"
	"github.com/benfaruna/medverify/internal/platform/httpx"
	"github.com/benfaruna/medverify/internal/platform/logging"
	"github.com/benfaruna/medverify/internal/platform/testkit"
	"github.com/benfaruna/medverify/internal/storage"
	"github.com/benfaruna/medverify/migrations"
)

func TestReadyzAgainstRealDependencies(t *testing.T) {
	databaseURL := testkit.Postgres(t)
	require.NoError(t, migrations.Up(context.Background(), databaseURL))

	store, _ := testkit.ObjectStorage(t)
	logger, _ := logging.New(logging.Options{Level: 8})

	pool, err := db.Open(config.Database{URL: databaseURL, MaxConns: 4, ConnectTimeout: 5 * time.Second})
	require.NoError(t, err)
	defer pool.Close()

	handler := newReadyHandler(logger, pool, store, 5*time.Second)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	assert.Equal(t, "ready", body["status"])
}

func TestReadyzReturns503WhenPostgresIsDown(t *testing.T) {
	store, _ := testkit.ObjectStorage(t)
	logger, _ := logging.New(logging.Options{Level: 8})

	// Nothing listens on port 1, so the ping fails fast like a dead database.
	pool, err := db.Open(config.Database{
		URL:            "postgres://medverify:medverify@127.0.0.1:1/medverify?sslmode=disable",
		MaxConns:       2,
		ConnectTimeout: time.Second,
	})
	require.NoError(t, err)
	defer pool.Close()

	handler := newReadyHandler(logger, pool, store, 2*time.Second)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	assert.Equal(t, httpx.ContentTypeProblemJSON, recorder.Header().Get("Content-Type"))

	var problem httpx.Problem
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &problem))
	assert.Equal(t, httpx.CodeUnavailable, problem.Code)
	assert.Contains(t, problem.Detail, "database")
}

func TestReadyzReturns503WhenBucketIsMissing(t *testing.T) {
	databaseURL := testkit.Postgres(t)
	logger, _ := logging.New(logging.Options{Level: 8})

	pool, err := db.Open(config.Database{URL: databaseURL, MaxConns: 2, ConnectTimeout: 5 * time.Second})
	require.NoError(t, err)
	defer pool.Close()

	_, endpoint := testkit.ObjectStorage(t)
	missingBucket := testkit.StoreWithBucket(t, endpoint, "bucket-that-was-never-created")

	handler := newReadyHandler(logger, pool, missingBucket, 5*time.Second)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)

	var problem httpx.Problem
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &problem))
	assert.Contains(t, problem.Detail, "storage")
}

func newReadyHandler(logger *slog.Logger, pool *pgxpool.Pool, store storage.Store, readyTimeout time.Duration) http.Handler {
	return NewHandler(Options{
		Logger: logger,
		Dependencies: []Dependency{
			{Name: "database", Check: func(ctx context.Context) error { return db.Ping(ctx, pool) }},
			{Name: "storage", Check: store.Ping},
		},
		ReadyTimeout: readyTimeout,
	})
}
