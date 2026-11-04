//go:build integration

package auth_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/auth"
	"github.com/benfaruna/medverify/internal/platform/db/dbgen"
	"github.com/benfaruna/medverify/internal/platform/testkit"
	"github.com/benfaruna/medverify/migrations"
)

type keyFixture struct {
	pool    *pgxpool.Pool
	queries *dbgen.Queries
	store   *auth.DBStore
	client  dbgen.ApiClient
	key     dbgen.ApiKey
	fullKey string
}

// newKeyFixture creates one client and one live key, which is the exact state
// the admin CLI leaves behind.
func newKeyFixture(t *testing.T) *keyFixture {
	t.Helper()

	ctx := context.Background()
	databaseURL := testkit.Postgres(t)
	require.NoError(t, migrations.Up(ctx, databaseURL))

	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	queries := dbgen.New(pool)
	client, err := queries.CreateAPIClient(ctx, dbgen.CreateAPIClientParams{
		Name:              "acme",
		Sandbox:           true,
		RatePerMin:        120,
		DailyRequestQuota: 1000,
		DailyScanQuota:    50,
	})
	require.NoError(t, err)

	fullKey, hash, prefix, _, err := auth.GenerateAPIKey()
	require.NoError(t, err)

	key, err := queries.CreateAPIKey(ctx, dbgen.CreateAPIKeyParams{
		ClientID:  client.ID,
		KeyPrefix: prefix,
		KeyHash:   hash,
		Scopes:    auth.AllScopes,
	})
	require.NoError(t, err)

	return &keyFixture{
		pool:    pool,
		queries: queries,
		store:   auth.NewDBStore(pool),
		client:  client,
		key:     key,
		fullKey: fullKey,
	}
}

func TestAPIKeyLooksUpAndCarriesTheClientPlan(t *testing.T) {
	fixture := newKeyFixture(t)
	ctx := context.Background()

	key, err := fixture.store.Lookup(ctx, fixture.fullKey)
	require.NoError(t, err)

	assert.Equal(t, fixture.client.ID, key.ClientID)
	assert.Equal(t, fixture.client.Name, key.ClientName)
	assert.True(t, key.Sandbox)
	assert.Equal(t, int32(120), key.RatePerMin)
	assert.Equal(t, int32(1000), key.DailyRequestQuota)
	assert.Equal(t, int32(50), key.DailyScanQuota)
	assert.Equal(t, auth.AllScopes, key.Scopes)
}

func TestAPIKeyIsStoredOnlyAsAHash(t *testing.T) {
	fixture := newKeyFixture(t)

	assert.NotContains(t, fixture.key.KeyHash, fixture.fullKey,
		"the raw key must never be stored")
	sum := sha256.Sum256([]byte(fixture.fullKey))
	assert.Equal(t, hex.EncodeToString(sum[:]), fixture.key.KeyHash)
	require.True(t, strings.HasPrefix(fixture.fullKey, "mv_"+fixture.key.KeyPrefix+"_"))
	secret := strings.TrimPrefix(fixture.fullKey, "mv_"+fixture.key.KeyPrefix+"_")
	assert.NotContains(t, fixture.key.KeyHash, secret, "the secret must not be stored")
}

func TestAPIKeyRejectsEveryInvalidState(t *testing.T) {
	ctx := context.Background()

	t.Run("wrong secret", func(t *testing.T) {
		fixture := newKeyFixture(t)
		_, err := fixture.store.Lookup(ctx, "mv_"+fixture.key.KeyPrefix+"_wrong")
		assert.ErrorIs(t, err, auth.ErrUnknownClient)
	})

	t.Run("unknown prefix", func(t *testing.T) {
		fixture := newKeyFixture(t)
		_, err := fixture.store.Lookup(ctx, "mv_000000000000_"+fixture.fullKey[len("mv_"):])
		assert.ErrorIs(t, err, auth.ErrUnknownClient)
	})

	t.Run("malformed token", func(t *testing.T) {
		fixture := newKeyFixture(t)
		for _, token := range []string{"", "not-a-key", "mv_only", "mv__secret"} {
			_, err := fixture.store.Lookup(ctx, token)
			assert.ErrorIs(t, err, auth.ErrUnknownClient, token)
		}
	})

	t.Run("revoked", func(t *testing.T) {
		fixture := newKeyFixture(t)
		revoked, err := fixture.queries.RevokeAPIKey(ctx, fixture.key.KeyPrefix)
		require.NoError(t, err)
		require.EqualValues(t, 1, revoked)

		_, err = fixture.store.Lookup(ctx, fixture.fullKey)
		assert.ErrorIs(t, err, auth.ErrUnknownClient)
	})

	t.Run("expired", func(t *testing.T) {
		fixture := newKeyFixture(t)
		past := time.Now().Add(-time.Minute)
		_, err := fixture.pool.Exec(ctx, "UPDATE api_keys SET expires_at = $1 WHERE id = $2", past, fixture.key.ID)
		require.NoError(t, err)

		_, err = fixture.store.Lookup(ctx, fixture.fullKey)
		assert.ErrorIs(t, err, auth.ErrUnknownClient)
	})

	t.Run("inactive client", func(t *testing.T) {
		fixture := newKeyFixture(t)
		_, err := fixture.pool.Exec(ctx, "UPDATE api_clients SET active = false WHERE id = $1", fixture.client.ID)
		require.NoError(t, err)

		_, err = fixture.store.Lookup(ctx, fixture.fullKey)
		assert.ErrorIs(t, err, auth.ErrUnknownClient)
	})
}

