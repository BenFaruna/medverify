package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sessionFromHeader stands in for the account resolver: it treats a
// "session" header as a valid browser session for client 7.
func sessionFromHeader(err error) SessionAuth {
	return func(r *http.Request) (*Principal, bool, error) {
		if r.Header.Get("session") == "" {
			return nil, false, nil
		}
		if err != nil {
			return nil, true, err
		}
		return &Principal{
			ClientID:          7,
			Scopes:            AllScopes,
			ClientName:        "user_abc123",
			RatePerMin:        60,
			DailyRequestQuota: 10000,
			DailyScanQuota:    200,
		}, true, nil
	}
}

// identity reports which client reached the handler, and fails the test when no
// principal was set: a handler behind the middleware must never see a zero one.
func identity(t *testing.T) http.Handler {
	t.Helper()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok {
			t.Error("handler ran without a principal")
			return
		}
		_, _ = w.Write([]byte(principal.TenantKey()))
	})
}

func TestAPIKeyOrSessionAcceptsAnAPIKey(t *testing.T) {
	t.Parallel()

	middleware := APIKeyOrSession(
		stubStore{key: Key{ClientID: 42, Scopes: AllScopes}},
		sessionFromHeader(nil),
	)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/scans/abc", nil)
	request.Header.Set(HeaderAPIKey, "mv_abc_secret")

	middleware(identity(t)).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got := recorder.Body.String(); got != "client:42" {
		t.Fatalf("handler saw %q, want the key's client", got)
	}
}

func TestAPIKeyOrSessionAcceptsASessionCookie(t *testing.T) {
	t.Parallel()

	middleware := APIKeyOrSession(nil, sessionFromHeader(nil))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/scans/abc", nil)
	request.Header.Set("session", "opaque-token")

	middleware(identity(t)).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got := recorder.Body.String(); got != "client:7" {
		t.Fatalf("handler saw %q, want the session's client", got)
	}
}

// The header wins when both are present, so a programmatic caller's scopes and
// limits always come from its own key rather than from a cookie it happens to
// carry.
func TestAPIKeyOrSessionPrefersTheKeyHeader(t *testing.T) {
	t.Parallel()

	middleware := APIKeyOrSession(
		stubStore{key: Key{ClientID: 42, Scopes: AllScopes}},
		sessionFromHeader(nil),
	)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/scans/abc", nil)
	request.Header.Set(HeaderAPIKey, "mv_abc_secret")
	request.Header.Set("session", "opaque-token")

	middleware(identity(t)).ServeHTTP(recorder, request)

	if got := recorder.Body.String(); got != "client:42" {
		t.Fatalf("handler saw %q, want the key's client", got)
	}
}

func TestAPIKeyOrSessionRejectsRequestsWithNoCredentials(t *testing.T) {
	t.Parallel()

	middleware := APIKeyOrSession(nil, sessionFromHeader(nil))
	recorder := httptest.NewRecorder()
	middleware(identity(t)).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/scans/abc", nil))

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if body := recorder.Body.String(); !strings.Contains(body, HeaderAPIKey) {
		t.Fatalf("detail should name the missing header, got %s", body)
	}
}

func TestAPIKeyOrSessionSendsAnExpiredSessionBackToSignIn(t *testing.T) {
	t.Parallel()

	middleware := APIKeyOrSession(nil, sessionFromHeader(errors.New("expired")))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/scans/abc", nil)
	request.Header.Set("session", "stale-token")

	middleware(identity(t)).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "Sign in to continue") {
		t.Fatalf("a dead session should ask the person to sign in, got %s", body)
	}
}

// A cookie is attached by the browser to any request to this host, so a write
// the browser attributes to another site is refused before the handler runs.
func TestAPIKeyOrSessionRefusesCrossSiteCookieWrites(t *testing.T) {
	t.Parallel()

	middleware := APIKeyOrSession(nil, sessionFromHeader(nil))
	cases := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{
			name:    "cross-site form post",
			headers: map[string]string{"Sec-Fetch-Site": "cross-site"},
			want:    http.StatusForbidden,
		},
		{
			name:    "foreign origin",
			headers: map[string]string{"Origin": "https://attacker.example"},
			want:    http.StatusForbidden,
		},
		{
			name:    "same-origin fetch",
			headers: map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://example.com"},
			want:    http.StatusOK,
		},
		{
			// A non-browser client sends neither header and cannot have been
			// tricked into carrying a cookie it does not own.
			name:    "no browser headers",
			headers: map[string]string{},
			want:    http.StatusOK,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "http://example.com/v1/scans", nil)
			request.Header.Set("session", "opaque-token")
			for name, value := range testCase.headers {
				request.Header.Set(name, value)
			}

			middleware(identity(t)).ServeHTTP(recorder, request)

			if recorder.Code != testCase.want {
				t.Fatalf("status = %d, want %d (%s)", recorder.Code, testCase.want, recorder.Body.String())
			}
		})
	}
}

// Reads are not state-changing, so a same-site navigation from a link is fine.
func TestAPIKeyOrSessionAllowsCrossSiteReadsWithTheCookie(t *testing.T) {
	t.Parallel()

	middleware := APIKeyOrSession(nil, sessionFromHeader(nil))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://example.com/v1/account", nil)
	request.Header.Set("session", "opaque-token")
	request.Header.Set("Sec-Fetch-Site", "cross-site")

	middleware(identity(t)).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
}

// A request that carries a key still fails closed when no store is configured,
// even if the deployment also has sessions.
func TestAPIKeyOrSessionFailsClosedForAKeyWithoutAStore(t *testing.T) {
	t.Parallel()

	middleware := APIKeyOrSession(nil, sessionFromHeader(nil))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/scans/abc", nil)
	request.Header.Set(HeaderAPIKey, "mv_abc_secret")

	middleware(identity(t)).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}

// The middleware must not treat a nil principal as authenticated.
func TestAPIKeyOrSessionRejectsANilPrincipal(t *testing.T) {
	t.Parallel()

	resolver := func(*http.Request) (*Principal, bool, error) { return nil, true, nil }
	middleware := APIKeyOrSession(nil, resolver)
	recorder := httptest.NewRecorder()
	middleware(identity(t)).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/scans/abc", nil))

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}
