package scan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/benfaruna/medverify/internal/platform/auth"
	"github.com/benfaruna/medverify/internal/platform/httpx"
	"github.com/benfaruna/medverify/internal/verdict"
)

// HeaderIdempotencyKey makes a POST /v1/scans retry-safe.
const HeaderIdempotencyKey = "Idempotency-Key"

const (
	maxFieldBytes      = 1024
	maxIdempotencyKey  = 255
	detachedCreateTime = 30 * time.Second
)

// HTTPHandler serves the scan upload and lookup endpoints.
type HTTPHandler struct {
	service         *Service
	logger          *slog.Logger
	maxImages       int
	maxImageBytes   int64
	maxRequestBytes int64
	syncWait        time.Duration
	reasons         *verdict.Catalog
}

// HTTPOptions configures the scan endpoints. ReasonCatalog supplies the
// reviewed wording for reason codes; nil keeps the embedded English catalog.
type HTTPOptions struct {
	Service         *Service
	Logger          *slog.Logger
	MaxImages       int
	MaxImageBytes   int64
	MaxRequestBytes int64
	SyncWait        time.Duration
	ReasonCatalog   *verdict.Catalog
}

// NewHTTPHandler builds the scan endpoints.
func NewHTTPHandler(opts HTTPOptions) *HTTPHandler {
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	catalog := opts.ReasonCatalog
	if catalog == nil {
		loaded, err := verdict.LoadCatalog("")
		if err != nil {
			// The embedded catalog is compiled in and covered by tests, so this
			// is unreachable in a working build. Log it and ship codes without
			// sentences rather than failing every scan request.
			logger.Error("scan reason catalog unavailable", slog.Any("err", err))
		} else {
			catalog = &loaded
		}
	}
	return &HTTPHandler{
		service:         opts.Service,
		logger:          logger,
		maxImages:       opts.MaxImages,
		maxImageBytes:   opts.MaxImageBytes,
		maxRequestBytes: opts.MaxRequestBytes,
		syncWait:        opts.SyncWait,
		reasons:         catalog,
	}
}

// Create handles POST /v1/scans.
func (h *HTTPHandler) Create(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusUnauthorized, httpx.CodeUnauthorized, "A valid X-API-Key header is required."))
		return
	}

	if h.maxRequestBytes > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, h.maxRequestBytes)
	}

	hasher := sha256.New()
	original := r.Body
	r.Body = &teeReadCloser{Reader: io.TeeReader(original, hasher), Closer: original}

	upload, err := h.parseUpload(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	// multipart may stop at the final boundary; drain the epilogue so the hash
	// covers the same bytes a retry would send.
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		h.writeError(w, r, err)
		return
	}
	requestHash := hex.EncodeToString(hasher.Sum(nil))

	// Creation continues even if the client disconnects: the job is independent
	// of the HTTP request. The detached context still has its own cap.
	createCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), detachedCreateTime)
	defer cancel()

	scan, created, err := h.service.Create(createCtx, CreateRequest{
		ClientKey:      principal.TenantKey(),
		APIClientID:    optionalClientID(principal),
		Sandbox:        principal.Sandbox,
		IdempotencyKey: upload.idempotencyKey,
		RequestHash:    requestHash,
		Language:       upload.language,
		LocationCell:   upload.locationCell,
		Images:         upload.images,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	waitCtx, waitCancel := context.WithTimeout(r.Context(), h.syncWait)
	defer waitCancel()
	final, terminal, waitErr := h.service.Wait(waitCtx, scan.ID)
	if waitErr != nil {
		if !errors.Is(waitErr, context.DeadlineExceeded) && !errors.Is(waitErr, context.Canceled) {
			h.writeError(w, r, waitErr)
			return
		}
		// The client went away before the job reached a terminal state. Answer
		// with the scan that was just created; Wait returns a zero value here,
		// which would hand the client an empty id.
		final, terminal = scan, false
	}

	status := http.StatusAccepted
	if terminal {
		status = http.StatusCreated
	}
	w.Header().Set("Location", "/v1/scans/"+scan.ID.String())
	writeJSON(w, status, final.View(h.reasons))

	h.logger.InfoContext(r.Context(), "scan uploaded",
		slog.String("scan_id", scan.ID.String()),
		slog.Bool("created", created),
		slog.Bool("terminal", terminal),
		slog.Int("images", len(upload.images)),
	)
}

// Get handles GET /v1/scans/{id}.
func (h *HTTPHandler) Get(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusUnauthorized, httpx.CodeUnauthorized, "A valid X-API-Key header is required."))
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusBadRequest, httpx.CodeBadRequest, "The scan ID is not a valid UUID."))
		return
	}

	scan, err := h.service.GetForTenant(r.Context(), id, principal.TenantKey())
	if errors.Is(err, ErrScanNotFound) {
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusNotFound, httpx.CodeNotFound, "No scan exists with this ID."))
		return
	}
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, scan.View(h.reasons))
}

