package httpx_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/httpx"
)

func TestWriteProblemProducesRFC7807JSON(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)

	httpx.WriteProblem(recorder, request, httpx.NewProblem(http.StatusServiceUnavailable, httpx.CodeUnavailable, "Postgres is not reachable."))

	response := recorder.Result()
	defer response.Body.Close()

	assert.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
	assert.Equal(t, httpx.ContentTypeProblemJSON, response.Header.Get("Content-Type"))
	assert.Equal(t, "nosniff", response.Header.Get("X-Content-Type-Options"))

	var body map[string]any
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))

	assert.Equal(t, float64(http.StatusServiceUnavailable), body["status"])
	assert.Equal(t, "Service Unavailable", body["title"])
	assert.Equal(t, "Postgres is not reachable.", body["detail"])
	assert.Equal(t, "/readyz", body["instance"])
	assert.Equal(t, httpx.CodeUnavailable, body["code"])
	assert.Equal(t, httpx.ProblemTypeBase+httpx.CodeUnavailable, body["type"])
}

func TestProblemImplementsError(t *testing.T) {
	t.Parallel()

	var err error = httpx.NewProblem(http.StatusNotFound, httpx.CodeNotFound, "no route")
	assert.EqualError(t, err, "not_found: no route")
}

func TestNotFoundAndMethodNotAllowedHandlers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		handler  http.Handler
		status   int
		wantCode string
	}{
		{"not found", httpx.NotFoundHandler(), http.StatusNotFound, httpx.CodeNotFound},
		{"method not allowed", httpx.MethodNotAllowedHandler(), http.StatusMethodNotAllowed, httpx.CodeMethodNotAllowed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			recorder := httptest.NewRecorder()
			tc.handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/nope", nil))

			require.Equal(t, tc.status, recorder.Code)
			assert.Equal(t, httpx.ContentTypeProblemJSON, recorder.Header().Get("Content-Type"))

			var body httpx.Problem
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
			assert.Equal(t, tc.wantCode, body.Code)
		})
	}
}
