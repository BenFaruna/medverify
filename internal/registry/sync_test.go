package registry

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsDuplicateKey(t *testing.T) {
	duplicate := fmt.Errorf("upsert product: %w", &pgconn.PgError{Code: "23505", ConstraintName: "products_nrn_key"})
	assert.True(t, isDuplicateKey(duplicate))
	assert.False(t, isDuplicateKey(errors.New("connection reset")))
	assert.False(t, isDuplicateKey(&pgconn.PgError{Code: "23503"}))
}

func TestRowFailureSummary(t *testing.T) {
	assert.Empty(t, rowFailureSummary(nil))

	failures := make([]RowFailure, 0, 7)
	for i := range 7 {
		failures = append(failures, RowFailure{
			SourceID: fmt.Sprintf("p%d", i),
			NRN:      "04-5555",
			Name:     "Example",
			Err:      "duplicate key value violates unique constraint",
		})
	}
	summary := rowFailureSummary(failures)
	assert.Contains(t, summary, "7 product(s) skipped")
	assert.Contains(t, summary, "source_id=p0")
	assert.Contains(t, summary, "and 2 more")
	require.LessOrEqual(t, len(summary), 2000)
}
