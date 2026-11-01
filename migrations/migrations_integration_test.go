//go:build integration

package migrations_test

import (
	"context"
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
	require.Len(t, statuses, 1)
	assert.Equal(t, "applied", string(statuses[0].State))
	assert.Equal(t, int64(1), statuses[0].Source.Version)
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
