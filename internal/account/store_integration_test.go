//go:build integration

package account_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/account"
	"github.com/benfaruna/medverify/internal/platform/auth"
	"github.com/benfaruna/medverify/internal/platform/testkit"
	"github.com/benfaruna/medverify/migrations"
)

// newFixture gives the test a migrated database and a service wired the way
// cmd/api wires it: the same pool, the same store, the same session rules.
func newFixture(t *testing.T) (*pgxpool.Pool, *account.Service) {
	t.Helper()

	ctx := context.Background()
	databaseURL := testkit.Postgres(t)
	require.NoError(t, migrations.Up(ctx, databaseURL))

	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	return pool, account.NewService(account.ServiceOptions{Store: account.NewDBStore(pool)})
}

// The key handed back by registration has to authenticate on the existing scan
// API. That is the whole point of reusing api_clients instead of inventing a
// second credential: the same key store the middleware uses must accept it.
func TestRegisteredKeyAuthenticatesThroughTheExistingKeyStore(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool, service := newFixture(t)
	keyStore := auth.NewDBStore(pool)

	registration, err := service.Register(ctx, account.RegisterInput{
		Email:    "Ada@Example.test",
		Name:     "Ada",
		Password: "a-long-enough-password",
	})
	require.NoError(t, err)

	key, err := keyStore.Lookup(ctx, registration.Key.Key)
	require.NoError(t, err, "the registered key must resolve in the API key store")
	assert.Equal(t, registration.User.ClientID, key.ClientID)
	assert.False(t, key.Sandbox)
	assert.Equal(t, auth.AllScopes, key.Scopes)
	assert.Positive(t, key.RatePerMin)
	assert.Positive(t, key.DailyScanQuota)

	// The address is stored lower-cased, which is what the unique index relies on.
	assert.Equal(t, "ada@example.test", registration.User.Email)

	// The session token resolves to the same account.
	signedIn, err := service.Authenticate(ctx, registration.Session.Token)
	require.NoError(t, err)
	assert.Equal(t, registration.User.ID, signedIn.ID)

	// The key is still usable after a sign-out: rotating the browser session
	// must not disturb the credential a client program holds.
	require.NoError(t, service.Logout(ctx, registration.Session.Token))
	_, err = service.Authenticate(ctx, registration.Session.Token)
	assert.ErrorIs(t, err, account.ErrNoSession)

	_, err = keyStore.Lookup(ctx, registration.Key.Key)
	require.NoError(t, err)
}

// Registration writes four rows together. If any of them fails, none may remain:
// an account with a password and no key could never be recovered through the UI.
func TestRegisterIsAtomicAndRejectsDuplicates(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool, service := newFixture(t)

	_, err := service.Register(ctx, account.RegisterInput{
		Email:    "ada@example.test",
		Password: "a-long-enough-password",
	})
	require.NoError(t, err)

	_, err = service.Register(ctx, account.RegisterInput{
		Email:    "ADA@example.test",
		Password: "another-long-password",
	})
	assert.ErrorIs(t, err, account.ErrEmailTaken)

	var clients, users int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM api_clients`).Scan(&clients))
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&users))
	assert.Equal(t, 1, clients, "a duplicate registration must not leave an orphan client")
	assert.Equal(t, 1, users)
}

// Rotation must end with exactly one live key: the old one is rejected by the
// key store, and the replacement is accepted.
func TestRotateRevokesTheOldKeyInTheDatabase(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool, service := newFixture(t)
	keyStore := auth.NewDBStore(pool)

	registration, err := service.Register(ctx, account.RegisterInput{
		Email:    "ada@example.test",
		Password: "a-long-enough-password",
	})
	require.NoError(t, err)

	rotated, revoked, err := service.Rotate(ctx, registration.User)
	require.NoError(t, err)
	assert.Equal(t, int64(1), revoked)
	assert.NotEqual(t, registration.Key.Key, rotated.Key)

	_, err = keyStore.Lookup(ctx, registration.Key.Key)
	assert.ErrorIs(t, err, auth.ErrUnknownClient, "the previous key must stop working")

	key, err := keyStore.Lookup(ctx, rotated.Key)
	require.NoError(t, err)
	assert.Equal(t, registration.User.ClientID, key.ClientID)

	keys, err := service.Keys(ctx, registration.User)
	require.NoError(t, err)
	require.Len(t, keys, 2)
	assert.True(t, keys[0].Active)
	assert.NotNil(t, keys[1].RevokedAt)
}

func TestLoginVerifiesTheStoredPassword(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, service := newFixture(t)

	const password = "a-long-enough-password"
	_, err := service.Register(ctx, account.RegisterInput{
		Email:    "ada@example.test",
		Password: password,
	})
	require.NoError(t, err)

	user, session, err := service.Login(ctx, "Ada@Example.test", password)
	require.NoError(t, err)
	assert.Equal(t, "ada@example.test", user.Email)
	assert.NotEmpty(t, session.Token)

	_, _, err = service.Login(ctx, "ada@example.test", "the-wrong-password")
	assert.ErrorIs(t, err, account.ErrInvalidCredentials)

	_, _, err = service.Login(ctx, "nobody@example.test", password)
	assert.ErrorIs(t, err, account.ErrInvalidCredentials)
}
