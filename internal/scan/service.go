package scan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/benfaruna/medverify/internal/storage"
	"github.com/benfaruna/medverify/internal/vision"
)

// Upload errors.
var (
	ErrNoImages          = errors.New("at least one image is required")
	ErrTooManyImages     = errors.New("too many images")
	ErrIdempotencyReused = errors.New("idempotency key was used with a different request")
)

// Enqueuer hands a created scan to the background queue.
type Enqueuer interface {
	EnqueueScan(ctx context.Context, scanID string) error
}

// ServiceOptions configures the scan pipeline.
type ServiceOptions struct {
	Store         *Store
	Objects       storage.Store
	Provider      vision.Provider
	Enqueuer      Enqueuer
	Logger        *slog.Logger
	MaxImages     int
	Limits        ImageLimits
	MinConfidence float64
	PollInterval  time.Duration
	Random        func() string
}

// Service orchestrates upload validation, storage, persistence, and the
// background extraction job. It never calls a real vision provider in phase 3.
type Service struct {
	store         *Store
	objects       storage.Store
	provider      vision.Provider
	enqueuer      Enqueuer
	logger        *slog.Logger
	maxImages     int
	limits        ImageLimits
	minConfidence float64
	pollInterval  time.Duration
	random        func() string
}

// NewService builds a service, applying safe defaults for omitted options.
func NewService(opts ServiceOptions) (*Service, error) {
	if opts.Store == nil {
		return nil, fmt.Errorf("scan service requires a store")
	}
	if opts.Objects == nil {
		return nil, fmt.Errorf("scan service requires object storage")
	}
	if opts.Provider == nil {
		return nil, fmt.Errorf("scan service requires a vision provider")
	}
	if opts.MaxImages <= 0 {
		opts.MaxImages = 4
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 100 * time.Millisecond
	}
	if opts.Random == nil {
		opts.Random = func() string { return uuid.NewString() }
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Service{
		store:         opts.Store,
		objects:       opts.Objects,
		provider:      opts.Provider,
		enqueuer:      opts.Enqueuer,
		logger:        logger,
		maxImages:     opts.MaxImages,
		limits:        opts.Limits,
		minConfidence: opts.MinConfidence,
		pollInterval:  opts.PollInterval,
		random:        opts.Random,
	}, nil
}

// CreateRequest is one validated upload request.
type CreateRequest struct {
	ClientKey      string
	IdempotencyKey string
	RequestHash    string
	Language       string
	LocationCell   string
	Images         [][]byte
}

// Create validates and stores the images, creates the scan, and enqueues the
// job. created is false when an Idempotency-Key replayed an earlier request.
func (s *Service) Create(ctx context.Context, request CreateRequest) (Scan, bool, error) {
	if s.enqueuer == nil {
		return Scan{}, false, fmt.Errorf("scan service has no job enqueuer")
	}
	if len(request.Images) == 0 {
		return Scan{}, false, ErrNoImages
	}
	if len(request.Images) > s.maxImages {
		return Scan{}, false, ErrTooManyImages
	}

	if request.IdempotencyKey != "" {
		existing, found, err := s.store.GetIdempotency(ctx, request.ClientKey, request.IdempotencyKey)
		if err != nil {
			return Scan{}, false, err
		}
		if found {
			if existing.RequestHash != request.RequestHash {
				return Scan{}, false, ErrIdempotencyReused
			}
			scan, err := s.store.GetScan(ctx, existing.ScanID)
			if err != nil {
				return Scan{}, false, err
			}
			return scan, false, nil
		}
	}

	scanID := uuid.New()
	prepared := make([]Image, 0, len(request.Images))
	preparedData := make([][]byte, 0, len(request.Images))
	for i, data := range request.Images {
		image, err := PrepareImage(data, s.limits)
		if err != nil {
			return Scan{}, false, err
		}
		prepared = append(prepared, Image{
			Position:    i + 1,
			StorageKey:  ObjectKey(scanID, s.random()),
			ContentType: image.ContentType,
			ByteSize:    len(image.Data),
			Width:       image.Width,
			Height:      image.Height,
			SHA256:      image.SHA256,
		})
		preparedData = append(preparedData, image.Data)
	}

	if err := s.uploadAll(ctx, prepared, preparedData); err != nil {
		s.cleanup(ctx, prepared)
		return Scan{}, false, err
	}

	scan, created, err := s.store.CreateScan(ctx, CreateInput{
		ID:             scanID,
		ClientKey:      request.ClientKey,
		IdempotencyKey: request.IdempotencyKey,
		RequestHash:    request.RequestHash,
		Language:       request.Language,
		LocationCell:   request.LocationCell,
		Images:         prepared,
	})
	if err != nil {
		s.cleanup(ctx, prepared)
		return Scan{}, false, err
	}
	if !created {
		// Lost the idempotency race: remove this request's objects and return
		// the scan that won.
		s.cleanup(ctx, prepared)
		existing, found, err := s.store.GetIdempotency(ctx, request.ClientKey, request.IdempotencyKey)
		if err != nil {
			return Scan{}, false, err
		}
		if !found {
			return Scan{}, false, fmt.Errorf("idempotency race lost but no record exists")
		}
		if existing.RequestHash != request.RequestHash {
			return Scan{}, false, ErrIdempotencyReused
		}
		winner, err := s.store.GetScan(ctx, existing.ScanID)
		if err != nil {
			return Scan{}, false, err
		}
		return winner, false, nil
	}

	if err := s.enqueuer.EnqueueScan(ctx, scan.ID.String()); err != nil {
		// The scan row is queued; a later reconciliation can enqueue it. Log the
		// ID, never image bytes or filenames.
		s.logger.ErrorContext(ctx, "scan created but job enqueue failed",
			slog.String("scan_id", scan.ID.String()),
			slog.Any("err", err),
		)
		return scan, true, fmt.Errorf("enqueue scan job: %w", err)
	}
	return scan, true, nil
}

// Wait polls until the scan reaches a terminal state or the context ends. The
// bool is false on timeout, so the handler can answer 202 and let the client
// poll GET.
func (s *Service) Wait(ctx context.Context, id uuid.UUID) (Scan, bool, error) {
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()

	for {
		scan, err := s.store.GetScan(ctx, id)
		if err != nil {
			return Scan{}, false, err
		}
		if scan.Status.Terminal() {
			return scan, true, nil
		}

		select {
		case <-ctx.Done():
			return scan, false, ctx.Err()
		case <-ticker.C:
		}
	}
}

// Get loads one scan.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Scan, error) {
	return s.store.GetScan(ctx, id)
}

