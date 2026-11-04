//go:build integration

package webhook_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/testkit"
	"github.com/benfaruna/medverify/internal/webhook"
	"github.com/benfaruna/medverify/migrations"
)

func newWebhookStore(t *testing.T) (*webhook.DBStore, *pgxpool.Pool, int64, uuid.UUID) {
	t.Helper()

	ctx := context.Background()
	databaseURL := testkit.Postgres(t)
	require.NoError(t, migrations.Up(ctx, databaseURL))

	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	var clientID int64
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO api_clients (name) VALUES ('webhook-client') RETURNING id`).Scan(&clientID))

	scanID := uuid.New()
	_, err = pool.Exec(ctx,
		`INSERT INTO scans (id, client_key, status, api_client_id) VALUES ($1, $2, 'completed', $3)`,
		scanID, "client:1", clientID)
	require.NoError(t, err)

	return webhook.NewDBStore(pool), pool, clientID, scanID
}

func TestCreateEndpointRejectsAnUnsafeURL(t *testing.T) {
	store, _, clientID, _ := newWebhookStore(t)

	_, err := store.CreateEndpoint(context.Background(), clientID, "http://127.0.0.1/hook", "s3cret")
	require.ErrorIs(t, err, webhook.ErrUnsafeURL, "an unsafe target must never become a delivery row")
}

func TestEnqueueIsIdempotentPerEndpointAndScan(t *testing.T) {
	store, _, clientID, scanID := newWebhookStore(t)
	ctx := context.Background()

	_, err := store.CreateEndpoint(ctx, clientID, "https://hooks.example.test/medverify", "s3cret")
	require.NoError(t, err)

	created, err := store.Enqueue(ctx, clientID, scanID, webhook.EventScanCompleted, []byte(`{"event":"scan.completed"}`))
	require.NoError(t, err)
	assert.Equal(t, 1, created)

	// A replayed completion must not create a second delivery.
	created, err = store.Enqueue(ctx, clientID, scanID, webhook.EventScanCompleted, []byte(`{"event":"scan.completed"}`))
	require.NoError(t, err)
	assert.Equal(t, 0, created)
}

func TestDeliveryRetryThenDeadLetterThenReplay(t *testing.T) {
	store, pool, clientID, scanID := newWebhookStore(t)
	ctx := context.Background()

	_, err := store.CreateEndpoint(ctx, clientID, "https://hooks.example.test/medverify", "s3cret")
	require.NoError(t, err)
	_, err = store.Enqueue(ctx, clientID, scanID, webhook.EventScanCompleted, []byte(`{}`))
	require.NoError(t, err)

	due, err := store.ListDue(ctx, 10)
	require.NoError(t, err)
	require.Len(t, due, 1)
	delivery := due[0]
	assert.Equal(t, "https://hooks.example.test/medverify", delivery.URL)
	assert.Equal(t, "s3cret", delivery.Secret)
	assert.Equal(t, int32(0), delivery.Attempts)

	// A retry pushes the next attempt into the future, so it is no longer due.
	require.NoError(t, store.Retry(ctx, delivery.ID, time.Now().Add(time.Hour), 500, "endpoint returned 500"))
	due, err = store.ListDue(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, due, "a delivery waiting on backoff must not be due")

	var attempts int32
	var lastError *string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT attempts, last_error FROM webhook_deliveries WHERE id = $1`, delivery.ID).Scan(&attempts, &lastError))
	assert.Equal(t, int32(1), attempts)
	require.NotNil(t, lastError)

	// Dead-letter it, then replay it back into the queue.
	require.NoError(t, store.DeadLetter(ctx, delivery.ID, 500, "endpoint returned 500"))
	due, err = store.ListDue(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, due, "a dead delivery is not due")

	replayed, err := store.Replay(ctx, delivery.ID)
	require.NoError(t, err)
	assert.True(t, replayed)

	due, err = store.ListDue(ctx, 10)
	require.NoError(t, err)
	require.Len(t, due, 1, "replay must put the delivery back in the queue")

	// Replaying a live delivery is a no-op.
	replayed, err = store.Replay(ctx, delivery.ID)
	require.NoError(t, err)
	assert.False(t, replayed)
}

func TestMarkDeliveredRemovesItFromTheQueue(t *testing.T) {
	store, pool, clientID, scanID := newWebhookStore(t)
	ctx := context.Background()

	_, err := store.CreateEndpoint(ctx, clientID, "https://hooks.example.test/medverify", "s3cret")
	require.NoError(t, err)
	_, err = store.Enqueue(ctx, clientID, scanID, webhook.EventScanCompleted, []byte(`{}`))
	require.NoError(t, err)

	due, err := store.ListDue(ctx, 10)
	require.NoError(t, err)
	require.Len(t, due, 1)
	deliveryID := due[0].ID

	require.NoError(t, store.MarkDelivered(ctx, deliveryID, 204))

	due, err = store.ListDue(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, due)

	var status string
	var deliveredAt *time.Time
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT status, delivered_at FROM webhook_deliveries WHERE id = $1`, deliveryID).Scan(&status, &deliveredAt))
	assert.Equal(t, "delivered", status)
	assert.NotNil(t, deliveredAt)
}
