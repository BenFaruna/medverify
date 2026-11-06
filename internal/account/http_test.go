package account

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/auth"
)

// fakeStore keeps accounts in memory. It records what the service wrote so a
// test can assert on hashes rather than on plaintext that was returned once.
type fakeStore struct {
	mu       sync.Mutex
	users    map[string]StoredUser // keyed by lower-cased email
	sessions map[string]int64      // token hash -> user id
	keys     map[int64][]APIKey    // client id -> keys, newest first
	revoked  []string
	nextUser int64
	nextKey  int64
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		users:    make(map[string]StoredUser),
		sessions: make(map[string]int64),
		keys:     make(map[int64][]APIKey),
	}
}

func (f *fakeStore) Register(_ context.Context, params RegisterParams) (User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, exists := f.users[params.Email]; exists {
		return User{}, ErrEmailTaken
	}
	f.nextUser++
	user := User{
		ID:        f.nextUser,
		Email:     params.Email,
		Name:      params.Name,
		ClientID:  f.nextUser,
		CreatedAt: time.Now().UTC(),
	}
	f.users[params.Email] = StoredUser{User: user, PasswordHash: params.PasswordHash}
	f.sessions[params.SessionTokenHash] = user.ID

	f.nextKey++
	f.keys[user.ClientID] = []APIKey{{
		ID:        f.nextKey,
		Prefix:    params.KeyPrefix,
		Scopes:    params.Scopes,
		Active:    true,
		CreatedAt: user.CreatedAt,
	}}
	return user, nil
}

func (f *fakeStore) UserByEmail(_ context.Context, email string) (StoredUser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	user, ok := f.users[strings.ToLower(email)]
	if !ok {
		return StoredUser{}, ErrNoUser
	}
	return user, nil
}

func (f *fakeStore) UserBySession(_ context.Context, tokenHash string) (User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	userID, ok := f.sessions[tokenHash]
	if !ok {
		return User{}, ErrNoUser
	}
	for _, stored := range f.users {
		if stored.ID == userID {
			return stored.User, nil
		}
	}
	return User{}, ErrNoUser
}

func (f *fakeStore) TouchLogin(context.Context, int64) error    { return nil }
func (f *fakeStore) TouchSession(context.Context, string) error { return nil }

func (f *fakeStore) CreateSession(_ context.Context, params SessionParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.sessions[params.TokenHash] = params.UserID
	return nil
}

func (f *fakeStore) RevokeSession(_ context.Context, tokenHash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.sessions, tokenHash)
	f.revoked = append(f.revoked, tokenHash)
	return nil
}

func (f *fakeStore) Keys(_ context.Context, clientID int64) ([]APIKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]APIKey(nil), f.keys[clientID]...), nil
}

func (f *fakeStore) RotateKey(_ context.Context, params RotateParams) (int64, APIKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	now := time.Now().UTC()
	var revoked int64
	for i := range f.keys[params.ClientID] {
		if f.keys[params.ClientID][i].Active {
			f.keys[params.ClientID][i].Active = false
			f.keys[params.ClientID][i].RevokedAt = &now
			revoked++
		}
	}

	f.nextKey++
	key := APIKey{
		ID:        f.nextKey,
		Prefix:    params.KeyPrefix,
		Scopes:    params.Scopes,
		Active:    true,
		CreatedAt: now,
	}
	// Newest first, matching ListAPIKeysForClient.
	f.keys[params.ClientID] = append([]APIKey{key}, f.keys[params.ClientID]...)
	return revoked, key, nil
}

func (f *fakeStore) ClientPlan(_ context.Context, clientID int64) (ClientPlan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, user := range f.users {
		if user.ClientID == clientID {
			return ClientPlan{
				Name:              "user_fake",
				RatePerMin:        60,
				DailyRequestQuota: 10000,
				DailyScanQuota:    200,
			}, nil
		}
	}
	return ClientPlan{}, ErrNoClient
}

func (f *fakeStore) user(email string) (StoredUser, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	user, ok := f.users[strings.ToLower(email)]
	return user, ok
}

func (f *fakeStore) clientKeys(clientID int64) []APIKey {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]APIKey(nil), f.keys[clientID]...)
}

