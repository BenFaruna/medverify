package webhook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeStore records dispatcher decisions.
type fakeStore struct {
	due       []Pending
	delivered []uuid.UUID
	retried   []time.Time
	dead      []uuid.UUID
	statuses  []int32
}

func (s *fakeStore) ListDue(ctx context.Context, limit int32) ([]Pending, error) {
	return s.due, nil
}

func (s *fakeStore) MarkDelivered(ctx context.Context, id uuid.UUID, statusCode int32) error {
	s.delivered = append(s.delivered, id)
	s.statuses = append(s.statuses, statusCode)
	return nil
}

func (s *fakeStore) Retry(ctx context.Context, id uuid.UUID, next time.Time, statusCode int32, message string) error {
	s.retried = append(s.retried, next)
	s.statuses = append(s.statuses, statusCode)
	return nil
}

func (s *fakeStore) DeadLetter(ctx context.Context, id uuid.UUID, statusCode int32, message string) error {
	s.dead = append(s.dead, id)
	s.statuses = append(s.statuses, statusCode)
	return nil
}

// loopbackGuard lets a test talk to its own httptest server. The production
// guard is exercised separately in guard_test.go and is never relaxed here.
type loopbackGuard struct{}

func (loopbackGuard) Check(ctx context.Context, target *url.URL) error {
	if target.Scheme != "http" && target.Scheme != "https" {
		return ErrUnsafeURL
	}
	return nil
}

func senderFor(t *testing.T, server *httptest.Server, now time.Time) *Sender {
	t.Helper()

	sender := NewSender(2 * time.Second)
	sender.Client = server.Client()
	sender.Client.Timeout = 2 * time.Second
	sender.Client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	sender.Guard = loopbackGuard{}
	sender.Now = func() time.Time { return now }
	return sender
}

func pending(t *testing.T, url string) Pending {
	t.Helper()
	return Pending{
		ID:         uuid.New(),
		EndpointID: 1,
		ClientID:   1,
		Attempts:   0,
		URL:        url,
		Secret:     "s3cret",
		Payload:    []byte(`{"event":"scan.completed"}`),
	}
}

func TestDispatcherDeliversOnSuccess(t *testing.T) {
	t.Parallel()

	var gotSignature string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSignature = r.Header.Get(HeaderSignature)
		if err := Verify("s3cret", r.Header.Get(HeaderTimestamp), gotSignature, []byte(`{"event":"scan.completed"}`), 0); err != nil {
			t.Errorf("receiver could not verify the delivery: %v", err)
		}
		if r.Header.Get(HeaderEvent) != "scan.completed" {
			t.Errorf("event header = %q", r.Header.Get(HeaderEvent))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	store := &fakeStore{}
	item := pending(t, server.URL)
	store.due = []Pending{item}

	dispatcher := &Dispatcher{Store: store, Sender: senderFor(t, server, time.Now())}
	handled, err := dispatcher.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if handled != 1 {
		t.Fatalf("handled = %d, want 1", handled)
	}
	if len(store.delivered) != 1 {
		t.Fatalf("delivered = %d, want 1 (retried=%d dead=%d)", len(store.delivered), len(store.retried), len(store.dead))
	}
	if gotSignature == "" {
		t.Fatal("the delivery must carry a signature header")
	}
}

func TestDispatcherRetriesOnServerError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	store := &fakeStore{}
	store.due = []Pending{pending(t, server.URL)}

	dispatcher := &Dispatcher{Store: store, Sender: senderFor(t, server, time.Now())}
	if _, err := dispatcher.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(store.retried) != 1 {
		t.Fatalf("retried = %d, want 1", len(store.retried))
	}
	if len(store.dead) != 0 {
		t.Fatalf("dead = %d, want 0 on a first 500", len(store.dead))
	}
}

func TestDispatcherDeadLettersAfterTheLimit(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	store := &fakeStore{}
	item := pending(t, server.URL)
	item.Attempts = DefaultMaxAttempts - 1
	store.due = []Pending{item}

	dispatcher := &Dispatcher{Store: store, Sender: senderFor(t, server, time.Now())}
	if _, err := dispatcher.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(store.dead) != 1 {
		t.Fatalf("dead = %d, want 1 at the attempt limit", len(store.dead))
	}
	if len(store.retried) != 0 {
		t.Fatalf("retried = %d, want 0 at the attempt limit", len(store.retried))
	}
}

func TestDispatcherDeadLettersOnPermanentClientError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	store := &fakeStore{}
	store.due = []Pending{pending(t, server.URL)}

	dispatcher := &Dispatcher{Store: store, Sender: senderFor(t, server, time.Now())}
	if _, err := dispatcher.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(store.dead) != 1 {
		t.Fatal("a 404 will never succeed, so it must dead-letter instead of retrying")
	}
}

func TestDispatcherRequiresAStoreAndSender(t *testing.T) {
	t.Parallel()

	dispatcher := &Dispatcher{}
	if _, err := dispatcher.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce must refuse to run without a store and sender")
	}
}