// optionalClientID returns the database client id for per-client scans. The
// legacy single-key mode has no row, so the scan keeps a NULL client id and is
// scoped by its tenant key instead.
func optionalClientID(principal auth.Principal) *int64 {
	if principal.ClientID == 0 {
		return nil
	}
	id := principal.ClientID
	return &id
}

type upload struct {
	images         [][]byte
	idempotencyKey string
	language       string
	locationCell   string
}

func (h *HTTPHandler) parseUpload(r *http.Request) (upload, error) {
	var result upload
	result.idempotencyKey = strings.TrimSpace(r.Header.Get(HeaderIdempotencyKey))
	if len(result.idempotencyKey) > maxIdempotencyKey {
		return upload{}, fmt.Errorf("%w: idempotency key is too long", ErrInvalidIdempotencyKey)
	}

	reader, err := r.MultipartReader()
	if err != nil {
		return upload{}, fmt.Errorf("%w: expected a multipart/form-data body", ErrBadUpload)
	}

	var latitude, longitude string
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return upload{}, err
		}

		switch part.FormName() {
		case "images":
			if len(result.images) >= h.maxImages {
				_ = part.Close()
				return upload{}, ErrTooManyImages
			}
			data, err := readLimited(part, h.maxImageBytes)
			_ = part.Close()
			if err != nil {
				return upload{}, err
			}
			result.images = append(result.images, data)
		case "latitude":
			latitude, err = readField(part)
		case "longitude":
			longitude, err = readField(part)
		case "language":
			result.language, err = readField(part)
		default:
			_, err = io.Copy(io.Discard, io.LimitReader(part, maxFieldBytes))
			_ = part.Close()
		}
		if err != nil {
			return upload{}, err
		}
	}

	if len(result.images) == 0 {
		return upload{}, ErrNoImages
	}

	cell, err := ParseLocation(latitude, longitude)
	if err != nil {
		return upload{}, err
	}
	result.locationCell = cell

	language, err := NormalizeLanguage(result.language)
	if err != nil {
		return upload{}, err
	}
	result.language = language
	return result, nil
}

// ErrBadUpload marks a body that is not valid multipart form data.
var ErrBadUpload = errors.New("invalid multipart upload")

// ErrInvalidIdempotencyKey marks a key that is too long to store.
var ErrInvalidIdempotencyKey = errors.New("invalid idempotency key")

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		return io.ReadAll(r)
	}
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, ErrImageTooLarge
	}
	return data, nil
}

func readField(r io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxFieldBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxFieldBytes {
		return "", ErrBadUpload
	}
	return strings.TrimSpace(string(data)), nil
}

func (h *HTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var maxBytes *http.MaxBytesError
	switch {
	case errors.As(err, &maxBytes):
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusRequestEntityTooLarge, httpx.CodePayloadTooLarge, "The upload is larger than this endpoint allows."))
	case errors.Is(err, ErrNoImages):
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusBadRequest, httpx.CodeBadRequest, "At least one image is required."))
	case errors.Is(err, ErrTooManyImages):
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusBadRequest, httpx.CodeTooManyImages, fmt.Sprintf("A scan accepts at most %d images.", h.maxImages)))
	case errors.Is(err, ErrUnsupportedFormat):
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusUnsupportedMediaType, httpx.CodeUnsupportedFormat, "Only JPEG, PNG, and WebP images are accepted."))
	case errors.Is(err, ErrImageTooLarge):
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusRequestEntityTooLarge, httpx.CodeImageTooLarge, "An image is larger than this endpoint allows."))
	case errors.Is(err, ErrCorruptImage):
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusBadRequest, httpx.CodeCorruptImage, "An image could not be read."))
	case errors.Is(err, ErrInvalidLocation):
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusBadRequest, httpx.CodeInvalidLocation, "Location must be a valid latitude and longitude pair."))
	case errors.Is(err, ErrInvalidLanguage):
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusBadRequest, httpx.CodeInvalidLanguage, "Language must be a short language tag such as en or en-GB."))
	case errors.Is(err, ErrInvalidIdempotencyKey):
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusBadRequest, httpx.CodeBadRequest, "The Idempotency-Key header is too long."))
	case errors.Is(err, ErrBadUpload):
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusBadRequest, httpx.CodeBadRequest, "The request must be multipart/form-data."))
	case errors.Is(err, ErrIdempotencyReused):
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, httpx.CodeConflict, "This Idempotency-Key was already used with a different request."))
	case errors.Is(err, ErrScanNotFound):
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusNotFound, httpx.CodeNotFound, "No scan exists with this ID."))
	default:
		h.logger.ErrorContext(r.Context(), "scan request failed", slog.Any("err", err))
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusInternalServerError, httpx.CodeInternalError, "The scan could not be created."))
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// teeReadCloser keeps the original body's Close while hashing every byte read.
type teeReadCloser struct {
	io.Reader
	io.Closer
}
