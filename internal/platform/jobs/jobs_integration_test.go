//go:build integration

package jobs_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/config"
	"github.com/benfaruna/medverify/internal/platform/jobs"
	"github.com/benfaruna/medverify/internal/platform/logging"
	"github.com/benfaruna/medverify/internal/platform/testkit"
	"github.com/benfaruna/medverify/migrations"
)

func TestNoopJobEnqueuedByAPIWindowIsExecutedByWorker(t *testing.T) {
	databaseURL := testkit.Postgres(t)
	ctx := context.Background()

	require.NoError(t, migrations.Up(ctx, databaseURL))
	require.NoError(t, jobs.Migrate(ctx, databaseURL, jobs.DirectionUp))
	assert.True(t, riverTableExists(t, databaseURL), "river schema should be applied")

	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	defer pool.Close()

	logger, _ := logging.New(logging.Options{Level: 8, Secrets: nil}) // error level: keep test output quiet

	workerClient, err := jobs.NewClient(pool, logger, jobs.NewWorkers(logger), config.Jobs{Concurrency: 2})
	require.NoError(t, err)

	completed, cancel := workerClient.Subscribe(river.EventKindJobCompleted)
	defer cancel()

	require.NoError(t, workerClient.Start(ctx))
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		_ = workerClient.Stop(stopCtx)
	}()

	// The API enqueues through its own client. In phase 1 the admin command is
	// that client; later phases enqueue ingestion work from HTTP handlers.
	producerClient, err := jobs.NewClient(pool, logger, jobs.NewWorkers(logger), config.Jobs{Concurrency: 1})
	require.NoError(t, err)

	jobID, err := jobs.EnqueueNoop(ctx, producerClient, "phase 1 wiring check")
	require.NoError(t, err)
	require.Positive(t, jobID)

	select {
	case event := <-completed:
		require.NotNil(t, event.Job)
		assert.Equal(t, jobID, event.Job.ID)
	case <-time.After(30 * time.Second):
		t.Fatal("no-op job was not executed by the worker")
	}

	assert.Equal(t, "completed", jobState(t, databaseURL, jobID))
}

func TestMigrateDownRemovesRiverSchema(t *testing.T) {
	databaseURL := testkit.Postgres(t)
	ctx := context.Background()

	require.NoError(t, jobs.Migrate(ctx, databaseURL, jobs.DirectionUp))
	require.True(t, riverTableExists(t, databaseURL))

	require.NoError(t, jobs.Migrate(ctx, databaseURL, jobs.DirectionDown))
	assert.False(t, riverTableExists(t, databaseURL))
}

func riverTableExists(t *testing.T, databaseURL string) bool {
	t.Helper()

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	defer pool.Close()

	var exists bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_regclass('river_job') IS NOT NULL`).Scan(&exists))
	return exists
}

func jobState(t *testing.T, databaseURL string, jobID int64) string {
	t.Helper()

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	defer pool.Close()

	var state string
	require.NoError(t, pool.QueryRow(ctx, `SELECT state FROM river_job WHERE id = $1`, jobID).Scan(&state))
	return state
}
