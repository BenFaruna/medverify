// Package auth authenticates integrator API clients.
//
// Phase 11 replaces the placeholder single-key check with per-client API keys
// stored in api_clients and api_keys, behind the same middleware seam. A key is
// a public prefix plus a random secret; only a SHA-256 hash of the full key is
// stored, and the full value is shown once at creation.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/benfaruna/medverify/internal/platform/httpx"
)

// HeaderAPIKey is the request header carrying the client key.
const HeaderAPIKey = "X-API-Key"

// DefaultClientKey is the tenant key recorded for the legacy placeholder API
// key. It is used only when the API is started with the legacy single-key mode;
// production requires database-backed keys.
const DefaultClientKey = "default"

// Scope constants define the permissions a key may carry. Every HTTP handler
// that needs a capability should gate on these values with RequireScope.
const (
	ScopeScansWrite     = "scans:write"
	ScopeScansRead      = "scans:read"
	ScopeQuestionsWrite = "questions:write"
)

// AllScopes is the set of scopes this build understands. Rows may carry other
// strings; they are ignored rather than treated as a grant.
var AllScopes = []string{ScopeScansWrite, ScopeScansRead, ScopeQuestionsWrite}

var (
	// ErrUnknownClient is returned when a key's prefix does not resolve to a
	// row, or the client or key is inactive, expired, or revoked.
	ErrUnknownClient = errors.New("unknown API client")
	// ErrInsufficientScope is returned when a key lacks a required scope.
	ErrInsufficientScope = errors.New("insufficient scope")
	// ErrMalformedKey is returned for a value that is not an mv_ key.
	ErrMalformedKey = errors.New("malformed API key")
)

type principalKey struct{}

// Principal is the authenticated caller set by APIKey middleware. A zero
// Principal is never stored: PrincipalFromContext reports false instead.
type Principal struct {
	ClientID   int64
	Scopes     []string
	Sandbox    bool
	ClientName string
	// RatePerMin and the daily quotas come from the key's client row so the
	// middleware never hard-codes a plan. Zero means "not configured".
	RatePerMin        int32
	DailyRequestQuota int32
	DailyScanQuota    int32
}

// TenantKey is the stable, non-secret identifier that scopes stored rows to
// this client. It is safe to persist, log, and show in support tooling, unlike
// the API key itself.
func (p Principal) TenantKey() string {
	if p.ClientID != 0 {
		return "client:" + strconv.FormatInt(p.ClientID, 10)
	}
	return DefaultClientKey
}

// HasScope reports whether the principal carries a given scope.
func (p Principal) HasScope(scope string) bool {
	for _, s := range p.Scopes {
		if strings.EqualFold(s, scope) {
			return true
		}
	}
	return false
}

