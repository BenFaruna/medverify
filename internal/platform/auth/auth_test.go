package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestKeyPrefixFromToken(t *testing.T) {
	t.Parallel()

	prefix, err := KeyPrefixFromToken("mv_abc123_secret")
	if err != nil {
		t.Fatalf("KeyPrefixFromToken: %v", err)
	}
	if prefix != "abc123" {
		t.Fatalf("prefix = %q, want abc123", prefix)
	}

	for _, bad := range []string{"", "abc123_secret", "mv_", "mv_abc123", "mv__secret"} {
		if _, err := KeyPrefixFromToken(bad); !errors.Is(err, ErrMalformedKey) {
			t.Fatalf("KeyPrefixFromToken(%q) error = %v, want ErrMalformedKey", bad, err)
		}
	}
}

func TestHashAPIKeyNeverReturnsTheSecret(t *testing.T) {
	t.Parallel()

	full := "mv_abc123_supersecretvalue"
	hash, prefix, fullPrefix := HashAPIKey(full)
	if hash == full || hash == "supersecretvalue" {
		t.Fatal("the hash must not be the key itself")
	}
	if len(hash) != 64 {
		t.Fatalf("hash must be a hex SHA-256, got %d chars", len(hash))
	}
	if prefix != "abc123" || fullPrefix != "mv_abc123" {
		t.Fatalf("prefix = %q fullPrefix = %q", prefix, fullPrefix)
	}
}

func TestConstantTimeEqual(t *testing.T) {
	t.Parallel()

	if !ConstantTimeEqual("abc", "abc") {
		t.Fatal("equal values must compare equal")
	}
	if ConstantTimeEqual("abc", "abd") {
		t.Fatal("different values must not compare equal")
	}
	if ConstantTimeEqual("abc", "abcdef") {
		t.Fatal("different lengths must not compare equal")
	}
}

func TestTenantKeyIsStableAndNotTheSecret(t *testing.T) {
	t.Parallel()

	database := Principal{ClientID: 42}
	if got := database.TenantKey(); got != "client:42" {
		t.Fatalf("TenantKey = %q, want client:42", got)
	}
	legacy := Principal{}
	if got := legacy.TenantKey(); got != DefaultClientKey {
		t.Fatalf("legacy TenantKey = %q, want %q", got, DefaultClientKey)
	}
}

func TestPrincipalFromContextRejectsAbsentAndZeroValue(t *testing.T) {
	t.Parallel()

	if _, ok := PrincipalFromContext(context.Background()); ok {
		t.Fatal("an empty context must not yield a principal")
	}

	// Storing a zero Principal directly must not authenticate anything: the
	// middleware is the only writer and always stores a pointer.
	ctx := context.WithValue(context.Background(), principalKey{}, Principal{})
	if _, ok := PrincipalFromContext(ctx); ok {
		t.Fatal("a zero-value principal must not authenticate")
	}
}

func TestAPIKeyMiddlewareFailsClosedWithoutStore(t *testing.T) {
	t.Parallel()

	handler := APIKey(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run without a store")
	}))

	request := httptest.NewRequest(http.MethodGet, "/v1/scans", nil)
	request.Header.Set(HeaderAPIKey, "anything")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}

func TestAPIKeyMiddlewareRejectsWrongKey(t *testing.T) {
	t.Parallel()

	store := NewSingleKeyStore(SingleKeyOptions{Key: "mv_abc_secret"})
	handler := APIKey(store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run for a rejected key")
	}))

	request := httptest.NewRequest(http.MethodGet, "/v1/scans", nil)
	request.Header.Set(HeaderAPIKey, "mv_abc_other")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}

func TestAPIKeyMiddlewareSetsPrincipal(t *testing.T) {
	t.Parallel()

	store := NewSingleKeyStore(SingleKeyOptions{Key: "mv_abc_secret", ClientID: 7})
	handler := APIKey(store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok {
			t.Error("principal missing from context")
			return
		}
		if principal.ClientID != 7 {
			t.Errorf("ClientID = %d, want 7", principal.ClientID)
		}
		if !principal.HasScope(ScopeScansWrite) {
			t.Error("default key must carry every scope")
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest(http.MethodGet, "/v1/scans", nil)
	request.Header.Set(HeaderAPIKey, "mv_abc_secret")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", recorder.Code)
	}
}

func TestRequireScopeRejectsMissingScope(t *testing.T) {
	t.Parallel()

	store := NewSingleKeyStore(SingleKeyOptions{
		Key:    "mv_abc_secret",
		Scopes: []string{ScopeScansRead},
	})
	handler := APIKey(store)(RequireScope(ScopeScansWrite)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("handler must not run without the scope")
		})))

	request := httptest.NewRequest(http.MethodPost, "/v1/scans", nil)
	request.Header.Set(HeaderAPIKey, "mv_abc_secret")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
}

func TestSingleKeyStoreRequiresTheConfiguredKey(t *testing.T) {
	t.Parallel()

	store := NewSingleKeyStore(SingleKeyOptions{Key: "mv_abc_secret"})
	if _, err := store.Lookup(context.Background(), "mv_abc_secret"); err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if _, err := store.Lookup(context.Background(), "mv_abc_other"); !errors.Is(err, ErrUnknownClient) {
		t.Fatalf("Lookup(wrong) error = %v, want ErrUnknownClient", err)
	}

	// An unconfigured store must not accept an empty key.
	empty := NewSingleKeyStore(SingleKeyOptions{})
	if _, err := empty.Lookup(context.Background(), ""); !errors.Is(err, ErrUnknownClient) {
		t.Fatalf("Lookup(empty) error = %v, want ErrUnknownClient", err)
	}
}

func TestGenerateAPIKeyIsUniqueAndResolvable(t *testing.T) {
	t.Parallel()

	first, hash, prefix, fullPrefix, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey: %v", err)
	}
	second, _, _, _, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey: %v", err)
	}
	if first == second {
		t.Fatal("two generated keys must differ")
	}

	roundTripHash, roundTripPrefix, roundTripFull := HashAPIKey(first)
	if roundTripHash != hash || roundTripPrefix != prefix || roundTripFull != fullPrefix {
		t.Fatalf("HashAPIKey disagrees with GenerateAPIKey: %q %q %q", roundTripHash, roundTripPrefix, roundTripFull)
	}

	parsed, err := KeyPrefixFromToken(first)
	if err != nil {
		t.Fatalf("KeyPrefixFromToken: %v", err)
	}
	if parsed != prefix {
		t.Fatalf("parsed prefix = %q, want %q", parsed, prefix)
	}
}
