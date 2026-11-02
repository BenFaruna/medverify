//go:build integration

// Package testkit starts throwaway services for integration tests. Every test
// package gets its own Postgres and object store, so tests never share state.
//
// Run with: go test -tags=integration ./...
//
// Containers publish on a pinned host port chosen below the ephemeral range.
// Random ports are not used because rootless Docker cannot forward them, and a
// predictable port makes a failed run easy to reproduce.
package testkit

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/benfaruna/medverify/internal/storage"
)

// PostgresImage must match docker-compose.yml and CI: the vector extension is
// only present in the pgvector image.
const PostgresImage = "pgvector/pgvector:pg16"

// StorageImage is the S3-compatible object store used locally. MinIO images are
// no longer publicly pullable, so the stack uses SeaweedFS's S3 gateway.
const StorageImage = "chrislusf/seaweedfs:latest"

const (
	// StorageAccessKey is a throwaway test credential. The local S3 gateway
	// accepts any credential pair; production uses real ones.
	StorageAccessKey = "medverify-test"
	// StorageSecretKey is a throwaway test credential.
	StorageSecretKey = "medverify-test-secret"
	// StorageBucket is the bucket every test package creates.
	StorageBucket = "medverify"
)

// Postgres starts a pgvector-enabled Postgres and returns its connection URL.
func Postgres(t testing.TB) string {
	t.Helper()

	hostPort := pinnedHostPort(t)
	pgPort := "5432/tcp"

	req := testcontainers.ContainerRequest{
		Image:        PostgresImage,
		ExposedPorts: []string{pgPort},
		Env: map[string]string{
			"POSTGRES_USER":     "medverify",
			"POSTGRES_PASSWORD": "medverify",
			"POSTGRES_DB":       "medverify",
		},
		WaitingFor: wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).
			WithStartupTimeout(2 * time.Minute),
		HostConfigModifier: pinnedBinding(pgPort, hostPort),
	}

	start(t, req)
	return fmt.Sprintf("postgres://medverify:medverify@127.0.0.1:%d/medverify?sslmode=disable", hostPort)
}

// ObjectStorage starts an S3-compatible store, creates the bucket, and returns
// a ready store plus its host endpoint.
func ObjectStorage(t testing.TB) (*storage.S3, string) {
	t.Helper()

	hostPort := pinnedHostPort(t)
	apiPort := "8333/tcp"

	req := testcontainers.ContainerRequest{
		Image:              StorageImage,
		ExposedPorts:       []string{apiPort},
		Cmd:                []string{"server", "-s3", "-s3.port=8333", "-dir=/data"},
		WaitingFor:         wait.ForHTTP("/").WithPort(apiPort).WithStartupTimeout(2 * time.Minute),
		HostConfigModifier: pinnedBinding(apiPort, hostPort),
	}

	start(t, req)

	endpoint := fmt.Sprintf("127.0.0.1:%d", hostPort)
	store, err := storage.NewS3(storage.Options{
		Endpoint:  endpoint,
		AccessKey: StorageAccessKey,
		SecretKey: StorageSecretKey,
		Bucket:    StorageBucket,
		Region:    "us-east-1",
	})
	require.NoError(t, err)

	// The S3 gateway accepts connections slightly before it can create buckets.
	ensureBucket(t, store)
	return store, endpoint
}

// StoreWithBucket builds a second client on an existing endpoint, optionally
// pointing at a bucket that does not exist. Used to test readiness failures.
func StoreWithBucket(t testing.TB, endpoint, bucket string) *storage.S3 {
	t.Helper()

	store, err := storage.NewS3(storage.Options{
		Endpoint:  endpoint,
		AccessKey: StorageAccessKey,
		SecretKey: StorageSecretKey,
		Bucket:    bucket,
		Region:    "us-east-1",
	})
	require.NoError(t, err)
	return store
}

func ensureBucket(t testing.TB, store *storage.S3) {
	t.Helper()

	deadline := time.Now().Add(time.Minute)
	var lastErr error
	for time.Now().Before(deadline) {
		ensureCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		lastErr = store.EnsureBucket(ensureCtx)
		cancel()
		if lastErr == nil {
			return
		}
		time.Sleep(time.Second)
	}
	require.NoError(t, lastErr, "create test bucket")
}

func start(t testing.TB, req testcontainers.ContainerRequest) testcontainers.Container {
	t.Helper()

	container, err := testcontainers.GenericContainer(context.Background(), testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err, "start container for image %s", req.Image)

	t.Cleanup(func() {
		_ = container.Terminate(context.Background())
	})
	return container
}

// pinnedHostPort returns a free TCP port from a range below the ephemeral port
// range that Docker allocates from.
func pinnedHostPort(t testing.TB) int {
	t.Helper()

	const low, high = 24000, 32000
	//nolint:gosec // test-only port selection, not a security decision
	start := low + rand.Intn(high-low)

	for i := 0; i < high-low; i++ {
		port := low + (start-low+i)%(high-low)
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		_ = listener.Close()
		return port
	}

	t.Fatalf("no free host port in range %d-%d", low, high)
	return 0
}

func pinnedBinding(containerPort string, hostPort int) func(*container.HostConfig) {
	return func(hostConfig *container.HostConfig) {
		hostConfig.PortBindings = network.PortMap{
			network.MustParsePort(containerPort): {
				{
					HostIP:   netip.MustParseAddr("127.0.0.1"),
					HostPort: strconv.Itoa(hostPort),
				},
			},
		}
	}
}