// Process runs one extraction attempt. It is safe to call twice: the guarded
// claim and finish updates make duplicate jobs no-ops. A provider error marks
// the scan failed and is returned so River retries it.
func (s *Service) Process(ctx context.Context, rawID string) error {
	id, err := uuid.Parse(rawID)
	if err != nil {
		return fmt.Errorf("process scan: invalid id: %w", err)
	}

	scan, err := s.store.GetScan(ctx, id)
	if err != nil {
		return err
	}
	switch scan.Status {
	case StatusCompleted, StatusUnreadable:
		// Idempotent replay of a job that already finished.
		return nil
	case StatusFailed:
		requeued, err := s.store.RequeueFailedScan(ctx, id)
		if err != nil {
			return err
		}
		if !requeued {
			return nil
		}
	case StatusProcessing:
		// Another worker owns this scan.
		return nil
	case StatusQueued:
	}

	claimed, err := s.store.ClaimScan(ctx, id)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}

	images := make([]vision.Image, 0, len(scan.Images))
	for _, image := range scan.Images {
		data, err := s.objects.Get(ctx, image.StorageKey)
		if err != nil {
			s.finishFailed(ctx, id, fmt.Errorf("load stored image: %w", err))
			return fmt.Errorf("load stored image for scan %s: %w", id, err)
		}
		images = append(images, vision.Image{Position: image.Position, Data: data})
	}

	result, err := s.provider.Extract(ctx, vision.Request{
		ScanID:   id.String(),
		Language: scan.Language,
		Images:   images,
	})
	if errors.Is(err, vision.ErrUnreadable) {
		s.finish(ctx, id, StatusUnreadable, Result{Reasons: []byte("[]")})
		return nil
	}
	if err != nil {
		s.finishFailed(ctx, id, err)
		return fmt.Errorf("extract scan %s: %w", id, err)
	}
	if s.minConfidence > 0 && result.Confidence < s.minConfidence {
		// Low confidence is unreadable, never a guess.
		s.finish(ctx, id, StatusUnreadable, Result{
			Provider: result.Provider,
			Reasons:  []byte("[]"),
		})
		return nil
	}

	// Extracted stays null until phase 04 and the verdict until phase 07. The
	// placeholder result carries only the provider name and empty reasons.
	s.finish(ctx, id, StatusCompleted, Result{
		Provider: result.Provider,
		Reasons:  []byte("[]"),
	})
	s.logger.InfoContext(ctx, "scan processed",
		slog.String("scan_id", id.String()),
		slog.String("status", string(StatusCompleted)),
	)
	return nil
}

func (s *Service) finish(ctx context.Context, id uuid.UUID, status Status, result Result) {
	updated, err := s.store.FinishScan(ctx, id, status, result)
	if err != nil {
		s.logger.ErrorContext(ctx, "could not finish scan",
			slog.String("scan_id", id.String()),
			slog.String("status", string(status)),
			slog.Any("err", err),
		)
		return
	}
	if !updated {
		s.logger.WarnContext(ctx, "scan was already finished by another worker",
			slog.String("scan_id", id.String()),
		)
	}
}

func (s *Service) finishFailed(ctx context.Context, id uuid.UUID, cause error) {
	message := cause.Error()
	const limit = 500
	if len(message) > limit {
		message = message[:limit]
	}
	s.finish(ctx, id, StatusFailed, Result{Reasons: []byte("[]"), Error: message})
}

func (s *Service) uploadAll(ctx context.Context, images []Image, data [][]byte) error {
	for i, image := range images {
		if err := s.objects.Put(ctx, image.StorageKey, bytes.NewReader(data[i]), storage.PutOptions{
			ContentType: image.ContentType,
			Size:        int64(len(data[i])),
		}); err != nil {
			return fmt.Errorf("store scan image: %w", err)
		}
	}
	return nil
}

func (s *Service) cleanup(ctx context.Context, images []Image) {
	for _, image := range images {
		if err := s.objects.Delete(ctx, image.StorageKey); err != nil {
			s.logger.WarnContext(ctx, "could not delete orphaned scan image",
				slog.String("storage_key", image.StorageKey),
				slog.Any("err", err),
			)
		}
	}
}

// ObjectKey builds a random storage key. The client filename is never used.
func ObjectKey(scanID uuid.UUID, random string) string {
	return fmt.Sprintf("scans/%s/%s.jpg", scanID, random)
}
