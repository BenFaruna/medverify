package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/benfaruna/medverify/internal/platform/config"
)

// Fetcher errors. Callers use errors.Is to decide whether to retry or abort.
var (
	// ErrDisabled is returned when the Greenbook kill switch is off.
	ErrDisabled = errors.New("greenbook fetching is disabled")
	// ErrDisallowedHost is returned when a leaflet URL is outside the allowlist.
	ErrDisallowedHost = errors.New("leaflet host is not allowlisted")
	// ErrTooLarge is returned when a response or file exceeds its byte cap.
	ErrTooLarge = errors.New("response exceeds the configured size limit")
)

// DefaultLeafletTimeout bounds one leaflet download when none is configured. A
// 200 KB leaflet over a slow link takes seconds, not milliseconds, and the page
// timeout is far too tight for it.
const DefaultLeafletTimeout = 2 * time.Minute

// HTTPError is a response the fetcher will not retry or could not retry.
type HTTPError struct {
	URL        string
	StatusCode int
	Status     string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("greenbook request failed: %s: %s", e.Status, e.URL)
}

// PageRequest selects one page of the Greenbook list. Start and Length are
// zero-based offset and page size. The filters are optional and mirror the
// site's DataTables parameters.
type PageRequest struct {
	Start      int
	Length     int
	CategoryID string
	Status     string
	Search     string
	Ingredient string
}

// Leaflet is a streaming PDF response. The caller must close Body.
type Leaflet struct {
	URL         string
	Body        io.ReadCloser
	Size        int64 // -1 when the server did not send Content-Length
	ContentType string
}

// Fetcher is the single client for all Greenbook traffic. One instance owns
// the rate limiter and the on-disk cache, so nothing else can accidentally
// bypass politeness.
type Fetcher struct {
	cfg     config.Greenbook
	logger  *slog.Logger
	base    *url.URL
	client  *http.Client
	limiter *rate.Limiter
	cache   *fileCache

	allowedHosts map[string]struct{}
	backoffBase  time.Duration
	sleep        func(context.Context, time.Duration) error
}

// FetcherOption customises the fetcher, mainly for deterministic tests.
type FetcherOption func(*Fetcher)

// WithBackoffBase changes the first retry delay. Tests use a few milliseconds.
func WithBackoffBase(delay time.Duration) FetcherOption {
	return func(f *Fetcher) { f.backoffBase = delay }
}

// WithHTTPClient replaces the HTTP client. Tests may need a custom transport.
func WithHTTPClient(client *http.Client) FetcherOption {
	return func(f *Fetcher) { f.client = client }
}

// NewFetcher builds the client. It does not make a network call.
func NewFetcher(cfg config.Greenbook, logger *slog.Logger, opts ...FetcherOption) (*Fetcher, error) {
	base, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse greenbook base URL: %w", err)
	}
	if base.Host == "" {
		return nil, fmt.Errorf("greenbook base URL must include a host")
	}
	if base.Scheme != "https" && base.Scheme != "http" {
		return nil, fmt.Errorf("greenbook base URL must be http or https, got %q", base.Scheme)
	}
	if cfg.RequestsPerSecond <= 0 {
		return nil, fmt.Errorf("greenbook requests per second must be positive")
	}
	if cfg.LeafletTimeout <= 0 {
		cfg.LeafletTimeout = DefaultLeafletTimeout
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	cache, err := newFileCache(cfg.CacheDir, cfg.CacheTTL)
	if err != nil {
		return nil, err
	}

	allowed := make(map[string]struct{}, len(cfg.AllowedHosts))
	for _, host := range cfg.AllowedHosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if host != "" {
			allowed[host] = struct{}{}
		}
	}

	f := &Fetcher{
		cfg:    cfg,
		logger: logger,
		base:   base,
		client: &http.Client{
			// Never follow a redirect to a host outside the leaflet allowlist.
			// This closes the SSRF hole an allowlisted URL could otherwise use.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("too many redirects")
				}
				if _, ok := allowed[strings.ToLower(req.URL.Hostname())]; !ok {
					return fmt.Errorf("%w: redirect to %s", ErrDisallowedHost, req.URL.Hostname())
				}
				return nil
			},
		},
		limiter:      rate.NewLimiter(rate.Limit(cfg.RequestsPerSecond), 1),
		cache:        cache,
		allowedHosts: allowed,
		backoffBase:  500 * time.Millisecond,
		sleep:        sleepContext,
	}
	for _, opt := range opts {
		opt(f)
	}
	return f, nil
}

