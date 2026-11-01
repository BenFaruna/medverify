package httpx_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/httpx"
	"github.com/benfaruna/medverify/internal/platform/logging"
)

func TestRequestIDGeneratesAndPropagatesID(t *testing.T) {
	t.Parallel()

	var seen string
	handler := httpx.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpx.RequestIDFromContext(r.Context())
		require.True(t, ok)
		seen = id
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	assert.NotEmpty(t, seen)
	assert.Equal(t, seen, recorder.Header().Get(httpx.HeaderRequestID))
	assert.GreaterOrEqual(t, len(seen), 32)
}

func TestRequestIDReusesWellFormedInboundID(t *testing.T) {
	t.Parallel()

	var seen string
	handler := httpx.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = httpx.RequestIDFromContext(r.Context())
	}))

	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set(httpx.HeaderRequestID, "client-supplied-request-1")

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	assert.Equal(t, "client-supplied-request-1", seen)
	assert.Equal(t, "client-supplied-request-1", recorder.Header().Get(httpx.HeaderRequestID))
}

func TestRequestIDRejectsMalformedInboundID(t *testing.T) {
	t.Parallel()

	var seen string
	handler := httpx.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = httpx.RequestIDFromContext(r.Context())
	}))

	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set(httpx.HeaderRequestID, "not valid: id with spaces and <script>")

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	assert.NotEqual(t, "not valid: id with spaces and <script>", seen)
	assert.NotContains(t, recorder.Header().Get(httpx.HeaderRequestID), " ")
}

func TestRecoverTurnsPanicIntoProblemResponse(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	logger := newLogger(&logs, "api-secret-value")

	handler := httpx.RequestID(httpx.Recover(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom: api-secret-value")
	})))

	recorder := httptest.NewRecorder()
	require.NotPanics(t, func() {
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/panic", nil))
	})

	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
	assert.Equal(t, httpx.ContentTypeProblemJSON, recorder.Header().Get("Content-Type"))

	var body httpx.Problem
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	assert.Equal(t, httpx.CodeInternalError, body.Code)
	assert.Contains(t, body.Detail, recorder.Header().Get(httpx.HeaderRequestID))
	assert.NotContains(t, body.Detail, "api-secret-value")

	assert.Contains(t, logs.String(), "panic recovered")
	assert.NotContains(t, logs.String(), "api-secret-value")
}

func TestRecoverKeepsProcessAliveForLaterRequests(t *testing.T) {
	t.Parallel()

	router := http.NewServeMux()
	router.Handle("/panic", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	router.Handle("/ok", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	handler := httpx.Recover(nil)(router)

	panicRecorder := httptest.NewRecorder()
	handler.ServeHTTP(panicRecorder, httptest.NewRequest(http.MethodGet, "/panic", nil))
	require.Equal(t, http.StatusInternalServerError, panicRecorder.Code)

	okRecorder := httptest.NewRecorder()
	handler.ServeHTTP(okRecorder, httptest.NewRequest(http.MethodGet, "/ok", nil))
	assert.Equal(t, http.StatusNoContent, okRecorder.Code)
}

func TestTimeoutReturnsProblemWhenHandlerOutlivesDeadline(t *testing.T) {
	t.Parallel()

	handler := httpx.Timeout(20 * time.Millisecond)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/slow", nil))

	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)

	var body httpx.Problem
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	assert.Equal(t, httpx.CodeTimeout, body.Code)
}

func TestTimeoutLeavesFastHandlerAlone(t *testing.T) {
	t.Parallel()

	handler := httpx.Timeout(500 * time.Millisecond)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("fine"))
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/fast", nil))

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "fine", recorder.Body.String())
}

func TestMaxBodyBytesRejectsOversizedBody(t *testing.T) {
	t.Parallel()

	var readErr error
	handler := httpx.MaxBodyBytes(16)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	}))

	request := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(strings.Repeat("x", 128)))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	require.Error(t, readErr)
	var maxBytesErr *http.MaxBytesError
	assert.ErrorAs(t, readErr, &maxBytesErr)
}

func TestAccessLogRecordsRequestFields(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	logger := newLogger(&logs)

	handler := httpx.RequestID(httpx.AccessLog(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("short and stout"))
	})))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/teapot", nil))

	var line map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &line))

	assert.Equal(t, "http request", line["msg"])
	assert.Equal(t, "GET", line["method"])
	assert.Equal(t, "/teapot", line["path"])
	assert.Equal(t, float64(http.StatusTeapot), line["status"])
	assert.Equal(t, float64(len("short and stout")), line["response_bytes"])
	assert.NotEmpty(t, line["request_id"])
	assert.NotEmpty(t, line["duration"])
}

func newLogger(w io.Writer, secrets ...string) *slog.Logger {
	logger, _ := logging.New(logging.Options{Level: slog.LevelDebug, Secrets: secrets, Writer: w})
	return logger
}
