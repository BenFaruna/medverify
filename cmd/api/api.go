package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/benfaruna/medverify/internal/assistant"
	"github.com/benfaruna/medverify/internal/platform/auth"
	"github.com/benfaruna/medverify/internal/platform/httpx"
	"github.com/benfaruna/medverify/internal/platform/ratelimit"
	"github.com/benfaruna/medverify/internal/scan"
)

// Dependency is one thing /readyz probes. A dependency is ready when Check
// returns nil within the readiness timeout.
type Dependency struct {
	Name  string
	Check func(context.Context) error
}

// Options configures the HTTP API.
type Options struct {
	Logger         *slog.Logger
	Dependencies   []Dependency
	ReadyTimeout   time.Duration
	RequestTimeout time.Duration
	MaxBodyBytes   int64
	// Auth is the database-backed key store. When it is nil and APIKey is set,
	// the legacy single-key mode is used; that mode is development only.
	Auth auth.Store
	// Usage enforces daily quotas. A nil store skips quota checks, which is
	// what the legacy single-key mode and unit tests need.
	Usage auth.UsageStore
	// Limiter holds the per-key and per-IP token buckets. A nil limiter
	// disables rate limiting.
	Limiter *ratelimit.Limiter
	// RatePerMin and IPRatePerMin bound requests per key and per address;
	// RateLimitBurst is the token bucket size.
	RatePerMin     int
	IPRatePerMin   int
	RateLimitBurst int
	// CORSOrigins allowlists browser origins. Empty disables CORS headers.
	CORSOrigins []string
	// Web serves the static MVP client at /. Nil disables it.
	Web       http.Handler
	APIKey    string
	Scan      *scan.HTTPHandler
	Assistant *assistant.HTTPHandler
}

// NewHandler builds the router: middleware, health endpoints, and the problem
// handlers for unmatched routes.
func NewHandler(opts Options) http.Handler {
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if opts.ReadyTimeout <= 0 {
		opts.ReadyTimeout = 2 * time.Second
	}
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = 15 * time.Second
	}
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = 1 << 20
	}

	router := chi.NewRouter()
	router.Use(httpx.RequestID)
	router.Use(httpx.Recover(logger))
	router.Use(httpx.AccessLog(logger))
	router.Use(httpx.Timeout(opts.RequestTimeout))
	router.Use(httpx.MaxBodyBytesExcept(opts.MaxBodyBytes, "/v1/scans"))
	router.Use(httpx.CORS(opts.CORSOrigins))

	router.NotFound(httpx.NotFoundHandler().ServeHTTP)
	router.MethodNotAllowed(httpx.MethodNotAllowedHandler().ServeHTTP)

	router.Get("/healthz", healthz)
	router.Get("/readyz", readyz(logger, opts.Dependencies, opts.ReadyTimeout))

	// The MVP client is static and talks only to the public API. It is mounted
	// after the probes so it can never shadow them.
	if opts.Web != nil {
		router.Mount("/", opts.Web)
	}

	if opts.Scan != nil {
		store := opts.Auth
		if store == nil && opts.APIKey != "" {
			store = auth.NewSingleKeyStore(auth.SingleKeyOptions{Key: opts.APIKey})
		}

		router.Route("/v1", func(r chi.Router) {
			// Address-level limiting runs before authentication so an unknown
			// caller cannot hammer the key lookup.
			r.Use(auth.IPRateLimit(opts.Limiter, opts.IPRatePerMin, opts.RateLimitBurst))
			// A nil store means no key can authenticate: every route below fails
			// closed with 401 rather than serving unauthenticated traffic.
			r.Use(auth.APIKey(store))
			r.Use(auth.RateLimit(auth.RateLimitOptions{
				Limiter:           opts.Limiter,
				DefaultRatePerMin: opts.RatePerMin,
				DefaultBurst:      opts.RateLimitBurst,
			}))

			// Every request counts against the daily request quota; the scan
			// route additionally counts a scan.
			scanWrite := chain(auth.RequireScope(auth.ScopeScansWrite), auth.Quota(opts.Usage, auth.QuotaScans))
			scanRead := chain(auth.RequireScope(auth.ScopeScansRead), auth.Quota(opts.Usage, auth.QuotaRequests))
			questionWrite := chain(auth.RequireScope(auth.ScopeQuestionsWrite), auth.Quota(opts.Usage, auth.QuotaQuestions))

			r.Group(func(r chi.Router) {
				r.Use(scanWrite)
				r.Post("/scans", opts.Scan.Create)
			})
			r.Group(func(r chi.Router) {
				r.Use(scanRead)
				r.Get("/scans/{id}", opts.Scan.Get)
			})
			if opts.Assistant != nil {
				r.Group(func(r chi.Router) {
					r.Use(questionWrite)
					r.Post("/scans/{id}/questions", opts.Assistant.Ask)
				})
				r.Group(func(r chi.Router) {
					r.Use(scanRead)
					r.Get("/scans/{id}/conversation", opts.Assistant.Conversation)
				})
			}
		})
	}

	return router
}

// chain applies middlewares in order, so the first one runs first.
func chain(middlewares ...func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		for i := len(middlewares) - 1; i >= 0; i-- {
			next = middlewares[i](next)
		}
		return next
	}
}

// healthz reports that the process is alive. It never touches a dependency, so
// a database outage cannot restart a health-check-driven container.
func healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type checkResult struct {
	name string
	err  error
}

// readyz reports whether every dependency is reachable.
func readyz(logger *slog.Logger, dependencies []Dependency, timeout time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(dependencies) == 0 {
			writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "checks": map[string]string{}})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		results := make(chan checkResult, len(dependencies))
		for _, dependency := range dependencies {
			go func(dependency Dependency) {
				results <- checkResult{name: dependency.Name, err: dependency.check(ctx)}
			}(dependency)
		}

		checks := make(map[string]string, len(dependencies))
		failed := make([]string, 0, len(dependencies))
		for range dependencies {
			result := <-results
			if result.err != nil {
				checks[result.name] = "unavailable"
				failed = append(failed, result.name)
				logger.WarnContext(r.Context(), "readiness check failed",
					slog.String("dependency", result.name),
					slog.Any("err", result.err),
				)
				continue
			}
			checks[result.name] = "ok"
		}

		if len(failed) > 0 {
			httpx.WriteProblem(w, r, httpx.NewProblem(
				http.StatusServiceUnavailable,
				httpx.CodeUnavailable,
				"Unavailable dependencies: "+strings.Join(failed, ", ")+".",
			))
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "checks": checks})
	}
}

func (d Dependency) check(ctx context.Context) error {
	if d.Check == nil {
		return nil
	}
	return d.Check(ctx)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
