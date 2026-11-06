package account

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/benfaruna/medverify/internal/platform/auth"
)

// Password and profile bounds. The minimum password length is deliberately
// above the current floor for consumer sites: these accounts hold an API key
// that can spend money and read personal scan history.
const (
	MinPasswordBytes = 12
	MaxPasswordBytes = 1024
	MaxEmailBytes    = 254
	MaxNameRunes     = 100
	// DefaultSessionTTL is used when a caller does not configure one.
	DefaultSessionTTL = 720 * time.Hour
)

var (
	// ErrEmailTaken is returned when the address already has an account. It is
	// reported to the caller because registration cannot be completed
	// otherwise; the HTTP layer answers 409.
	ErrEmailTaken = errors.New("email already registered")
	// ErrInvalidCredentials covers every sign-in failure, so a caller cannot
	// tell an unknown address from a wrong password.
	ErrInvalidCredentials = errors.New("invalid credentials")
	// ErrNoUser is the store's signal that a lookup found nothing.
	ErrNoUser = errors.New("no such user")
	// ErrNoSession is returned when a session token is absent, expired, or
	// revoked.
	ErrNoSession = errors.New("no valid session")
)

// ValidationError names the offending field so the client can point at it.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

// User is the account an authenticated caller acts as.
type User struct {
	ID          int64
	Email       string
	Name        string
	ClientID    int64
	CreatedAt   time.Time
	LastLoginAt *time.Time
}

// StoredUser is a User plus the password material a sign-in needs. It never
// leaves the package: the HTTP layer only sees User.
type StoredUser struct {
	User
	PasswordHash string
}

// APIKey is the non-secret description of a key. The full key value is not
// part of this type; it exists only in IssuedKey, once, at issue time.
type APIKey struct {
	ID         int64
	Prefix     string
	Scopes     []string
	Active     bool
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
	CreatedAt  time.Time
}

// State reduces the timestamps to the one word a person needs.
func (k APIKey) State(now time.Time) string {
	switch {
	case k.RevokedAt != nil:
		return "revoked"
	case !k.Active:
		return "inactive"
	case k.ExpiresAt != nil && !k.ExpiresAt.After(now):
		return "expired"
	default:
		return "active"
	}
}

// IssuedKey is a newly created key. The Key field is the only copy that will
// ever exist; it is shown once and never persisted.
type IssuedKey struct {
	Key       string
	Prefix    string
	Scopes    []string
	CreatedAt time.Time
}

// Session is the cookie value a browser holds plus its expiry.
type Session struct {
	Token     string
	ExpiresAt time.Time
}

// RegisterParams is one transaction's worth of registration rows: a client, a
// user, the first key, and the first session. Writing them together means a
// half-made account with no usable key can never exist.
type RegisterParams struct {
	Email            string
	Name             string
	PasswordHash     string
	ClientName       string
	KeyPrefix        string
	KeyHash          string
	Scopes           []string
	SessionTokenHash string
	SessionExpiresAt time.Time
}

// SessionParams creates one session for an existing user.
type SessionParams struct {
	UserID    int64
	TokenHash string
	ExpiresAt time.Time
}

// RotateParams replaces every active key on a client with one new key.
type RotateParams struct {
	ClientID  int64
	KeyPrefix string
	KeyHash   string
	Scopes    []string
}

// Store is the persistence seam. It is an interface so the handlers can be
// tested without a database, and so the transaction boundaries are visible in
// one place instead of spread across the HTTP layer.
type Store interface {
	// Register creates the client, user, first key, and first session in one
	// transaction. It returns ErrEmailTaken when the address exists.
	Register(ctx context.Context, params RegisterParams) (User, error)
	// UserByEmail returns the account for an address, or ErrNoUser.
	UserByEmail(ctx context.Context, email string) (StoredUser, error)
	// UserBySession resolves a hashed session token, or ErrNoSession.
	UserBySession(ctx context.Context, tokenHash string) (User, error)
	TouchLogin(ctx context.Context, userID int64) error
	TouchSession(ctx context.Context, tokenHash string) error
	CreateSession(ctx context.Context, params SessionParams) error
	RevokeSession(ctx context.Context, tokenHash string) error
	// Keys lists every key ever issued to a client, newest first.
	Keys(ctx context.Context, clientID int64) ([]APIKey, error)
	// RotateKey revokes the client's active keys and inserts the new one in one
	// transaction, returning how many keys it revoked.
	RotateKey(ctx context.Context, params RotateParams) (int64, APIKey, error)
}