func TestAPIKeyRecordsLastUse(t *testing.T) {
	fixture := newKeyFixture(t)
	ctx := context.Background()

	_, err := fixture.store.Lookup(ctx, fixture.fullKey)
	require.NoError(t, err)

	var lastUsed *time.Time
	require.NoError(t, fixture.pool.QueryRow(ctx,
		"SELECT last_used_at FROM api_keys WHERE id = $1", fixture.key.ID).Scan(&lastUsed))
	require.NotNil(t, lastUsed, "a successful lookup records last use")
}

func TestUsageReserveCountsOneRequestPerScan(t *testing.T) {
	fixture := newKeyFixture(t)
	ctx := context.Background()
	usage := auth.NewDBUsageStore(fixture.pool)

	request := auth.ReserveRequest{
		ClientID:     fixture.client.ID,
		Class:        auth.QuotaScans,
		RequestLimit: 10,
		ClassLimit:   2,
	}
	require.NoError(t, usage.Reserve(ctx, request))
	require.NoError(t, usage.Reserve(ctx, request))

	err := usage.Reserve(ctx, request)
	assert.ErrorIs(t, err, auth.ErrQuotaExceeded, "the scan allowance is spent")

	// The request counter moved twice, not three times: a refusal writes
	// nothing.
	assert.Equal(t, int32(2), usageRequests(t, fixture.pool, fixture.client.ID))
	assert.Equal(t, int32(2), usageScans(t, fixture.pool, fixture.client.ID))
}

func TestUsageReserveEnforcesTheRequestQuota(t *testing.T) {
	fixture := newKeyFixture(t)
	ctx := context.Background()
	usage := auth.NewDBUsageStore(fixture.pool)

	request := auth.ReserveRequest{
		ClientID:     fixture.client.ID,
		Class:        auth.QuotaQuestions,
		RequestLimit: 1,
		ClassLimit:   100,
	}
	require.NoError(t, usage.Reserve(ctx, request))

	err := usage.Reserve(ctx, request)
	assert.ErrorIs(t, err, auth.ErrQuotaExceeded)
	assert.Equal(t, int32(1), usageQuestions(t, fixture.pool, fixture.client.ID))
}

func usageRequests(t *testing.T, pool *pgxpool.Pool, clientID int64) int32 {
	t.Helper()
	var value int32
	require.NoError(t, pool.QueryRow(context.Background(),
		"SELECT requests FROM usage_counters WHERE client_id = $1 ORDER BY day DESC LIMIT 1",
		clientID).Scan(&value))
	return value
}

func usageScans(t *testing.T, pool *pgxpool.Pool, clientID int64) int32 {
	t.Helper()
	var value int32
	require.NoError(t, pool.QueryRow(context.Background(),
		"SELECT scans FROM usage_counters WHERE client_id = $1 ORDER BY day DESC LIMIT 1",
		clientID).Scan(&value))
	return value
}

func usageQuestions(t *testing.T, pool *pgxpool.Pool, clientID int64) int32 {
	t.Helper()
	var value int32
	require.NoError(t, pool.QueryRow(context.Background(),
		"SELECT questions FROM usage_counters WHERE client_id = $1 ORDER BY day DESC LIMIT 1",
		clientID).Scan(&value))
	return value
}
