//go:build integration

package retention_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/testkit"
	"github.com/benfaruna/medverify/internal/retention"
	"github.com/benfaruna/medverify/internal/storage"
	"github.com/benfaruna/medverify/migrations"
)

type fixture struct {
	pool    *pgxpool.Pool
	objects *storage.Fake
	sweeper *retention.Sweeper
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	ctx := context.Background()
	databaseURL := testkit.Postgres(t)
	require.NoError(t, migrations.Up(ctx, databaseURL))

	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	objects := &storage.Fake{BucketExists: true, Objects: map[string][]byte{}}
	return &fixture{
		pool:    pool,
		objects: objects,
		sweeper: retention.NewSweeper(retention.NewStore(pool), objects, nil),
	}
}

func (f *fixture) scan(t *testing.T, createdAt time.Time) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`INSERT INTO scans (client_key, status, created_at, updated_at)
		 VALUES ('client:1', 'completed', $1, $1) RETURNING id`,
		createdAt).Scan(&id))
	return id
}

func (f *fixture) image(t *testing.T, scanID uuid.UUID, position int, key string) {
	t.Helper()
	_, err := f.pool.Exec(context.Background(),
		`INSERT INTO scan_images (scan_id, position, storage_key, content_type, byte_size, width, height, sha256)
		 VALUES ($1, $2, $3, 'image/jpeg', 100, 10, 10, $4)`,
		scanID, position, key, fmt.Sprintf("sha-%d", position))
	require.NoError(t, err)
	f.objects.Objects[key] = []byte("jpeg")
}

func (f *fixture) flag(t *testing.T, scanID uuid.UUID) {
	t.Helper()
	_, err := f.pool.Exec(context.Background(),
		`INSERT INTO safety_flags (scan_id, flag_type, severity, reason_code, evidence, source)
		 VALUES ($1, 'missing_ingredient', 'low', 'test', '{}'::jsonb, '{}'::jsonb)`, scanID)
	require.NoError(t, err)
}

func (f *fixture) conversation(t *testing.T, scanID uuid.UUID, expiresAt time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := f.pool.Exec(context.Background(),
		`INSERT INTO conversations (id, scan_id, rules_version, prompt_version, expires_at)
		 VALUES ($1, $2, 'test', 'test', $3)`, id, scanID, expiresAt)
	require.NoError(t, err)
	return id
}

func retentionOptions() retention.Options {
	return retention.Options{
		ImageRetention:        30 * 24 * time.Hour,
		FlaggedImageRetention: 365 * 24 * time.Hour,
		BatchSize:             2,
	}
}

func TestSweepDeletesOrdinaryImagesAndKeepsFlaggedOnes(t *testing.T) {
	fixture := newFixture(t)
	now := time.Now()

	ordinary := fixture.scan(t, now.Add(-40*24*time.Hour))
	fixture.image(t, ordinary, 1, "ordinary-1.jpg")

	flagged := fixture.scan(t, now.Add(-40*24*time.Hour))
	fixture.image(t, flagged, 1, "flagged-1.jpg")
	fixture.flag(t, flagged)

	oldFlagged := fixture.scan(t, now.Add(-400*24*time.Hour))
	fixture.image(t, oldFlagged, 1, "old-flagged-1.jpg")
	fixture.flag(t, oldFlagged)

	report, err := fixture.sweeper.Sweep(context.Background(), retentionOptions(), now)
	require.NoError(t, err)

	assert.Equal(t, 2, report.ImagesDeleted,
		"the ordinary image and the flag older than the review window are deleted")
	assert.Zero(t, report.FailedDeletes)

	assert.NotContains(t, fixture.objects.Objects, "ordinary-1.jpg")
	assert.NotContains(t, fixture.objects.Objects, "old-flagged-1.jpg")
	assert.Contains(t, fixture.objects.Objects, "flagged-1.jpg",
		"a recent flagged case keeps its image for review")

	assertImageRows(t, fixture.pool, flagged, 1)
	assertImageRows(t, fixture.pool, ordinary, 0)
	assertImageRows(t, fixture.pool, oldFlagged, 0)

	// The scan row and verdict survive: only the personal data was deleted.
	assertScanExists(t, fixture.pool, ordinary)
	assertScanExists(t, fixture.pool, flagged)
}

func TestSweepDeletesExpiredConversations(t *testing.T) {
	fixture := newFixture(t)
	now := time.Now()
	scanID := fixture.scan(t, now)

	expired := fixture.conversation(t, scanID, now.Add(-time.Hour))
	liveScan := fixture.scan(t, now)
	live := fixture.conversation(t, liveScan, now.Add(time.Hour))

	report, err := fixture.sweeper.Sweep(context.Background(), retentionOptions(), now)
	require.NoError(t, err)
	assert.Equal(t, 1, report.ConversationsDeleted)

	assertConversationGone(t, fixture.pool, expired)
	assertConversationExists(t, fixture.pool, live)
}

