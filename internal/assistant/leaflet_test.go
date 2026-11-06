package assistant

import (
	"context"
	"testing"

	"github.com/benfaruna/medverify/internal/llm"
	"github.com/benfaruna/medverify/internal/rag"
)

type fakeLeaflets struct {
	status rag.LeafletStatus
	found  bool
	err    error
}

func (f *fakeLeaflets) LeafletStatus(context.Context, int64) (rag.LeafletStatus, bool, error) {
	return f.status, f.found, f.err
}

type fakeFetches struct {
	products []int64
	err      error
}

func (f *fakeFetches) EnsureLeaflet(_ context.Context, productID int64) error {
	f.products = append(f.products, productID)
	return f.err
}

func newLeafletService(t *testing.T, client llm.Client, leaflets LeafletSource, fetches LeafletFetcher, searcher LeafletSearcher, resolver ProductResolver) (*Service, *fakeTranscriptStore, *fakeScans) {
	t.Helper()
	catalog, err := LoadTemplates(DefaultLanguage)
	if err != nil {
		t.Fatal(err)
	}
	prompts, err := LoadPrompts(PromptVersion)
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeTranscriptStore{}
	scans := &fakeScans{scanned: scannedFixture()}
	if resolver == nil {
		resolver = &fakeResolver{id: 42, found: true}
	}
	service, err := NewService(ServiceOptions{
		Store: store, Scans: scans, Resolver: resolver, Searcher: searcher,
		Leaflets: leaflets, Fetches: fetches, Client: client, Prompts: prompts,
		Catalogs: map[string]Catalog{"en": catalog}, MaxTurns: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, store, scans
}

// The reported bug: "Where can I read the leaflet?" was answered with "I did not
// understand that as a question". A pack whose leaflet has a known URL but no
// stored text gets a fixed answer that admits the gap, keeps the link, and asks
// for the download instead of pretending the question was unclear.
func TestAskPointsAtTheLeafletAndQueuesAFetchWhenNoneIsStored(t *testing.T) {
	t.Parallel()

	const link = "https://admin.greenbook.nafdac.gov.ng/leaflets/04-0858.pdf"
	leaflets := &fakeLeaflets{found: true, status: rag.LeafletStatus{
		ProductID: 42, URL: link, DocumentID: 0, DocumentStatus: "", Chunks: 0,
	}}
	fetches := &fakeFetches{}
	client := llm.NewFake(routeResponse("leaflet"))
	service, store, scans := newLeafletService(t, client, leaflets, fetches, &fakeSearcher{}, nil)
	catalog, _ := LoadTemplates(DefaultLanguage)
	expected, _ := catalog.Text(TemplateFallbackNoLeaflet)

	answer, err := service.Ask(context.Background(), AskRequest{
		ScanID: scans.scanned.ID, ClientKey: "client", Question: "Where can I read the leaflet?",
	})
	if err != nil {
		t.Fatal(err)
	}

	if answer.Answer != expected {
		t.Fatalf("answer = %q", answer.Answer)
	}
	if answer.Intent != IntentLeaflet || !answer.Fallback || answer.Verified {
		t.Fatalf("view = %#v", answer)
	}
	if len(answer.Sources) != 1 || answer.Sources[0].URL != link {
		t.Fatalf("sources = %#v; the answer must carry the leaflet link", answer.Sources)
	}
	if len(fetches.products) != 1 || fetches.products[0] != 42 {
		t.Fatalf("queued fetches = %v; the download must start in the background", fetches.products)
	}
	// The router is the only model call: with nothing to quote there is nothing
	// for the orchestrator to verify, so it must not run.
	if client.CallsCount() != 1 {
		t.Fatalf("model was called %d times", client.CallsCount())
	}
	if len(store.recorded) != 1 || store.recorded[0].Verifier != "leaflet_unavailable" {
		t.Fatalf("recorded = %#v", store.recorded)
	}
}

// The same question for a pack with no confirmed registry record must say so,
// and must not queue a download that has no URL to fetch.
func TestAskAdmitsThereIsNoRecordInsteadOfQueueingAFetch(t *testing.T) {
	t.Parallel()

	leaflets := &fakeLeaflets{}
	fetches := &fakeFetches{}
	client := llm.NewFake(routeResponse("leaflet"))
	service, store, scans := newLeafletService(t, client, leaflets, fetches, &fakeSearcher{},
		&fakeResolver{found: false})
	catalog, _ := LoadTemplates(DefaultLanguage)
	expected, _ := catalog.Text(TemplateFallbackNotFound)

	answer, err := service.Ask(context.Background(), AskRequest{
		ScanID: scans.scanned.ID, ClientKey: "client", Question: "What does the leaflet say about storage?",
	})
	if err != nil {
		t.Fatal(err)
	}
	if answer.Answer != expected {
		t.Fatalf("answer = %q", answer.Answer)
	}
	if len(answer.Sources) != 0 {
		t.Fatalf("sources = %#v", answer.Sources)
	}
	if len(fetches.products) != 0 {
		t.Fatalf("queued fetches = %v", fetches.products)
	}
	if len(store.recorded) != 1 || store.recorded[0].Verifier != "leaflet_no_record" {
		t.Fatalf("recorded = %#v", store.recorded)
	}
}

// A stored, indexed leaflet is quoted by the normal path: the fixed "no leaflet"
// answer would be wrong, and no download should be queued.
func TestAskQuotesAReadableLeafletWithoutQueueingAFetch(t *testing.T) {
	t.Parallel()

	leaflets := &fakeLeaflets{found: true, status: rag.LeafletStatus{
		ProductID: 42, URL: "https://example.test/leaflet.pdf", DocumentID: 9,
		DocumentURL: "https://example.test/leaflet.pdf", DocumentStatus: "ok", Chunks: 12,
	}}
	fetches := &fakeFetches{}
	searcher := &fakeSearcher{hits: []rag.Hit{{
		ChunkID: 5, DocumentID: 9, SectionCode: "4.4", Heading: "Warnings",
		Text: "Keep out of reach of children.", SourceURL: "https://example.test/leaflet.pdf",
	}}}
	client := llm.NewFake(
		routeResponse("leaflet"),
		toolCall("search-1", ToolSearchLeaflet, `{"query":"warnings"}`),
		submitAnswer(`{"not_found":false,"claims":[{"text":"The leaflet says: \"Keep out of reach of children.\"","source_ids":["leaflet:5"]}]}`),
		supportAll(),
	)
	service, _, scans := newLeafletService(t, client, leaflets, fetches, searcher, nil)
	catalog, _ := LoadTemplates(DefaultLanguage)
	missing, _ := catalog.Text(TemplateFallbackNoLeaflet)

	answer, err := service.Ask(context.Background(), AskRequest{
		ScanID: scans.scanned.ID, ClientKey: "client", Question: "What does the leaflet say about warnings?",
	})
	if err != nil {
		t.Fatal(err)
	}
	if answer.Answer == missing {
		t.Fatal("a readable leaflet must not use the no-leaflet fallback")
	}
	if !answer.Verified {
		t.Fatalf("view = %#v", answer)
	}
	if len(fetches.products) != 0 {
		t.Fatalf("queued fetches = %v", fetches.products)
	}
}

// A readable leaflet that simply has no passage for the question still gives the
// person a way to read it: the fixed not-found text arrives with the link.
func TestAskKeepsTheLeafletLinkWhenNoPassageMatches(t *testing.T) {
	t.Parallel()

	const link = "https://admin.greenbook.nafdac.gov.ng/leaflets/04-0858.pdf"
	leaflets := &fakeLeaflets{found: true, status: rag.LeafletStatus{
		ProductID: 42, URL: link, DocumentID: 9, DocumentURL: link,
		DocumentStatus: "ok", Chunks: 6,
	}}
	client := llm.NewFake(
		routeResponse("leaflet"),
		toolCall("search-1", ToolSearchLeaflet, `{"query":"batch number"}`),
		submitAnswer(`{"not_found":true,"claims":[]}`),
	)
	service, _, scans := newLeafletService(t, client, leaflets, &fakeFetches{}, &fakeSearcher{}, nil)
	catalog, _ := LoadTemplates(DefaultLanguage)
	expected, _ := catalog.Text(TemplateFallbackNotFound)

	answer, err := service.Ask(context.Background(), AskRequest{
		ScanID: scans.scanned.ID, ClientKey: "client", Question: "What is the batch number in the leaflet?",
	})
	if err != nil {
		t.Fatal(err)
	}
	if answer.Answer != expected {
		t.Fatalf("answer = %q", answer.Answer)
	}
	if len(answer.Sources) != 1 || answer.Sources[0].URL != link {
		t.Fatalf("sources = %#v", answer.Sources)
	}
}
