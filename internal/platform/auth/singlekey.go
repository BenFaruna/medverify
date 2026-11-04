package auth

import (
	"context"
	"time"
)

// SingleKeyOptions describes the one key a SingleKeyStore accepts.
type SingleKeyOptions struct {
	// Key is the full key value a client presents.
	Key string
	// ClientID is the tenant id recorded for scans created with this key. The
	// legacy development mode leaves it zero and is scoped by tenant key.
	ClientID int64
	// ClientName is a non-secret label used in logs and support tooling.
	ClientName string
	// Sandbox makes the key return fixed responses without provider calls.
	Sandbox bool
	// Scopes limits what the key may do. Empty means every scope.
	Scopes []string
	// RatePerMin and the quotas default to generous development values when
	// zero, so a local setup does not fight 429s.
	RatePerMin        int32
	DailyRequestQuota int32
	DailyScanQuota    int32
}

// SingleKeyStore authenticates exactly one key. It exists for local development
// and tests; production requires database-backed keys so every client has its
// own identity, scopes, and quotas.
type SingleKeyStore struct {
	key  Key
	full string
}

// NewSingleKeyStore builds a store for one key.
func NewSingleKeyStore(opts SingleKeyOptions) *SingleKeyStore {
	scopes := opts.Scopes
	if len(scopes) == 0 {
		scopes = append([]string(nil), AllScopes...)
	}
	rate := opts.RatePerMin
	if rate <= 0 {
		rate = 60
	}
	requestQuota := opts.DailyRequestQuota
	if requestQuota <= 0 {
		requestQuota = 10000
	}
	scanQuota := opts.DailyScanQuota
	if scanQuota <= 0 {
		scanQuota = 200
	}
	name := opts.ClientName
	if name == "" {
		name = DefaultClientKey
	}
	return &SingleKeyStore{
		full: opts.Key,
		key: Key{
			ID:                0,
			ClientID:          opts.ClientID,
			KeyPrefix:         "legacy",
			Scopes:            scopes,
			Active:            true,
			ExpiresAt:         nil,
			ClientName:        name,
			Sandbox:           opts.Sandbox,
			RatePerMin:        rate,
			DailyRequestQuota: requestQuota,
			DailyScanQuota:    scanQuota,
		},
	}
}

// Lookup accepts the configured key and rejects everything else. Comparison is
// constant time and the key is never logged.
func (s *SingleKeyStore) Lookup(ctx context.Context, accessToken string) (*Key, error) {
	if s == nil || s.full == "" || !ConstantTimeEqual(s.full, accessToken) {
		return nil, ErrUnknownClient
	}
	key := s.key
	if key.ExpiresAt != nil && !key.ExpiresAt.After(time.Now()) {
		return nil, ErrUnknownClient
	}
	return &key, nil
}
