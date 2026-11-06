package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/benfaruna/medverify/internal/rag"
	"github.com/benfaruna/medverify/internal/safety"
	"github.com/benfaruna/medverify/internal/verify"
)

type fakeSearcher struct {
	requests []rag.SearchRequest
	hits     []rag.Hit
	err      error
}

func (f *fakeSearcher) Search(_ context.Context, request rag.SearchRequest) ([]rag.Hit, error) {
	f.requests = append(f.requests, request)
	return f.hits, f.err
}

func testSession() Session {
	expires := time.Date(2028, 1, 31, 0, 0, 0, 0, time.UTC)
	return Session{
		ScanID:    uuid.MustParse("11111111-1111-4111-8111-111111111111"),
		ProductID: 42,
		Language:  "en",
		Match: verify.MatchResult{
			Outcome: verify.MatchedConsistent,
			Product: &verify.ProductRef{
				SourceID: "8875", NRN: "04-0858", Name: "Example Tablets",
				Manufacturer: "Example Pharma", Applicant: "Example Applicant",
				Status: "Active", ExpiresAt: &expires,
				Ingredients: []verify.IngredientRef{{Name: "Paracetamol", Strength: "500 mg"}},
			},
		},
	}
}

func TestSearchLeafletIgnoresModelSuppliedProductScope(t *testing.T) {
	searcher := &fakeSearcher{hits: []rag.Hit{{
		ChunkID: 9, SectionCode: "warnings", Heading: "Warnings", Text: "Keep out of reach of children.",
		SourceURL: "https://example.test/leaflet.pdf",
	}}}
	toolbox := NewToolbox(searcher)
	row := DefaultPolicy().Decide(IntentLeaflet)
	session := testSession()

	args := json.RawMessage(`{"query":"children","product_id":999,"nrn":"99-9999","scan_id":"22222222-2222-4222-8222-222222222222"}`)
	result, err := toolbox.Call(context.Background(), row, session, ToolSearchLeaflet, args)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatal(result.Content)
	}
	if len(searcher.requests) != 1 {
		t.Fatalf("requests = %d", len(searcher.requests))
	}
	if searcher.requests[0].ProductID != session.ProductID {
		t.Fatalf("retrieval used product %d, want session product %d", searcher.requests[0].ProductID, session.ProductID)
	}
	if len(result.Citations) != 1 || result.Citations[0].ID != "leaflet:9" {
		t.Fatalf("citations = %#v", result.Citations)
	}
}

func TestIndicationsRowForcesItsSection(t *testing.T) {
	searcher := &fakeSearcher{}
	toolbox := NewToolbox(searcher)
	row := DefaultPolicy().Decide(IntentIndications)
	_, err := toolbox.Call(context.Background(), row, testSession(), ToolSearchLeaflet,
		json.RawMessage(`{"query":"what is it used for","section":"warnings"}`))
	if err != nil {
		t.Fatal(err)
	}
	if searcher.requests[0].SectionCode != "indications" {
		t.Fatalf("section = %q, want indications", searcher.requests[0].SectionCode)
	}
}

func TestToolsRefuseWhenPolicyDoesNotAllowThem(t *testing.T) {
	toolbox := NewToolbox(&fakeSearcher{})
	row := DefaultPolicy().Decide(IntentRegistration)
	_, err := toolbox.Call(context.Background(), row, testSession(), ToolSearchLeaflet,
		json.RawMessage(`{"query":"children"}`))
	if err == nil {
		t.Fatal("expected the policy gate to reject the tool")
	}
}

