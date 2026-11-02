package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/benfaruna/medverify/internal/platform/auth"
	"github.com/benfaruna/medverify/internal/platform/httpx"
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
	APIKey         string
	Scan           *scan.HTTPHandler
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

	router.NotFound(httpx.NotFoundHandler().ServeHTTP)
	router.MethodNotAllowed(httpx.MethodNotAllowedHandler().ServeHTTP)

	router.Get("/healthz", healthz)
	router.Get("/readyz", readyz(logger, opts.Dependencies, opts.ReadyTimeout))

	if opts.Scan != nil {
		router.Route("/v1", func(r chi.Router) {
			r.Use(auth.APIKey(opts.APIKey))
			r.Post("/scans", opts.Scan.Create)
			r.Get("/scans/{id}", opts.Scan.Get)
		})
	}

	return router
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
