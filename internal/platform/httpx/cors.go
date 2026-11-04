package httpx

import (
	"net/http"
	"strings"
)

// CORS returns middleware that answers cross-origin requests for an explicit
// allowlist of origins.
//
// An empty allowlist disables CORS entirely: no Access-Control-Allow-Origin
// header is sent, so a browser cannot read the response. A wildcard is never
// allowed, because these routes are authenticated with a key and browsers must
// not be able to send it from an origin we did not choose.
func CORS(origins []string) func(http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		origin = strings.TrimSpace(strings.TrimSuffix(origin, "/"))
		if origin == "" || origin == "*" {
			continue
		}
		allowed[origin] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		if len(allowed) == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := strings.TrimSuffix(r.Header.Get("Origin"), "/")
			if _, ok := allowed[origin]; ok && origin != "" {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Add("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "X-API-Key, Content-Type, Idempotency-Key")
				w.Header().Set("Access-Control-Max-Age", "600")
			}

			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				if _, ok := allowed[origin]; !ok {
					// A preflight from an origin we do not allow gets no CORS
					// headers, which the browser turns into a blocked request.
					w.WriteHeader(http.StatusNoContent)
					return
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
