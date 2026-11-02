package auth_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/auth"
	"github.com/benfaruna/medverify/internal/platform/httpx"
)

func TestAPIKeyRejectsMissingAndInvalidKeys(t *testing.T) {
	t.Parallel()

	handler := auth.APIKey("correct-key")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run without a valid key")
	}))

	for name, key := range map[string]string{"missing": "", "wrong": "nope"} {
		request := httptest.NewRequest(http.MethodPost, "/v1/scans", nil)
		if key != "" {
			request.Header.Set(auth.HeaderAPIKey, key)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)

		assert.Equal(t, http.StatusUnauthorized, recorder.Code, name)
		assert.Equal(t, httpx.ContentTypeProblemJSON, recorder.Header().Get("Content-Type"))

		var problem httpx.Problem
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &problem))
		assert.Equal(t, httpx.CodeUnauthorized, problem.Code)
	}
}

func TestAPIKeySetsPrincipal(t *testing.T) {
	t.Parallel()

	var principal auth.Principal
	handler := auth.APIKey("correct-key")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ok bool
		principal, ok = auth.PrincipalFromContext(r.Context())
		require.True(t, ok)
		w.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest(http.MethodPost, "/v1/scans", nil)
	request.Header.Set(auth.HeaderAPIKey, "correct-key")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusNoContent, recorder.Code)
	assert.Equal(t, auth.DefaultClientKey, principal.Key)
}