// ServiceOptions configures the account service. Every field has a safe
// default; a service with only a Store works.
type ServiceOptions struct {
	Store Store
	// SessionTTL bounds how long a sign-in lasts. Zero uses DefaultSessionTTL.
	SessionTTL time.Duration
	// Now is a clock seam for tests. Nil uses time.Now.
	Now func() time.Time
}

// Service holds the account rules that do not belong in SQL or in HTTP.
type Service struct {
	store      Store
	sessionTTL time.Duration
	now        func() time.Time
}

// NewService builds a service. A nil store is kept so the constructor never
// panics; every method reports a failure instead of dereferencing it.
func NewService(opts ServiceOptions) *Service {
	ttl := opts.SessionTTL
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{store: opts.Store, sessionTTL: ttl, now: now}
}

// SessionTTL reports the configured session lifetime.
func (s *Service) SessionTTL() time.Duration { return s.sessionTTL }

// RegisterInput is the parsed registration form.
type RegisterInput struct {
	Email    string
	Name     string
	Password string
}

// Registration is what a new account gets back: the account, its first key
// (shown once), and a signed-in session.
type Registration struct {
	User    User
	Key     IssuedKey
	Session Session
}

// Register creates an account and its first API key. The key is returned as
// plaintext exactly once; nothing in the store can reproduce it.
func (s *Service) Register(ctx context.Context, input RegisterInput) (Registration, error) {
	email, err := normalizeEmail(input.Email)
	if err != nil {
		return Registration{}, err
	}
	name, err := normalizeName(input.Name)
	if err != nil {
		return Registration{}, err
	}
	if err := checkPassword(input.Password); err != nil {
		return Registration{}, err
	}
	if s.store == nil {
		return Registration{}, errors.New("account store is not configured")
	}

	passwordHash, err := HashPassword(input.Password)
	if err != nil {
		return Registration{}, err
	}
	clientName, err := randomClientName()
	if err != nil {
		return Registration{}, fmt.Errorf("name api client: %w", err)
	}
	fullKey, keyHash, prefix, _, err := auth.GenerateAPIKey()
	if err != nil {
		return Registration{}, fmt.Errorf("generate api key: %w", err)
	}
	token, err := randomToken()
	if err != nil {
		return Registration{}, fmt.Errorf("generate session token: %w", err)
	}
	expires := s.now().Add(s.sessionTTL)

	user, err := s.store.Register(ctx, RegisterParams{
		Email:            email,
		Name:             name,
		PasswordHash:     passwordHash,
		ClientName:       clientName,
		KeyPrefix:        prefix,
		KeyHash:          keyHash,
		Scopes:           auth.AllScopes,
		SessionTokenHash: HashSessionToken(token),
		SessionExpiresAt: expires,
	})
	if err != nil {
		return Registration{}, err
	}

	return Registration{
		User: user,
		Key: IssuedKey{
			Key:       fullKey,
			Prefix:    displayPrefix(prefix),
			Scopes:    append([]string(nil), auth.AllScopes...),
			CreatedAt: user.CreatedAt,
		},
		Session: Session{Token: token, ExpiresAt: expires},
	}, nil
}

// Login verifies a password and starts a session. It spends the same PBKDF2
// work on an unknown address as on a known one, so timing does not disclose
// which addresses have accounts.
func (s *Service) Login(ctx context.Context, email, password string) (User, Session, error) {
	normalized, err := normalizeEmail(email)
	if err != nil {
		// The address cannot be an account, but it must not be cheaper than a
		// real attempt either.
		_ = VerifyPassword(DummyPasswordHash(), password)
		return User{}, Session{}, ErrInvalidCredentials
	}
	if s.store == nil {
		return User{}, Session{}, errors.New("account store is not configured")
	}

	stored, err := s.store.UserByEmail(ctx, normalized)
	if errors.Is(err, ErrNoUser) {
		_ = VerifyPassword(DummyPasswordHash(), password)
		return User{}, Session{}, ErrInvalidCredentials
	}
	if err != nil {
		return User{}, Session{}, err
	}
	if !VerifyPassword(stored.PasswordHash, password) {
		return User{}, Session{}, ErrInvalidCredentials
	}

	session, err := s.issueSession(ctx, stored.ID)
	if err != nil {
		return User{}, Session{}, err
	}
	// A last-login timestamp is telemetry, not authentication: a failure here
	// must not turn a valid sign-in into an error.
	_ = s.store.TouchLogin(ctx, stored.ID)

	return stored.User, session, nil
}