func TestRegistrationUsesTheConfirmedRecordAndIgnoresArguments(t *testing.T) {
	toolbox := NewToolbox(&fakeSearcher{})
	row := DefaultPolicy().Decide(IntentRegistration)
	result, err := toolbox.Call(context.Background(), row, testSession(), ToolRegistration,
		json.RawMessage(`{"nrn":"00-0000"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(result.Citations) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if result.Citations[0].ID != "registry:8875" {
		t.Fatalf("citation = %#v", result.Citations[0])
	}
	var content map[string]any
	if err := json.Unmarshal([]byte(result.Content), &content); err != nil {
		t.Fatal(err)
	}
	registration := content["registration"].(map[string]any)
	if registration["nrn"] != "04-0858" {
		t.Fatalf("content used model argument: %#v", registration)
	}
}

func TestRegistrationWithoutAConfirmedProductIsNotFound(t *testing.T) {
	toolbox := NewToolbox(&fakeSearcher{})
	session := testSession()
	session.Match = verify.MatchResult{Outcome: verify.NotFoundInDataset}
	result, err := toolbox.Call(context.Background(), DefaultPolicy().Decide(IntentRegistration),
		session, ToolRegistration, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Citations) != 0 {
		t.Fatalf("not-found result must not cite: %#v", result.Citations)
	}
	var content map[string]any
	_ = json.Unmarshal([]byte(result.Content), &content)
	if content["found"] != false {
		t.Fatalf("content = %s", result.Content)
	}
}

func TestSafetyFlagsAbsenceIsNotAnAnswer(t *testing.T) {
	toolbox := NewToolbox(&fakeSearcher{})
	result, err := toolbox.Call(context.Background(), DefaultPolicy().Decide(IntentSafetyFlags),
		testSession(), ToolSafetyFlags, nil)
	if err != nil {
		t.Fatal(err)
	}
	var content map[string]any
	_ = json.Unmarshal([]byte(result.Content), &content)
	if content["found"] != false {
		t.Fatalf("absence of a flag must not become a grounded claim: %s", result.Content)
	}
	if len(result.Citations) != 0 {
		t.Fatalf("unexpected citations: %#v", result.Citations)
	}
}

func TestSafetyFlagsCarryTheirSource(t *testing.T) {
	toolbox := NewToolbox(&fakeSearcher{})
	session := testSession()
	session.Flags = []safety.Flag{{
		Type: safety.WatchlistProduct, Severity: safety.SeverityHigh,
		ReasonCode: safety.ReasonWatchlistAlert,
		Evidence:   map[string]string{"nrn": "04-0858"},
		Source: safety.Source{
			WatchlistEntryID: 7, IssuingBody: "NAFDAC", Title: "Alert 1",
			URL: "https://example.test/alert",
		},
	}}
	result, err := toolbox.Call(context.Background(), DefaultPolicy().Decide(IntentSafetyFlags),
		session, ToolSafetyFlags, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Citations) != 1 || result.Citations[0].ID != "watchlist:7" {
		t.Fatalf("citations = %#v", result.Citations)
	}
	if result.Citations[0].URL == "" {
		t.Fatal("a flag citation must keep its URL")
	}
}

// A leaflet question always has a document to point at, and the tool says which
// of the two gaps it hit: nothing downloaded, or downloaded but not searchable.
func TestSearchLeafletCarriesTheDocumentAndSaysWhyNothingWasFound(t *testing.T) {
	t.Parallel()

	const link = "https://admin.greenbook.nafdac.gov.ng/leaflets/04-0858.pdf"
	cases := []struct {
		name       string
		status     rag.LeafletStatus
		wantReason string
	}{
		{
			name:       "no document on file",
			status:     rag.LeafletStatus{ProductID: 42, URL: link},
			wantReason: "leaflet_not_readable",
		},
		{
			name: "downloaded but not indexed",
			status: rag.LeafletStatus{
				ProductID: 42, URL: link, DocumentID: 9, DocumentURL: link,
				DocumentStatus: "ok", Chunks: 0,
			},
			wantReason: "leaflet_not_readable",
		},
		{
			name: "indexed but no matching passage",
			status: rag.LeafletStatus{
				ProductID: 42, URL: link, DocumentID: 9, DocumentURL: link,
				DocumentStatus: "ok", Chunks: 3,
			},
			wantReason: "no_match",
		},
	}
	for _, tc := range cases {
		session := testSession()
		session.Leaflet = tc.status
		result, err := NewToolbox(&fakeSearcher{}).Call(
			context.Background(), DefaultPolicy().Decide(IntentLeaflet), session, ToolSearchLeaflet,
			json.RawMessage(`{"query":"warnings"}`),
		)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}

		var content struct {
			Found       bool   `json:"found"`
			Reason      string `json:"reason"`
			DocumentURL string `json:"document_url"`
		}
		if err := json.Unmarshal([]byte(result.Content), &content); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if content.Found || content.Reason != tc.wantReason || content.DocumentURL != link {
			t.Fatalf("%s: content = %s", tc.name, result.Content)
		}

		// The link is a citation, so an answer that gives it can be verified.
		if len(result.Citations) != 1 {
			t.Fatalf("%s: citations = %#v", tc.name, result.Citations)
		}
		citation := result.Citations[0]
		if citation.Kind != "leaflet_document" || citation.URL != link {
			t.Fatalf("%s: citation = %#v", tc.name, citation)
		}
	}
}

// A scan with no confirmed registry record cannot name a leaflet at all, which
// is a different answer from a leaflet that has not been read yet.
func TestSearchLeafletWithoutAProductSaysThereIsNoRecord(t *testing.T) {
	t.Parallel()

	session := testSession()
	session.ProductID = 0
	result, err := NewToolbox(&fakeSearcher{}).Call(
		context.Background(), DefaultPolicy().Decide(IntentLeaflet), session, ToolSearchLeaflet,
		json.RawMessage(`{"query":"warnings"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	var content struct {
		Found  bool   `json:"found"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(result.Content), &content); err != nil {
		t.Fatal(err)
	}
	if content.Found || content.Reason != "no_registry_product" {
		t.Fatalf("content = %s", result.Content)
	}
}
