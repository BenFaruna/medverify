package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/metrics"
)

func TestHandlerRendersPrometheusText(t *testing.T) {
	t.Parallel()

	registry := metrics.New()
	registry.ObserveRequest("GET", "/v1/scans/{id}", 200, 150*time.Millisecond)
	registry.ObserveRequest("GET", "/v1/scans/{id}", 200, 50*time.Millisecond)
	registry.ObserveRequest("POST", "/v1/scans", 429, 10*time.Millisecond)
	registry.Inc("medverify_scan_results_total", "verdict", "needs_review")

	recorder := httptest.NewRecorder()
	registry.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Header().Get("Content-Type"), "text/plain")
	assert.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))

	body := recorder.Body.String()
	assert.Contains(t, body, `medverify_http_requests_total{method="GET",route="/v1/scans/{id}",status="200"} 2`)
	assert.Contains(t, body, `medverify_http_requests_total{method="POST",route="/v1/scans",status="429"} 1`)
	assert.Contains(t, body, `medverify_http_request_duration_seconds_sum{method="GET",route="/v1/scans/{id}",status="200"} 0.2`)
	assert.Contains(t, body, `medverify_http_request_duration_seconds_count{method="GET",route="/v1/scans/{id}",status="200"} 2`)
	assert.Contains(t, body, `medverify_scan_results_total{verdict="needs_review"} 1`)
	assert.Less(t, strings.Index(body, `route="/v1/scans"`), strings.Index(body, `route="/v1/scans/{id}"`),
		"output must be stable, not map order")
}

func TestEmptyRouteIsLabelledUnmatched(t *testing.T) {
	t.Parallel()

	registry := metrics.New()
	registry.ObserveRequest("GET", "", 404, time.Millisecond)

	recorder := httptest.NewRecorder()
	registry.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	assert.Contains(t, recorder.Body.String(), `route="unmatched"`)
}

func TestLabelValuesAreEscaped(t *testing.T) {
	t.Parallel()

	registry := metrics.New()
	registry.Inc("medverify_events_total", "reason", "line\nbreak \"quoted\"")

	recorder := httptest.NewRecorder()
	registry.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	assert.Contains(t, recorder.Body.String(), `reason="line\nbreak \"quoted\""`)
}

func TestNilRegistryIsSafe(t *testing.T) {
	t.Parallel()

	var registry *metrics.Metrics
	registry.ObserveRequest("GET", "/x", 200, time.Millisecond)
	registry.Inc("event")
}
