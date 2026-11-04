package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/benfaruna/medverify/internal/scan"
)

// EventScanCompleted is the only event this build emits.
const EventScanCompleted = "scan.completed"

// Notifier enqueues scan.completed deliveries. It satisfies
// scan.CompletionNotifier.
type Notifier struct {
	Store  *DBStore
	Logger *slog.Logger
	Now    func() time.Time
}

// ScanCompleted records one delivery per active endpoint the client owns.
//
// The payload is the public scan view, so a webhook can never carry the
// internal score. Scans from the legacy single-key mode have no client row and
// therefore no endpoints.
func (n *Notifier) ScanCompleted(ctx context.Context, scanned scan.Scan) error {
	if n.Store == nil || scanned.APIClientID == nil {
		return nil
	}

	payload, err := json.Marshal(struct {
		Event     string    `json:"event"`
		Delivered time.Time `json:"occurred_at"`
		Scan      scan.View `json:"scan"`
	}{
		Event:     EventScanCompleted,
		Delivered: n.now(),
		Scan:      scanned.View(nil),
	})
	if err != nil {
		return fmt.Errorf("encode scan.completed payload: %w", err)
	}

	created, err := n.Store.Enqueue(ctx, *scanned.APIClientID, scanned.ID, EventScanCompleted, payload)
	if err != nil {
		return err
	}
	if created > 0 {
		n.logger().InfoContext(ctx, "queued scan.completed deliveries",
			slog.String("scan_id", scanned.ID.String()),
			slog.Int("deliveries", created),
		)
	}
	return nil
}

func (n *Notifier) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

func (n *Notifier) logger() *slog.Logger {
	if n.Logger != nil {
		return n.Logger
	}
	return slog.New(slog.DiscardHandler)
}
