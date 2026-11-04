package assistant

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/benfaruna/medverify/internal/llm"
	"github.com/benfaruna/medverify/internal/platform/auth"
	"github.com/benfaruna/medverify/internal/platform/httpx"
	"github.com/benfaruna/medverify/internal/scan"
)

const defaultMaxQuestionBody = 8 << 10

// HTTPHandler serves the per-scan question and transcript endpoints.
type HTTPHandler struct {
	service      *Service
	logger       *slog.Logger
	maxBodyBytes int64
}

// HTTPOptions configures the endpoints.
type HTTPOptions struct {
	Service      *Service
	Logger       *slog.Logger
	MaxBodyBytes int64
}

// NewHTTPHandler builds the assistant endpoints.
func NewHTTPHandler(opts HTTPOptions) *HTTPHandler {
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	maxBody := opts.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = defaultMaxQuestionBody
	}
	return &HTTPHandler{service: opts.Service, logger: logger, maxBodyBytes: maxBody}
}

type askRequest struct {
	Question string `json:"question"`
}

// Ask handles POST /v1/scans/{id}/questions.
func (h *HTTPHandler) Ask(w http.ResponseWriter, r *http.Request) {
	scanID, ok := h.scanID(w, r)
	if !ok {
		return
	}
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusUnauthorized, httpx.CodeUnauthorized, "A valid X-API-Key header is required."))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, h.maxBodyBytes)
	var body askRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, h.maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusBadRequest, httpx.CodeBadRequest, "The body must be JSON with a question field."))
		return
	}
	if strings.TrimSpace(body.Question) == "" {
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusBadRequest, httpx.CodeBadRequest, "A question is required."))
		return
	}

	answer, err := h.service.Ask(r.Context(), AskRequest{
		ScanID: scanID, ClientKey: principal.TenantKey(), Question: body.Question,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, answer)
}

// Conversation handles GET /v1/scans/{id}/conversation.
func (h *HTTPHandler) Conversation(w http.ResponseWriter, r *http.Request) {
	scanID, ok := h.scanID(w, r)
	if !ok {
		return
	}
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusUnauthorized, httpx.CodeUnauthorized, "A valid X-API-Key header is required."))
		return
	}
	view, err := h.service.Conversation(r.Context(), scanID, principal.TenantKey())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *HTTPHandler) scanID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusBadRequest, httpx.CodeBadRequest, "The scan ID is not a valid UUID."))
		return uuid.Nil, false
	}
	return id, true
}

func (h *HTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrQuestionEmpty):
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusBadRequest, httpx.CodeBadRequest, "A question is required."))
	case errors.Is(err, ErrQuestionTooLong):
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, httpx.CodeBadRequest, "The question is too long."))
	case errors.Is(err, scan.ErrScanNotFound):
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusNotFound, httpx.CodeNotFound, "No scan exists with this ID."))
	case errors.Is(err, ErrScanNotReady):
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusConflict, httpx.CodeConflict, "The scan has not finished and cannot answer questions yet."))
	case errors.Is(err, ErrTurnLimit):
		w.Header().Set("Retry-After", "3600")
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusTooManyRequests, httpx.CodeRateLimited, "This conversation has reached its turn limit."))
	case errors.Is(err, ErrConversationExpired), errors.Is(err, ErrConversationMissing):
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusGone, httpx.CodeNotFound, "This conversation is no longer available."))
	case llm.IsTransient(err):
		w.Header().Set("Retry-After", "5")
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusServiceUnavailable, httpx.CodeUnavailable, "The assistant is temporarily unavailable. Please retry."))
	default:
		// Never log the question or any model text.
		h.logger.ErrorContext(r.Context(), "assistant request failed", slog.Any("err", err))
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusInternalServerError, httpx.CodeInternalError, "The question could not be answered."))
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
