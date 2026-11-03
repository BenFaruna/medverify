package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/benfaruna/medverify/internal/platform/db/dbgen"
)

// Conversation errors.
var (
	ErrConversationExpired = errors.New("conversation has expired")
	ErrTurnLimit           = errors.New("conversation has reached its turn limit")
	ErrConversationMissing = errors.New("conversation does not exist")
)

// Conversation is one scan's assistant session.
type Conversation struct {
	ID            uuid.UUID
	ScanID        uuid.UUID
	Language      string
	RulesVersion  string
	PromptVersion string
	CreatedAt     time.Time
	LastTurnAt    time.Time
	ExpiresAt     time.Time
}

// Message is one transcript row.
type Message struct {
	ID            int64
	Turn          int
	Role          string
	Content       string
	Intent        string
	Language      string
	ToolNames     []string
	SourceIDs     []string
	Verifier      string
	RefusalReason string
	CreatedAt     time.Time
}

// Store persists transcripts and tool audit rows.
type Store struct {
	pool    *pgxpool.Pool
	queries *dbgen.Queries
}

// NewStore wraps a pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, queries: dbgen.New(pool)}
}

// GetConversation returns one conversation or found=false.
func (s *Store) GetConversation(ctx context.Context, scanID uuid.UUID) (Conversation, bool, error) {
	row, err := s.queries.GetConversationByScan(ctx, scanID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Conversation{}, false, nil
	}
	if err != nil {
		return Conversation{}, false, fmt.Errorf("assistant: load conversation: %w", err)
	}
	return conversationFromRow(row), true, nil
}

