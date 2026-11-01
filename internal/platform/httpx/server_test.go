package httpx_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/httpx"
)

func TestServeCompletesInFlightRequestOnShutdown(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		// Simulate slow work that has already begun when shutdown is requested.
		select {
		case <-time.After(150 * time.Millisecond):
		case <-r.Context().Done():
			// A graceful shutdown must not cancel in-flight request contexts.
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("done"))
	})

	server := httpx.NewServer(httpx.ServerOptions{
		Addr:            "127.0.0.1:0",
		Handler:         handler,
		ReadTimeout:     5 * time.Second,
		WriteTimeout:    5 * time.Second,
		IdleTimeout:     5 * time.Second,
		ShutdownTimeout: 5 * time.Second,
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(ctx, listener) }()

	type result struct {
		status int
		body   string
		err    error
	}

	url := "http://" + listener.Addr().String() + "/slow"
	resultCh := make(chan result, 1)
	errCh := make(chan error, 1)
	go func() {
		response, err := http.Get(url)
		if err != nil {
			errCh <- err
			return
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		resultCh <- result{status: response.StatusCode, body: string(body), err: err}
	}()

	select {
	case <-started:
	case err := <-errCh:
		t.Fatalf("request failed before starting: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("request never reached the handler")
	}

	cancel()

	select {
	case got := <-resultCh:
		require.NoError(t, got.err)
		assert.Equal(t, http.StatusOK, got.status)
		assert.Equal(t, "done", got.body)
	case err := <-errCh:
		t.Fatalf("in-flight request failed during shutdown: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight request did not complete")
	}

	select {
	case err := <-serveDone:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down")
	}
}

func TestServeStopsAcceptingNewConnectionsAfterShutdown(t *testing.T) {
	t.Parallel()

	server := httpx.NewServer(httpx.ServerOptions{
		Handler:         http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		ShutdownTimeout: 2 * time.Second,
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down")
	}

	_, err = net.DialTimeout("tcp", listener.Addr().String(), 200*time.Millisecond)
	assert.Error(t, err, "listener should be closed after shutdown")
}
