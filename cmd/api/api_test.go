package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/httpx"
)

func TestHealthzReturnsOKWithoutDependencies(t *testing.T) {
	t.Parallel()

	handler := NewHandler(Options{
		Dependencies: []Dependency{
			{Name: "database", Check: func(context.Context) error { return errors.New("down") }},
		},
	})

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"))

	var body map[string]string
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	assert.Equal(t, "ok", body["status"])
}

func TestReadyzReturnsOKWhenEveryDependencyResponds(t *testing.T) {
	t.Parallel()

	handler := NewHandler(Options{
		Dependencies: []Dependency{
			{Name: "database", Check: func(context.Context) error { return nil }},
			{Name: "storage", Check: func(context.Context) error { return nil }},
		},
	})

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	assert.Equal(t, http.StatusOK, recorder.Code)

	var body struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	assert.Equal(t, "ready", body.Status)
	assert.Equal(t, "ok", body.Checks["database"])
	assert.Equal(t, "ok", body.Checks["storage"])
}

func TestReadyzReturnsProblemWhenPostgresIsDown(t *testing.T) {
	t.Parallel()

	handler := NewHandler(Options{
		Dependencies: []Dependency{
			{Name: "database", Check: func(context.Context) error { return errors.New("connection refused") }},
			{Name: "storage", Check: func(context.Context) error { return nil }},
		},
	})

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	assert.Equal(t, httpx.ContentTypeProblemJSON, recorder.Header().Get("Content-Type"))

	var problem httpx.Problem
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &problem))
	assert.Equal(t, httpx.CodeUnavailable, problem.Code)
	assert.Contains(t, problem.Detail, "database")
	assert.NotContains(t, problem.Detail, "storage")
}

func TestReadyzReportsEveryFailingDependency(t *testing.T) {
	t.Parallel()

	handler := NewHandler(Options{
		Dependencies: []Dependency{
			{Name: "database", Check: func(context.Context) error { return errors.New("down") }},
			{Name: "storage", Check: func(context.Context) error { return errors.New("down") }},
		},
	})

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	var problem httpx.Problem
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &problem))
	assert.Contains(t, problem.Detail, "database")
	assert.Contains(t, problem.Detail, "storage")
}

func TestReadyzTimesOutSlowDependency(t *testing.T) {
	t.Parallel()

	handler := NewHandler(Options{
		ReadyTimeout: 20 * time.Millisecond,
		Dependencies: []Dependency{
			{Name: "database", Check: func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			}},
		},
	})

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}

func TestUnknownRouteReturnsProblem(t *testing.T) {
	t.Parallel()

	handler := NewHandler(Options{})

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/nope", nil))

	assert.Equal(t, http.StatusNotFound, recorder.Code)
	assert.Equal(t, httpx.ContentTypeProblemJSON, recorder.Header().Get("Content-Type"))
}

func TestReadinessResponseIsNotCached(t *testing.T) {
	t.Parallel()

	handler := NewHandler(Options{})

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	assert.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
}
