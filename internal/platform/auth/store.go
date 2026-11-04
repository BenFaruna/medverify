package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/benfaruna/medverify/internal/platform/db/dbgen"
)

// DBStore authenticates keys against api_keys and api_clients. Lookup is a
// single indexed read on the public prefix; the presented value is compared to
// the stored hash in constant time, so a leaked prefix alone is useless.
type DBStore struct {
	queries *dbgen.Queries
}

// NewDBStore builds a store around an existing pool.
func NewDBStore(pool *pgxpool.Pool) *DBStore {
	return &DBStore{queries: dbgen.New(pool)}
}

// Lookup resolves an access token to its key. Every failure is ErrUnknownClient
// so a caller cannot distinguish a bad prefix from a revoked or expired key.
func (s *DBStore) Lookup(ctx context.Context, accessToken string) (*Key, error) {
	prefix, err := KeyPrefixFromToken(accessToken)
	if err != nil {
		return nil, ErrUnknownClient
	}

	row, err := s.queries.GetAPIKeyByPrefix(ctx, prefix)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUnknownClient
	}
	if err != nil {
		return nil, fmt.Errorf("look up api key: %w", err)
	}

	// Compare the hash of what was presented with the stored hash. Both are
	// hex encoded SHA-256, so a constant-time compare is sufficient.
	if !ConstantTimeEqual(hashFullKey(accessToken), row.KeyHash) {
		return nil, ErrUnknownClient
	}
	if !row.Active || !row.ClientActive {
		return nil, ErrUnknownClient
	}
	if row.RevokedAt != nil {
		return nil, ErrUnknownClient
	}
	now := time.Now()
	if row.ExpiresAt != nil && !row.ExpiresAt.After(now) {
		return nil, ErrUnknownClient
	}

	// A last-used timestamp is operational telemetry, not authentication. A
	// failure here must not reject a valid request.
	_ = s.queries.TouchAPIKey(ctx, row.ID)

	return &Key{
		ID:                row.ID,
		ClientID:          row.ClientID,
		KeyPrefix:         row.KeyPrefix,
		Scopes:            row.Scopes,
		Active:            row.Active,
		ExpiresAt:         row.ExpiresAt,
		ClientName:        row.ClientName,
		Sandbox:           row.Sandbox,
		RatePerMin:        row.RatePerMin,
		DailyRequestQuota: row.DailyRequestQuota,
		DailyScanQuota:    row.DailyScanQuota,
	}, nil
}
