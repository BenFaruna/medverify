package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/benfaruna/medverify/internal/account"
	"github.com/benfaruna/medverify/internal/assistant"
	"github.com/benfaruna/medverify/internal/platform/auth"
	"github.com/benfaruna/medverify/internal/platform/httpx"
	"github.com/benfaruna/medverify/internal/platform/metrics"
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
	Web http.Handler
	// Docs serves the interactive API documentation at /docs and the OpenAPI
	// document at /openapi.yaml. Nil disables both.
	Docs   http.Handler
	APIKey string
	// Account serves registration, sign-in, and API key management. It mounts
	// outside the X-API-Key middleware: people authenticate with a session
	// cookie and then manage the key their client code uses. Nil disables the
	// account routes.
	Account   *account.HTTPHandler
	Scan      *scan.HTTPHandler
	Assistant *assistant.HTTPHandler
	// Metrics exposes /metrics when set. Only aggregate HTTP counters are
	// recorded; paths are reduced to route patterns.
	Metrics *metrics.Metrics
	// Kill switches. The zero value keeps every feature enabled, so tests and
	// local runs need no extra setup; production passes the parsed config.
	DisableAPI       bool
	DisableScans     bool
	DisableAssistant bool
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
	if opts.Metrics != nil {
		router.Use(observeRequests(opts.Metrics))
		router.Method(http.MethodGet, "/metrics", opts.Metrics.Handler())
	}

	router.NotFound(httpx.NotFoundHandler().ServeHTTP)
	router.MethodNotAllowed(httpx.MethodNotAllowedHandler().ServeHTTP)

	router.Get("/healthz", healthz)
	router.Get("/readyz", readyz(logger, opts.Dependencies, opts.ReadyTimeout))

	// The docs handler owns /docs, /docs/*, and /openapi.yaml. Register the
	// exact prefix and wildcard rather than mounting at /, because the static
	// web client also mounts at / and the more specific routes must win.
	if opts.Docs != nil {
		router.Handle("/docs", opts.Docs)
		router.Handle("/docs/*", opts.Docs)
		router.Handle("/openapi.yaml", opts.Docs)
	}

	// Account routes mount before the static client, and outside the /v1 group,
	// so a browser session never has to hold an API key to reach them. The
	// per-address limiter still applies: these are the endpoints an attacker
	// would guess passwords against.
	if opts.Account != nil && !opts.DisableAPI {
		ipLimit := auth.IPRateLimit(opts.Limiter, opts.IPRatePerMin, opts.RateLimitBurst)

		router.Group(func(r chi.Router) {
			r.Use(ipLimit)
			r.Use(account.CSRF)

			r.Post("/v1/auth/register", opts.Account.Register)
			r.Post("/v1/auth/login", opts.Account.Login)
			r.Post("/v1/auth/logout", opts.Account.Logout)
		})

		router.Group(func(r chi.Router) {
			r.Use(ipLimit)
			r.Use(account.CSRF)
			r.Use(opts.Account.RequireSession)

			r.Get("/v1/account", opts.Account.Me)
			r.Get("/v1/account/api-keys", opts.Account.ListKeys)
			r.Post("/v1/account/api-keys/rotate", opts.Account.RotateKey)
		})
	}

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

		// A browser calls these routes with its session cookie instead of a key,
		// so the page never holds a credential a person could leak. Programmatic
		// clients are unaffected: X-API-Key still wins when present.
		var sessions auth.SessionAuth
		if opts.Account != nil && !opts.DisableAPI {
			sessions = opts.Account.SessionAuth()
		}

		router.Route("/v1", func(r chi.Router) {
			// Kill switches answer before any auth or database work, so a
			// maintenance window does not spend capacity on rejected calls.
			r.Use(featureGate(opts))
			// Address-level limiting runs before authentication so an unknown
			// caller cannot hammer the key lookup.
			r.Use(auth.IPRateLimit(opts.Limiter, opts.IPRatePerMin, opts.RateLimitBurst))
			// A nil store means no key can authenticate: every route below fails
			// closed with 401 rather than serving unauthenticated traffic.
			r.Use(auth.APIKeyOrSession(store, sessions))
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

// featureGate turns the configured kill switches into 503 problem responses.
// It runs before authentication, so a disabled feature never touches a key or
// the database. The switch matches the documented route, not a prefix: a typo
// in an unrelated path can never be silently disabled.
func featureGate(opts Options) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case opts.DisableAPI:
				unavailable(w, r, "API")
			case opts.DisableScans && r.Method == http.MethodPost && r.URL.Path == "/v1/scans":
				unavailable(w, r, "scan submission")
			case opts.DisableAssistant && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/questions"):
				unavailable(w, r, "question assistant")
			default:
				next.ServeHTTP(w, r)
			}
		})
	}
}

func unavailable(w http.ResponseWriter, r *http.Request, feature string) {
	httpx.WriteProblem(w, r, httpx.NewProblem(
		http.StatusServiceUnavailable,
		httpx.CodeUnavailable,
		"The "+feature+" is temporarily disabled.",
	))
}

// observeRequests records one metric sample per request, labelled by the
// router's pattern rather than the concrete path.
type metricsRecorder struct {
	http.ResponseWriter
	status int
}

func (r *metricsRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *metricsRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *metricsRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func observeRequests(registry *metrics.Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			recorder := &metricsRecorder{ResponseWriter: w}

			next.ServeHTTP(recorder, r)

			status := recorder.status
			if status == 0 {
				status = http.StatusOK
			}
			registry.ObserveRequest(r.Method, routePattern(r), status, time.Since(started))
		})
	}
}

// routePattern returns the most specific matched pattern. A middleware that
// answers early (401, 403, 429) stops before the leaf route is recorded, so a
// small fixed map names the documented /v1 routes instead. It never returns a
// concrete path, so a scan ID cannot become a metric label.
func routePattern(r *http.Request) string {
	if routeContext := chi.RouteContext(r.Context()); routeContext != nil {
		pattern := routeContext.RoutePattern()
		if pattern != "" && !strings.HasSuffix(pattern, "/*") {
			return pattern
		}
	}

	path := r.URL.Path
	switch {
	case path == "/v1/scans":
		return "/v1/scans"
	case path == "/metrics":
		return "/metrics"
	case strings.HasPrefix(path, "/v1/scans/") && strings.HasSuffix(path, "/questions"):
		return "/v1/scans/{id}/questions"
	case strings.HasPrefix(path, "/v1/scans/") && strings.HasSuffix(path, "/conversation"):
		return "/v1/scans/{id}/conversation"
	case strings.HasPrefix(path, "/v1/scans/"):
		return "/v1/scans/{id}"
	case path == "/v1/auth/register", path == "/v1/auth/login",
		path == "/v1/auth/logout", path == "/v1/account",
		path == "/v1/account/api-keys", path == "/v1/account/api-keys/rotate":
		// Account routes answer before the leaf is recorded only when a
		// middleware rejects them; keep the paths out of the /v1/* bucket so a
		// sign-in surge is visible in the metrics.
		return path
	case strings.HasPrefix(path, "/v1/"):
		return "/v1/*"
	default:
		return "unmatched"
	}
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
