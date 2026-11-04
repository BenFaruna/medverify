package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func corsHandler(origins []string) http.Handler {
	return CORS(origins)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}

func TestCORSAllowsListedOrigin(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/v1/scans", nil)
	request.Header.Set("Origin", "https://app.medverify.test")
	recorder := httptest.NewRecorder()
	corsHandler([]string{"https://app.medverify.test"}).ServeHTTP(recorder, request)

	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "https://app.medverify.test" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
	if got := recorder.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("Vary = %q, want Origin", got)
	}
}

func TestCORSRejectsUnlistedOrigin(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/v1/scans", nil)
	request.Header.Set("Origin", "https://evil.example")
	recorder := httptest.NewRecorder()
	corsHandler([]string{"https://app.medverify.test"}).ServeHTTP(recorder, request)

	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want no header", got)
	}
}

func TestCORSWithNoAllowlistSendsNoHeaders(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/v1/scans", nil)
	request.Header.Set("Origin", "https://app.medverify.test")
	recorder := httptest.NewRecorder()
	corsHandler(nil).ServeHTTP(recorder, request)

	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want no header", got)
	}
}

func TestCORSIgnoresWildcard(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/v1/scans", nil)
	request.Header.Set("Origin", "https://app.medverify.test")
	recorder := httptest.NewRecorder()
	corsHandler([]string{"*"}).ServeHTTP(recorder, request)

	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("a wildcard must never be echoed, got %q", got)
	}
}

func TestCORSPreflightFromAllowedOrigin(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodOptions, "/v1/scans", nil)
	request.Header.Set("Origin", "https://app.medverify.test")
	request.Header.Set("Access-Control-Request-Method", "POST")
	recorder := httptest.NewRecorder()
	corsHandler([]string{"https://app.medverify.test"}).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", recorder.Code)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Fatal("a preflight must advertise the allowed methods")
	}
	if got := recorder.Header().Get("Access-Control-Allow-Headers"); got == "" {
		t.Fatal("a preflight must advertise the allowed headers")
	}
}

func TestCORSPreflightFromUnlistedOriginGetsNoGrant(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodOptions, "/v1/scans", nil)
	request.Header.Set("Origin", "https://evil.example")
	request.Header.Set("Access-Control-Request-Method", "POST")
	recorder := httptest.NewRecorder()
	corsHandler([]string{"https://app.medverify.test"}).ServeHTTP(recorder, request)

	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want no header", got)
	}
}
