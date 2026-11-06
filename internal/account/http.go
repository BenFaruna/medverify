package account

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/benfaruna/medverify/internal/platform/httpx"
)

// SessionCookieName is the browser cookie carrying the session token. The
// token is opaque and HttpOnly; no script on the page can read it.
const SessionCookieName = "mv_session"

// defaultMaxBodyBytes bounds a sign-in or sign-up body. The largest legitimate
// request is a password of MaxPasswordBytes plus an address and a name.
const defaultMaxBodyBytes = 8 << 10

// HTTPOptions configures the account endpoints.
type HTTPOptions struct {
	// Service holds the account rules. It is required.
	Service *Service
	Logger  *slog.Logger
	// SessionTTL sets the cookie lifetime. Zero uses the service's TTL.
	SessionTTL time.Duration
	// SecureCookie forces the Secure attribute even when the request arrived
	// over plain HTTP. A production deployment sets this; a local `go run` does
	// not, so the cookie still works on localhost.
	SecureCookie bool
	MaxBodyBytes int64
}

// HTTPHandler serves registration, sign-in, and key management. It is mounted
// outside the X-API-Key middleware: a person signs in once with a cookie and
// then reads or rotates the key their client code uses.
type HTTPHandler struct {
	service      *Service
	logger       *slog.Logger
	sessionTTL   time.Duration
	secureCookie bool
	maxBodyBytes int64
}

// NewHTTPHandler builds the account endpoints.
func NewHTTPHandler(opts HTTPOptions) *HTTPHandler {
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	ttl := opts.SessionTTL
	if ttl <= 0 && opts.Service != nil {
		ttl = opts.Service.SessionTTL()
	}
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	maxBody := opts.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = defaultMaxBodyBytes
	}
	return &HTTPHandler{
		service:      opts.Service,
		logger:       logger,
		sessionTTL:   ttl,
		secureCookie: opts.SecureCookie,
		maxBodyBytes: maxBody,
	}
}

type userResponse struct {
	ID          int64      `json:"id"`
	Email       string     `json:"email"`
	Name        string     `json:"name"`
	ClientID    int64      `json:"client_id"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

// keyResponse describes a key. Key is populated only in the response to the
// request that created it; a list never carries a secret.
type keyResponse struct {
	Key        string     `json:"key,omitempty"`
	Prefix     string     `json:"prefix"`
	Scopes     []string   `json:"scopes"`
	State      string     `json:"state"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

type sessionResponse struct {
	ExpiresAt time.Time `json:"expires_at"`
}

type registerResponse struct {
	User    userResponse    `json:"user"`
	Session sessionResponse `json:"session"`
	APIKey  keyResponse     `json:"api_key"`
}

type loginResponse struct {
	User    userResponse    `json:"user"`
	Session sessionResponse `json:"session"`
}

type meResponse struct {
	User userResponse `json:"user"`
}

type keysResponse struct {
	APIKeys []keyResponse `json:"api_keys"`
}

type rotateResponse struct {
	APIKey  keyResponse `json:"api_key"`
	Revoked int64       `json:"revoked"`
}

type registerRequest struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Register handles POST /v1/auth/register. It creates the account, its first
// API key, and a signed-in session. The full key appears in this response and
// nowhere else.
func (h *HTTPHandler) Register(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		h.internalError(w, r, errors.New("account service is not configured"))
		return
	}

	var body registerRequest
	if !h.decode(w, r, &body) {
		return
	}

	registration, err := h.service.Register(r.Context(), RegisterInput(body))
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	h.setSessionCookie(w, r, registration.Session)
	writeJSON(w, http.StatusCreated, registerResponse{
		User:    newUserResponse(registration.User),
		Session: sessionResponse{ExpiresAt: registration.Session.ExpiresAt},
		APIKey:  newIssuedKeyResponse(registration.Key),
	})
}

