package registry_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/config"
	"github.com/benfaruna/medverify/internal/registry"
)

func TestFetchProductsBuildsPoliteRequest(t *testing.T) {
	t.Parallel()

	var seen *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Clone(context.Background())
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"draw":1,"recordsTotal":0,"recordsFiltered":0,"data":[]}`)
	}))
	t.Cleanup(server.Close)

	fetcher := newTestFetcher(t, server.URL, nil)
	body, err := fetcher.FetchProducts(context.Background(), registry.PageRequest{
		Start:      100,
		Length:     25,
		CategoryID: "1",
		Status:     "Active",
		Search:     "paracetamol",
		Ingredient: "paracetamol",
	})
	require.NoError(t, err)
	assert.Contains(t, string(body), `"recordsTotal":0`)

	require.NotNil(t, seen)
	query := seen.URL.Query()
	assert.Equal(t, "100", query.Get("start"))
	assert.Equal(t, "25", query.Get("length"))
	assert.Equal(t, "1", query.Get("columns[3][search][value]"))
	assert.Equal(t, "Active", query.Get("columns[11][search][value]"))
	assert.Equal(t, "paracetamol", query.Get("search_ingredient"))
	assert.Equal(t, "paracetamol", query.Get("search[value]"))
	assert.Equal(t, "product_name", query.Get("columns[0][data]"))
	assert.Contains(t, seen.Header.Get("User-Agent"), "MedVerifyNG")
	assert.Equal(t, "XMLHttpRequest", seen.Header.Get("X-Requested-With"))
}

func TestFetchProductsRetriesFiveHundredThreeThenSucceeds(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			http.Error(w, "try later", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, `{"recordsTotal":1,"data":[]}`)
	}))
	t.Cleanup(server.Close)

	fetcher := newTestFetcher(t, server.URL, func(cfg *config.Greenbook) {
		cfg.Retries = 3
	}, registry.WithBackoffBase(time.Millisecond))

	_, err := fetcher.FetchProducts(context.Background(), registry.PageRequest{Length: 1})
	require.NoError(t, err)
	assert.Equal(t, int32(3), calls.Load())
}

func TestFetchProductsDoesNotRetryFourHundredFour(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.NotFound(w, nil)
	}))
	t.Cleanup(server.Close)

	fetcher := newTestFetcher(t, server.URL, func(cfg *config.Greenbook) {
		cfg.Retries = 5
	}, registry.WithBackoffBase(time.Millisecond))

	_, err := fetcher.FetchProducts(context.Background(), registry.PageRequest{Length: 1})
	require.Error(t, err)
	var httpErr *registry.HTTPError
	require.True(t, errors.As(err, &httpErr))
	assert.Equal(t, http.StatusNotFound, httpErr.StatusCode)
	assert.Equal(t, int32(1), calls.Load())
}

func TestFetchProductsHonoursKillSwitch(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(server.Close)

	fetcher := newTestFetcher(t, server.URL, func(cfg *config.Greenbook) {
		cfg.Enabled = false
	})

	_, err := fetcher.FetchProducts(context.Background(), registry.PageRequest{Length: 1})
	require.ErrorIs(t, err, registry.ErrDisabled)
	assert.Equal(t, int32(0), calls.Load(), "kill switch must stop outbound calls")
}

func TestFetchProductsCachesResponses(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"recordsTotal":1,"data":[]}`)
	}))
	t.Cleanup(server.Close)

	fetcher := newTestFetcher(t, server.URL, nil)
	first, err := fetcher.FetchProducts(context.Background(), registry.PageRequest{Length: 1})
	require.NoError(t, err)
	second, err := fetcher.FetchProducts(context.Background(), registry.PageRequest{Length: 1})
	require.NoError(t, err)

	assert.Equal(t, first, second)
	assert.Equal(t, int32(1), calls.Load(), "second call should be served from cache")
}

