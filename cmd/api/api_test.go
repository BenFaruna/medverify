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

	medapi "github.com/benfaruna/medverify/api"
	"github.com/benfaruna/medverify/internal/platform/auth"
	"github.com/benfaruna/medverify/internal/platform/httpx"
	"github.com/benfaruna/medverify/internal/platform/metrics"
	"github.com/benfaruna/medverify/internal/platform/ratelimit"
	"github.com/benfaruna/medverify/internal/scan"
	"github.com/google/uuid"
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

func TestDocsAreMountedWithoutAuthentication(t *testing.T) {
	t.Parallel()

	handler := NewHandler(Options{
		Docs: medapi.DocsHandler(),
		Scan: &scan.HTTPHandler{},
	})

	for _, test := range []struct {
		path   string
		status int
		body   string
	}{
		{path: "/docs", status: http.StatusMovedPermanently},
		{path: "/docs/", status: http.StatusOK, body: "SwaggerUIBundle"},
		{path: "/openapi.yaml", status: http.StatusOK, body: "openapi: 3."},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))

		assert.Equal(t, test.status, recorder.Code, test.path)
		if test.body != "" {
			assert.Contains(t, recorder.Body.String(), test.body, test.path)
		}
	}

	// Documentation is public, but the versioned API is still gated.
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/scans/00000000-0000-0000-0000-000000000000", nil))
	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func TestDocsAreNotShadowedByTheWebClient(t *testing.T) {
	t.Parallel()

	web := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("web client"))
	})
	handler := NewHandler(Options{Docs: medapi.DocsHandler(), Web: web})

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/docs/", nil))
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "SwaggerUIBundle")

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "web client", recorder.Body.String())
}

func TestUnknownRouteReturnsProblem(t *testing.T) {
	t.Parallel()

	handler := NewHandler(Options{})

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/nope", nil))

	assert.Equal(t, http.StatusNotFound, recorder.Code)
	assert.Equal(t, httpx.ContentTypeProblemJSON, recorder.Header().Get("Content-Type"))
}

func TestScanRoutesRequireAPIKey(t *testing.T) {
	t.Parallel()

	handler := NewHandler(Options{
		APIKey: "test-key",
		Scan:   scan.NewHTTPHandler(scan.HTTPOptions{MaxImages: 4, MaxImageBytes: 1 << 20, MaxRequestBytes: 1 << 20}),
	})

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/scans", nil))

	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
	assert.Equal(t, httpx.ContentTypeProblemJSON, recorder.Header().Get("Content-Type"))

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/scans/"+uuid.NewString(), nil))
	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func TestScanGetRejectsMalformedID(t *testing.T) {
	t.Parallel()

	handler := NewHandler(Options{
		APIKey: "test-key",
		Scan:   scan.NewHTTPHandler(scan.HTTPOptions{}),
	})

	request := httptest.NewRequest(http.MethodGet, "/v1/scans/not-a-uuid", nil)
	request.Header.Set("X-API-Key", "test-key")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestScanCreateRequiresTheWriteScope(t *testing.T) {
	t.Parallel()

	// A read-only key must not be able to create a scan, even though it
	// authenticates successfully.
	store := auth.NewSingleKeyStore(auth.SingleKeyOptions{
		Key:    "mv_readonly_secret",
		Scopes: []string{auth.ScopeScansRead},
	})
	handler := NewHandler(Options{
		Auth: store,
		Scan: scan.NewHTTPHandler(scan.HTTPOptions{MaxImages: 4, MaxImageBytes: 1 << 20, MaxRequestBytes: 1 << 20}),
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/scans", nil)
	request.Header.Set(auth.HeaderAPIKey, "mv_readonly_secret")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusForbidden, recorder.Code)

	var problem httpx.Problem
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &problem))
	assert.Equal(t, httpx.CodeInsufficientScope, problem.Code)
}

func TestRateLimitReturnsRetryAfter(t *testing.T) {
	t.Parallel()

	store := auth.NewSingleKeyStore(auth.SingleKeyOptions{Key: "mv_fast_secret", RatePerMin: 60})
	handler := NewHandler(Options{
		Auth:           store,
		Limiter:        ratelimit.New(),
		RatePerMin:     60,
		RateLimitBurst: 1,
		Scan:           scan.NewHTTPHandler(scan.HTTPOptions{MaxImages: 4, MaxImageBytes: 1 << 20, MaxRequestBytes: 1 << 20}),
	})

	var last *httptest.ResponseRecorder
	for i := 0; i < 2; i++ {
		request := httptest.NewRequest(http.MethodGet, "/v1/scans/not-a-uuid", nil)
		request.Header.Set(auth.HeaderAPIKey, "mv_fast_secret")
		last = httptest.NewRecorder()
		handler.ServeHTTP(last, request)
	}

	assert.Equal(t, http.StatusTooManyRequests, last.Code)
	assert.NotEmpty(t, last.Header().Get("Retry-After"))
}

func TestCORSHeadersOnlyForAllowlistedOrigin(t *testing.T) {
	t.Parallel()

	handler := NewHandler(Options{CORSOrigins: []string{"https://app.medverify.test"}})

	allowed := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	allowed.Header.Set("Origin", "https://app.medverify.test")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, allowed)
	assert.Equal(t, "https://app.medverify.test", recorder.Header().Get("Access-Control-Allow-Origin"))

	blocked := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	blocked.Header.Set("Origin", "https://evil.example")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, blocked)
	assert.Empty(t, recorder.Header().Get("Access-Control-Allow-Origin"))
}