// Login handles POST /v1/auth/login.
func (h *HTTPHandler) Login(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		h.internalError(w, r, errors.New("account service is not configured"))
		return
	}

	var body loginRequest
	if !h.decode(w, r, &body) {
		return
	}

	user, session, err := h.service.Login(r.Context(), body.Email, body.Password)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	h.setSessionCookie(w, r, session)
	writeJSON(w, http.StatusOK, loginResponse{
		User:    newUserResponse(user),
		Session: sessionResponse{ExpiresAt: session.ExpiresAt},
	})
}

// Logout handles POST /v1/auth/logout. It revokes the session row as well as
// clearing the cookie: clearing alone would leave a token valid if it had been
// copied. Signing out without a session is not an error.
func (h *HTTPHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if h.service != nil {
		if token := sessionToken(r); token != "" {
			if err := h.service.Logout(r.Context(), token); err != nil {
				// The cookie is still cleared below; a stale row expires on its
				// own, so a database hiccup must not trap the person signed in.
				h.logger.WarnContext(r.Context(), "revoke session on logout", slog.Any("err", err))
			}
		}
	}
	h.clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// Me handles GET /v1/account.
func (h *HTTPHandler) Me(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		h.writeError(w, r, ErrNoSession)
		return
	}
	writeJSON(w, http.StatusOK, meResponse{User: newUserResponse(user)})
}

// ListKeys handles GET /v1/account/api-keys. Only metadata is returned.
func (h *HTTPHandler) ListKeys(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		h.writeError(w, r, ErrNoSession)
		return
	}

	keys, err := h.service.Keys(r.Context(), user)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	now := time.Now()
	out := make([]keyResponse, 0, len(keys))
	for _, key := range keys {
		out = append(out, keyResponse{
			Prefix:     displayPrefix(key.Prefix),
			Scopes:     key.Scopes,
			State:      key.State(now),
			CreatedAt:  key.CreatedAt,
			LastUsedAt: key.LastUsedAt,
			ExpiresAt:  key.ExpiresAt,
		})
	}
	writeJSON(w, http.StatusOK, keysResponse{APIKeys: out})
}

// RotateKey handles POST /v1/account/api-keys/rotate. The previous keys stop
// working immediately; the replacement is returned once.
func (h *HTTPHandler) RotateKey(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		h.writeError(w, r, ErrNoSession)
		return
	}

	key, revoked, err := h.service.Rotate(r.Context(), user)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	h.logger.InfoContext(r.Context(), "api key rotated",
		slog.Int64("user_id", user.ID),
		slog.Int64("client_id", user.ClientID),
		slog.Int("revoked", int(revoked)),
	)
	writeJSON(w, http.StatusOK, rotateResponse{
		APIKey:  newIssuedKeyResponse(key),
		Revoked: revoked,
	})
}

// RequireSession authenticates the session cookie and puts the account in the
// request context. It fails closed: a missing, expired, or revoked session is
// a 401, never an anonymous request.
func (h *HTTPHandler) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := h.currentUser(r)
		if err != nil {
			h.writeError(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), user)))
	})
}

func (h *HTTPHandler) currentUser(r *http.Request) (User, error) {
	if h.service == nil {
		return User{}, ErrNoSession
	}
	token := sessionToken(r)
	if token == "" {
		return User{}, ErrNoSession
	}
	return h.service.Authenticate(r.Context(), token)
}

type userContextKey struct{}

// WithUser attaches an authenticated account to a context.
func WithUser(ctx context.Context, user User) context.Context {
	return context.WithValue(ctx, userContextKey{}, user)
}

// UserFromContext returns the account RequireSession authenticated.
func UserFromContext(ctx context.Context) (User, bool) {
	user, ok := ctx.Value(userContextKey{}).(User)
	return user, ok
}

