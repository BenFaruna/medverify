package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/google/uuid"

	"github.com/benfaruna/medverify/internal/platform/httpx"
	"github.com/benfaruna/medverify/internal/scan"
	"github.com/benfaruna/medverify/internal/verdict"
)

// validateBody checks a JSON body against a named schema in the spec.
func validateBody(t *testing.T, document *openapi3.T, schemaName string, body []byte) {
	t.Helper()

	schema, ok := document.Components.Schemas[schemaName]
	if !ok {
		t.Fatalf("schema %q is missing from the spec", schemaName)
	}
	if err := schema.Value.VisitJSON(decoded(t, body)); err != nil {
		t.Fatalf("%s response does not match the spec: %v\nbody: %s", schemaName, err, body)
	}
}

func decoded(t *testing.T, body []byte) any {
	t.Helper()

	var value any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode body: %v\nbody: %s", err, body)
	}
	return value
}

// A completed scan is the body every client reads. It must match the Scan
// schema exactly, including the disclaimer and the absence of a score.
func TestCompletedScanMatchesTheSchema(t *testing.T) {
	t.Parallel()

	document := loadSpec(t)
	catalog, err := verdict.LoadCatalog("")
	if err != nil {
		t.Fatalf("load reason catalog: %v", err)
	}

	completed := time.Now().UTC().Truncate(time.Second)
	scanned := scan.Scan{
		ID:          uuid.New(),
		Status:      scan.StatusCompleted,
		CreatedAt:   completed,
		CompletedAt: &completed,
		Result: &scan.Result{
			Verdict: &verdict.Result{
				Verdict:    verdict.ConsistentWithRecords,
				Score:      91,
				Confidence: verdict.ConfidenceHigh,
				Reasons: []verdict.Reason{
					{Code: verdict.ReasonLabelConsistent, Params: map[string]string{}},
				},
				RulesVersion: "verdict-v1",
			},
			Extracted: json.RawMessage(`{"nrn":{"value":"01-2345","confidence":0.97,"source":1},"unreadable":[],"conflicts":[]}`),
			// verify.MatchResult always emits fields, null when nothing was
			// compared, so this exercises the documented null case.
			Features: json.RawMessage(`{"outcome":"matched_consistent","fields":null,"dataset":{"config_version":"v1","products":198}}`),
		},
	}

	body, err := json.Marshal(scanned.View(&catalog))
	if err != nil {
		t.Fatalf("marshal view: %v", err)
	}
	validateBody(t, document, "Scan", body)

	if strings.Contains(string(body), `"score"`) {
		t.Fatalf("the contract response must not contain a score: %s", body)
	}
}

// A pending scan is also a documented response, and its nulls must be allowed.
func TestPendingScanMatchesTheSchema(t *testing.T) {
	t.Parallel()

	document := loadSpec(t)

	scanned := scan.Scan{
		ID:        uuid.New(),
		Status:    scan.StatusQueued,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}

	body, err := json.Marshal(scanned.View(nil))
	if err != nil {
		t.Fatalf("marshal view: %v", err)
	}
	validateBody(t, document, "Scan", body)
}

// Every problem the API can return must match the Problem schema, including the
// newer auth and quota codes.
func TestProblemResponsesMatchTheSchema(t *testing.T) {
	t.Parallel()

	document := loadSpec(t)

	cases := []struct {
		name    string
		problem *httpx.Problem
	}{
		{"unauthorized", httpx.NewProblem(http.StatusUnauthorized, httpx.CodeUnauthorized, "A valid X-API-Key header is required.")},
		{"insufficient_scope", httpx.NewProblem(http.StatusForbidden, httpx.CodeInsufficientScope, "The API key does not have permission to perform this action.")},
		{"rate_limited", httpx.NewProblem(http.StatusTooManyRequests, httpx.CodeRateLimited, "Too many requests.")},
		{"quota_exceeded", httpx.NewProblem(http.StatusTooManyRequests, httpx.CodeQuotaExceeded, "The daily scan quota for this API key is used up.")},
	}

	for _, testCase := range cases {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/v1/scans", nil)
		httpx.WriteProblem(recorder, request, testCase.problem)

		validateBody(t, document, "Problem", recorder.Body.Bytes())

		var problem httpx.Problem
		if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
			t.Fatalf("%s: decode problem: %v", testCase.name, err)
		}
		if problem.Code != testCase.problem.Code {
			t.Fatalf("%s: code = %q, want %q", testCase.name, problem.Code, testCase.problem.Code)
		}
	}
}

// The documented error enum must cover every code the problems package can
// emit, so a client generated from the spec can branch on all of them.
func TestProblemSchemaCoversEveryCode(t *testing.T) {
	t.Parallel()

	document := loadSpec(t)
	problem := document.Components.Schemas["Problem"]
	if problem == nil {
		t.Fatal("the Problem schema is missing")
	}

	codeSchema := problem.Value.Properties["code"]
	if codeSchema == nil || codeSchema.Value == nil {
		t.Fatal("the Problem schema must define code")
	}

	documented := make(map[string]bool, len(codeSchema.Value.Enum))
	for _, value := range codeSchema.Value.Enum {
		if text, ok := value.(string); ok {
			documented[text] = true
		}
	}

	for _, code := range []string{
		httpx.CodeBadRequest,
		httpx.CodeConflict,
		httpx.CodeCorruptImage,
		httpx.CodeImageTooLarge,
		httpx.CodeInsufficientScope,
		httpx.CodeInternalError,
		httpx.CodeInvalidLanguage,
		httpx.CodeInvalidLocation,
		httpx.CodeMethodNotAllowed,
		httpx.CodeNotFound,
		httpx.CodePayloadTooLarge,
		httpx.CodeQuotaExceeded,
		httpx.CodeRateLimited,
		httpx.CodeTimeout,
		httpx.CodeTooManyImages,
		httpx.CodeUnauthorized,
		httpx.CodeUnavailable,
		httpx.CodeUnsupportedFormat,
	} {
		if !documented[code] {
			t.Errorf("code %q is emitted by the API but not documented in the Problem enum", code)
		}
	}
}
