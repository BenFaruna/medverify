package webhook

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// DefaultMaxAttempts bounds retries before a delivery is dead-lettered. It is a
// constant rather than configuration so a receiver cannot be hammered forever
// by a bad deploy.
const DefaultMaxAttempts = 8

// baseBackoff is the first retry delay; each attempt doubles it up to
// maxBackoff. The total window is roughly six hours across eight attempts.
const (
	baseBackoff = 30 * time.Second
	maxBackoff  = 2 * time.Hour
)

// Backoff returns the delay before the next attempt. attempt is 1-based.
func Backoff(attempt int32) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	// Clamp the exponent before converting: 2^(n-1) overflows a nanosecond
	// Duration well before the attempt limit is reached.
	shift := float64(attempt - 1)
	if shift > 32 {
		shift = 32
	}
	delay := time.Duration(float64(baseBackoff) * math.Pow(2, shift))
	if delay <= 0 || delay > maxBackoff {
		delay = maxBackoff
	}
	return delay
}

// Pending is one delivery the dispatcher has claimed.
type Pending struct {
	ID         uuid.UUID
	EndpointID int64
	ClientID   int64
	Attempts   int32
	URL        string
	Secret     string
	Payload    []byte
}

// Store is the persistence the dispatcher needs.
type Store interface {
	// ListDue returns deliveries whose next attempt is due.
	ListDue(ctx context.Context, limit int32) ([]Pending, error)
	MarkDelivered(ctx context.Context, id uuid.UUID, statusCode int32) error
	Retry(ctx context.Context, id uuid.UUID, nextAttemptAt time.Time, statusCode int32, message string) error
	DeadLetter(ctx context.Context, id uuid.UUID, statusCode int32, message string) error
}

// Dispatcher sends due deliveries.
type Dispatcher struct {
	Store       Store
	Sender      *Sender
	Logger      *slog.Logger
	MaxAttempts int32
	Now         func() time.Time
	Batch       int32
}

// RunOnce processes one batch of due deliveries and reports how many were
// handled. It never returns an error for a single failed delivery: a broken
// customer endpoint must not stop the others.
func (d *Dispatcher) RunOnce(ctx context.Context) (int, error) {
	if d.Store == nil || d.Sender == nil {
		return 0, errors.New("webhook: dispatcher needs a store and a sender")
	}
	batch := d.Batch
	if batch <= 0 {
		batch = 50
	}
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}

	due, err := d.Store.ListDue(ctx, batch)
	if err != nil {
		return 0, fmt.Errorf("list due webhook deliveries: %w", err)
	}

	for _, item := range due {
		if err := d.deliver(ctx, item, now()); err != nil {
			// Log the delivery id and endpoint id only; the secret and payload
			// never reach a log line.
			d.logger().ErrorContext(ctx, "webhook delivery bookkeeping failed",
				slog.String("delivery_id", item.ID.String()),
				slog.Int64("endpoint_id", item.EndpointID),
				slog.Any("err", err),
			)
		}
	}
	return len(due), nil
}

func (d *Dispatcher) deliver(ctx context.Context, item Pending, now time.Time) error {
	result := d.Sender.Send(ctx, Delivery{
		ID:        item.ID.String(),
		EventType: "scan.completed",
		URL:       item.URL,
		Secret:    item.Secret,
		Payload:   item.Payload,
	})

	maxAttempts := d.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = DefaultMaxAttempts
	}
	attempt := item.Attempts + 1

	// Only a 2xx counts as delivered. Anything else is a failed attempt, so a
	// 4xx can never be mistaken for success.
	if result.Err == nil && result.StatusCode >= 200 && result.StatusCode < 300 {
		return d.Store.MarkDelivered(ctx, item.ID, int32(result.StatusCode))
	}
	// A 4xx other than 429 means the endpoint is wrong, and a 3xx means the
	// receiver tried to redirect us somewhere we will not follow.
	permanent := result.Err == nil &&
		result.StatusCode >= 300 && result.StatusCode < 500 &&
		result.StatusCode != http.StatusTooManyRequests

	message := resultMessage(result)
	if permanent || attempt >= maxAttempts {
		return d.Store.DeadLetter(ctx, item.ID, int32(result.StatusCode), message)
	}
	return d.Store.Retry(ctx, item.ID, now.Add(Backoff(attempt)), int32(result.StatusCode), message)
}

func resultMessage(result Result) string {
	if result.Err != nil {
		return truncate(result.Err.Error())
	}
	return fmt.Sprintf("endpoint returned %d", result.StatusCode)
}

// truncate keeps a provider message short enough for a text column and drops
// any chance of an unbounded response body leaking into storage.
func truncate(message string) string {
	const limit = 200
	if len(message) <= limit {
		return message
	}
	return message[:limit]
}

func (d *Dispatcher) logger() *slog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	return slog.New(slog.DiscardHandler)
}