func TestReadinessResponseIsNotCached(t *testing.T) {
	t.Parallel()

	handler := NewHandler(Options{})

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	assert.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
}

func TestFeatureSwitchesReturnUnavailableBeforeAuth(t *testing.T) {
	t.Parallel()

	handler := NewHandler(Options{
		DisableScans: true,
		Scan:         scan.NewHTTPHandler(scan.HTTPOptions{}),
	})

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/scans", nil))

	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	var problem httpx.Problem
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &problem))
	assert.Equal(t, httpx.CodeUnavailable, problem.Code)

	// Reads are not part of the submission switch: a finished scan stays
	// readable, so the check reaches auth and fails with 401.
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/scans/"+uuid.NewString(), nil))
	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func TestAPISwitchDisablesEveryVersionedRoute(t *testing.T) {
	t.Parallel()

	handler := NewHandler(Options{
		DisableAPI: true,
		APIKey:     "test-key",
		Scan:       scan.NewHTTPHandler(scan.HTTPOptions{}),
	})

	request := httptest.NewRequest(http.MethodGet, "/v1/scans/"+uuid.NewString(), nil)
	request.Header.Set(auth.HeaderAPIKey, "test-key")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)

	// Probes stay available so an orchestrator does not restart the process.
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	assert.Equal(t, http.StatusOK, recorder.Code)
}

func TestMetricsUseRoutePatternsAndNeverScanIDs(t *testing.T) {
	t.Parallel()

	registry := metrics.New()
	handler := NewHandler(Options{
		APIKey:  "test-key",
		Scan:    scan.NewHTTPHandler(scan.HTTPOptions{}),
		Metrics: registry,
	})

	scanID := uuid.NewString()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/scans/"+scanID, nil))
	require.Equal(t, http.StatusUnauthorized, recorder.Code)

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Header().Get("Content-Type"), "text/plain")

	body := recorder.Body.String()
	assert.Contains(t, body, `route="/v1/scans/{id}"`)
	assert.Contains(t, body, "medverify_http_requests_total")
	assert.Contains(t, body, "medverify_http_request_duration_seconds_count")
	assert.NotContains(t, body, scanID, "a scan ID must never become a metric label")
}
