package scan_test

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/auth"
	"github.com/benfaruna/medverify/internal/platform/httpx"
	"github.com/benfaruna/medverify/internal/scan"
)

func TestCreateRejectsTooManyImagesBeforeService(t *testing.T) {
	t.Parallel()

	handler := newTestScanHandler(t, &scan.HTTPOptions{MaxImages: 4, MaxImageBytes: 1 << 20, MaxRequestBytes: 10 << 20, SyncWait: 0})
	body, contentType := multipartUpload(t, [][]byte{
		[]byte("1"), []byte("2"), []byte("3"), []byte("4"), []byte("5"),
	}, nil)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, multipartRequest(body, contentType))

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assertProblemCode(t, recorder, httpx.CodeTooManyImages)
}

func TestCreateRejectsOversizedImage(t *testing.T) {
	t.Parallel()

	handler := newTestScanHandler(t, &scan.HTTPOptions{MaxImages: 4, MaxImageBytes: 8, MaxRequestBytes: 1 << 20, SyncWait: 0})
	body, contentType := multipartUpload(t, [][]byte{bytes.Repeat([]byte("x"), 9)}, nil)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, multipartRequest(body, contentType))

	assert.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
	assertProblemCode(t, recorder, httpx.CodeImageTooLarge)
}

func TestCreateRejectsNoImages(t *testing.T) {
	t.Parallel()

	handler := newTestScanHandler(t, &scan.HTTPOptions{MaxImages: 4, MaxImageBytes: 1 << 20, MaxRequestBytes: 1 << 20, SyncWait: 0})
	body, contentType := multipartUpload(t, nil, map[string]string{"language": "en"})

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, multipartRequest(body, contentType))

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assertProblemCode(t, recorder, httpx.CodeBadRequest)
}

func TestCreateRejectsInvalidLocationAndLanguage(t *testing.T) {
	t.Parallel()

	handler := newTestScanHandler(t, &scan.HTTPOptions{MaxImages: 4, MaxImageBytes: 1 << 20, MaxRequestBytes: 1 << 20, SyncWait: 0})

	cases := []struct {
		name   string
		fields map[string]string
		code   string
	}{
		{"partial location", map[string]string{"latitude": "6.45"}, httpx.CodeInvalidLocation},
		{"bad language", map[string]string{"language": "not a language"}, httpx.CodeInvalidLanguage},
	}
	for _, tc := range cases {
		body, contentType := multipartUpload(t, [][]byte{[]byte("image")}, tc.fields)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, multipartRequest(body, contentType))
		assert.Equal(t, http.StatusBadRequest, recorder.Code, tc.name)
		assertProblemCode(t, recorder, tc.code)
	}
}

func TestCreateRejectsNonMultipartBody(t *testing.T) {
	t.Parallel()

	handler := newTestScanHandler(t, &scan.HTTPOptions{MaxImages: 4, MaxImageBytes: 1 << 20, MaxRequestBytes: 1 << 20, SyncWait: 0})
	request := httptest.NewRequest(http.MethodPost, "/v1/scans", bytes.NewBufferString(`{"images":[]}`))
	request.Header.Set(auth.HeaderAPIKey, testAPIKey)
	request.Header.Set("Content-Type", "application/json")

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assertProblemCode(t, recorder, httpx.CodeBadRequest)
}

const testAPIKey = "test-key"

func newTestScanHandler(t *testing.T, opts *scan.HTTPOptions) http.Handler {
	t.Helper()

	opts.Service = nil // these tests stop before the service is called
	handler := scan.NewHTTPHandler(*opts)
	return auth.APIKey(testAPIKey)(http.HandlerFunc(handler.Create))
}

func multipartRequest(body *bytes.Buffer, contentType string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/v1/scans", body)
	request.Header.Set(auth.HeaderAPIKey, testAPIKey)
	request.Header.Set("Content-Type", contentType)
	return request
}

func multipartUpload(t *testing.T, images [][]byte, fields map[string]string) (*bytes.Buffer, string) {
	t.Helper()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for _, image := range images {
		part, err := writer.CreateFormFile("images", "photo.jpg")
		require.NoError(t, err)
		_, err = part.Write(image)
		require.NoError(t, err)
	}
	for name, value := range fields {
		require.NoError(t, writer.WriteField(name, value))
	}
	require.NoError(t, writer.Close())
	return body, writer.FormDataContentType()
}

func assertProblemCode(t *testing.T, recorder *httptest.ResponseRecorder, code string) {
	t.Helper()

	assert.Equal(t, httpx.ContentTypeProblemJSON, recorder.Header().Get("Content-Type"))
	var problem httpx.Problem
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &problem))
	assert.Equal(t, code, problem.Code)
}
