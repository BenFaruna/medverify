// Package auth authenticates integrator API clients. Phase 3 ships a
// placeholder single-key check; phase 11 replaces it with the api_clients table
// and per-client scopes behind the same middleware seam.
package auth

import (
	"context"
	"crypto/subtle"
	"net/http"

	"github.com/benfaruna/medverify/internal/platform/httpx"
)

// HeaderAPIKey is the request header carrying the client key.
const HeaderAPIKey = "X-API-Key"

// DefaultClientKey is the principal recorded for the placeholder API key. It
// becomes a real api_clients reference in phase 11.
const DefaultClientKey = "default"

type principalKey struct{}

// Principal is the authenticated caller.
type Principal struct {
	Key string
}

// APIKey rejects requests without the configured key. The comparison is
// constant time so a wrong key cannot be discovered byte by byte.
func APIKey(expected string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provided := r.Header.Get(HeaderAPIKey)
			if expected == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
				httpx.WriteProblem(w, r, httpx.NewProblem(
					http.StatusUnauthorized,
					httpx.CodeUnauthorized,
					"A valid X-API-Key header is required.",
				))
				return
			}

			ctx := context.WithValue(r.Context(), principalKey{}, Principal{Key: DefaultClientKey})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// PrincipalFromContext returns the caller set by APIKey.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(Principal)
	return principal, ok && principal.Key != ""
}