// testServer mounts the handlers the way the API router does: auth routes
// without a session, account routes behind one, all behind the CSRF guard.
func testServer(t *testing.T) (*httptest.Server, *fakeStore) {
	t.Helper()

	store := newFakeStore()
	service := NewService(ServiceOptions{Store: store})
	handler := NewHTTPHandler(HTTPOptions{Service: service})

	mux := http.NewServeMux()
	mux.Handle("POST /v1/auth/register", CSRF(http.HandlerFunc(handler.Register)))
	mux.Handle("POST /v1/auth/login", CSRF(http.HandlerFunc(handler.Login)))
	mux.Handle("POST /v1/auth/logout", CSRF(http.HandlerFunc(handler.Logout)))
	mux.Handle("GET /v1/account", handler.RequireSession(http.HandlerFunc(handler.Me)))
	mux.Handle("GET /v1/account/api-keys", handler.RequireSession(http.HandlerFunc(handler.ListKeys)))
	mux.Handle("POST /v1/account/api-keys/rotate", CSRF(handler.RequireSession(http.HandlerFunc(handler.RotateKey))))

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, store
}

// newClient returns a browser-like client that keeps cookies. Each test client
// is one browser, which is what makes the cookie assertions meaningful.
func newClient(t *testing.T) *http.Client {
	t.Helper()

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	return &http.Client{Jar: jar, Timeout: 5 * time.Second}
}

// testResponse is the part of an HTTP response a test needs. The body is read
// and closed inside the helper so no call site can leak a connection.
type testResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

func postJSON(t *testing.T, client *http.Client, url string, body any, headers map[string]string) (testResponse, []byte) {
	t.Helper()

	payload, err := json.Marshal(body)
	require.NoError(t, err)

	request, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		request.Header.Set(name, value)
	}

	return do(t, client, request)
}

func get(t *testing.T, client *http.Client, url string) (testResponse, []byte) {
	t.Helper()

	request, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	return do(t, client, request)
}

func do(t *testing.T, client *http.Client, request *http.Request) (testResponse, []byte) {
	t.Helper()

	response, err := client.Do(request)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()

	data, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return testResponse{StatusCode: response.StatusCode, Header: response.Header, Body: data}, data
}

type registerBody struct {
	User struct {
		ID       int64  `json:"id"`
		Email    string `json:"email"`
		Name     string `json:"name"`
		ClientID int64  `json:"client_id"`
	} `json:"user"`
	Session struct {
		ExpiresAt time.Time `json:"expires_at"`
	} `json:"session"`
	APIKey struct {
		Key    string   `json:"key"`
		Prefix string   `json:"prefix"`
		Scopes []string `json:"scopes"`
		State  string   `json:"state"`
	} `json:"api_key"`
}

func register(t *testing.T, server *httptest.Server, client *http.Client, email, password string) registerBody {
	t.Helper()

	response, data := postJSON(t, client, server.URL+"/v1/auth/register", map[string]string{
		"email":    email,
		"name":     "Ada",
		"password": password,
	}, nil)
	require.Equal(t, http.StatusCreated, response.StatusCode, string(data))

	var body registerBody
	require.NoError(t, json.Unmarshal(data, &body))
	return body
}

func TestRegisterCreatesAccountSessionAndKey(t *testing.T) {
	t.Parallel()

	server, store := testServer(t)
	client := newClient(t)
	const password = "a-long-enough-password"
	body := register(t, server, client, "Ada@Example.test", password)

	assert.Equal(t, "ada@example.test", body.User.Email)
	assert.Equal(t, "Ada", body.User.Name)
	assert.Equal(t, "active", body.APIKey.State)
	assert.Contains(t, body.APIKey.Scopes, auth.ScopeScansWrite)
	assert.True(t, body.Session.ExpiresAt.After(time.Now()))

	// The key is a real mv_ key whose prefix the session page can show.
	assert.True(t, strings.HasPrefix(body.APIKey.Key, "mv_"), body.APIKey.Key)
	_, prefix, fullPrefix := auth.HashAPIKey(body.APIKey.Key)
	assert.Equal(t, fullPrefix, body.APIKey.Prefix)
	assert.NotEmpty(t, prefix)

	// The password is stored as a verifiable hash, never as the password.
	stored, ok := store.user("ada@example.test")
	require.True(t, ok)
	assert.NotContains(t, stored.PasswordHash, password)
	assert.True(t, VerifyPassword(stored.PasswordHash, password))

	// The key row holds only the hash of the value that was returned once.
	keys := store.clientKeys(body.User.ClientID)
	require.Len(t, keys, 1)
	hash, _, _ := auth.HashAPIKey(body.APIKey.Key)
	assert.Equal(t, prefix, keys[0].Prefix)
	assert.NotContains(t, hash, body.APIKey.Key)

	// Registration signs the browser in, and the cookie is not script-readable.
	response, data := get(t, client, server.URL+"/v1/account")
	require.Equal(t, http.StatusOK, response.StatusCode, string(data))
	assert.Contains(t, string(data), "ada@example.test")

	cookies := client.Jar.Cookies(mustParse(t, server.URL))
	require.Len(t, cookies, 1)
	assert.Equal(t, SessionCookieName, cookies[0].Name)
	assert.NotEmpty(t, cookies[0].Value)
}

