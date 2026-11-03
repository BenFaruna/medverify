package jobs_test

import (
	"context"
	"errors"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/config"
	"github.com/benfaruna/medverify/internal/platform/jobs"
	"github.com/benfaruna/medverify/internal/rag"
)

type fakeRAGRunner struct {
	summary rag.IndexSummary
	err     error
	limits  []int
}

func (f *fakeRAGRunner) IndexLeaflets(_ context.Context, limit int) (rag.IndexSummary, error) {
	f.limits = append(f.limits, limit)
	return f.summary, f.err
}

func TestRAGIndexWorkerRequiresRunner(t *testing.T) {
	worker := &jobs.RAGIndexWorker{}
	err := worker.Work(context.Background(), nil)
	require.Error(t, err)
}

func TestRAGIndexWorkerPassesLimitAndReturnsErrors(t *testing.T) {
	runner := &fakeRAGRunner{summary: rag.IndexSummary{Documents: 2, Chunks: 7}}
	worker := &jobs.RAGIndexWorker{Runner: runner}

	job := &river.Job[jobs.RAGIndexArgs]{
		JobRow: &rivertype.JobRow{ID: 1},
		Args:   jobs.RAGIndexArgs{Trigger: "manual", Limit: 25},
	}
	require.NoError(t, worker.Work(context.Background(), job))
	assert.Equal(t, []int{25}, runner.limits)

	runner.err = errors.New("provider down")
	require.Error(t, worker.Work(context.Background(), job))
}

func TestRAGIndexPeriodicJobIsOptIn(t *testing.T) {
	assert.Empty(t, jobs.RAGIndexPeriodicJob(config.Embeddings{}))
	assert.Len(t, jobs.RAGIndexPeriodicJob(config.Embeddings{IndexInterval: 1}), 1)
}
