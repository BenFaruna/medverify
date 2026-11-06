package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	medapi "github.com/benfaruna/medverify/api"
	"github.com/benfaruna/medverify/internal/account"
	"github.com/benfaruna/medverify/internal/assistant"
	"github.com/benfaruna/medverify/internal/scan"
)

// The served document must describe the router, in both directions: a route
// added without an operation fails here, and a documented operation with no
// route fails too. Documentation is part of the route, not an afterthought.
func TestEveryV1RouteIsDocumented(t *testing.T) {
	t.Parallel()

	router, ok := NewHandler(Options{
		Scan:      &scan.HTTPHandler{},
		Assistant: &assistant.HTTPHandler{},
		Account:   account.NewHTTPHandler(account.HTTPOptions{}),
	}).(*chi.Mux)
	require.True(t, ok, "NewHandler must return the chi router")

	registered := make(map[string]bool)
	require.NoError(t, chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(route, "/v1/") {
			registered[method+" "+route] = true
		}
		return nil
	}))
	require.NotEmpty(t, registered, "the router must expose the documented API")

	loader := openapi3.NewLoader()
	document, err := loader.LoadFromData(medapi.Spec())
	require.NoError(t, err, "the served document must parse")

	documented := make(map[string]bool)
	for path, item := range document.Paths.Map() {
		if !strings.HasPrefix(path, "/v1/") {
			continue
		}
		for method := range item.Operations() {
			name := method + " " + path
			documented[name] = true
			assert.True(t, registered[name], "%s is documented but not routed", name)
		}
	}

	for route := range registered {
		assert.True(t, documented[route], "%s is routed but not documented", route)
	}
}