// GetOrCreate returns the scan's conversation, creating it with the given
// expiry when absent. A concurrent create loses the race and reads the winner.
func (s *Store) GetOrCreate(ctx context.Context, scanID uuid.UUID, language, rulesVersion, promptVersion string, ttl time.Duration) (Conversation, error) {
	if existing, found, err := s.GetConversation(ctx, scanID); err != nil {
		return Conversation{}, err
	} else if found {
		return existing, nil
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	row, err := s.queries.CreateConversation(ctx, dbgen.CreateConversationParams{
		ID:            uuid.New(),
		ScanID:        scanID,
		Language:      language,
		RulesVersion:  rulesVersion,
		PromptVersion: promptVersion,
		ExpiresAt:     time.Now().UTC().Add(ttl),
	})
	if err == nil {
		return conversationFromRow(row), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Conversation{}, fmt.Errorf("assistant: create conversation: %w", err)
	}
	existing, found, err := s.GetConversation(ctx, scanID)
	if err != nil {
		return Conversation{}, err
	}
	if !found {
		return Conversation{}, ErrConversationMissing
	}
	return existing, nil
}

// CountUserMessages counts completed turns.
func (s *Store) CountUserMessages(ctx context.Context, conversationID uuid.UUID) (int, error) {
	count, err := s.queries.CountConversationUserMessages(ctx, conversationID)
	if err != nil {
		return 0, fmt.Errorf("assistant: count turns: %w", err)
	}
	return int(count), nil
}

// Messages returns the transcript in turn order.
func (s *Store) Messages(ctx context.Context, conversationID uuid.UUID) ([]Message, error) {
	rows, err := s.queries.ListMessages(ctx, conversationID)
	if err != nil {
		return nil, fmt.Errorf("assistant: list messages: %w", err)
	}
	out := make([]Message, 0, len(rows))
	for _, row := range rows {
		out = append(out, messageFromRow(row))
	}
	return out, nil
}

// TurnRecord is everything written for one completed turn. User text is stored
// for the transcript and never logged.
type TurnRecord struct {
	Conversation Conversation
	Turn         int
	User         string
	Answer       string
	Intent       Intent
	Language     string
	ToolLogs     []ToolLog
	SourceIDs    []string
	Verifier     string
	RefusalKey   string
}

// RecordTurn writes both messages, every tool audit row, and advances the
// conversation in one transaction. A failure means the turn is not recorded
// and the caller must not show it as audited.
func (s *Store) RecordTurn(ctx context.Context, record TurnRecord) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("assistant: begin turn: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbgen.New(tx)

	intent := string(record.Intent)
	language := record.Language
	verifier := record.Verifier
	refusal := record.RefusalKey
	userRow, err := q.InsertMessage(ctx, dbgen.InsertMessageParams{
		ConversationID: record.Conversation.ID,
		ScanID:         record.Conversation.ScanID,
		Turn:           int32(record.Turn),
		Role:           "user",
		Content:        record.User,
		Intent:         &intent,
		Language:       &language,
		ToolNames:      []string{},
		SourceIds:      []string{},
	})
	if err != nil {
		return fmt.Errorf("assistant: insert user message: %w", err)
	}
	_ = userRow

	toolNames := make([]string, 0, len(record.ToolLogs))
	for _, log := range record.ToolLogs {
		toolNames = append(toolNames, log.Name)
		arguments := log.Arguments
		if len(arguments) == 0 {
			arguments = json.RawMessage("{}")
		}
		outcome := log.Outcome
		if outcome != "ok" && outcome != "error" && outcome != "rejected" {
			outcome = "error"
		}
		if err := q.InsertAssistantToolCall(ctx, dbgen.InsertAssistantToolCallParams{
			ConversationID: record.Conversation.ID,
			ScanID:         record.Conversation.ScanID,
			Turn:           int32(record.Turn),
			ToolName:       log.Name,
			Arguments:      arguments,
			Outcome:        outcome,
			SourceIds:      emptyIfNil(log.SourceIDs),
			LatencyMs:      clampLatency(log.Latency),
		}); err != nil {
			return fmt.Errorf("assistant: insert tool call: %w", err)
		}
	}

	if _, err := q.InsertMessage(ctx, dbgen.InsertMessageParams{
		ConversationID: record.Conversation.ID,
		ScanID:         record.Conversation.ScanID,
		Turn:           int32(record.Turn),
		Role:           "assistant",
		Content:        record.Answer,
		Intent:         &intent,
		Language:       &language,
		ToolNames:      emptyIfNil(toolNames),
		SourceIds:      emptyIfNil(record.SourceIDs),
		Verifier:       optionalString(verifier),
		RefusalReason:  optionalString(refusal),
	}); err != nil {
		return fmt.Errorf("assistant: insert assistant message: %w", err)
	}
	if err := q.TouchConversation(ctx, record.Conversation.ID); err != nil {
		return fmt.Errorf("assistant: touch conversation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("assistant: commit turn: %w", err)
	}
	return nil
}

func conversationFromRow(row dbgen.Conversation) Conversation {
	return Conversation{
		ID: row.ID, ScanID: row.ScanID, Language: row.Language,
		RulesVersion: row.RulesVersion, PromptVersion: row.PromptVersion,
		CreatedAt: row.CreatedAt, LastTurnAt: row.LastTurnAt, ExpiresAt: row.ExpiresAt,
	}
}

func messageFromRow(row dbgen.Message) Message {
	message := Message{
		ID: row.ID, Turn: int(row.Turn), Role: row.Role, Content: row.Content,
		ToolNames: row.ToolNames, SourceIDs: row.SourceIds, CreatedAt: row.CreatedAt,
	}
	if row.Intent != nil {
		message.Intent = *row.Intent
	}
	if row.Language != nil {
		message.Language = *row.Language
	}
	if row.Verifier != nil {
		message.Verifier = *row.Verifier
	}
	if row.RefusalReason != nil {
		message.RefusalReason = *row.RefusalReason
	}
	return message
}

func emptyIfNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func clampLatency(value time.Duration) int32 {
	ms := value.Milliseconds()
	if ms < 0 {
		return 0
	}
	if ms > 1<<31-1 {
		return 1<<31 - 1
	}
	return int32(ms)
}