func TestFetchProductsObeysRateLimit(t *testing.T) {
	t.Parallel()

	var (
		calls atomic.Int32
		last  atomic.Int64
		gap   atomic.Int64
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		now := time.Now().UnixNano()
		if previous := last.Swap(now); previous != 0 {
			gap.Store(now - previous)
		}
		calls.Add(1)
		_, _ = io.WriteString(w, `{"recordsTotal":1,"data":[]}`)
	}))
	t.Cleanup(server.Close)

	fetcher := newTestFetcher(t, server.URL, func(cfg *config.Greenbook) {
		cfg.RequestsPerSecond = 10
		cfg.CacheDir = ""
		cfg.CacheTTL = 0
	})

	var start time.Time
	_, err := fetcher.FetchProducts(context.Background(), registry.PageRequest{Length: 1})
	require.NoError(t, err)
	start = time.Now()
	_, err = fetcher.FetchProducts(context.Background(), registry.PageRequest{Length: 2})
	require.NoError(t, err)

	assert.Equal(t, int32(2), calls.Load())
	assert.GreaterOrEqual(t, gap.Load(), int64(60*time.Millisecond), "requests should be at least ~100ms apart")
	assert.GreaterOrEqual(t, time.Since(start), 60*time.Millisecond)
}

func TestFetchLeafletRejectsDisallowedHostBeforeDialling(t *testing.T) {
	t.Parallel()

	fetcher := newTestFetcher(t, "https://greenbook.example", nil)
	leaflet, err := fetcher.FetchLeaflet(context.Background(), "https://evil.example/file.pdf")
	require.ErrorIs(t, err, registry.ErrDisallowedHost)
	assert.Nil(t, leaflet)
}

func TestFetchLeafletRejectsKnownOversizeBeforeReading(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(strings.Repeat("x", 1000)))
	}))
	t.Cleanup(server.Close)

	fetcher := newTestFetcher(t, server.URL, func(cfg *config.Greenbook) {
		cfg.MaxPDFBytes = 10
	})

	leaflet, err := fetcher.FetchLeaflet(context.Background(), server.URL+"/leaflet.pdf")
	require.ErrorIs(t, err, registry.ErrTooLarge)
	assert.Nil(t, leaflet)
}

func TestFetchLeafletCapsUnknownLengthStreams(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Flush the headers without Content-Length, then stream more than the cap.
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = w.Write([]byte(strings.Repeat("x", 64)))
	}))
	t.Cleanup(server.Close)

	fetcher := newTestFetcher(t, server.URL, func(cfg *config.Greenbook) {
		cfg.MaxPDFBytes = 10
	})

	leaflet, err := fetcher.FetchLeaflet(context.Background(), server.URL+"/leaflet.pdf")
	require.NoError(t, err)
	require.NotNil(t, leaflet)
	defer func() { _ = leaflet.Body.Close() }()

	_, err = io.ReadAll(leaflet.Body)
	require.ErrorIs(t, err, registry.ErrTooLarge)
}

func TestFetchLeafletRejectsRedirectOutsideAllowlist(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/leaflet.pdf" {
			_, port, err := net.SplitHostPort(r.Host)
			require.NoError(t, err)
			http.Redirect(w, r, "http://localhost:"+port+"/other.pdf", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "pdf")
	}))
	t.Cleanup(server.Close)

	fetcher := newTestFetcher(t, server.URL, nil)
	leaflet, err := fetcher.FetchLeaflet(context.Background(), server.URL+"/leaflet.pdf")
	require.ErrorIs(t, err, registry.ErrDisallowedHost)
	assert.Nil(t, leaflet)
}

func newTestFetcher(t *testing.T, baseURL string, mutate func(*config.Greenbook), opts ...registry.FetcherOption) *registry.Fetcher {
	t.Helper()

	parsed, err := url.Parse(baseURL)
	require.NoError(t, err)
	host := parsed.Hostname()

	cfg := config.Greenbook{
		Enabled:           true,
		BaseURL:           baseURL,
		UserAgent:         "MedVerifyNG/test",
		RequestsPerSecond: 1000,
		MaxConcurrency:    1,
		PageSize:          25,
		RequestTimeout:    5 * time.Second,
		Retries:           0,
		CacheDir:          t.TempDir(),
		CacheTTL:          time.Hour,
		MaxPDFBytes:       1 << 20,
		MaxPDFPages:       100,
		AllowedHosts:      []string{host},
	}
	if mutate != nil {
		mutate(&cfg)
	}

	fetcher, err := registry.NewFetcher(cfg, nil, opts...)
	require.NoError(t, err)
	return fetcher
}
