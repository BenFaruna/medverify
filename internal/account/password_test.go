package account

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHashPasswordRoundTrips(t *testing.T) {
	t.Parallel()

	encoded, err := HashPassword("correct horse battery staple")
	require.NoError(t, err)

	// The stored value describes its own scheme and cost, so a future change to
	// the defaults can still verify an old hash.
	parts := strings.Split(encoded, "$")
	require.Len(t, parts, 4)
	assert.Equal(t, passwordScheme, parts[0])
	assert.Equal(t, "600000", parts[1])
	assert.Equal(t, 32, len(parts[2])) // 16 salt bytes, hex encoded
	assert.Equal(t, 64, len(parts[3])) // 32 derive bytes, hex encoded

	assert.True(t, VerifyPassword(encoded, "correct horse battery staple"))
}

func TestHashPasswordRejectsWrongPassword(t *testing.T) {
	t.Parallel()

	encoded, err := HashPassword("a-long-enough-password")
	require.NoError(t, err)

	assert.False(t, VerifyPassword(encoded, "a-long-enough-passwore"))
	assert.False(t, VerifyPassword(encoded, ""))
	assert.False(t, VerifyPassword(encoded, "a-long-enough-password "))
}

// The same password must not produce the same stored value twice: the salt is
// what stops a precomputed table from covering every account at once.
func TestHashPasswordUsesAFreshSalt(t *testing.T) {
	t.Parallel()

	first, err := HashPassword("a-long-enough-password")
	require.NoError(t, err)
	second, err := HashPassword("a-long-enough-password")
	require.NoError(t, err)

	assert.NotEqual(t, first, second)
	assert.True(t, VerifyPassword(first, "a-long-enough-password"))
	assert.True(t, VerifyPassword(second, "a-long-enough-password"))
}

// A stored hash must fail closed. Each case below is a value that could end up
// in the column: a truncated row, an unknown scheme, an absurd cost.
func TestVerifyPasswordRejectsMalformedHashes(t *testing.T) {
	t.Parallel()

	valid, err := HashPassword("a-long-enough-password")
	require.NoError(t, err)
	parts := strings.Split(valid, "$")

	cases := map[string]string{
		"empty":               "",
		"no separators":       "pbkdf2sha256",
		"missing derive":      strings.Join(parts[:3], "$"),
		"unknown scheme":      "bcrypt$" + strings.Join(parts[1:], "$"),
		"non-numeric cost":    "pbkdf2-sha256$rounds$" + strings.Join(parts[2:], "$"),
		"weak cost":           "pbkdf2-sha256$1000$" + strings.Join(parts[2:], "$"),
		"absurd cost":         "pbkdf2-sha256$999999999$" + strings.Join(parts[2:], "$"),
		"short salt":          "pbkdf2-sha256$600000$abcd$" + parts[3],
		"non-hex salt":        "pbkdf2-sha256$600000$zzzz$" + parts[3],
		"short derive":        "pbkdf2-sha256$600000$" + parts[2] + "$abcd",
		"extra field":         valid + "$extra",
		"cost over the bound": "pbkdf2-sha256$5000000$" + strings.Join(parts[2:], "$"),
	}

	for name, encoded := range cases {
		assert.False(t, VerifyPassword(encoded, "a-long-enough-password"), name)
	}
}

// The dummy hash exists so an unknown address costs the same PBKDF2 work as a
// known one. It must be a real, constant value that no password satisfies.
func TestDummyPasswordHashIsStableAndUnusable(t *testing.T) {
	t.Parallel()

	first := DummyPasswordHash()
	require.NotEmpty(t, first)
	assert.Equal(t, first, DummyPasswordHash())

	for _, guess := range []string{"", "password", "a-long-enough-password"} {
		assert.False(t, VerifyPassword(first, guess), guess)
	}
}

func TestHashSessionTokenIsStableAndOpaque(t *testing.T) {
	t.Parallel()

	token := "0123456789abcdef"
	hash := HashSessionToken(token)

	assert.Equal(t, hash, HashSessionToken(token))
	assert.NotContains(t, hash, token)
	assert.Len(t, hash, 64)
	assert.NotEqual(t, hash, HashSessionToken(token+"x"))
}