// CSRF guards cookie-authenticated writes. A session cookie is sent by the
// browser on any request to this origin, so a form on another site could
// otherwise rotate a key. SameSite=Lax already blocks a cross-site POST in
// current browsers; this is the server-side check that does not rely on the
// browser at all.
//
// A non-browser client with neither header is allowed through: it cannot have
// been tricked into sending a cookie it does not control.
func CSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}

		if site := r.Header.Get("Sec-Fetch-Site"); site != "" &&
			site != "same-origin" && site != "none" {
			forbidden(w, r, "This request came from another site.")
			return
		}

		if origin := r.Header.Get("Origin"); origin != "" {
			parsed, err := url.Parse(origin)
			if err != nil || !strings.EqualFold(parsed.Host, r.Host) {
				forbidden(w, r, "This request came from another origin.")
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func (h *HTTPHandler) decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			httpx.WriteProblem(w, r, httpx.NewProblem(
				http.StatusRequestEntityTooLarge,
				httpx.CodePayloadTooLarge,
				"That request is larger than this endpoint allows.",
			))
			return false
		}
		httpx.WriteProblem(w, r, httpx.NewProblem(
			http.StatusBadRequest,
			httpx.CodeBadRequest,
			"Send a JSON object with the expected fields.",
		))
		return false
	}
	// Reject a second JSON value so a malformed request cannot be half-read.
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		httpx.WriteProblem(w, r, httpx.NewProblem(
			http.StatusBadRequest,
			httpx.CodeBadRequest,
			"Send exactly one JSON object.",
		))
		return false
	}
	return true
}

func (h *HTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var invalid *ValidationError
	switch {
	case errors.As(err, &invalid):
		httpx.WriteProblem(w, r, httpx.NewProblem(
			http.StatusBadRequest,
			httpx.CodeBadRequest,
			invalid.Message,
		))
	case errors.Is(err, ErrEmailTaken):
		httpx.WriteProblem(w, r, httpx.NewProblem(
			http.StatusConflict,
			httpx.CodeConflict,
			"That email address already has an account.",
		))
	case errors.Is(err, ErrInvalidCredentials):
		httpx.WriteProblem(w, r, httpx.NewProblem(
			http.StatusUnauthorized,
			httpx.CodeUnauthorized,
			"Email or password is incorrect.",
		))
	case errors.Is(err, ErrNoSession):
		httpx.WriteProblem(w, r, httpx.NewProblem(
			http.StatusUnauthorized,
			httpx.CodeUnauthorized,
			"Sign in to continue.",
		))
	default:
		h.internalError(w, r, err)
	}
}

func (h *HTTPHandler) internalError(w http.ResponseWriter, r *http.Request, err error) {
	// The detail stays generic: an internal message can carry a query, a host,
	// or a secret fragment.
	h.logger.ErrorContext(r.Context(), "account request failed", slog.Any("err", err))
	httpx.WriteProblem(w, r, httpx.NewProblem(
		http.StatusInternalServerError,
		httpx.CodeInternalError,
		"Something went wrong on our side. Try again.",
	))
}

// setSessionCookie writes the session cookie. Secure is forced in production
// and otherwise follows the connection, so a local plain-HTTP run still works.
func (h *HTTPHandler) setSessionCookie(w http.ResponseWriter, r *http.Request, session Session) {
	maxAge := int(h.sessionTTL.Seconds())
	if maxAge <= 0 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    session.Token,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.secureCookie || r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		Expires:  session.ExpiresAt,
		MaxAge:   maxAge,
	})
}

func (h *HTTPHandler) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   h.secureCookie || r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(1, 0).UTC(),
		MaxAge:   -1,
	})
}

func sessionToken(r *http.Request) string {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cookie.Value)
}

func forbidden(w http.ResponseWriter, r *http.Request, detail string) {
	httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusForbidden, httpx.CodeForbidden, detail))
}

// newUserResponse copies an account into its wire form. The two structs share
// a field order, so the conversion is explicit rather than reflective.
func newUserResponse(user User) userResponse {
	return userResponse(user)
}

func newIssuedKeyResponse(key IssuedKey) keyResponse {
	return keyResponse{
		Key:       key.Key,
		Prefix:    key.Prefix,
		Scopes:    key.Scopes,
		State:     "active",
		CreatedAt: key.CreatedAt,
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	// Account responses carry personal data and, once, a secret. Never cache.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
