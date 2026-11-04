//go:build integration

package assistant_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/assistant"
	"github.com/benfaruna/medverify/internal/platform/testkit"
	"github.com/benfaruna/medverify/migrations"
)

type storeFixture struct {
	pool  *pgxpool.Pool
	store *assistant.Store
	scan  uuid.UUID
}

func newStoreFixture(t *testing.T) *storeFixture {
	t.Helper()
	ctx := context.Background()
	databaseURL := testkit.Postgres(t)
	require.NoError(t, migrations.Up(ctx, databaseURL))

	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	var scanID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO scans (client_key, status, language, completed_at)
		VALUES ('client-a', 'completed', 'en', now())
		RETURNING id`).Scan(&scanID))

	return &storeFixture{pool: pool, store: assistant.NewStore(pool), scan: scanID}
}

func TestStorePersistsTranscriptAndToolAuditRows(t *testing.T) {
	fixture := newStoreFixture(t)
	ctx := context.Background()

	conversation, err := fixture.store.GetOrCreate(ctx, fixture.scan, "en", "verdict-v1", assistant.PromptVersion, time.Hour)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, conversation.ID)

	// A second GetOrCreate returns the same conversation, not a second one.
	again, err := fixture.store.GetOrCreate(ctx, fixture.scan, "en", "verdict-v1", assistant.PromptVersion, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, conversation.ID, again.ID)

	err = fixture.store.RecordTurn(ctx, assistant.TurnRecord{
		Conversation: conversation,
		Turn:         1,
		User:         "What does the leaflet say about warnings?",
		Answer:       "Warnings are listed in the leaflet. [1]",
		Intent:       assistant.IntentLeaflet,
		Language:     "en",
		ToolLogs: []assistant.ToolLog{{
			Name:      assistant.ToolSearchLeaflet,
			Arguments: []byte(`{"query":"warnings"}`),
			Outcome:   "ok",
			SourceIDs: []string{"leaflet:9"},
			Latency:   12 * time.Millisecond,
		}},
		SourceIDs: []string{"leaflet:9"},
		Verifier:  "ok",
	})
	require.NoError(t, err)

	messages, err := fixture.store.Messages(ctx, conversation.ID)
	require.NoError(t, err)
	require.Len(t, messages, 2)
	assert.Equal(t, "user", messages[0].Role)
	assert.Equal(t, "What does the leaflet say about warnings?", messages[0].Content)
	assert.Equal(t, "assistant", messages[1].Role)
	assert.Equal(t, assistant.IntentLeaflet, assistant.Intent(messages[1].Intent))
	assert.Equal(t, []string{"leaflet:9"}, messages[1].SourceIDs)
	assert.Equal(t, []string{assistant.ToolSearchLeaflet}, messages[1].ToolNames)
	assert.Equal(t, "ok", messages[1].Verifier)

	count, err := fixture.store.CountUserMessages(ctx, conversation.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	var toolName, outcome string
	var sourceIDs []string
	require.NoError(t, fixture.pool.QueryRow(ctx, `
		SELECT tool_name, outcome, source_ids
		FROM assistant_tool_calls
		WHERE conversation_id = $1 AND turn = 1`, conversation.ID).
		Scan(&toolName, &outcome, &sourceIDs))
	assert.Equal(t, assistant.ToolSearchLeaflet, toolName)
	assert.Equal(t, "ok", outcome)
	assert.Equal(t, []string{"leaflet:9"}, sourceIDs)
}

func TestRecordTurnRollsBackWhenAMessageFails(t *testing.T) {
	fixture := newStoreFixture(t)
	ctx := context.Background()

	conversation, err := fixture.store.GetOrCreate(ctx, fixture.scan, "en", "verdict-v1", assistant.PromptVersion, time.Hour)
	require.NoError(t, err)

	// The schema rejects a non-positive turn, so the user insert fails and the
	// transaction must not leave a partial transcript behind.
	err = fixture.store.RecordTurn(ctx, assistant.TurnRecord{
		Conversation: conversation,
		Turn:         0,
		User:         "q",
		Answer:       "a",
		Intent:       assistant.IntentRegistration,
		Language:     "en",
	})
	require.Error(t, err)

	messages, err := fixture.store.Messages(ctx, conversation.ID)
	require.NoError(t, err)
	assert.Empty(t, messages)

	count, err := fixture.store.CountUserMessages(ctx, conversation.ID)
	require.NoError(t, err)
	assert.Zero(t, count)
}

func TestStoreKeepsConversationsScopedToTheirScan(t *testing.T) {
	fixture := newStoreFixture(t)
	ctx := context.Background()

	first, err := fixture.store.GetOrCreate(ctx, fixture.scan, "en", "verdict-v1", assistant.PromptVersion, time.Hour)
	require.NoError(t, err)

	var otherScan uuid.UUID
	require.NoError(t, fixture.pool.QueryRow(ctx, `
		INSERT INTO scans (client_key, status, language, completed_at)
		VALUES ('client-a', 'completed', 'en', now())
		RETURNING id`).Scan(&otherScan))

	second, err := fixture.store.GetOrCreate(ctx, otherScan, "en", "verdict-v1", assistant.PromptVersion, time.Hour)
	require.NoError(t, err)
	assert.NotEqual(t, first.ID, second.ID)

	_, found, err := fixture.store.GetConversation(ctx, otherScan)
	require.NoError(t, err)
	assert.True(t, found)

	// Messages for one conversation never leak into the other.
	require.NoError(t, fixture.store.RecordTurn(ctx, assistant.TurnRecord{
		Conversation: first, Turn: 1, User: "first", Answer: "first answer",
		Intent: assistant.IntentRegistration, Language: "en", Verifier: "ok",
	}))
	messages, err := fixture.store.Messages(ctx, second.ID)
	require.NoError(t, err)
	assert.Empty(t, messages)
}
