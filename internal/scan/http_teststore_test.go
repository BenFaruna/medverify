package scan_test

import (
	"context"

	"github.com/benfaruna/medverify/internal/platform/auth"
)

// testAPIKeyStore is a minimal auth.Store for handler tests. It models a real
// database-backed key so tenant scoping and scope checks behave the same way
// they do in production.
type testAPIKeyStore struct{ key string }

func (s *testAPIKeyStore) Lookup(ctx context.Context, accessToken string) (*auth.Key, error) {
	if s.key == "" || accessToken != s.key {
		return nil, auth.ErrUnknownClient
	}
	return &auth.Key{
		ID:                1,
		ClientID:          1,
		KeyPrefix:         "test",
		Scopes:            []string{auth.ScopeScansWrite, auth.ScopeScansRead, auth.ScopeQuestionsWrite},
		Active:            true,
		ClientName:        "test-client",
		Sandbox:           false,
		RatePerMin:        600,
		DailyRequestQuota: 100000,
		DailyScanQuota:    100000,
	}, nil
}
