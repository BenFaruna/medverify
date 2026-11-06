package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newWebHandler(t *testing.T) http.Handler {
	t.Helper()

	handler, err := webClient("../../web")
	require.NoError(t, err)
	require.NotNil(t, handler)
	return handler
}

func TestWebClientServesTheAppAtRoot(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	newWebHandler(t).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Header().Get("Content-Type"), "text/html")
	assert.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))

	body := recorder.Body.String()
	assert.Contains(t, body, "MedVerify NG")
	// The disclaimer is part of the served page, not added by a script.
	assert.Contains(t, body, "not medical advice")
	assert.Contains(t, body, `href="assets/styles.css"`)
	assert.Contains(t, body, `src="assets/app.js"`)
}

func TestWebClientServesAssets(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/assets/app.js", "/assets/auth.js", "/assets/styles.css"} {
		recorder := httptest.NewRecorder()
		newWebHandler(t).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))

		require.Equal(t, http.StatusOK, recorder.Code, path)
		assert.NotEmpty(t, recorder.Body.String(), path)
	}
}

// The account pages are separate documents rather than routes inside the chat
// page: a sign-in form has to work even when the chat bundle fails to load.
func TestWebClientServesAccountPages(t *testing.T) {
	t.Parallel()

	pages := map[string]string{
		"/login":    `data-page="login"`,
		"/register": `data-page="register"`,
		"/settings": `data-page="settings"`,
	}

	for path, marker := range pages {
		recorder := httptest.NewRecorder()
		newWebHandler(t).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))

		require.Equal(t, http.StatusOK, recorder.Code, path)
		assert.Contains(t, recorder.Header().Get("Content-Type"), "text/html", path)
		assert.Equal(t, "no-store", recorder.Header().Get("Cache-Control"), path)

		body := recorder.Body.String()
		assert.Contains(t, body, marker, path)
		assert.Contains(t, body, `src="assets/auth.js"`, path)
		assert.Contains(t, body, `href="assets/styles.css"`, path)
	}
}

// A path under / that is not the app must stay a 404, so the SPA mount cannot
// swallow API or probe routes.
func TestWebClientDoesNotCatchOtherPaths(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/v1/scans", "/healthz", "/nope"} {
		recorder := httptest.NewRecorder()
		newWebHandler(t).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, http.StatusNotFound, recorder.Code, path)
	}
}

func TestWebClientRejectsTraversal(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/assets/../../go.mod", nil)
	newWebHandler(t).ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusNotFound, recorder.Code)
}

func TestWebClientRejectsUnsupportedMethod(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	newWebHandler(t).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/", nil))

	assert.Equal(t, http.StatusMethodNotAllowed, recorder.Code)
	assert.Equal(t, "GET, HEAD", recorder.Header().Get("Allow"))
}

func TestWebClientDisabledByEmptyDir(t *testing.T) {
	t.Parallel()

	handler, err := webClient("")
	require.NoError(t, err)
	assert.Nil(t, handler)
}

func TestWebClientFailsForAMissingDirectory(t *testing.T) {
	t.Parallel()

	_, err := webClient("testdata/does-not-exist")
	require.Error(t, err)
}
