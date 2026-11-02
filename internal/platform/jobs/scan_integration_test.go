//go:build integration

package jobs_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/config"
	"github.com/benfaruna/medverify/internal/platform/jobs"
	"github.com/benfaruna/medverify/internal/platform/testkit"
	"github.com/benfaruna/medverify/internal/scan"
	"github.com/benfaruna/medverify/internal/storage"
	"github.com/benfaruna/medverify/internal/vision"
	"github.com/benfaruna/medverify/migrations"
)

func TestScanUploadReachesCompletedThroughRiver(t *testing.T) {
	databaseURL := testkit.Postgres(t)
	ctx := context.Background()
	require.NoError(t, migrations.Up(ctx, databaseURL))
	require.NoError(t, jobs.Migrate(ctx, databaseURL, jobs.DirectionUp))

	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	defer pool.Close()

	logger := slog.New(slog.DiscardHandler)
	objects := &storage.Fake{BucketExists: true, Objects: map[string][]byte{}}
	provider := vision.NewFake()
	store := scan.NewStore(pool)

	// The worker process owns the scans client and runs the extraction.
	workerService, err := scan.NewService(scan.ServiceOptions{
		Store:         store,
		Objects:       objects,
		Provider:      provider,
		Logger:        logger,
		MaxImages:     4,
		Limits:        scan.ImageLimits{MaxBytes: 10 << 20, MaxPixels: 40_000_000},
		MinConfidence: 0.5,
		PollInterval:  25 * time.Millisecond,
	})
	require.NoError(t, err)

	workerClient, err := jobs.NewClient(pool, logger, jobs.NewWorkers(logger, jobs.WithScanRunner(workerService)), config.Jobs{Concurrency: 2})
	require.NoError(t, err)
	require.NoError(t, workerClient.Start(ctx))
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = workerClient.Stop(stopCtx)
	}()

	// The API process only enqueues. An insert-only client needs no workers and
	// is not started.
	producerClient, err := jobs.NewClient(pool, logger, nil, config.Jobs{Concurrency: 1})
	require.NoError(t, err)
	apiService, err := scan.NewService(scan.ServiceOptions{
		Store:         store,
		Objects:       objects,
		Provider:      provider,
		Enqueuer:      jobs.NewScanEnqueuer(producerClient),
		Logger:        logger,
		MaxImages:     4,
		Limits:        scan.ImageLimits{MaxBytes: 10 << 20, MaxPixels: 40_000_000},
		MinConfidence: 0.5,
		PollInterval:  25 * time.Millisecond,
	})
	require.NoError(t, err)

	image, err := os.ReadFile("../../../testdata/images/normal.jpg")
	require.NoError(t, err)

	created, isNew, err := apiService.Create(ctx, scan.CreateRequest{
		ClientKey:   "default",
		RequestHash: "river-hash",
		Images:      [][]byte{image},
	})
	require.NoError(t, err)
	require.True(t, isNew)

	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	final, terminal, err := apiService.Wait(waitCtx, created.ID)
	require.NoError(t, err)
	require.True(t, terminal, "the worker should finish the scan before the deadline")
	assert.Equal(t, scan.StatusCompleted, final.Status)
	assert.Equal(t, 1, provider.CallsCount())

	var jobState string
	require.Eventually(t, func() bool {
		if err := pool.QueryRow(ctx, `SELECT state FROM river_job WHERE kind = $1`, jobs.KindProcessScan).Scan(&jobState); err != nil {
			return false
		}
		return jobState == "completed"
	}, 5*time.Second, 50*time.Millisecond, "River should mark the extraction job completed")
	assert.Equal(t, "completed", jobState)
}
