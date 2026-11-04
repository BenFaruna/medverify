package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/benfaruna/medverify/internal/platform/db/dbgen"
)

// DBUsageStore keeps daily counters in usage_counters. Quotas live in the
// database rather than in code so a plan change does not need a deploy.
type DBUsageStore struct {
	pool *pgxpool.Pool
}

// NewDBUsageStore builds a usage store around an existing pool.
func NewDBUsageStore(pool *pgxpool.Pool) *DBUsageStore {
	return &DBUsageStore{pool: pool}
}

// ReserveRequest is one quota reservation. Every request counts against the
// daily request quota; a scan or a question also counts against its own
// counter, so one scan is never double counted as two requests.
type ReserveRequest struct {
	ClientID     int64
	Class        QuotaClass
	RequestLimit int32
	ClassLimit   int32
}

// Reserve atomically checks and increments every counter the request consumes.
// The row lock makes two concurrent requests unable to both take the last unit.
func (s *DBUsageStore) Reserve(ctx context.Context, request ReserveRequest) error {
	if request.ClientID == 0 {
		return nil
	}

	day := startOfDay(time.Now())

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin usage transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := dbgen.New(tx)
	if err := q.EnsureUsageRow(ctx, dbgen.EnsureUsageRowParams{ClientID: request.ClientID, Day: day}); err != nil {
		return fmt.Errorf("ensure usage row: %w", err)
	}
	usage, err := q.GetUsageForUpdate(ctx, dbgen.GetUsageForUpdateParams{ClientID: request.ClientID, Day: day})
	if err != nil {
		return fmt.Errorf("lock usage row: %w", err)
	}

	if request.RequestLimit > 0 && usage.Requests >= request.RequestLimit {
		return ErrQuotaExceeded
	}
	switch request.Class {
	case QuotaScans:
		if request.ClassLimit > 0 && usage.Scans >= request.ClassLimit {
			return ErrQuotaExceeded
		}
	case QuotaQuestions:
		if request.ClassLimit > 0 && usage.Questions >= request.ClassLimit {
			return ErrQuotaExceeded
		}
	}

	params := dbgen.IncrementUsageParams{ClientID: request.ClientID, Day: day}
	switch request.Class {
	case QuotaScans:
		params.ScanInc = 1
	case QuotaQuestions:
		params.QuestionInc = 1
	}
	if err := q.IncrementUsage(ctx, params); err != nil {
		return fmt.Errorf("increment usage: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit usage: %w", err)
	}
	return nil
}

func startOfDay(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}
