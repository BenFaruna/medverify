//go:build integration

package scan_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/auth"
	"github.com/benfaruna/medverify/internal/scan"
)

const (
	tenantOwnerKey = "mv_owner_secret"
	tenantOtherKey = "mv_other_secret"
)

// A scan created by one client must be invisible to every other client, and the
// answer must be the same "not found" a missing scan gets so the response
// cannot be used to probe for IDs.
func TestOneClientCannotReadAnotherClientsScan(t *testing.T) {
	pipeline := newPipeline(t)
	ctx := context.Background()

	created := createScanForTenant(t, pipeline, "client:1", "tenant-hash-1")

	owner, err := pipeline.service.GetForTenant(ctx, created.ID, "client:1")
	require.NoError(t, err)
	assert.Equal(t, created.ID, owner.ID)
	assert.Equal(t, "client:1", owner.ClientKey)

	_, err = pipeline.service.GetForTenant(ctx, created.ID, "client:2")
	require.ErrorIs(t, err, scan.ErrScanNotFound, "another client must not see this scan")

	_, err = pipeline.service.GetForTenant(ctx, created.ID, auth.DefaultClientKey)
	require.ErrorIs(t, err, scan.ErrScanNotFound, "the legacy tenant must not see a client scan")

	_, err = pipeline.service.GetForTenant(ctx, uuid.New(), "client:1")
	require.ErrorIs(t, err, scan.ErrScanNotFound, "a missing scan must look the same as a foreign one")
}

// The HTTP surface must apply the same rule and must not leak the internal
// score or a reassurance word.
func TestScanHTTPGetIsTenantScopedAndScoreFree(t *testing.T) {
	pipeline := newPipeline(t)
	created := createScanForTenant(t, pipeline, "client:1", "tenant-hash-2")

	owner := tenantRouter(t, pipeline, tenantOwnerKey, 1)
	response := httptest.NewRecorder()
	owner.ServeHTTP(response, tenantRequest(created.ID.String(), tenantOwnerKey))

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())

	var view scan.View
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &view))
	assert.Equal(t, created.ID.String(), view.ID)
	assert.Equal(t, scan.Disclaimer, view.Disclaimer)
	assert.NotContains(t, response.Body.String(), `"score"`)

	lower := strings.ToLower(response.Body.String())
	assert.NotContains(t, lower, "safe")
	assert.NotContains(t, lower, "genuine")
	assert.NotContains(t, lower, "authentic")

	other := tenantRouter(t, pipeline, tenantOtherKey, 2)
	response = httptest.NewRecorder()
	other.ServeHTTP(response, tenantRequest(created.ID.String(), tenantOtherKey))

	assert.Equal(t, http.StatusNotFound, response.Code, "a foreign scan must be a 404, not a 403")
	assert.NotContains(t, response.Body.String(), "client:1", "the tenant key must not leak")
}

func createScanForTenant(t *testing.T, pipeline *testPipeline, tenantKey, hash string) scan.Scan {
	t.Helper()

	created, isNew, err := pipeline.service.Create(context.Background(), scan.CreateRequest{
		ClientKey:   tenantKey,
		RequestHash: hash,
		Images:      [][]byte{readImage(t, "normal.jpg")},
	})
	require.NoError(t, err)
	require.True(t, isNew)
	return created
}

func tenantRouter(t *testing.T, pipeline *testPipeline, key string, clientID int64) http.Handler {
	t.Helper()

	handler := scan.NewHTTPHandler(scan.HTTPOptions{Service: pipeline.service})
	router := chi.NewRouter()
	router.Use(auth.APIKey(auth.NewSingleKeyStore(auth.SingleKeyOptions{Key: key, ClientID: clientID})))
	router.Get("/v1/scans/{id}", handler.Get)
	return router
}

func tenantRequest(scanID, key string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/v1/scans/"+scanID, nil)
	request.Header.Set(auth.HeaderAPIKey, key)
	return request
}
