package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/benfaruna/medverify/internal/llm"
	"github.com/benfaruna/medverify/internal/scan"
	"github.com/benfaruna/medverify/internal/verdict"
	"github.com/benfaruna/medverify/internal/verify"
)

type fakeTranscriptStore struct {
	conversation Conversation
	exists       bool
	messages     []Message
	recorded     []TurnRecord
}

func (f *fakeTranscriptStore) GetConversation(_ context.Context, scanID uuid.UUID) (Conversation, bool, error) {
	if !f.exists || f.conversation.ScanID != scanID {
		return Conversation{}, false, nil
	}
	return f.conversation, true, nil
}

func (f *fakeTranscriptStore) GetOrCreate(_ context.Context, scanID uuid.UUID, language, rulesVersion, promptVersion string, ttl time.Duration) (Conversation, error) {
	if f.exists {
		return f.conversation, nil
	}
	f.exists = true
	f.conversation = Conversation{
		ID: uuid.New(), ScanID: scanID, Language: language,
		RulesVersion: rulesVersion, PromptVersion: promptVersion,
		ExpiresAt: time.Now().Add(ttl),
	}
	return f.conversation, nil
}

func (f *fakeTranscriptStore) CountUserMessages(_ context.Context, _ uuid.UUID) (int, error) {
	count := 0
	for _, message := range f.messages {
		if message.Role == "user" {
			count++
		}
	}
	return count, nil
}

func (f *fakeTranscriptStore) Messages(_ context.Context, _ uuid.UUID) ([]Message, error) {
	return f.messages, nil
}

func (f *fakeTranscriptStore) RecordTurn(_ context.Context, record TurnRecord) error {
	f.recorded = append(f.recorded, record)
	f.messages = append(f.messages,
		Message{Turn: record.Turn, Role: "user", Content: record.User},
		Message{Turn: record.Turn, Role: "assistant", Content: record.Answer},
	)
	return nil
}

type fakeScans struct {
	scanned scan.Scan
	err     error
}

func (f *fakeScans) Get(_ context.Context, _ uuid.UUID) (scan.Scan, error) {
	return f.scanned, f.err
}

func scannedFixture() scan.Scan {
	features, _ := json.Marshal(verify.MatchResult{
		Outcome: verify.MatchedConsistent,
		Product: &verify.ProductRef{
			SourceID: "8875", NRN: "04-0858", Name: "Example Tablets",
			Manufacturer: "Example Pharma", Status: "Active",
			Ingredients: []verify.IngredientRef{{Name: "Paracetamol", Strength: "500 mg"}},
		},
	})
	return scan.Scan{
		ID: uuid.New(), ClientKey: "client", Status: scan.StatusCompleted, Language: "en",
		Result: &scan.Result{
			Features: features,
			Verdict:  &verdict.Result{Verdict: verdict.ConsistentWithRecords, RulesVersion: "verdict-v1"},
		},
	}
}

type fakeResolver struct {
	id    int64
	found bool
	err   error
}

func (f *fakeResolver) FindProductID(context.Context, string, string) (int64, bool, error) {
	return f.id, f.found, f.err
}

func routeResponse(intent string) llm.FakeResponse {
	return llm.FakeResponse{Response: llm.Response{ToolCalls: []llm.ToolCall{{
		ID: "route-1", Name: routeToolName,
		Input: json.RawMessage(`{"intent":"` + intent + `","language":"en"}`),
	}}}}
}