func TestSweepKeepsRowsWhenObjectDeletionFails(t *testing.T) {
	fixture := newFixture(t)
	now := time.Now()
	scanID := fixture.scan(t, now.Add(-40*24*time.Hour))
	fixture.image(t, scanID, 1, "unreachable.jpg")

	fixture.objects.Err = errors.New("object store is down")
	report, err := fixture.sweeper.Sweep(context.Background(), retentionOptions(), now)
	require.Error(t, err)
	assert.Equal(t, 1, report.FailedDeletes)
	assert.Equal(t, 0, report.ImagesDeleted)
	assertImageRows(t, fixture.pool, scanID, 1,
		"the row must stay so a later sweep still knows the object key")

	fixture.objects.Err = nil
	report, err = fixture.sweeper.Sweep(context.Background(), retentionOptions(), now)
	require.NoError(t, err)
	assert.Equal(t, 1, report.ImagesDeleted)
	assertImageRows(t, fixture.pool, scanID, 0)
}

func TestEraseRemovesTheScanAndEverythingDerivedFromIt(t *testing.T) {
	fixture := newFixture(t)
	now := time.Now()
	scanID := fixture.scan(t, now)
	fixture.image(t, scanID, 1, "erase-1.jpg")
	fixture.image(t, scanID, 2, "erase-2.jpg")
	fixture.flag(t, scanID)
	conversation := fixture.conversation(t, scanID, now.Add(time.Hour))

	report, err := fixture.sweeper.Erase(context.Background(), scanID)
	require.NoError(t, err)
	assert.Equal(t, 2, report.ImagesDeleted)

	assert.NotContains(t, fixture.objects.Objects, "erase-1.jpg")
	assert.NotContains(t, fixture.objects.Objects, "erase-2.jpg")
	assertScanGone(t, fixture.pool, scanID)
	assertConversationGone(t, fixture.pool, conversation)
	assertFlagsGone(t, fixture.pool, scanID)

	_, err = fixture.sweeper.Erase(context.Background(), scanID)
	assert.ErrorIs(t, err, retention.ErrScanNotFound)
}

func TestSweepRejectsABadPolicy(t *testing.T) {
	fixture := newFixture(t)

	_, err := fixture.sweeper.Sweep(context.Background(), retention.Options{
		ImageRetention:        365 * 24 * time.Hour,
		FlaggedImageRetention: 24 * time.Hour,
	}, time.Now())
	assert.Error(t, err)
}

func assertImageRows(t *testing.T, pool *pgxpool.Pool, scanID uuid.UUID, want int, message ...any) {
	t.Helper()
	var count int
	require.NoError(t, pool.QueryRow(context.Background(),
		"SELECT count(*) FROM scan_images WHERE scan_id = $1", scanID).Scan(&count))
	assert.Equal(t, want, count, message...)
}

func assertScanExists(t *testing.T, pool *pgxpool.Pool, scanID uuid.UUID) {
	t.Helper()
	var count int
	require.NoError(t, pool.QueryRow(context.Background(),
		"SELECT count(*) FROM scans WHERE id = $1", scanID).Scan(&count))
	assert.Equal(t, 1, count)
}

func assertScanGone(t *testing.T, pool *pgxpool.Pool, scanID uuid.UUID) {
	t.Helper()
	var count int
	require.NoError(t, pool.QueryRow(context.Background(),
		"SELECT count(*) FROM scans WHERE id = $1", scanID).Scan(&count))
	assert.Zero(t, count)
}

func assertConversationExists(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) {
	t.Helper()
	var count int
	require.NoError(t, pool.QueryRow(context.Background(),
		"SELECT count(*) FROM conversations WHERE id = $1", id).Scan(&count))
	assert.Equal(t, 1, count)
}

func assertConversationGone(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) {
	t.Helper()
	var count int
	require.NoError(t, pool.QueryRow(context.Background(),
		"SELECT count(*) FROM conversations WHERE id = $1", id).Scan(&count))
	assert.Zero(t, count)
}

func assertFlagsGone(t *testing.T, pool *pgxpool.Pool, scanID uuid.UUID) {
	t.Helper()
	var count int
	require.NoError(t, pool.QueryRow(context.Background(),
		"SELECT count(*) FROM safety_flags WHERE scan_id = $1", scanID).Scan(&count))
	assert.Zero(t, count)
}