// The cookie attributes are what keep the token out of reach of page script
// and out of cross-site form posts. The cookie jar drops them, so they are
// asserted on the header itself.
func TestRegisterSetsAHardenedSessionCookie(t *testing.T) {
	t.Parallel()

	server, _ := testServer(t)
	response, data := postJSON(t, newClient(t), server.URL+"/v1/auth/register", map[string]string{
		"email":    "ada@example.test",
		"password": "a-long-enough-password",
	}, nil)
	require.Equal(t, http.StatusCreated, response.StatusCode, string(data))

	setCookie := response.Header.Get("Set-Cookie")
	require.NotEmpty(t, setCookie)
	assert.Contains(t, setCookie, SessionCookieName+"=")
	assert.Contains(t, setCookie, "HttpOnly")
	assert.Contains(t, setCookie, "SameSite=Lax")
	assert.Contains(t, setCookie, "Path=/")
	assert.Contains(t, setCookie, "Max-Age=")
	// A plain-HTTP test server must not set Secure, or the cookie would be
	// dropped by the browser and the test would pass for the wrong reason.
	assert.NotContains(t, setCookie, "Secure")
}

func TestRegisterRejectsDuplicateEmailRegardlessOfCase(t *testing.T) {
	t.Parallel()

	server, _ := testServer(t)
	register(t, server, newClient(t), "ada@example.test", "a-long-enough-password")

	response, data := postJSON(t, newClient(t), server.URL+"/v1/auth/register", map[string]string{
		"email":    "ADA@example.test",
		"password": "another-long-password",
	}, nil)

	assert.Equal(t, http.StatusConflict, response.StatusCode)
	assert.Contains(t, string(data), "already has an account")
}

func TestRegisterRejectsWeakOrMissingFields(t *testing.T) {
	t.Parallel()

	server, _ := testServer(t)

	cases := map[string]map[string]string{
		"short password": {"email": "ada@example.test", "password": "short"},
		"no email":       {"email": "  ", "password": "a-long-enough-password"},
		"bad email":      {"email": "not-an-address", "password": "a-long-enough-password"},
	}
	for name, payload := range cases {
		response, data := postJSON(t, newClient(t), server.URL+"/v1/auth/register", payload, nil)
		assert.Equal(t, http.StatusBadRequest, response.StatusCode, name)
		assert.Contains(t, response.Header.Get("Content-Type"), "application/problem+json", name)
		assert.NotEmpty(t, data, name)
	}
}

func TestRegisterRejectsUnknownJSONField(t *testing.T) {
	t.Parallel()

	server, _ := testServer(t)

	response, _ := postJSON(t, newClient(t), server.URL+"/v1/auth/register", map[string]string{
		"email":    "ada@example.test",
		"password": "a-long-enough-password",
		"admin":    "true",
	}, nil)

	assert.Equal(t, http.StatusBadRequest, response.StatusCode)
}

func TestLoginStartsASessionAndRejectsWrongPassword(t *testing.T) {
	t.Parallel()

	server, _ := testServer(t)
	const password = "a-long-enough-password"
	register(t, server, newClient(t), "ada@example.test", password)

	// A fresh client is a fresh browser: no cookie yet.
	browser := newClient(t)

	response, data := postJSON(t, browser, server.URL+"/v1/auth/login", map[string]string{
		"email":    "ada@example.test",
		"password": password,
	}, nil)
	require.Equal(t, http.StatusOK, response.StatusCode, string(data))

	accountResponse, accountBody := get(t, browser, server.URL+"/v1/account")
	require.Equal(t, http.StatusOK, accountResponse.StatusCode, string(accountBody))
	assert.Contains(t, string(accountBody), "ada@example.test")

	wrong := newClient(t)
	response, data = postJSON(t, wrong, server.URL+"/v1/auth/login", map[string]string{
		"email":    "ada@example.test",
		"password": "the-wrong-password",
	}, nil)
	assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
	assert.NotContains(t, string(data), "ada@example.test")

	// An unknown address is the same answer as a wrong password.
	response, _ = postJSON(t, wrong, server.URL+"/v1/auth/login", map[string]string{
		"email":    "nobody@example.test",
		"password": password,
	}, nil)
	assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
	assert.Empty(t, wrong.Jar.Cookies(mustParse(t, server.URL)))
}