// Authenticate resolves a cookie token to an account.
func (s *Service) Authenticate(ctx context.Context, token string) (User, error) {
	if token == "" || s.store == nil {
		return User{}, ErrNoSession
	}
	hash := HashSessionToken(token)
	user, err := s.store.UserBySession(ctx, hash)
	if err != nil {
		// An expired or revoked session and an unknown one are the same answer.
		if errors.Is(err, ErrNoUser) {
			return User{}, ErrNoSession
		}
		return User{}, err
	}
	_ = s.store.TouchSession(ctx, hash)
	return user, nil
}

// Logout revokes a session. Revoking an already-dead token is not an error:
// signing out twice should look like signing out.
func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" || s.store == nil {
		return nil
	}
	return s.store.RevokeSession(ctx, HashSessionToken(token))
}

// Keys lists the account's keys, newest first.
func (s *Service) Keys(ctx context.Context, user User) ([]APIKey, error) {
	if s.store == nil {
		return nil, errors.New("account store is not configured")
	}
	return s.store.Keys(ctx, user.ClientID)
}

// Rotate replaces the account's API keys with a new one and returns it in
// plaintext once. The old keys stop working immediately, which is the point:
// a rotation after a leak must close the leak.
func (s *Service) Rotate(ctx context.Context, user User) (IssuedKey, int64, error) {
	if s.store == nil {
		return IssuedKey{}, 0, errors.New("account store is not configured")
	}
	fullKey, keyHash, prefix, _, err := auth.GenerateAPIKey()
	if err != nil {
		return IssuedKey{}, 0, fmt.Errorf("generate api key: %w", err)
	}

	revoked, key, err := s.store.RotateKey(ctx, RotateParams{
		ClientID:  user.ClientID,
		KeyPrefix: prefix,
		KeyHash:   keyHash,
		Scopes:    auth.AllScopes,
	})
	if err != nil {
		return IssuedKey{}, 0, err
	}

	return IssuedKey{
		Key:       fullKey,
		Prefix:    displayPrefix(prefix),
		Scopes:    key.Scopes,
		CreatedAt: key.CreatedAt,
	}, revoked, nil
}

func (s *Service) issueSession(ctx context.Context, userID int64) (Session, error) {
	token, err := randomToken()
	if err != nil {
		return Session{}, fmt.Errorf("generate session token: %w", err)
	}
	expires := s.now().Add(s.sessionTTL)
	err = s.store.CreateSession(ctx, SessionParams{
		UserID:    userID,
		TokenHash: HashSessionToken(token),
		ExpiresAt: expires,
	})
	if err != nil {
		return Session{}, err
	}
	return Session{Token: token, ExpiresAt: expires}, nil
}

// normalizeEmail lower-cases and validates an address. Storing the lower-cased
// form is what makes the unique index on lower(email) enough on its own.
func normalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" {
		return "", &ValidationError{Field: "email", Message: "An email address is required."}
	}
	if len(email) > MaxEmailBytes {
		return "", &ValidationError{Field: "email", Message: "That email address is too long."}
	}
	parsed, err := mail.ParseAddress(email)
	// ParseAddress accepts display-name forms such as `A <a@b.test>`; requiring
	// the round trip keeps the stored value equal to the address itself.
	if err != nil || parsed.Address != email || !strings.Contains(email, "@") {
		return "", &ValidationError{Field: "email", Message: "That does not look like an email address."}
	}
	return email, nil
}

func normalizeName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if utf8.RuneCountInString(name) > MaxNameRunes {
		return "", &ValidationError{Field: "name", Message: "That name is too long."}
	}
	return name, nil
}

func checkPassword(password string) error {
	if len(password) < MinPasswordBytes {
		return &ValidationError{
			Field:   "password",
			Message: fmt.Sprintf("Use at least %d characters.", MinPasswordBytes),
		}
	}
	if len(password) > MaxPasswordBytes {
		return &ValidationError{Field: "password", Message: "That password is too long."}
	}
	return nil
}

// randomClientName builds the non-secret tenant label an operator sees in
// support tooling. It carries no personal data and stays stable if the person
// changes their address.
func randomClientName() (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	return "user_" + token[:12], nil
}

// displayPrefix renders the public lookup prefix the way the admin CLI and the
// documents show it.
func displayPrefix(prefix string) string { return "mv_" + prefix }
