package assistant

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/benfaruna/medverify/internal/llm"
)

func TestRouterUnknownIntentFallsBack(t *testing.T) {
	fake := llm.NewFakeTools(llm.ToolCall{
		ID: "route-1", Name: routeToolName,
		Input: json.RawMessage(`{"intent":"tell_me_everything","language":"en"}`),
	})
	router := NewRouter(fake, "router prompt", []string{"en"})
	route, err := router.Route(context.Background(), "question", "en")
	if err != nil {
		t.Fatal(err)
	}
	if route.Intent != IntentUnknown || !route.Malformed {
		t.Fatalf("route = %#v", route)
	}
}

func TestRouterMalformedToolInputFallsBack(t *testing.T) {
	fake := llm.NewFakeTools(llm.ToolCall{
		ID: "route-1", Name: routeToolName, Input: json.RawMessage(`not json`),
	})
	route, err := NewRouter(fake, "prompt", []string{"en"}).Route(context.Background(), "q", "en")
	if err != nil {
		t.Fatal(err)
	}
	if route.Intent != IntentUnknown || !route.Malformed {
		t.Fatalf("route = %#v", route)
	}
}

func TestRouterUnsupportedLanguageUsesDefault(t *testing.T) {
	fake := llm.NewFakeTools(llm.ToolCall{
		ID: "route-1", Name: routeToolName,
		Input: json.RawMessage(`{"intent":"dosing","language":"xx"}`),
	})
	route, err := NewRouter(fake, "prompt", []string{"en"}).Route(context.Background(), "q", "")
	if err != nil {
		t.Fatal(err)
	}
	if route.Intent != IntentDosing || route.Language != "en" {
		t.Fatalf("route = %#v", route)
	}
}

func TestRouterAcceptsKnownIntent(t *testing.T) {
	fake := llm.NewFakeTools(llm.ToolCall{
		ID: "route-1", Name: routeToolName,
		Input: json.RawMessage(`{"intent":"registration","language":"en"}`),
	})
	route, err := NewRouter(fake, "prompt", []string{"en"}).Route(context.Background(), "is it registered?", "en")
	if err != nil {
		t.Fatal(err)
	}
	if route.Intent != IntentRegistration || route.Malformed {
		t.Fatalf("route = %#v", route)
	}
}

func TestRouterNeverPutsQuestionInToolCall(t *testing.T) {
	fake := llm.NewFakeTools(llm.ToolCall{
		ID: "route-1", Name: routeToolName,
		Input: json.RawMessage(`{"intent":"off_topic","language":"en"}`),
	})
	_, _ = NewRouter(fake, "prompt", []string{"en"}).Route(context.Background(), "my private question", "en")
	requests := fake.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %d", len(requests))
	}
	if len(requests[0].Messages) != 1 || requests[0].Messages[0].Text == "" {
		t.Fatal("question was not sent to the router")
	}
}

// A leaflet question can never come back as "I did not understand": whatever the
// small model returns, a question that names the leaflet is routed to the
// leaflet row, which can only quote the leaflet on file.
func TestRouterPromotesAnUnclassifiedLeafletQuestion(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		intent string
	}{
		{name: "unknown intent", intent: "unknown"},
		{name: "intent outside the enum", intent: "tell_me_everything"},
		{name: "empty intent", intent: ""},
	}
	questions := []string{
		"Where can I read the leaflet?",
		"What does the leaflet say about warnings?",
		"Is there a patient information leaflet for this pack?",
		"where is the PIL",
	}
	for _, tc := range cases {
		for _, question := range questions {
			fake := llm.NewFakeTools(llm.ToolCall{
				ID: "route-1", Name: routeToolName,
				Input: json.RawMessage(`{"intent":"` + tc.intent + `","language":"en"}`),
			})
			route, err := NewRouter(fake, "prompt", []string{"en"}).Route(context.Background(), question, "en")
			if err != nil {
				t.Fatalf("%s / %q: %v", tc.name, question, err)
			}
			if route.Intent != IntentLeaflet || route.Malformed {
				t.Fatalf("%s / %q: route = %#v", tc.name, question, route)
			}
		}
	}
}

// The promotion is a fallback, not an override: a question the model classified
// as a refusal stays a refusal even when it mentions the leaflet. A person
// asking for personal advice behind the word "leaflet" must still be refused.
func TestRouterDoesNotOverrideAClassifiedIntent(t *testing.T) {
	t.Parallel()

	fake := llm.NewFakeTools(llm.ToolCall{
		ID: "route-1", Name: routeToolName,
		Input: json.RawMessage(`{"intent":"personal_safety","language":"en"}`),
	})
	route, err := NewRouter(fake, "prompt", []string{"en"}).
		Route(context.Background(), "Does the leaflet say this is safe for my baby?", "en")
	if err != nil {
		t.Fatal(err)
	}
	if route.Intent != IntentPersonalSafety {
		t.Fatalf("route = %#v", route)
	}
}

// A question that does not name the leaflet keeps the safer unknown fallback.
func TestRouterLeavesAnUnrelatedQuestionUnknown(t *testing.T) {
	t.Parallel()

	fake := llm.NewFakeTools(llm.ToolCall{
		ID: "route-1", Name: routeToolName,
		Input: json.RawMessage(`{"intent":"unknown","language":"en"}`),
	})
	route, err := NewRouter(fake, "prompt", []string{"en"}).
		Route(context.Background(), "What is the weather in Lagos?", "en")
	if err != nil {
		t.Fatal(err)
	}
	if route.Intent != IntentUnknown {
		t.Fatalf("route = %#v", route)
	}
}

func TestNamesLeafletIgnoresWordsThatOnlyContainIt(t *testing.T) {
	t.Parallel()

	for _, question := range []string{"Can I take this pill with water?", "How do I pilot this?"} {
		if namesLeaflet(question) {
			t.Fatalf("%q was treated as a leaflet question", question)
		}
	}
}
