package auth

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/benfaruna/medverify/internal/platform/httpx"
	"github.com/benfaruna/medverify/internal/platform/ratelimit"
)

// ErrQuotaExceeded is returned by UsageStore.Reserve when a client has spent
// its daily allowance for a counter.
var ErrQuotaExceeded = errors.New("daily quota exceeded")

// QuotaClass names the daily counter a request consumes.
type QuotaClass int

// Quota classes, one per usage_counters column.
const (
	QuotaRequests QuotaClass = iota
	QuotaScans
	QuotaQuestions
)

// String returns the counter name used in problem details.
func (c QuotaClass) String() string {
	switch c {
	case QuotaScans:
		return "scan"
	case QuotaQuestions:
		return "question"
	default:
		return "request"
	}
}

// UsageStore reserves one request against a client's daily counters. The
// reserve is atomic: a caller that receives nil has definitely been counted,
// and a caller that receives ErrQuotaExceeded has definitely not been.
type UsageStore interface {
	Reserve(ctx context.Context, request ReserveRequest) error
}

// RateLimitOptions configures per-key rate limiting.
type RateLimitOptions struct {
	// Limiter holds the token buckets. It is required.
	Limiter *ratelimit.Limiter
	// DefaultRatePerMin and DefaultBurst apply when the key carries no rate,
	// such as in the legacy single-key development mode.
	DefaultRatePerMin int
	DefaultBurst      int
}

// RateLimit rejects a request once the tenant's token bucket is empty. It must
// run after APIKey.
func RateLimit(opts RateLimitOptions) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if opts.Limiter == nil {
			// No limiter configured: limiting is off, not broken.
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := PrincipalFromContext(r.Context())
			if !ok {
				writeUnauthorized(w, r)
				return
			}

			rate := int(principal.RatePerMin)
			if rate <= 0 {
				rate = opts.DefaultRatePerMin
			}
			burst := opts.DefaultBurst
			if burst <= 0 {
				burst = rate
			}
			if rate <= 0 {
				// No limit configured: nothing to enforce.
				next.ServeHTTP(w, r)
				return
			}

			result := opts.Limiter.Allow(principal.TenantKey(), rate, burst)
			w.Header().Set("X-RateLimit-Limit", strconv.Itoa(result.Limit))
			w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(result.Remaining))

			if !result.Allowed {
				w.Header().Set("Retry-After", strconv.Itoa(int(result.RetryAfter.Seconds())))
				httpx.WriteProblem(w, r, httpx.NewProblem(
					http.StatusTooManyRequests,
					httpx.CodeRateLimited,
					"Too many requests. Retry after the number of seconds in the Retry-After header.",
				))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// IPRateLimit rejects a request once the calling address's bucket is empty. It
// protects the unauthenticated edge, so it runs before APIKey.
//
// The key is the connection's remote address. Forwarded headers are ignored on
// purpose: trusting them without a configured, verified proxy list lets any
// caller choose its own bucket.
func IPRateLimit(limiter *ratelimit.Limiter, perMin, burst int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if limiter == nil || perMin <= 0 {
			return next
		}
		if burst <= 0 {
			burst = perMin
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := remoteIP(r)
			result := limiter.Allow("ip:"+ip, perMin, burst)
			if !result.Allowed {
				w.Header().Set("Retry-After", strconv.Itoa(int(result.RetryAfter.Seconds())))
				httpx.WriteProblem(w, r, httpx.NewProblem(
					http.StatusTooManyRequests,
					httpx.CodeRateLimited,
					"Too many requests from this address.",
				))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Quota enforces a client's daily allowance for one class of request. It must
// run after APIKey and after RateLimit so a burst is rejected before it costs a
// database write.
//
// The legacy single-key mode has no client row, so it has no daily quota; that
// mode is development only and production requires database-backed keys.
func Quota(store UsageStore, class QuotaClass) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := PrincipalFromContext(r.Context())
			if !ok {
				writeUnauthorized(w, r)
				return
			}
			if principal.ClientID == 0 || store == nil {
				next.ServeHTTP(w, r)
				return
			}

			err := store.Reserve(r.Context(), ReserveRequest{
				ClientID:     principal.ClientID,
				Class:        class,
				RequestLimit: principal.DailyRequestQuota,
				ClassLimit:   quotaLimit(principal, class),
			})
			switch {
			case errors.Is(err, ErrQuotaExceeded):
				w.Header().Set("Retry-After", strconv.Itoa(secondsUntilTomorrow()))
				httpx.WriteProblem(w, r, httpx.NewProblem(
					http.StatusTooManyRequests,
					httpx.CodeQuotaExceeded,
					"The daily "+class.String()+" quota for this API key is used up.",
				))
			case err != nil:
				// A counter we cannot write cannot be enforced. Refusing the
				// request is the only option that does not risk unbounded spend.
				httpx.WriteProblem(w, r, httpx.NewProblem(
					http.StatusServiceUnavailable,
					httpx.CodeUnavailable,
					"The usage counter is unavailable; the request was not counted.",
				))
			default:
				next.ServeHTTP(w, r)
			}
		})
	}
}

func quotaLimit(principal Principal, class QuotaClass) int32 {
	switch class {
	case QuotaScans:
		return principal.DailyScanQuota
	case QuotaQuestions:
		return principal.DailyRequestQuota
	default:
		return principal.DailyRequestQuota
	}
}

// secondsUntilTomorrow returns the Retry-After for a daily quota, never below
// one second.
func secondsUntilTomorrow() int {
	now := time.Now()
	tomorrow := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, 1)
	seconds := int(tomorrow.Sub(now).Seconds())
	if seconds < 1 {
		return 1
	}
	return seconds
}