func TestAccountRoutesRequireASession(t *testing.T) {
	t.Parallel()

	server, _ := testServer(t)
	client := newClient(t)

	response, data := get(t, client, server.URL+"/v1/account")
	assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
	assert.Contains(t, string(data), "Sign in to continue")

	response, _ = postJSON(t, client, server.URL+"/v1/account/api-keys/rotate", map[string]string{}, nil)
	assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
}

func TestRotateRevokesOldKeysAndIssuesANewOne(t *testing.T) {
	t.Parallel()

	server, store := testServer(t)
	client := newClient(t)
	body := register(t, server, client, "ada@example.test", "a-long-enough-password")

	before := store.clientKeys(body.User.ClientID)
	require.Len(t, before, 1)

	response, data := postJSON(t, client, server.URL+"/v1/account/api-keys/rotate", map[string]string{}, nil)
	require.Equal(t, http.StatusOK, response.StatusCode, string(data))

	var rotated struct {
		APIKey struct {
			Key       string   `json:"key"`
			Prefix    string   `json:"prefix"`
			Scopes    []string `json:"scopes"`
			CreatedAt string   `json:"created_at"`
		} `json:"api_key"`
		Revoked int64 `json:"revoked"`
	}
	require.NoError(t, json.Unmarshal(data, &rotated))

	assert.Equal(t, int64(1), rotated.Revoked)
	assert.NotEqual(t, body.APIKey.Key, rotated.APIKey.Key)
	assert.True(t, strings.HasPrefix(rotated.APIKey.Key, "mv_"))
	assert.NotEqual(t, body.APIKey.Prefix, rotated.APIKey.Prefix)

	keys := store.clientKeys(body.User.ClientID)
	require.Len(t, keys, 2)

	// Newest first: the new key is live, the previous one is revoked.
	assert.True(t, keys[0].Active)
	assert.Nil(t, keys[0].RevokedAt)
	assert.False(t, keys[1].Active)
	require.NotNil(t, keys[1].RevokedAt)

	newHash, newPrefix, _ := auth.HashAPIKey(rotated.APIKey.Key)
	assert.Equal(t, newPrefix, keys[0].Prefix)
	assert.Equal(t, rotated.APIKey.Prefix, "mv_"+keys[0].Prefix)
	assert.NotContains(t, newHash, rotated.APIKey.Key)
}

func TestRotateTwiceOnlyRevokesTheOneLiveKey(t *testing.T) {
	t.Parallel()

	server, store := testServer(t)
	client := newClient(t)
	body := register(t, server, client, "ada@example.test", "a-long-enough-password")

	for range 2 {
		response, data := postJSON(t, client, server.URL+"/v1/account/api-keys/rotate", map[string]string{}, nil)
		require.Equal(t, http.StatusOK, response.StatusCode, string(data))

		var rotated struct {
			Revoked int64 `json:"revoked"`
		}
		require.NoError(t, json.Unmarshal(data, &rotated))
		assert.Equal(t, int64(1), rotated.Revoked)
	}

	keys := store.clientKeys(body.User.ClientID)
	require.Len(t, keys, 3)

	live := 0
	for _, key := range keys {
		if key.Active {
			live++
		}
	}
	assert.Equal(t, 1, live, "exactly one key may remain active")
}

func TestListKeysNeverReturnsASecret(t *testing.T) {
	t.Parallel()

	server, _ := testServer(t)
	client := newClient(t)
	body := register(t, server, client, "ada@example.test", "a-long-enough-password")

	response, data := get(t, client, server.URL+"/v1/account/api-keys")
	require.Equal(t, http.StatusOK, response.StatusCode, string(data))

	var listed struct {
		APIKeys []map[string]any `json:"api_keys"`
	}
	require.NoError(t, json.Unmarshal(data, &listed))
	require.Len(t, listed.APIKeys, 1)

	assert.NotContains(t, string(data), body.APIKey.Key)
	assert.NotContains(t, listed.APIKeys[0], "key")
	assert.Equal(t, body.APIKey.Prefix, listed.APIKeys[0]["prefix"])
	assert.Equal(t, "active", listed.APIKeys[0]["state"])
}

