//go:build integration

package scan_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/scan"
	"github.com/benfaruna/medverify/internal/verdict"
)

// assertNoProviderCalls proves a sandbox scan never spends money on a provider.
func assertNoProviderCalls(t *testing.T, pipeline *testPipeline) {
	t.Helper()
	require.Equal(t, 0, pipeline.provider.CallsCount(),
		"a sandbox scan must never call the vision provider")
}

func TestSandboxScanReturnsAFixedResultWithoutAProviderCall(t *testing.T) {
	pipeline := newPipeline(t)

	created, isNew, err := pipeline.service.Create(context.Background(), scan.CreateRequest{
		ClientKey:   "client:1",
		Sandbox:     true,
		RequestHash: "sandbox-hash-1",
		Images:      [][]byte{readImage(t, "normal.jpg")},
	})
	require.NoError(t, err)
	require.True(t, isNew)
	assert.True(t, created.Sandbox)

	require.NoError(t, pipeline.service.Process(context.Background(), created.ID.String()))
	assertNoProviderCalls(t, pipeline)

	finished, err := pipeline.service.Get(context.Background(), created.ID)
	require.NoError(t, err)
	assert.Equal(t, scan.StatusCompleted, finished.Status)
	require.NotNil(t, finished.Result)
	assert.Equal(t, "sandbox", finished.Result.Provider, "the stored provider names the sandbox, not a model")
	require.NotNil(t, finished.Result.Verdict)
	assert.Equal(t, verdict.NeedsReview, finished.Result.Verdict.Verdict)
	assert.Equal(t, scan.SandboxConfidence, finished.Result.Verdict.Confidence)
	assert.Equal(t, "sandbox", finished.Result.Verdict.RulesVersion)

	// The public view labels the sandbox and still carries no score.
	view := finished.View(testReasonCatalog(t))
	assert.True(t, view.Sandbox)
	require.NotNil(t, view.Verdict)
	assert.Equal(t, string(verdict.NeedsReview), view.Verdict.Value)
	require.Len(t, view.Reasons, 1)
	assert.Equal(t, scan.SandboxReason, view.Reasons[0].Code)
	assert.NotEmpty(t, view.Reasons[0].Text)
	assert.Equal(t, scan.Disclaimer, view.Disclaimer)
}

// Running the sandbox job twice must be a no-op, like any other terminal write.
func TestSandboxScanIsIdempotent(t *testing.T) {
	pipeline := newPipeline(t)

	created, _, err := pipeline.service.Create(context.Background(), scan.CreateRequest{
		ClientKey:   "client:1",
		Sandbox:     true,
		RequestHash: "sandbox-hash-2",
		Images:      [][]byte{readImage(t, "normal.jpg")},
	})
	require.NoError(t, err)

	require.NoError(t, pipeline.service.Process(context.Background(), created.ID.String()))
	require.NoError(t, pipeline.service.Process(context.Background(), created.ID.String()))
	assertNoProviderCalls(t, pipeline)

	finished, err := pipeline.service.Get(context.Background(), created.ID)
	require.NoError(t, err)
	assert.Equal(t, scan.StatusCompleted, finished.Status)
}
