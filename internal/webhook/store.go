package webhook

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/benfaruna/medverify/internal/platform/db/dbgen"
)

// DBStore persists endpoints and deliveries.
type DBStore struct {
	pool    *pgxpool.Pool
	queries *dbgen.Queries
}

// NewDBStore builds a store around an existing pool.
func NewDBStore(pool *pgxpool.Pool) *DBStore {
	return &DBStore{pool: pool, queries: dbgen.New(pool)}
}

// ListDue returns pending deliveries whose next attempt is due.
func (s *DBStore) ListDue(ctx context.Context, limit int32) ([]Pending, error) {
	rows, err := s.queries.ListDueWebhookDeliveries(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("list due deliveries: %w", err)
	}
	items := make([]Pending, 0, len(rows))
	for _, row := range rows {
		items = append(items, Pending{
			ID:         row.ID,
			EndpointID: row.EndpointID,
			ClientID:   row.ClientID,
			Attempts:   row.Attempts,
			URL:        row.Url,
			Secret:     row.Secret,
			Payload:    row.Payload,
		})
	}
	return items, nil
}

// MarkDelivered records a successful attempt.
func (s *DBStore) MarkDelivered(ctx context.Context, id uuid.UUID, statusCode int32) error {
	return s.queries.MarkWebhookDelivered(ctx, dbgen.MarkWebhookDeliveredParams{ID: id, StatusCode: &statusCode})
}

// Retry schedules the next attempt.
func (s *DBStore) Retry(ctx context.Context, id uuid.UUID, next time.Time, statusCode int32, message string) error {
	code := statusCode
	return s.queries.RetryWebhookDelivery(ctx, dbgen.RetryWebhookDeliveryParams{
		ID:            id,
		NextAttemptAt: next,
		StatusCode:    &code,
		LastError:     &message,
	})
}

// DeadLetter stops retrying.
func (s *DBStore) DeadLetter(ctx context.Context, id uuid.UUID, statusCode int32, message string) error {
	code := statusCode
	return s.queries.DeadLetterWebhookDelivery(ctx, dbgen.DeadLetterWebhookDeliveryParams{
		ID:         id,
		StatusCode: &code,
		LastError:  &message,
	})
}

// Replay puts a dead delivery back in the queue.
func (s *DBStore) Replay(ctx context.Context, id uuid.UUID) (bool, error) {
	affected, err := s.queries.ReplayWebhookDelivery(ctx, id)
	if err != nil {
		return false, fmt.Errorf("replay delivery: %w", err)
	}
	return affected > 0, nil
}

// ActiveEndpoints returns the endpoints a client has configured.
func (s *DBStore) ActiveEndpoints(ctx context.Context, clientID int64) ([]dbgen.WebhookEndpoint, error) {
	return s.queries.ListWebhookEndpoints(ctx, clientID)
}

// CreateEndpoint registers a webhook target. The URL is validated before it is
// stored so an unsafe target never becomes a delivery row.
func (s *DBStore) CreateEndpoint(ctx context.Context, clientID int64, rawURL, secret string) (dbgen.WebhookEndpoint, error) {
	if _, err := ValidateURL(rawURL); err != nil {
		return dbgen.WebhookEndpoint{}, err
	}
	return s.queries.CreateWebhookEndpoint(ctx, dbgen.CreateWebhookEndpointParams{
		ClientID: clientID,
		Url:      rawURL,
		Secret:   secret,
	})
}

// Enqueue creates a delivery row for every active endpoint a client owns. The
// unique constraint makes a replayed completion a no-op instead of a duplicate.
func (s *DBStore) Enqueue(ctx context.Context, clientID int64, scanID uuid.UUID, eventType string, payload []byte) (int, error) {
	endpoints, err := s.queries.ListWebhookEndpoints(ctx, clientID)
	if err != nil {
		return 0, fmt.Errorf("list webhook endpoints: %w", err)
	}
	created := 0
	for _, endpoint := range endpoints {
		_, err := s.queries.InsertWebhookDelivery(ctx, dbgen.InsertWebhookDeliveryParams{
			EndpointID: endpoint.ID,
			ClientID:   clientID,
			ScanID:     scanID,
			EventType:  eventType,
			Payload:    payload,
		})
		if err != nil {
			// A duplicate is the expected outcome of a replayed completion.
			continue
		}
		created++
	}
	return created, nil
}
