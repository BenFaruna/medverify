package jobs_test

import (
	"context"
	"errors"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/jobs"
	"github.com/benfaruna/medverify/internal/registry"
)

type fakeLeafletRunner struct {
	productIDs []int64
	summary    registry.LeafletSummary
	err        error
}

func (f *fakeLeafletRunner) RunProduct(_ context.Context, productID int64) (registry.LeafletSummary, error) {
	f.productIDs = append(f.productIDs, productID)
	return f.summary, f.err
}

func TestLeafletFetchWorkerRequiresRunner(t *testing.T) {
	err := (&jobs.LeafletFetchWorker{}).Work(context.Background(), nil)
	require.Error(t, err)
}

// One question downloads one product. The indexing pass is queued from here, and
// with no River client in the context (as in this test) that step is skipped
// rather than panicking.
func TestLeafletFetchWorkerFetchesOnlyItsProduct(t *testing.T) {
	runner := &fakeLeafletRunner{summary: registry.LeafletSummary{Downloaded: 1, OK: 1, Sections: 4}}
	worker := &jobs.LeafletFetchWorker{Runner: runner}
	job := &river.Job[jobs.LeafletFetchArgs]{
		JobRow: &rivertype.JobRow{ID: 7},
		Args:   jobs.LeafletFetchArgs{ProductID: 42, Trigger: "question"},
	}

	require.NoError(t, worker.Work(context.Background(), job))
	assert.Equal(t, []int64{42}, runner.productIDs)
}

// A failed download is returned so River retries it; a silent success would
// leave the person waiting for a leaflet that was never fetched.
func TestLeafletFetchWorkerReturnsFetchErrors(t *testing.T) {
	runner := &fakeLeafletRunner{err: errors.New("download failed")}
	worker := &jobs.LeafletFetchWorker{Runner: runner}
	job := &river.Job[jobs.LeafletFetchArgs]{
		JobRow: &rivertype.JobRow{ID: 8},
		Args:   jobs.LeafletFetchArgs{ProductID: 42},
	}

	require.Error(t, worker.Work(context.Background(), job))
}

func TestLeafletEnqueuerRequiresAClient(t *testing.T) {
	err := (&jobs.LeafletEnqueuer{}).EnsureLeaflet(context.Background(), 42)
	require.Error(t, err)
}
