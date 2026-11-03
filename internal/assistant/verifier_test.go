package assistant

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/benfaruna/medverify/internal/llm"
)

func supportedFake() *llm.Fake {
	return llm.NewFakeTools(llm.ToolCall{
		ID: "support-1", Name: supportToolName,
		Input: json.RawMessage(`{"results":[{"index":0,"supported":true}]}`),
	})
}

func citationMap() map[string]Citation {
	return map[string]Citation{
		"leaflet:1": {ID: "leaflet:1", Kind: "leaflet", Text: "Do not take more than 4000 mg in 24 hours."},
	}
}

func TestVerifierAcceptsASupportedCitedClaim(t *testing.T) {
	verifier := NewVerifier(supportedFake(), "verifier prompt")
	result := verifier.Verify(context.Background(), Draft{Claims: []Claim{{
		Text: "Do not take more than 4000 mg in 24 hours.", SourceIDs: []string{"leaflet:1"},
	}}}, citationMap())
	if !result.Allowed {
		t.Fatalf("result = %#v", result)
	}
}

func TestVerifierRejectsUncitedClaim(t *testing.T) {
	verifier := NewVerifier(supportedFake(), "prompt")
	result := verifier.Verify(context.Background(), Draft{Claims: []Claim{{
		Text: "It is registered.",
	}}}, citationMap())
	if result.Allowed || result.Reason != "uncited_claim" {
		t.Fatalf("result = %#v", result)
	}
}

func TestVerifierRejectsUnknownSource(t *testing.T) {
	verifier := NewVerifier(supportedFake(), "prompt")
	result := verifier.Verify(context.Background(), Draft{Claims: []Claim{{
		Text: "It is registered.", SourceIDs: []string{"made:up"},
	}}}, citationMap())
	if result.Allowed || result.Reason != "unknown_source" {
		t.Fatalf("result = %#v", result)
	}
}

func TestVerifierRejectsNumberNotPresentInSources(t *testing.T) {
	verifier := NewVerifier(supportedFake(), "prompt")
	result := verifier.Verify(context.Background(), Draft{Claims: []Claim{{
		Text: "The dose is 500 mg.", SourceIDs: []string{"leaflet:1"},
	}}}, citationMap())
	if result.Allowed || result.Reason != "unsupported_number" {
		t.Fatalf("result = %#v", result)
	}
}

func TestVerifierRejectsForbiddenReassuranceOutsideQuotes(t *testing.T) {
	verifier := NewVerifier(supportedFake(), "prompt")
	result := verifier.Verify(context.Background(), Draft{Claims: []Claim{{
		Text: "This medicine is safe for you.", SourceIDs: []string{"leaflet:1"},
	}}}, citationMap())
	if result.Allowed || result.Reason != "forbidden_wording" {
		t.Fatalf("result = %#v", result)
	}
}

func TestVerifierAllowsQuotedLeafletWording(t *testing.T) {
	citations := map[string]Citation{
		"leaflet:1": {ID: "leaflet:1", Kind: "leaflet", Text: `The leaflet says "safe for most adults".`},
	}
	verifier := NewVerifier(supportedFake(), "prompt")
	result := verifier.Verify(context.Background(), Draft{Claims: []Claim{{
		Text: `The leaflet says "safe for most adults".`, SourceIDs: []string{"leaflet:1"},
	}}}, citations)
	if !result.Allowed {
		t.Fatalf("quoted leaflet wording must be allowed: %#v", result)
	}
}

func TestVerifierSupportCheckFailureLeadsToFallback(t *testing.T) {
	fake := llm.NewFakeTools(llm.ToolCall{
		ID: "support-1", Name: supportToolName,
		Input: json.RawMessage(`{"results":[{"index":0,"supported":false}]}`),
	})
	verifier := NewVerifier(fake, "prompt")
	result := verifier.Verify(context.Background(), Draft{Claims: []Claim{{
		Text: "Do not take more than 4000 mg in 24 hours.", SourceIDs: []string{"leaflet:1"},
	}}}, citationMap())
	if result.Allowed || result.Reason != "support_check_failed" {
		t.Fatalf("result = %#v", result)
	}
}

func TestVerifierSupportCheckMalformedFailsClosed(t *testing.T) {
	fake := llm.NewFakeTools(llm.ToolCall{
		ID: "support-1", Name: supportToolName,
		Input: json.RawMessage(`{"results":[{"index":0,"supported":true}]}`),
	})
	verifier := NewVerifier(fake, "prompt")
	result := verifier.Verify(context.Background(), Draft{Claims: []Claim{
		{Text: "Do not take more than 4000 mg in 24 hours.", SourceIDs: []string{"leaflet:1"}},
		{Text: "Keep out of reach of children.", SourceIDs: []string{"leaflet:1"}},
	}}, citationMap())
	if result.Allowed || result.Reason != "support_check_failed" {
		t.Fatalf("a missing result must fail closed: %#v", result)
	}
}

func TestVerifierWithoutClientFailsClosed(t *testing.T) {
	verifier := NewVerifier(nil, "prompt")
	result := verifier.Verify(context.Background(), Draft{Claims: []Claim{{
		Text: "Do not take more than 4000 mg in 24 hours.", SourceIDs: []string{"leaflet:1"},
	}}}, citationMap())
	if result.Allowed || result.Reason != "support_check_unavailable" {
		t.Fatalf("result = %#v", result)
	}
}
