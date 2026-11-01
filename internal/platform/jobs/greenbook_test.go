package jobs_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/config"
	"github.com/benfaruna/medverify/internal/platform/jobs"
	"github.com/benfaruna/medverify/internal/registry"
)

type fakeRunner struct {
	summary registry.SyncSummary
	err     error
	calls   []registry.SyncOptions
}

func (f *fakeRunner) Sync(_ context.Context, opts registry.SyncOptions) (registry.SyncSummary, error) {
	f.calls = append(f.calls, opts)
	return f.summary, f.err
}

func TestGreenbookPeriodicJobHonoursKillSwitchAndInterval(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		assert.Empty(t, jobs.GreenbookPeriodicJob(config.Greenbook{
			Enabled:      false,
			SyncInterval: time.Hour,
		}))
	})

	t.Run("no interval", func(t *testing.T) {
		assert.Empty(t, jobs.GreenbookPeriodicJob(config.Greenbook{
			Enabled:      true,
			SyncInterval: 0,
		}))
	})

	t.Run("enabled", func(t *testing.T) {
		periodic := jobs.GreenbookPeriodicJob(config.Greenbook{
			Enabled:      true,
			SyncInterval: 24 * time.Hour,
		})
		require.Len(t, periodic, 1)
	})
}

func TestGreenbookWorkerRequiresRunner(t *testing.T) {
	worker := &jobs.GreenbookSyncWorker{}
	err := worker.Work(context.Background(), nil)
	require.Error(t, err)
}

func TestGreenbookWorkerReturnsSyncError(t *testing.T) {
	want := errors.New("source unavailable")
	runner := &fakeRunner{err: want}
	worker := &jobs.GreenbookSyncWorker{Runner: runner}

	job := &river.Job[jobs.GreenbookSyncArgs]{
		JobRow: &rivertype.JobRow{ID: 1},
		Args:   jobs.GreenbookSyncArgs{Trigger: "nightly"},
	}
	err := worker.Work(context.Background(), job)
	require.ErrorIs(t, err, want)
	require.Len(t, runner.calls, 1)
	assert.Equal(t, "nightly", runner.calls[0].Trigger)
}