// APIKey builds middleware that authenticates every request with the key store.
// The store must be non-nil; a nil store fails closed.
func APIKey(store Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provided := strings.TrimSpace(r.Header.Get(HeaderAPIKey))
			if provided == "" {
				writeUnauthorized(w, r)
				return
			}
			if store == nil {
				writeUnauthorized(w, r)
				return
			}

			key, err := store.Lookup(r.Context(), provided)
			if err != nil || key == nil {
				writeUnauthorized(w, r)
				return
			}

			principal := &Principal{
				ClientID:          key.ClientID,
				Scopes:            key.Scopes,
				Sandbox:           key.Sandbox,
				ClientName:        key.ClientName,
				RatePerMin:        key.RatePerMin,
				DailyRequestQuota: key.DailyRequestQuota,
				DailyScanQuota:    key.DailyScanQuota,
			}
			ctx := context.WithValue(r.Context(), principalKey{}, principal)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireScope returns middleware that rejects requests whose principal lacks
// the named scope. It must run after APIKey.
func RequireScope(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := PrincipalFromContext(r.Context())
			if !ok {
				writeUnauthorized(w, r)
				return
			}
			if !principal.HasScope(scope) {
				httpx.WriteProblem(w, r, httpx.NewProblem(
					http.StatusForbidden,
					httpx.CodeInsufficientScope,
					"The API key does not have permission to perform this action.",
				))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// PrincipalFromContext returns the caller set by APIKey. The bool is false when
// no principal was set, so a zero value is never treated as authenticated.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(*Principal)
	if !ok || principal == nil {
		return Principal{}, false
	}
	return *principal, true
}

// Store authenticates a provided access token and returns the parsed key.
type Store interface {
	Lookup(ctx context.Context, accessToken string) (*Key, error)
}

// Key is the parsed, authenticated key for one request.
type Key struct {
	ID                int64
	ClientID          int64
	KeyPrefix         string
	Scopes            []string
	Active            bool
	ExpiresAt         *time.Time
	ClientName        string
	Sandbox           bool
	RatePerMin        int32
	DailyRequestQuota int32
	DailyScanQuota    int32
}

// GenerateAPIKey builds a new key, returns the full value once, and the hash to
// store. The caller must show fullKey once and never return it again.
func GenerateAPIKey() (fullKey, hash, prefix, fullPrefix string, err error) {
	fullKey, err = randomAPIKey()
	if err != nil {
		return "", "", "", "", err
	}
	hash, prefix, fullPrefix = HashAPIKey(fullKey)
	return fullKey, hash, prefix, fullPrefix, nil
}

// randomAPIKey returns a mv_<prefix>_<secret> key. The prefix is 12 hex
// characters so it stays unique in practice while remaining short enough to
// display; the secret is 32 random bytes.
func randomAPIKey() (string, error) {
	prefixBytes := make([]byte, 6)
	if _, err := rand.Read(prefixBytes); err != nil {
		return "", err
	}
	prefix := hex.EncodeToString(prefixBytes)

	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return "", err
	}
	secret := hex.EncodeToString(secretBytes)

	return "mv_" + prefix + "_" + secret, nil
}

// HashAPIKey returns the SHA-256 hash and public prefix of a full key.
//
// The prefix is the segment between "mv_" and the next "_": "mv_abc123_..."
// has prefix "abc123" and full prefix "mv_abc123". The hash covers the whole
// key, so a prefix lookup alone cannot authenticate.
func HashAPIKey(fullKey string) (hash, prefix, fullPrefix string) {
	rest, found := strings.CutPrefix(fullKey, "mv_")
	if !found {
		// Not a key this package issues. Still return a stable hash so callers
		// never compare raw values.
		return hashFullKey(fullKey), fullKey, fullKey
	}
	prefix, _, found = strings.Cut(rest, "_")
	if !found || prefix == "" {
		return hashFullKey(fullKey), rest, fullKey
	}
	return hashFullKey(fullKey), prefix, "mv_" + prefix
}

// KeyPrefixFromToken extracts the lookup prefix from a presented token without
// validating it. An error means the token cannot be an issued key.
func KeyPrefixFromToken(token string) (string, error) {
	rest, found := strings.CutPrefix(token, "mv_")
	if !found {
		return "", ErrMalformedKey
	}
	prefix, secret, found := strings.Cut(rest, "_")
	if !found || prefix == "" || secret == "" {
		return "", ErrMalformedKey
	}
	return prefix, nil
}

func hashFullKey(fullKey string) string {
	sum := sha256.Sum256([]byte(fullKey))
	return hex.EncodeToString(sum[:])
}

func writeUnauthorized(w http.ResponseWriter, r *http.Request) {
	httpx.WriteProblem(w, r, httpx.NewProblem(
		http.StatusUnauthorized,
		httpx.CodeUnauthorized,
		"A valid X-API-Key header is required.",
	))
}

// ConstantTimeEqual compares two strings without leaking their length or
// content through timing.
func ConstantTimeEqual(a, b string) bool {
	// hmac.Equal refuses unequal lengths immediately, which would leak the
	// length; hash both sides first so the comparison is uniform.
	left := sha256.Sum256([]byte(a))
	right := sha256.Sum256([]byte(b))
	return subtleCompare(left[:], right[:])
}

func subtleCompare(a, b []byte) bool {
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