func TestLogoutRevokesTheSession(t *testing.T) {
	t.Parallel()

	server, store := testServer(t)
	client := newClient(t)
	register(t, server, client, "ada@example.test", "a-long-enough-password")

	response, _ := postJSON(t, client, server.URL+"/v1/auth/logout", map[string]string{}, nil)
	assert.Equal(t, http.StatusNoContent, response.StatusCode)
	assert.Len(t, store.revoked, 1)

	// The cookie is cleared, so the browser cannot present it again.
	assert.Empty(t, client.Jar.Cookies(mustParse(t, server.URL)))

	after, _ := get(t, client, server.URL+"/v1/account")
	assert.Equal(t, http.StatusUnauthorized, after.StatusCode)
}

// A cookie is attached by the browser to any request to this origin, including
// one triggered by another site. The CSRF guard is what stops that request
// from rotating a key.
func TestCrossSitePostIsBlocked(t *testing.T) {
	t.Parallel()

	server, store := testServer(t)
	client := newClient(t)
	body := register(t, server, client, "ada@example.test", "a-long-enough-password")

	response, data := postJSON(t, client, server.URL+"/v1/account/api-keys/rotate", map[string]string{}, map[string]string{
		"Sec-Fetch-Site": "cross-site",
	})
	assert.Equal(t, http.StatusForbidden, response.StatusCode)
	assert.Contains(t, string(data), "forbidden")

	response, _ = postJSON(t, client, server.URL+"/v1/account/api-keys/rotate", map[string]string{}, map[string]string{
		"Origin": "https://evil.example",
	})
	assert.Equal(t, http.StatusForbidden, response.StatusCode)

	// Neither rejection reached the store: the original key is still the only one.
	assert.Len(t, store.clientKeys(body.User.ClientID), 1)
}

func TestSameOriginPostIsAllowed(t *testing.T) {
	t.Parallel()

	server, store := testServer(t)
	client := newClient(t)
	body := register(t, server, client, "ada@example.test", "a-long-enough-password")

	response, data := postJSON(t, client, server.URL+"/v1/account/api-keys/rotate", map[string]string{}, map[string]string{
		"Origin":         server.URL,
		"Sec-Fetch-Site": "same-origin",
	})
	require.Equal(t, http.StatusOK, response.StatusCode, string(data))
	assert.Len(t, store.clientKeys(body.User.ClientID), 2)
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()

	parsed, err := url.Parse(raw)
	require.NoError(t, err)
	return parsed
}

// The chat page calls /v1 with the cookie and no key. This mounts the real
// middleware around a handler that reports the principal, so the seam between
// the session resolver and the API's rate and quota limits is exercised: the
// browser inherits the plan of the client row the account owns.
func TestSessionAuthFeedsTheAPIWithTheAccountPlan(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	service := NewService(ServiceOptions{Store: store})
	handler := NewHTTPHandler(HTTPOptions{Service: service})

	registration, err := service.Register(context.Background(), RegisterInput{
		Email:    "ada@example.test",
		Password: "a-long-enough-password",
	})
	require.NoError(t, err)

	api := auth.APIKeyOrSession(nil, handler.SessionAuth())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := auth.PrincipalFromContext(r.Context())
		require.True(t, ok, "the handler must see a principal")
		_, _ = w.Write([]byte(principal.TenantKey()))
	}))
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)

	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/scans", nil)
	require.NoError(t, err)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: registration.Session.Token})
	request.Header.Set("Sec-Fetch-Site", "same-origin")

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()

	require.Equal(t, http.StatusOK, response.StatusCode)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Equal(t, "client:"+strconv.FormatInt(registration.User.ClientID, 10), string(body))

	// The same request without the cookie is the documented 401: a browser that
	// signed out is sent back to the sign-in page by the client.
	anonymous, err := http.NewRequest(http.MethodGet, server.URL+"/v1/scans/abc", nil)
	require.NoError(t, err)
	anonResponse, err := http.DefaultClient.Do(anonymous)
	require.NoError(t, err)
	defer func() { _ = anonResponse.Body.Close() }()
	assert.Equal(t, http.StatusUnauthorized, anonResponse.StatusCode)
}
