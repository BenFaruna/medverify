package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/benfaruna/medverify/internal/platform/ratelimit"
)

func authenticatedHandler(t *testing.T, key SingleKeyOptions, middlewares ...func(http.Handler) http.Handler) http.Handler {
	t.Helper()
	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	for i := len(middlewares) - 1; i >= 0; i-- {
		handler = middlewares[i](handler)
	}
	return APIKey(NewSingleKeyStore(key))(handler)
}

func apiRequest(key string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/v1/scans", nil)
	request.Header.Set(HeaderAPIKey, key)
	return request
}

func TestRateLimitAllowsThenRejects(t *testing.T) {
	t.Parallel()

	const key = "mv_test_secret"
	handler := authenticatedHandler(t, SingleKeyOptions{Key: key, RatePerMin: 60},
		RateLimit(RateLimitOptions{Limiter: ratelimit.New(), DefaultBurst: 2}))

	for i := 0; i < 2; i++ {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, apiRequest(key))
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("request %d status = %d, want 204", i+1, recorder.Code)
		}
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, apiRequest(key))
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", recorder.Code)
	}
	if recorder.Header().Get("Retry-After") == "" {
		t.Fatal("a 429 must carry Retry-After")
	}
	if recorder.Header().Get("X-RateLimit-Limit") != "60" {
		t.Fatalf("X-RateLimit-Limit = %q", recorder.Header().Get("X-RateLimit-Limit"))
	}
}

func TestRateLimitWithoutLimiterIsDisabled(t *testing.T) {
	t.Parallel()

	const key = "mv_test_secret"
	handler := authenticatedHandler(t, SingleKeyOptions{Key: key},
		RateLimit(RateLimitOptions{}))

	for i := 0; i < 5; i++ {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, apiRequest(key))
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204 with no limiter configured", recorder.Code)
		}
	}
}

func TestIPRateLimitRejectsByAddress(t *testing.T) {
	t.Parallel()

	limiter := ratelimit.New()
	limited := IPRateLimit(limiter, 60, 1)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest(http.MethodGet, "/v1/scans", nil)
	request.RemoteAddr = "203.0.113.9:5555"
	recorder := httptest.NewRecorder()
	limited.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("first request status = %d, want 204", recorder.Code)
	}

	recorder = httptest.NewRecorder()
	limited.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want 429", recorder.Code)
	}

	// A different address has its own bucket.
	other := httptest.NewRequest(http.MethodGet, "/v1/scans", nil)
	other.RemoteAddr = "198.51.100.4:5555"
	recorder = httptest.NewRecorder()
	limited.ServeHTTP(recorder, other)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("other address status = %d, want 204", recorder.Code)
	}
}

type fakeUsageStore struct {
	err      error
	requests []ReserveRequest
}

func (s *fakeUsageStore) Reserve(ctx context.Context, request ReserveRequest) error {
	s.requests = append(s.requests, request)
	return s.err
}

func TestQuotaPassesWithinLimit(t *testing.T) {
	t.Parallel()

	const key = "mv_test_secret"
	store := &fakeUsageStore{}
	handler := authenticatedHandler(t,
		SingleKeyOptions{Key: key, ClientID: 9, DailyRequestQuota: 100, DailyScanQuota: 10},
		Quota(store, QuotaScans))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, apiRequest(key))

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", recorder.Code)
	}
	if len(store.requests) != 1 {
		t.Fatalf("Reserve calls = %d, want 1", len(store.requests))
	}
	got := store.requests[0]
	if got.ClientID != 9 || got.Class != QuotaScans || got.RequestLimit != 100 || got.ClassLimit != 10 {
		t.Fatalf("Reserve request = %+v", got)
	}
}

func TestQuotaExhaustionReturnsDocumentedError(t *testing.T) {
	t.Parallel()

	const key = "mv_test_secret"
	store := &fakeUsageStore{err: ErrQuotaExceeded}
	handler := authenticatedHandler(t,
		SingleKeyOptions{Key: key, ClientID: 9, DailyScanQuota: 10},
		Quota(store, QuotaScans))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, apiRequest(key))

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", recorder.Code)
	}
	retryAfter, err := strconv.Atoi(recorder.Header().Get("Retry-After"))
	if err != nil || retryAfter < 1 {
		t.Fatalf("Retry-After = %q, want a positive second count", recorder.Header().Get("Retry-After"))
	}
	if body := recorder.Body.String(); !strings.Contains(body, "quota_exceeded") {
		t.Fatalf("body = %s, want the quota_exceeded code", body)
	}
}

func TestQuotaStoreFailureFailsClosed(t *testing.T) {
	t.Parallel()

	const key = "mv_test_secret"
	store := &fakeUsageStore{err: errors.New("database down")}
	handler := authenticatedHandler(t,
		SingleKeyOptions{Key: key, ClientID: 9},
		Quota(store, QuotaRequests))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, apiRequest(key))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: an unenforceable quota must not allow traffic", recorder.Code)
	}
}

func TestQuotaSkipsLegacyKeysWithoutAClientRow(t *testing.T) {
	t.Parallel()

	const key = "mv_test_secret"
	store := &fakeUsageStore{err: ErrQuotaExceeded}
	handler := authenticatedHandler(t, SingleKeyOptions{Key: key},
		Quota(store, QuotaScans))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, apiRequest(key))

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 for the legacy key", recorder.Code)
	}
	if len(store.requests) != 0 {
		t.Fatal("the legacy key has no client row and must not consume quota")
	}
}
