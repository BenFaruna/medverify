package account

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/benfaruna/medverify/internal/platform/db/dbgen"
)

// Plan defaults for a self-registered account. They mirror the migration
// defaults and the values `admin apikey create` uses, so a person who signs up
// gets exactly the plan an operator would have given them.
const (
	defaultRatePerMin        int32 = 60
	defaultDailyRequestQuota int32 = 10000
	defaultDailyScanQuota    int32 = 200
)

// DBStore is the Postgres-backed store. Lookups are indexed reads; anything
// that must land together runs in one transaction.
type DBStore struct {
	pool *pgxpool.Pool
}

// NewDBStore builds a store around an existing pool. The store shares the
// API's pool rather than opening its own, so it inherits the same timeouts.
func NewDBStore(pool *pgxpool.Pool) *DBStore {
	return &DBStore{pool: pool}
}

// Register writes the client, user, first key, and first session together. A
// partial account would be unusable — it could hold a password with no key —
// so a failure at any step leaves no rows behind.
func (s *DBStore) Register(ctx context.Context, params RegisterParams) (User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return User{}, fmt.Errorf("begin registration: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	queries := dbgen.New(tx)

	client, err := queries.CreateAPIClient(ctx, dbgen.CreateAPIClientParams{
		Name:              params.ClientName,
		Sandbox:           false,
		RatePerMin:        defaultRatePerMin,
		DailyRequestQuota: defaultDailyRequestQuota,
		DailyScanQuota:    defaultDailyScanQuota,
	})
	if err != nil {
		return User{}, fmt.Errorf("create api client: %w", err)
	}

	user, err := queries.CreateUser(ctx, dbgen.CreateUserParams{
		Email:        params.Email,
		Name:         params.Name,
		PasswordHash: params.PasswordHash,
		ClientID:     client.ID,
	})
	if err != nil {
		if isDuplicateEmail(err) {
			return User{}, ErrEmailTaken
		}
		return User{}, fmt.Errorf("create user: %w", err)
	}

	if _, err := queries.CreateAPIKey(ctx, dbgen.CreateAPIKeyParams{
		ClientID:  client.ID,
		KeyPrefix: params.KeyPrefix,
		KeyHash:   params.KeyHash,
		Scopes:    params.Scopes,
	}); err != nil {
		return User{}, fmt.Errorf("create first api key: %w", err)
	}

	if _, err := queries.CreateUserSession(ctx, dbgen.CreateUserSessionParams{
		UserID:    user.ID,
		TokenHash: params.SessionTokenHash,
		ExpiresAt: params.SessionExpiresAt,
	}); err != nil {
		return User{}, fmt.Errorf("create first session: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("commit registration: %w", err)
	}
	return toUser(user), nil
}

// UserByEmail loads the account and its password material for a sign-in.
func (s *DBStore) UserByEmail(ctx context.Context, email string) (StoredUser, error) {
	user, err := dbgen.New(s.pool).GetUserByEmail(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		return StoredUser{}, ErrNoUser
	}
	if err != nil {
		return StoredUser{}, fmt.Errorf("load user by email: %w", err)
	}
	return StoredUser{User: toUser(user), PasswordHash: user.PasswordHash}, nil
}

// UserBySession resolves a hashed token. The query itself rejects an expired,
// revoked, or inactive pair, so expiry cannot drift from the clock in Go.
func (s *DBStore) UserBySession(ctx context.Context, tokenHash string) (User, error) {
	user, err := dbgen.New(s.pool).GetUserBySessionToken(ctx, tokenHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNoUser
	}
	if err != nil {
		return User{}, fmt.Errorf("load session user: %w", err)
	}
	return toUser(user), nil
}

// TouchLogin records the sign-in time.
func (s *DBStore) TouchLogin(ctx context.Context, userID int64) error {
	return dbgen.New(s.pool).TouchUserLogin(ctx, userID)
}

// TouchSession records that a session was used.
func (s *DBStore) TouchSession(ctx context.Context, tokenHash string) error {
	return dbgen.New(s.pool).TouchUserSession(ctx, tokenHash)
}

// CreateSession starts a session for an existing account.
func (s *DBStore) CreateSession(ctx context.Context, params SessionParams) error {
	if _, err := dbgen.New(s.pool).CreateUserSession(ctx, dbgen.CreateUserSessionParams{
		UserID:    params.UserID,
		TokenHash: params.TokenHash,
		ExpiresAt: params.ExpiresAt,
	}); err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// RevokeSession ends one session.
func (s *DBStore) RevokeSession(ctx context.Context, tokenHash string) error {
	if _, err := dbgen.New(s.pool).RevokeUserSession(ctx, tokenHash); err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

// Keys lists every key the client has held, newest first, including revoked
// ones so the settings page can show what a rotation replaced.
func (s *DBStore) Keys(ctx context.Context, clientID int64) ([]APIKey, error) {
	rows, err := dbgen.New(s.pool).ListAPIKeysForClient(ctx, clientID)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	keys := make([]APIKey, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, APIKey{
			ID:         row.ID,
			Prefix:     row.KeyPrefix,
			Scopes:     row.Scopes,
			Active:     row.Active,
			ExpiresAt:  row.ExpiresAt,
			RevokedAt:  row.RevokedAt,
			LastUsedAt: row.LastUsedAt,
			CreatedAt:  row.CreatedAt,
		})
	}
	return keys, nil
}

// RotateKey revokes the client's live keys and inserts the replacement in one
// transaction. Revoking first and failing to insert would leave the account
// with no working key, which is why neither step is allowed to stand alone.
func (s *DBStore) RotateKey(ctx context.Context, params RotateParams) (int64, APIKey, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, APIKey{}, fmt.Errorf("begin key rotation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	queries := dbgen.New(tx)

	revoked, err := queries.RotateAPIKeysForClient(ctx, params.ClientID)
	if err != nil {
		return 0, APIKey{}, fmt.Errorf("revoke api keys: %w", err)
	}

	key, err := queries.CreateAPIKey(ctx, dbgen.CreateAPIKeyParams{
		ClientID:  params.ClientID,
		KeyPrefix: params.KeyPrefix,
		KeyHash:   params.KeyHash,
		Scopes:    params.Scopes,
	})
	if err != nil {
		return 0, APIKey{}, fmt.Errorf("create replacement api key: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, APIKey{}, fmt.Errorf("commit key rotation: %w", err)
	}

	return revoked, APIKey{
		ID:        key.ID,
		Prefix:    key.KeyPrefix,
		Scopes:    key.Scopes,
		Active:    key.Active,
		ExpiresAt: key.ExpiresAt,
		CreatedAt: key.CreatedAt,
	}, nil
}

// isDuplicateEmail reports a unique violation on the account address. The
// client name carries its own unique constraint, so the constraint name is
// checked rather than the SQLSTATE alone.
func isDuplicateEmail(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return strings.Contains(pgErr.ConstraintName, "users_email")
}

func toUser(user dbgen.User) User {
	return User{
		ID:          user.ID,
		Email:       user.Email,
		Name:        user.Name,
		ClientID:    user.ClientID,
		CreatedAt:   user.CreatedAt,
		LastLoginAt: user.LastLoginAt,
	}
}
