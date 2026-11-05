package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDocsRedirectsToTrailingSlash(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	DocsHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/docs", nil))

	assert.Equal(t, http.StatusMovedPermanently, recorder.Code)
	assert.Equal(t, "/docs/", recorder.Header().Get("Location"))
}

func TestDocsPageLoadsTheLocalBundleAndSpec(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	DocsHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/docs/", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Header().Get("Content-Type"), "text/html")
	assert.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))

	body := recorder.Body.String()
	assert.Contains(t, body, `SwaggerUIBundle({`)
	assert.Contains(t, body, `url: "/openapi.yaml"`)
	assert.Contains(t, body, `/docs/swagger-ui-bundle.js`)
	assert.Contains(t, body, `/docs/swagger-ui.css`)
	assert.NotContains(t, body, "unpkg.com", "the docs page must not depend on a CDN")
}

func TestDocsAssetsAreServedFromTheEmbeddedBundle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path        string
		contentType string
	}{
		{path: "/docs/swagger-ui.css", contentType: "text/css"},
		{path: "/docs/swagger-ui-bundle.js", contentType: "javascript"},
		{path: "/docs/swagger-ui-standalone-preset.js", contentType: "javascript"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.path, func(t *testing.T) {
			t.Parallel()

			recorder := httptest.NewRecorder()
			DocsHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))

			require.Equal(t, http.StatusOK, recorder.Code)
			assert.Contains(t, recorder.Header().Get("Content-Type"), test.contentType)
			assert.NotEmpty(t, recorder.Body.Bytes())
		})
	}
}

func TestOpenAPIDocumentIsServedAndValid(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	DocsHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "application/yaml; charset=utf-8", recorder.Header().Get("Content-Type"))
	assert.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	assert.Equal(t, Spec(), recorder.Body.Bytes())

	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = false
	document, err := loader.LoadFromData(recorder.Body.Bytes())
	require.NoError(t, err)
	require.NoError(t, document.Validate(loader.Context))
	assert.True(t, strings.HasPrefix(document.OpenAPI, "3."))
}

func TestDocsRejectUnsupportedMethods(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/docs/", "/openapi.yaml"} {
		recorder := httptest.NewRecorder()
		DocsHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, nil))
		assert.Equal(t, http.StatusMethodNotAllowed, recorder.Code, path)
	}
}

func TestSpecReturnsACopy(t *testing.T) {
	t.Parallel()

	first := Spec()
	require.NotEmpty(t, first)
	first[0] = 'X'

	second := Spec()
	require.NotEmpty(t, second)
	assert.NotEqual(t, first, second, "callers must not be able to mutate the embedded spec")
}
