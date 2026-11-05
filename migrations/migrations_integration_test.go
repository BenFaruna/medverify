//go:build integration

package migrations_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/testkit"
	"github.com/benfaruna/medverify/migrations"
)

func TestMigrationsUpDownUpOnEmptyDatabase(t *testing.T) {
	databaseURL := testkit.Postgres(t)
	ctx := context.Background()

	require.NoError(t, migrations.Up(ctx, databaseURL), "first up")
	assertExtensions(t, databaseURL, "pg_trgm", "vector", "pgcrypto")
	assert.True(t, tableExists(t, databaseURL, "goose_db_version"), "goose version table should exist")

	require.NoError(t, migrations.DownToZero(ctx, databaseURL), "down to zero")
	remaining := extensions(t, databaseURL)
	for _, name := range []string{"pg_trgm", "vector", "pgcrypto"} {
		assert.NotContains(t, remaining, name, "down migration should drop %s", name)
	}

	require.NoError(t, migrations.Up(ctx, databaseURL), "second up")
	assertExtensions(t, databaseURL, "pg_trgm", "vector", "pgcrypto")
}

func TestMigrationsStatusReportsApplied(t *testing.T) {
	databaseURL := testkit.Postgres(t)
	ctx := context.Background()

	require.NoError(t, migrations.Up(ctx, databaseURL))

	statuses, err := migrations.Status(ctx, databaseURL)
	require.NoError(t, err)

	// Derive the expected count from the embedded migration files so adding a
	// migration does not silently rot this assertion.
	files, err := filepath.Glob("*.sql")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	require.Len(t, statuses, len(files))
	for i, status := range statuses {
		assert.Equal(t, "applied", string(status.State))
		assert.Equal(t, int64(i+1), status.Source.Version)
	}
}

func TestScanTablesExistAfterMigrateUp(t *testing.T) {
	databaseURL := testkit.Postgres(t)
	ctx := context.Background()

	require.NoError(t, migrations.Up(ctx, databaseURL))

	for _, table := range []string{
		"api_clients", "scans", "scan_images", "scan_results", "idempotency_keys", "llm_calls",
	} {
		assert.True(t, tableExists(t, databaseURL, table), "table %s should exist", table)
	}

	// Phase 05 stores registry verification on the result row.
	assert.True(t, columnExists(t, databaseURL, "scan_results", "features"),
		"scan_results.features should exist after migration 00005")

	// Phase 07 replaces the placeholder with typed verdict columns and the
	// signals the verdict was computed from.
	for _, column := range []string{
		"verdict_code", "verdict_score", "verdict_confidence",
		"verdict_rules_version", "signals",
	} {
		assert.True(t, columnExists(t, databaseURL, "scan_results", column),
			"scan_results.%s should exist after migration 00007", column)
	}
	assert.False(t, columnExists(t, databaseURL, "scan_results", "verdict"),
		"scan_results.verdict placeholder should be dropped by migration 00007")
}

func TestRegistryTablesExistAfterMigrateUp(t *testing.T) {
	databaseURL := testkit.Postgres(t)
	ctx := context.Background()

	require.NoError(t, migrations.Up(ctx, databaseURL))

	for _, table := range []string{
		"products", "product_ingredients", "sync_runs", "smpc_documents", "smpc_sections",
		"smpc_chunks", "watchlist_sources", "watchlist_entries", "safety_flags",
	} {
		assert.True(t, tableExists(t, databaseURL, table), "table %s should exist", table)
	}
}

func extensions(t *testing.T, databaseURL string) []string {
	t.Helper()

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	defer pool.Close()

	rows, err := pool.Query(ctx, `SELECT extname FROM pg_extension ORDER BY extname`)
	require.NoError(t, err)
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		names = append(names, name)
	}
	require.NoError(t, rows.Err())
	return names
}

func assertExtensions(t *testing.T, databaseURL string, want ...string) {
	t.Helper()

	names := extensions(t, databaseURL)
	for _, name := range want {
		assert.Contains(t, names, name)
	}
}

func columnExists(t *testing.T, databaseURL, table, column string) bool {
	t.Helper()

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	defer pool.Close()

	var exists bool
	err = pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = $1 AND column_name = $2
		)`, table, column).Scan(&exists)
	require.NoError(t, err)
	return exists
}

func tableExists(t *testing.T, databaseURL, table string) bool {
	t.Helper()

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	defer pool.Close()

	var exists bool
	err = pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists)
	require.NoError(t, err)
	return exists
}
