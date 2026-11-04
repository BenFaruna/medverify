package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/auth"
)

func TestParseKeyOptionsDefaultsToEveryScope(t *testing.T) {
	options, err := parseKeyOptions([]string{"--client", "acme"})
	require.NoError(t, err)

	assert.Equal(t, "acme", options.client)
	assert.False(t, options.sandbox)
	assert.Equal(t, auth.AllScopes, options.scopes)
	assert.Zero(t, options.expires)
	assert.Equal(t, defaultRatePerMin, options.ratePerMin)
	assert.False(t, options.planSet, "defaults are not an explicit plan change")
}

func TestParseKeyOptionsReadsEveryFlag(t *testing.T) {
	options, err := parseKeyOptions([]string{
		"--client", "acme",
		"--sandbox",
		"--scopes", "scans:read, scans:write,scans:read",
		"--expires", "720h",
		"--rate", "120",
		"--daily-requests", "5000",
		"--daily-scans", "50",
	})
	require.NoError(t, err)

	assert.True(t, options.sandbox)
	assert.Equal(t, []string{auth.ScopeScansRead, auth.ScopeScansWrite}, options.scopes,
		"duplicates are dropped and order is kept")
	assert.Equal(t, 720*time.Hour, options.expires)
	assert.Equal(t, int32(120), options.ratePerMin)
	assert.Equal(t, int32(5000), options.dailyRequestQuota)
	assert.Equal(t, int32(50), options.dailyScanQuota)
	assert.True(t, options.planSet)
}

func TestParseKeyOptionsRejectsBadInput(t *testing.T) {
	cases := map[string][]string{
		"missing client":       {"--rate", "10"},
		"unknown flag":         {"--client", "acme", "--rate-limit", "10"},
		"missing value":        {"--client"},
		"unknown scope":        {"--client", "acme", "--scopes", "scans:delete"},
		"empty scopes":         {"--client", "acme", "--scopes", ","},
		"non-positive rate":    {"--client", "acme", "--rate", "0"},
		"non-numeric quota":    {"--client", "acme", "--daily-scans", "many"},
		"negative expiry":      {"--client", "acme", "--expires", "-1h"},
		"unparseable duration": {"--client", "acme", "--expires", "soon"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parseKeyOptions(args)
			require.Error(t, err)
		})
	}
}

func TestKeyPrefixArgAcceptsFullKeyOrPrefix(t *testing.T) {
	prefix, err := keyPrefixArg("mv_abc123_secret")
	require.NoError(t, err)
	assert.Equal(t, "abc123", prefix)

	prefix, err = keyPrefixArg("abc123")
	require.NoError(t, err)
	assert.Equal(t, "abc123", prefix)

	// The display form from `apikey list` is itself revocable.
	prefix, err = keyPrefixArg("mv_abc123")
	require.NoError(t, err)
	assert.Equal(t, "abc123", prefix)

	for _, bad := range []string{"", "_", "mv_", "abc_123", "mv_abc123_"} {
		_, err := keyPrefixArg(bad)
		assert.Error(t, err, bad)
	}
}

func TestKeyStateNamesTheBlockingCondition(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	assert.Equal(t, "active", keyState(true, nil, nil, now))
	assert.Equal(t, "active", keyState(true, nil, &future, now))
	assert.Equal(t, "expired", keyState(true, nil, &past, now))
	assert.Equal(t, "inactive", keyState(false, nil, nil, now))
	assert.Equal(t, "revoked", keyState(false, &past, &future, now),
		"a revoked key is named revoked even when it is also inactive")
}
