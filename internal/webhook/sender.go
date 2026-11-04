package webhook

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// DestinationGuard decides whether a delivery may be sent to a URL. The default
// is SSRFGuard; tests inject a permissive one to talk to a local server.
type DestinationGuard interface {
	Check(ctx context.Context, target *url.URL) error
}

// SSRFGuard rejects targets that could reach an internal address. It is the
// default guard and must not be relaxed in production.
type SSRFGuard struct {
	// Resolver resolves hostnames. Nil uses the system resolver.
	Resolver Resolver
}

// Check validates the scheme, shape, and resolved addresses of a target.
func (g SSRFGuard) Check(ctx context.Context, target *url.URL) error {
	if _, err := ValidateURL(target.String()); err != nil {
		return err
	}
	return CheckResolution(ctx, g.Resolver, target.Hostname())
}

// Sender delivers one signed payload.
type Sender struct {
	Client *http.Client
	// Guard validates the destination. Nil uses SSRFGuard.
	Guard DestinationGuard
	// Timeout bounds a single attempt. It is short on purpose: a slow receiver
	// must not hold a worker.
	Timeout   time.Duration
	UserAgent string
	Now       func() time.Time
}

// NewSender builds a sender with the SSRF guard wired into its transport.
func NewSender(timeout time.Duration) *Sender {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         dialer(timeout).DialContext,
		TLSHandshakeTimeout: timeout,
		MaxIdleConns:        16,
		IdleConnTimeout:     30 * time.Second,
	}
	return &Sender{
		Client: &http.Client{
			Transport: transport,
			Timeout:   timeout,
			// A redirect could point anywhere, including inward. Treat any
			// 3xx as a failed delivery instead of following it.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		Guard:     SSRFGuard{},
		Timeout:   timeout,
		UserAgent: "MedVerifyNG-Webhook/1.0",
		Now:       time.Now,
	}
}

// Result is the outcome of one attempt.
type Result struct {
	StatusCode int
	// Retryable is true for timeouts, transport errors, 429, and 5xx. A 4xx
	// other than 429 means the endpoint is misconfigured and retrying will not
	// help.
	Retryable bool
	Err       error
}

// Delivery is one event to send.
type Delivery struct {
	ID        string
	EventType string
	URL       string
	Secret    string
	Payload   []byte
}

// Send posts one delivery with signature headers.
func (s *Sender) Send(ctx context.Context, delivery Delivery) Result {
	target, err := url.Parse(delivery.URL)
	if err != nil {
		return Result{Retryable: false, Err: ErrUnsafeURL}
	}
	guard := s.Guard
	if guard == nil {
		guard = SSRFGuard{}
	}
	if err := guard.Check(ctx, target); err != nil {
		return Result{Retryable: false, Err: err}
	}

	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	sentAt := now()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, delivery.URL, bytes.NewReader(delivery.Payload))
	if err != nil {
		return Result{Retryable: false, Err: err}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(HeaderSignature, Sign(delivery.Secret, sentAt, delivery.Payload))
	request.Header.Set(HeaderTimestamp, fmt.Sprintf("%d", sentAt.Unix()))
	request.Header.Set(HeaderEvent, delivery.EventType)
	request.Header.Set(HeaderID, delivery.ID)
	if s.UserAgent != "" {
		request.Header.Set("User-Agent", s.UserAgent)
	}

	client := s.Client
	if client == nil {
		client = NewSender(s.Timeout).Client
	}

	response, err := client.Do(request)
	if err != nil {
		return Result{Retryable: true, Err: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		_ = response.Body.Close()
	}()

	return Result{StatusCode: response.StatusCode, Retryable: retryableStatus(response.StatusCode)}
}

func retryableStatus(status int) bool {
	switch {
	case status == http.StatusTooManyRequests:
		return true
	case status >= 500:
		return true
	default:
		return false
	}
}
