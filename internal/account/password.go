// Package account provides self-service registration, sign-in, and API key
// management for people who use MedVerify NG directly.
//
// A person owns exactly one api_clients row, so the key issued at registration
// authenticates the same /v1 routes as a key an operator makes with
// `admin apikey create`. Nothing here invents a second credential type: the
// API key remains the only secret the scan endpoints accept, and the session
// cookie only reaches the account endpoints.
//
// Two invariants decide most of this package's shape:
//
//   - A secret is stored hashed and shown once. Passwords use PBKDF2, session
//     tokens and API keys use SHA-256. A database read cannot be replayed.
//   - A write that must land together lands in one transaction. Registering
//     creates a client, a user, and a key; rotating revokes and reissues.
package account

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

const (
	// passwordScheme names the KDF in the stored value, so the parameters can
	// change without invalidating existing hashes.
	passwordScheme = "pbkdf2-sha256"
	// passwordIterations follows current OWASP guidance for PBKDF2-HMAC-SHA256.
	passwordIterations = 600_000
	// The bounds reject a stored value that would make verification cheap (a
	// downgrade attack) or stall a process (a CPU exhaustion attack). Values
	// outside them are treated as a failed verification, never as a success.
	minPasswordIterations = 100_000
	maxPasswordIterations = 4_000_000
	passwordSaltBytes     = 16
	passwordKeyBytes      = 32
)

// ErrBadPasswordHash means a stored hash is malformed or uses parameters this
// build will not honour. It is always reported as a failed sign-in.
var ErrBadPasswordHash = errors.New("malformed password hash")

// HashPassword derives a storage string for password. The result is
// self-describing: scheme, cost, salt, and derive, separated by "$".
func HashPassword(password string) (string, error) {
	salt := make([]byte, passwordSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("read password salt: %w", err)
	}
	derive, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, passwordKeyBytes)
	if err != nil {
		return "", fmt.Errorf("derive password hash: %w", err)
	}
	return fmt.Sprintf("%s$%d$%s$%s",
		passwordScheme,
		passwordIterations,
		hex.EncodeToString(salt),
		hex.EncodeToString(derive),
	), nil
}

// VerifyPassword reports whether password matches a stored hash. Every failure
// mode — wrong password, unknown scheme, bad parameters — returns false, and
// the comparison is constant time so a near miss and a wrong length agree.
func VerifyPassword(encoded, password string) bool {
	scheme, iterations, salt, want, err := parsePasswordHash(encoded)
	if err != nil {
		return false
	}
	if scheme != passwordScheme {
		return false
	}

	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

func parsePasswordHash(encoded string) (scheme string, iterations int, salt, derive []byte, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 {
		return "", 0, nil, nil, ErrBadPasswordHash
	}

	iterations, err = strconv.Atoi(parts[1])
	if err != nil || iterations < minPasswordIterations || iterations > maxPasswordIterations {
		return "", 0, nil, nil, ErrBadPasswordHash
	}

	salt, err = hex.DecodeString(parts[2])
	if err != nil || len(salt) < passwordSaltBytes {
		return "", 0, nil, nil, ErrBadPasswordHash
	}

	derive, err = hex.DecodeString(parts[3])
	if err != nil || len(derive) < passwordKeyBytes {
		return "", 0, nil, nil, ErrBadPasswordHash
	}

	return parts[0], iterations, salt, derive, nil
}

// dummyHash is a real hash of a value nobody knows. Sign-in verifies against it
// when the address has no account, so an unknown email costs the same PBKDF2
// work as a known one and response time does not reveal which addresses are
// registered.
var dummyHash = sync.OnceValue(func() string {
	secret, err := randomToken()
	if err != nil {
		return "unavailable"
	}
	encoded, err := HashPassword(secret)
	if err != nil {
		// Hashing only fails if the system random source fails. Returning an
		// unverifiable string keeps the timing path alive without ever
		// accepting a password.
		return "unavailable"
	}
	return encoded
})

// DummyPasswordHash exposes the comparison target used for unknown addresses.
func DummyPasswordHash() string { return dummyHash() }

// randomToken returns 32 random bytes as lowercase hex. It is the shared shape
// of a session token and of the non-secret suffix used in client names.
func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// HashSessionToken returns the stored form of a session token. The token itself
// is only ever held by the browser cookie. Hashing without a salt is correct
// here: the token is already 256 bits of uniform randomness, so there is no
// dictionary to attack.
func HashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
