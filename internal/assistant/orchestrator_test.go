package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/benfaruna/medverify/internal/llm"
	"github.com/benfaruna/medverify/internal/rag"
)

func toolCall(id, name, input string) llm.FakeResponse {
	return llm.FakeResponse{Response: llm.Response{ToolCalls: []llm.ToolCall{{
		ID: id, Name: name, Input: json.RawMessage(input),
	}}}}
}

func submitAnswer(claims string) llm.FakeResponse {
	return toolCall("submit-1", submitAnswerTool, claims)
}

func supportAll() llm.FakeResponse {
	return toolCall("support-1", supportToolName, `{"results":[{"index":0,"supported":true}]}`)
}

func orchestratorFor(client llm.Client, searcher LeafletSearcher) *Orchestrator {
	prompts, _ := LoadPrompts(PromptVersion)
	return &Orchestrator{
		Client:   client,
		Tools:    NewToolbox(searcher),
		Verifier: NewVerifier(client, prompts.Verifier),
		Prompts:  prompts,
		Limits:   DefaultLimits(),
	}
}

func successSearcher() *fakeSearcher {
	return &fakeSearcher{hits: []rag.Hit{{
		ChunkID: 9, SectionCode: "warnings", Heading: "Warnings",
		Text: "Keep out of reach of children.", SourceURL: "https://example.test/leaflet.pdf",
	}}}
}

func TestOrchestratorRunsToolsAndSubmitsACitedAnswer(t *testing.T) {
	client := llm.NewFake(
		toolCall("search-1", ToolSearchLeaflet, `{"query":"children"}`),
		submitAnswer(`{"not_found":false,"claims":[{"text":"Keep out of reach of children.","source_ids":["leaflet:9"]}]}`),
		supportAll(),
	)
	catalog, _ := LoadTemplates(DefaultLanguage)
	turn, err := orchestratorFor(client, successSearcher()).Answer(context.Background(), AnswerRequest{
		Session: testSession(), Row: DefaultPolicy().Decide(IntentLeaflet),
		Question: "What does the leaflet say about children?", Catalog: catalog,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !turn.Verification.Allowed {
		t.Fatalf("turn = %#v", turn)
	}
	if !strings.Contains(turn.Text, "Keep out of reach of children.") || !strings.Contains(turn.Text, "[1]") {
		t.Fatalf("answer is not cited: %q", turn.Text)
	}
	if len(turn.Sources) != 1 || turn.Sources[0].ID != "leaflet:9" || turn.Sources[0].URL == "" {
		t.Fatalf("sources = %#v", turn.Sources)
	}
	if len(turn.ToolLogs) != 1 || turn.ToolLogs[0].Name != ToolSearchLeaflet {
		t.Fatalf("tool logs = %#v", turn.ToolLogs)
	}
}

func TestOrchestratorStopsAtTheStepLimit(t *testing.T) {
	client := llm.NewFake(toolCall("search-1", ToolSearchLeaflet, `{"query":"children"}`))
	orchestrator := orchestratorFor(client, successSearcher())
	orchestrator.Limits.MaxSteps = 2
	catalog, _ := LoadTemplates(DefaultLanguage)
	turn, err := orchestrator.Answer(context.Background(), AnswerRequest{
		Session: testSession(), Row: DefaultPolicy().Decide(IntentLeaflet),
		Question: "children?", Catalog: catalog,
	})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Verification.Allowed || turn.Verification.Reason != "step_limit" {
		t.Fatalf("turn = %#v", turn)
	}
	if turn.FallbackKey != TemplateFallbackUnsupported {
		t.Fatalf("fallback = %q", turn.FallbackKey)
	}
}

func TestOrchestratorFallsBackWhenSupportCheckFails(t *testing.T) {
	client := llm.NewFake(
		toolCall("search-1", ToolSearchLeaflet, `{"query":"children"}`),
		submitAnswer(`{"not_found":false,"claims":[{"text":"Store it in a cool place.","source_ids":["leaflet:9"]}]}`),
		toolCall("support-1", supportToolName, `{"results":[{"index":0,"supported":false}]}`),
	)
	catalog, _ := LoadTemplates(DefaultLanguage)
	turn, err := orchestratorFor(client, successSearcher()).Answer(context.Background(), AnswerRequest{
		Session: testSession(), Row: DefaultPolicy().Decide(IntentLeaflet),
		Question: "How should I store it?", Catalog: catalog,
	})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Verification.Allowed || turn.FallbackKey != TemplateFallbackUnsupported {
		t.Fatalf("turn = %#v", turn)
	}
}

func TestOrchestratorReturnsTheNotFoundTemplateWhenSourcesDoNotAnswer(t *testing.T) {
	client := llm.NewFake(
		toolCall("search-1", ToolSearchLeaflet, `{"query":"pregnancy"}`),
		submitAnswer(`{"not_found":true,"claims":[]}`),
	)
	catalog, _ := LoadTemplates(DefaultLanguage)
	turn, err := orchestratorFor(client, &fakeSearcher{}).Answer(context.Background(), AnswerRequest{
		Session: testSession(), Row: DefaultPolicy().Decide(IntentLeaflet),
		Question: "Is it for pregnancy?", Catalog: catalog,
	})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Verification.Reason != "not_found" || turn.FallbackKey != TemplateFallbackNotFound {
		t.Fatalf("turn = %#v", turn)
	}
	expected, _ := catalog.Text(TemplateFallbackNotFound)
	if turn.Text != expected {
		t.Fatalf("text = %q, want fixed fallback", turn.Text)
	}
}

func TestOrchestratorFailsClosedOnPlainText(t *testing.T) {
	client := llm.NewFake(llm.FakeResponse{Response: llm.Response{Text: "The medicine is safe."}})
	catalog, _ := LoadTemplates(DefaultLanguage)
	turn, err := orchestratorFor(client, successSearcher()).Answer(context.Background(), AnswerRequest{
		Session: testSession(), Row: DefaultPolicy().Decide(IntentLeaflet),
		Question: "anything", Catalog: catalog,
	})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Verification.Allowed || turn.Verification.Reason != "no_submission" {
		t.Fatalf("turn = %#v", turn)
	}
}

func TestOrchestratorReturnsProviderErrorsInsteadOfPartialAnswers(t *testing.T) {
	client := llm.NewFake(llm.FakeResponse{Err: context.DeadlineExceeded})
	catalog, _ := LoadTemplates(DefaultLanguage)
	_, err := orchestratorFor(client, successSearcher()).Answer(context.Background(), AnswerRequest{
		Session: testSession(), Row: DefaultPolicy().Decide(IntentLeaflet),
		Question: "anything", Catalog: catalog,
	})
	if err == nil || !llm.IsTransient(err) {
		t.Fatalf("expected a retryable error, got %v", err)
	}
}

func TestOrchestratorRejectsAToolThePolicyDoesNotAllow(t *testing.T) {
	client := llm.NewFake(toolCall("bad-1", ToolRegistration, `{}`))
	orchestrator := orchestratorFor(client, successSearcher())
	orchestrator.Limits.MaxSteps = 1
	catalog, _ := LoadTemplates(DefaultLanguage)
	turn, err := orchestrator.Answer(context.Background(), AnswerRequest{
		Session: testSession(), Row: DefaultPolicy().Decide(IntentLeaflet),
		Question: "registration?", Catalog: catalog,
	})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Verification.Allowed {
		t.Fatal("the model must not be able to call a tool outside its policy row")
	}
	if len(turn.ToolLogs) != 1 || turn.ToolLogs[0].Outcome != "error" {
		t.Fatalf("tool logs = %#v", turn.ToolLogs)
	}
}
