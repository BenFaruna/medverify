// Package migrations embeds the goose migration files and runs them.
//
// Every migration has an up and a down section. Files are never edited after
// they are merged; a change is a new migration.
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver used by goose
	"github.com/pressly/goose/v3"
)

//go:embed *.sql
var files embed.FS

const dialect = goose.DialectPostgres

// Up applies all pending migrations.
func Up(ctx context.Context, databaseURL string) error {
	return withProvider(ctx, databaseURL, func(ctx context.Context, provider *goose.Provider) error {
		if _, err := provider.Up(ctx); err != nil {
			return fmt.Errorf("migrate up: %w", err)
		}
		return nil
	})
}

// Down reverts the most recent migration.
func Down(ctx context.Context, databaseURL string) error {
	return withProvider(ctx, databaseURL, func(ctx context.Context, provider *goose.Provider) error {
		if _, err := provider.Down(ctx); err != nil {
			return fmt.Errorf("migrate down: %w", err)
		}
		return nil
	})
}

// DownToZero reverts every applied migration. Used by tests and by pilot
// rollbacks.
func DownToZero(ctx context.Context, databaseURL string) error {
	return withProvider(ctx, databaseURL, func(ctx context.Context, provider *goose.Provider) error {
		if _, err := provider.DownTo(ctx, 0); err != nil {
			return fmt.Errorf("migrate down to zero: %w", err)
		}
		return nil
	})
}

// Status reports each migration and whether it is applied.
func Status(ctx context.Context, databaseURL string) ([]*goose.MigrationStatus, error) {
	var statuses []*goose.MigrationStatus
	err := withProvider(ctx, databaseURL, func(ctx context.Context, provider *goose.Provider) error {
		result, err := provider.Status(ctx)
		if err != nil {
			return fmt.Errorf("migration status: %w", err)
		}
		statuses = result
		return nil
	})
	return statuses, err
}

func withProvider(ctx context.Context, databaseURL string, fn func(context.Context, *goose.Provider) error) error {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open database for migrations: %w", err)
	}
	defer func() { _ = db.Close() }()

	provider, err := goose.NewProvider(dialect, db, files)
	if err != nil {
		return fmt.Errorf("create migration provider: %w", err)
	}
	return fn(ctx, provider)
}
