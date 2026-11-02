//go:build integration

package llm_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/llm"
	"github.com/benfaruna/medverify/internal/platform/testkit"
	"github.com/benfaruna/medverify/internal/vision"
	"github.com/benfaruna/medverify/migrations"
)

func TestRecorderWritesEveryCallOutcome(t *testing.T) {
	databaseURL := testkit.Postgres(t)
	ctx := context.Background()
	require.NoError(t, migrations.Up(ctx, databaseURL))

	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	defer pool.Close()

	recorder := llm.NewRecorder(pool)
	for _, outcome := range []string{"success", "unreadable", "error"} {
		err := recorder.Record(ctx, vision.CallRecord{
			Provider:      "claude",
			Model:         "claude-sonnet-4-5-20250929",
			PromptVersion: "v1",
			Kind:          "vision",
			Latency:       1234 * time.Millisecond,
			Usage:         vision.Usage{InputTokens: 1000, OutputTokens: 200, CostUSD: 0.006},
			Outcome:       outcome,
			ErrorCode:     "",
		})
		require.NoError(t, err, outcome)
	}

	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM llm_calls`).Scan(&count))
	assert.Equal(t, 3, count)

	var latency int
	var cost float64
	var inputUnits int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT latency_ms, cost_usd::float8, input_units
		FROM llm_calls WHERE outcome = 'success'
	`).Scan(&latency, &cost, &inputUnits))
	assert.Equal(t, 1234, latency)
	assert.InDelta(t, 0.006, cost, 0.000001)
	assert.Equal(t, 1000, inputUnits)

	// No prompt or image text can leak into the ledger.
	var hasText bool
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = 'llm_calls'
			  AND column_name IN ('prompt', 'response', 'image', 'text')
		)
	`).Scan(&hasText))
	assert.False(t, hasText)
}
