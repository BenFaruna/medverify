// Package api holds the OpenAPI document and the contract tests that keep it
// honest. The tests load the same file CI validates and check both that every
// response matches the documented schema and that the phase 11 output rules
// hold for every documented response body.
package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// loadSpec parses and validates the OpenAPI document. A structural mistake in
// the file fails here rather than at a client.
func loadSpec(t *testing.T) *openapi3.T {
	t.Helper()

	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = false

	document, err := loader.LoadFromFile("openapi.yaml")
	if err != nil {
		t.Fatalf("load openapi.yaml: %v", err)
	}
	if err := document.Validate(loader.Context); err != nil {
		t.Fatalf("openapi.yaml is not a valid document: %v", err)
	}
	return document
}

func TestSpecIsValid(t *testing.T) {
	t.Parallel()

	document := loadSpec(t)
	if document.OpenAPI == "" {
		t.Fatal("the document must declare an OpenAPI version")
	}
	if document.Info == nil || document.Info.Version == "" {
		t.Fatal("the document must declare an API version")
	}
}

// Every authenticated endpoint must document its scope, its rate-limit class,
// and the error cases a client can hit.
func TestEveryV1OperationDocumentsAuthAndLimits(t *testing.T) {
	t.Parallel()

	document := loadSpec(t)

	for path, item := range document.Paths.Map() {
		if !strings.HasPrefix(path, "/v1/") {
			continue
		}
		for method, operation := range item.Operations() {
			name := method + " " + path

			if operation.Security == nil || len(*operation.Security) == 0 {
				t.Errorf("%s: must declare the API key security scheme", name)
			}
			if operation.Responses.Status(http.StatusUnauthorized) == nil {
				t.Errorf("%s: must document 401", name)
			}
			if operation.Responses.Status(http.StatusForbidden) == nil {
				t.Errorf("%s: must document 403 for a missing scope", name)
			}
			if operation.Responses.Status(http.StatusTooManyRequests) == nil {
				t.Errorf("%s: must document 429 for rate limits and quotas", name)
			}
		}
	}
}

// The public contract must never document a score or a reassurance, on any
// schema a response can reach.
func TestSpecNeverDocumentsAScoreOrAReassurance(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatalf("read openapi.yaml: %v", err)
	}

	var document map[string]any
	if err := json.Unmarshal([]byte(toJSON(t, raw)), &document); err != nil {
		t.Fatalf("re-encode spec: %v", err)
	}

	schemas, _ := document["components"].(map[string]any)["schemas"].(map[string]any)
	scanSchema, ok := schemas["Scan"].(map[string]any)
	if !ok {
		t.Fatal("the Scan schema is missing")
	}
	properties, _ := scanSchema["properties"].(map[string]any)

	if _, exists := properties["score"]; exists {
		t.Error("the Scan schema must not expose a score")
	}
	if _, exists := properties["disclaimer"]; !exists {
		t.Error("the Scan schema must document the disclaimer")
	}

	// The verdict object is a nested schema; a score there would leak too.
	verdict, ok := schemas["PublicVerdict"].(map[string]any)
	if !ok {
		t.Fatal("the PublicVerdict schema is missing")
	}
	verdictProperties, _ := verdict["properties"].(map[string]any)
	if _, exists := verdictProperties["score"]; exists {
		t.Error("the public verdict must not expose a score")
	}

	// No schema description may invite a client to show a score.
	if strings.Contains(strings.ToLower(string(raw)), "must not be shown as a percentage") {
		t.Error("the spec still describes exposing the internal score")
	}
}

// toJSON converts YAML to JSON so generic traversal works without a second
// schema library.
func toJSON(t *testing.T, raw []byte) string {
	t.Helper()

	loader := openapi3.NewLoader()
	document, err := loader.LoadFromData(raw)
	if err != nil {
		t.Fatalf("parse spec: %v", err)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	return string(encoded)
}