// FetchProducts returns the raw JSON body for one page, from the cache when it
// is fresh. The byte slice is safe for the caller to keep.
func (f *Fetcher) FetchProducts(ctx context.Context, req PageRequest) ([]byte, error) {
	if !f.cfg.Enabled {
		return nil, ErrDisabled
	}

	pageURL := f.pageURL(req).String()
	if cached, ok := f.cache.get(pageURL); ok {
		f.logger.DebugContext(ctx, "greenbook page served from cache", slog.Int("bytes", len(cached)))
		return cached, nil
	}

	body, err := f.get(ctx, pageURL, "application/json", 10<<20)
	if err != nil {
		return nil, err
	}
	if err := f.cache.put(pageURL, body); err != nil {
		// A cache write failure must not fail a sync; the next run re-fetches.
		f.logger.WarnContext(ctx, "could not cache greenbook page", slog.Any("err", err))
	}
	return body, nil
}

// FetchLeaflet opens a leaflet PDF after validating its scheme and host. The
// caller must close the body. A known oversized response is rejected before the
// body is read.
func (f *Fetcher) FetchLeaflet(ctx context.Context, rawURL string) (*Leaflet, error) {
	if !f.cfg.Enabled {
		return nil, ErrDisabled
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse leaflet URL: %w", err)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return nil, fmt.Errorf("%w: scheme %q", ErrDisallowedHost, parsed.Scheme)
	}
	host := strings.ToLower(parsed.Hostname())
	if _, ok := f.allowedHosts[host]; !ok {
		return nil, fmt.Errorf("%w: %s", ErrDisallowedHost, host)
	}

	// The whole PDF body is read under this budget, so it is much larger than a
	// page request's: a leaflet is a document, not a list row.
	resp, err := f.do(ctx, rawURL, "application/pdf", f.cfg.LeafletTimeout)
	if err != nil {
		return nil, err
	}
	if resp.ContentLength > f.cfg.MaxPDFBytes {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: content-length %d", ErrTooLarge, resp.ContentLength)
	}

	return &Leaflet{
		URL:         rawURL,
		Body:        &maxBytesReader{reader: resp.Body, limit: f.cfg.MaxPDFBytes},
		Size:        resp.ContentLength,
		ContentType: resp.Header.Get("Content-Type"),
	}, nil
}

// pageURL builds the DataTables query the Greenbook site expects.
func (f *Fetcher) pageURL(req PageRequest) *url.URL {
	u := *f.base
	query := u.Query()
	query.Set("draw", "1")
	query.Set("start", strconv.Itoa(req.Start))
	query.Set("length", strconv.Itoa(req.Length))
	query.Set("search[value]", req.Search)
	query.Set("search[regex]", "false")
	query.Set("search_ingredient", req.Ingredient)

	for i, column := range pageColumns {
		query.Set(fmt.Sprintf("columns[%d][data]", i), column)
		query.Set(fmt.Sprintf("columns[%d][name]", i), column)
		query.Set(fmt.Sprintf("columns[%d][searchable]", i), "true")
		query.Set(fmt.Sprintf("columns[%d][orderable]", i), "true")
		query.Set(fmt.Sprintf("columns[%d][search][value]", i), "")
		query.Set(fmt.Sprintf("columns[%d][search][regex]", i), "false")
	}
	// The site's grid filters category through the hidden category-id column
	// (index 3) and status through the last column (index 11).
	query.Set("columns[3][search][value]", req.CategoryID)
	query.Set("columns[11][search][value]", req.Status)
	query.Set("order[0][column]", "0")
	query.Set("order[0][dir]", "asc")

	u.RawQuery = query.Encode()
	return &u
}

var pageColumns = []string{
	"product_name",
	"ingredient.ingredient_name",
	"product_category.name",
	"product_category_id",
	"ingredient.synonym",
	"NAFDAC",
	"form.name",
	"route.name",
	"strength",
	"applicant.name",
	"approval_date",
	"status",
}

func (f *Fetcher) get(ctx context.Context, rawURL, accept string, maxBytes int64) ([]byte, error) {
	resp, err := f.do(ctx, rawURL, accept, f.cfg.RequestTimeout)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.ContentLength > maxBytes {
		return nil, fmt.Errorf("%w: content-length %d", ErrTooLarge, resp.ContentLength)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read greenbook response: %w", err)
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("%w: body larger than %d bytes", ErrTooLarge, maxBytes)
	}
	return body, nil
}