func newTestService(t *testing.T, client llm.Client, store *fakeTranscriptStore, scans *fakeScans, searcher LeafletSearcher) *Service {
	t.Helper()
	catalog, err := LoadTemplates(DefaultLanguage)
	if err != nil {
		t.Fatal(err)
	}
	prompts, err := LoadPrompts(PromptVersion)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceOptions{
		Store: store, Scans: scans, Searcher: searcher, Client: client,
		Prompts: prompts, Catalogs: map[string]Catalog{"en": catalog},
		Resolver: &fakeResolver{id: 42, found: true}, MaxTurns: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestAskRefusesPersonalDosingWithFixedTextAndNoModelAnswer(t *testing.T) {
	client := llm.NewFake(routeResponse("dosing"))
	store := &fakeTranscriptStore{}
	scans := &fakeScans{scanned: scannedFixture()}
	service := newTestService(t, client, store, scans, &fakeSearcher{})
	catalog, _ := LoadTemplates(DefaultLanguage)
	expected, _ := catalog.Text(TemplateRefuseDosing)

	answer, err := service.Ask(context.Background(), AskRequest{
		ScanID: scans.scanned.ID, ClientKey: "client", Question: "How much should I give my baby?",
	})
	if err != nil {
		t.Fatal(err)
	}
	if answer.Answer != expected {
		t.Fatalf("answer = %q", answer.Answer)
	}
	if answer.Verified || len(answer.Sources) != 0 || answer.Fallback {
		t.Fatalf("refusal view = %#v", answer)
	}
	if client.CallsCount() != 1 {
		t.Fatalf("model was called %d times; a refusal must not reach the orchestrator", client.CallsCount())
	}
	if len(store.recorded) != 1 || store.recorded[0].Intent != IntentDosing {
		t.Fatalf("recorded = %#v", store.recorded)
	}
}

func TestAskAnswersRegistrationWithACitationAndRecordsTheTurn(t *testing.T) {
	client := llm.NewFake(
		routeResponse("registration"),
		toolCall("reg-1", ToolRegistration, `{}`),
		submitAnswer(`{"not_found":false,"claims":[{"text":"The record lists NRN 04-0858.","source_ids":["registry:8875"]}]}`),
		supportAll(),
	)
	store := &fakeTranscriptStore{}
	scans := &fakeScans{scanned: scannedFixture()}
	service := newTestService(t, client, store, scans, &fakeSearcher{})

	answer, err := service.Ask(context.Background(), AskRequest{
		ScanID: scans.scanned.ID, ClientKey: "client", Question: "What is the registration number?",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !answer.Verified || !strings.Contains(answer.Answer, "[1]") || len(answer.Sources) != 1 {
		t.Fatalf("answer = %#v", answer)
	}
	if answer.Sources[0].ID != "registry:8875" {
		t.Fatalf("source = %#v", answer.Sources[0])
	}
	if len(store.recorded) != 1 {
		t.Fatalf("turn was not recorded: %#v", store.recorded)
	}
	record := store.recorded[0]
	if record.Intent != IntentRegistration || len(record.SourceIDs) != 1 || record.Verifier != "ok" {
		t.Fatalf("record = %#v", record)
	}
}

func TestAskReturnsNotFoundWhenIndicationsAreAbsent(t *testing.T) {
	client := llm.NewFake(
		routeResponse("indications"),
		toolCall("search-1", ToolSearchLeaflet, `{"query":"used for"}`),
		submitAnswer(`{"not_found":true,"claims":[]}`),
	)
	store := &fakeTranscriptStore{}
	scans := &fakeScans{scanned: scannedFixture()}
	service := newTestService(t, client, store, scans, &fakeSearcher{})

	answer, err := service.Ask(context.Background(), AskRequest{
		ScanID: scans.scanned.ID, ClientKey: "client", Question: "What is this used for?",
	})
	if err != nil {
		t.Fatal(err)
	}
	catalog, _ := LoadTemplates(DefaultLanguage)
	expected, _ := catalog.Text(TemplateFallbackNotFound)
	if answer.Answer != expected || answer.Verified {
		t.Fatalf("answer = %#v", answer)
	}
	if !answer.Fallback {
		t.Fatal("not-found must be marked as a fallback")
	}
}

func TestAskRefusesBeforeTheRouterAtTheTurnLimit(t *testing.T) {
	client := llm.NewFake(routeResponse("registration"))
	store := &fakeTranscriptStore{messages: []Message{
		{Turn: 1, Role: "user"}, {Turn: 1, Role: "assistant"},
		{Turn: 2, Role: "user"}, {Turn: 2, Role: "assistant"},
		{Turn: 3, Role: "user"}, {Turn: 3, Role: "assistant"},
		{Turn: 4, Role: "user"}, {Turn: 4, Role: "assistant"},
	}}
	scans := &fakeScans{scanned: scannedFixture()}
	service := newTestService(t, client, store, scans, &fakeSearcher{})
	_, err := service.Ask(context.Background(), AskRequest{
		ScanID: scans.scanned.ID, ClientKey: "client", Question: "anything",
	})
	if !errors.Is(err, ErrTurnLimit) {
		t.Fatalf("err = %v", err)
	}
	if client.CallsCount() != 0 {
		t.Fatal("the router must not run after the turn limit")
	}
}

func TestAskRejectsAScanThatIsNotReady(t *testing.T) {
	client := llm.NewFake()
	scans := &fakeScans{scanned: scannedFixture()}
	scans.scanned.Status = scan.StatusProcessing
	service := newTestService(t, client, &fakeTranscriptStore{}, scans, &fakeSearcher{})
	_, err := service.Ask(context.Background(), AskRequest{
		ScanID: scans.scanned.ID, ClientKey: "client", Question: "anything",
	})
	if !errors.Is(err, ErrScanNotReady) {
		t.Fatalf("err = %v", err)
	}
}

func TestAskRejectsAnotherClientsScan(t *testing.T) {
	client := llm.NewFake()
	scans := &fakeScans{scanned: scannedFixture()}
	service := newTestService(t, client, &fakeTranscriptStore{}, scans, &fakeSearcher{})
	_, err := service.Ask(context.Background(), AskRequest{
		ScanID: scans.scanned.ID, ClientKey: "someone-else", Question: "anything",
	})
	if !errors.Is(err, scan.ErrScanNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestAskReturnsRetryableRouterErrors(t *testing.T) {
	client := llm.NewFake(llm.FakeResponse{Err: context.DeadlineExceeded})
	scans := &fakeScans{scanned: scannedFixture()}
	service := newTestService(t, client, &fakeTranscriptStore{}, scans, &fakeSearcher{})
	_, err := service.Ask(context.Background(), AskRequest{
		ScanID: scans.scanned.ID, ClientKey: "client", Question: "anything",
	})
	if !llm.IsTransient(err) {
		t.Fatalf("err = %v", err)
	}
}

func TestConversationReturnsTheStoredTranscript(t *testing.T) {
	scans := &fakeScans{scanned: scannedFixture()}
	store := &fakeTranscriptStore{exists: true, conversation: Conversation{
		ID: uuid.New(), ScanID: scans.scanned.ID, Language: "en",
		ExpiresAt: time.Now().Add(time.Hour),
	}, messages: []Message{{Turn: 1, Role: "user", Content: "q"}, {Turn: 1, Role: "assistant", Content: "a"}}}
	service := newTestService(t, llm.NewFake(), store, scans, &fakeSearcher{})
	view, err := service.Conversation(context.Background(), scans.scanned.ID, "client")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Messages) != 2 || view.Messages[1].Content != "a" {
		t.Fatalf("view = %#v", view)
	}
}
