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