// do executes one logical request with retries for network errors and 5xx.
func (f *Fetcher) do(ctx context.Context, rawURL, accept string, timeout time.Duration) (*http.Response, error) {
	if timeout <= 0 {
		timeout = f.cfg.RequestTimeout
	}
	attempts := f.cfg.Retries + 1
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if !f.cfg.Enabled {
			return nil, ErrDisabled
		}
		if err := f.limiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("wait for greenbook rate limiter: %w", err)
		}

		attemptCtx, cancel := context.WithTimeout(ctx, timeout)
		req, err := http.NewRequestWithContext(attemptCtx, http.MethodGet, rawURL, nil)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("build greenbook request: %w", err)
		}
		req.Header.Set("User-Agent", f.cfg.UserAgent)
		req.Header.Set("Accept", accept)
		req.Header.Set("X-Requested-With", "XMLHttpRequest")

		resp, err := f.client.Do(req)
		if err != nil {
			cancel()
			lastErr = fmt.Errorf("greenbook request: %w", err)
			if !retryableError(err) {
				return nil, lastErr
			}
			if attempt == attempts-1 {
				break
			}
			if err := f.backoff(ctx, attempt); err != nil {
				return nil, err
			}
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			// The body stays under the request's own deadline: net/http cancels a
			// read when the request context is done, so the only way to give a PDF
			// time to arrive is to ask for that time here. cancel still runs when
			// the body is closed, which is why every caller closes it.
			resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
			return resp, nil
		}

		_ = resp.Body.Close()
		cancel()

		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return nil, &HTTPError{URL: rawURL, StatusCode: resp.StatusCode, Status: resp.Status}
		}

		lastErr = &HTTPError{URL: rawURL, StatusCode: resp.StatusCode, Status: resp.Status}
		f.logger.WarnContext(ctx, "greenbook request failed; retrying",
			slog.Int("status", resp.StatusCode),
			slog.Int("attempt", attempt+1),
		)
		if attempt == attempts-1 {
			break
		}
		if err := f.backoff(ctx, attempt); err != nil {
			return nil, err
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("greenbook request failed after %d attempts", attempts)
	}
	return nil, lastErr
}

func (f *Fetcher) backoff(ctx context.Context, attempt int) error {
	delay := f.backoffBase << attempt
	// Jitter up to 25% so parallel clients do not synchronise.
	if delay > 0 {
		delay += time.Duration(rand.Int64N(int64(delay/4) + 1))
	}
	return f.sleep(ctx, delay)
}

func retryableError(err error) bool {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// maxBytesReader fails with ErrTooLarge instead of silently truncating. A file
// of exactly limit bytes is accepted; one byte more is rejected.
type maxBytesReader struct {
	reader io.ReadCloser
	limit  int64
	read   int64
}

func (r *maxBytesReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.read += int64(n)
	if r.read > r.limit {
		return n, ErrTooLarge
	}
	return n, err
}

func (r *maxBytesReader) Close() error { return r.reader.Close() }

// fileCache is a small TTL cache for list pages. PDFs are not cached here: they
// live in object storage via the leaflet pipeline.
type fileCache struct {
	dir string
	ttl time.Duration
	now func() time.Time
}

func newFileCache(dir string, ttl time.Duration) (*fileCache, error) {
	if dir == "" {
		return &fileCache{ttl: 0}, nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create greenbook cache directory: %w", err)
	}
	return &fileCache{dir: dir, ttl: ttl, now: time.Now}, nil
}

func (c *fileCache) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(c.dir, hex.EncodeToString(sum[:])+".cache")
}

func (c *fileCache) get(key string) ([]byte, bool) {
	if c == nil || c.dir == "" || c.ttl <= 0 {
		return nil, false
	}
	path := c.path(key)
	info, err := os.Stat(path)
	if err != nil || c.now().Sub(info.ModTime()) > c.ttl {
		return nil, false
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return body, true
}

func (c *fileCache) put(key string, body []byte) error {
	if c == nil || c.dir == "" || c.ttl <= 0 {
		return nil
	}
	path := c.path(key)
	tmp, err := os.CreateTemp(c.dir, "page-*.tmp")
	if err != nil {
		return fmt.Errorf("create cache file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write cache file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close cache file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace cache file: %w", err)
	}
	return nil
}
