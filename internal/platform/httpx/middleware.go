package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"strings"
	"time"
)

// HeaderRequestID is the request correlation header we read and set.
const HeaderRequestID = "X-Request-Id"

// Validated on purpose: an inbound header is untrusted input that ends up in
// logs, so only a conservative character set is accepted.
var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{8,128}$`)

type requestIDKey struct{}

// RequestID reuses a well formed inbound request ID or generates a new one,
// stores it in the request context, and echoes it on the response.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(HeaderRequestID)
		if !requestIDPattern.MatchString(id) {
			id = newRequestID()
		}

		w.Header().Set(HeaderRequestID, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

// RequestIDFromContext returns the request ID stored by the RequestID
// middleware.
func RequestIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(requestIDKey{}).(string)
	return id, ok && id != ""
}

func newRequestID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// rand.Read never fails on supported platforms; fall back to a
		// timestamp rather than returning an empty correlation ID.
		return time.Now().UTC().Format("20060102T150405.000000000")
	}
	return hex.EncodeToString(buf[:])
}

// Recover turns a panic into a 500 problem response and keeps the process
// alive. The panic value is logged at error level; the response never echoes
// it.
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			recorder := &statusRecorder{ResponseWriter: w}
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}
				if err, ok := recovered.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					// http.ErrAbortHandler is the documented way to abort a
					// response without a stack trace or a 500 body.
					panic(recovered)
				}

				requestID, _ := RequestIDFromContext(r.Context())
				logger.ErrorContext(r.Context(), "panic recovered",
					slog.Any("panic", recovered),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.String("request_id", requestID),
					slog.String("stack", string(debug.Stack())),
				)

				if recorder.wroteHeader {
					// Too late to change the status; nothing else we can do.
					return
				}
				WriteProblem(recorder, r, NewProblem(
					http.StatusInternalServerError,
					CodeInternalError,
					"The server hit an unexpected error. Quote request ID "+requestID+" when reporting it.",
				))
			}()

			next.ServeHTTP(recorder, r)
		})
	}
}

// Timeout bounds handler execution. Handlers must honour the request context;
// if the deadline expires before they finish, the client receives a 503
// problem document instead of a partial response.
func Timeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()

			recorder := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(recorder, r.WithContext(ctx))

			if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return
			}
			if recorder.wroteHeader {
				return
			}
			WriteProblem(recorder, r, NewProblem(http.StatusServiceUnavailable, CodeTimeout, "The request took too long to process."))
		})
	}
}

// MaxBodyBytes caps the size of every request body. Handlers that read the body
// receive an error once the cap is exceeded; ErrPayloadTooLarge explains it.
func MaxBodyBytes(limit int64) func(http.Handler) http.Handler {
	return MaxBodyBytesExcept(limit)
}

// MaxBodyBytesExcept applies the cap to every path except those starting with
// one of the prefixes. Upload routes use it to enforce their own, larger limit
// without opening the cap for the rest of the API.
func MaxBodyBytesExcept(limit int64, prefixes ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, prefix := range prefixes {
				if prefix != "" && strings.HasPrefix(r.URL.Path, prefix) {
					next.ServeHTTP(w, r)
					return
				}
			}
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// AccessLog logs one line per request with method, path, status, duration,
// response size, and request ID.
func AccessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			recorder := &statusRecorder{ResponseWriter: w}

			next.ServeHTTP(recorder, r)

			requestID, _ := RequestIDFromContext(r.Context())
			logger.LogAttrs(r.Context(), slog.LevelInfo, "http request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", recorder.status()),
				slog.Duration("duration", time.Since(start)),
				slog.Int("response_bytes", recorder.bytes),
				slog.String("request_id", requestID),
			)
		})
	}
}

// statusRecorder captures the status code and body size for logging and for
// deciding whether a late error response can still be written.
type statusRecorder struct {
	http.ResponseWriter
	statusCode  int
	bytes       int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.statusCode = status
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

func (r *statusRecorder) status() int {
	if !r.wroteHeader {
		return http.StatusOK
	}
	return r.statusCode
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
