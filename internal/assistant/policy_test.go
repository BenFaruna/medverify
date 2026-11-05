package assistant

import (
	"strings"
	"testing"
)

func TestPolicyCoversEveryTableRow(t *testing.T) {
	policy := DefaultPolicy()
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		intent   Intent
		action   Action
		template string
		tools    []string
	}{
		{IntentRegistration, ActionAnswer, "", []string{ToolRegistration}},
		{IntentIngredients, ActionAnswer, "", []string{ToolIngredients}},
		{IntentSafetyFlags, ActionAnswer, "", []string{ToolSafetyFlags}},
		{IntentLeaflet, ActionAnswer, "", []string{ToolSearchLeaflet}},
		{IntentIndications, ActionAnswer, "", []string{ToolSearchLeaflet}},
		{IntentPersonalSafety, ActionRefuse, TemplateRefusePersonalSafety, nil},
		{IntentDosing, ActionRefuse, TemplateRefuseDosing, nil},
		{IntentDiagnosis, ActionRefuse, TemplateRefuseDiagnosis, nil},
		{IntentComparison, ActionRefuse, TemplateRefuseComparison, nil},
		{IntentOffTopic, ActionRedirect, TemplateRedirectOffTopic, nil},
		{IntentPromptInjection, ActionRefuse, TemplateRefuseInjection, nil},
		{IntentUnknown, ActionFallback, TemplateFallbackUnknown, nil},
	}
	if len(tests) != len(Intents) {
		t.Fatalf("policy test covers %d intents, table has %d", len(tests), len(Intents))
	}
	for _, test := range tests {
		row := policy.Decide(test.intent)
		if row.Action != test.action {
			t.Errorf("%s action = %s, want %s", test.intent, row.Action, test.action)
		}
		if row.Template != test.template {
			t.Errorf("%s template = %s, want %s", test.intent, row.Template, test.template)
		}
		if len(row.Tools) != len(test.tools) {
			t.Errorf("%s tools = %v, want %v", test.intent, row.Tools, test.tools)
		}
		for i := range test.tools {
			if i >= len(row.Tools) || row.Tools[i] != test.tools[i] {
				t.Errorf("%s tools = %v, want %v", test.intent, row.Tools, test.tools)
				break
			}
		}
	}
}

func TestUnknownIntentCanOnlyFallBack(t *testing.T) {
	row := DefaultPolicy().Decide(Intent("made-up"))
	if row.Action != ActionFallback {
		t.Fatalf("unknown intent action = %s", row.Action)
	}
	if row.Answerable() {
		t.Fatal("unknown intent must not reach the orchestrator")
	}
}

func TestAnswerRowsOnlyAllowTheirOwnTool(t *testing.T) {
	policy := DefaultPolicy()
	row := policy.Decide(IntentRegistration)
	if !row.AllowsTool(ToolRegistration) {
		t.Fatal("registration row should allow get_registration")
	}
	if row.AllowsTool(ToolSearchLeaflet) {
		t.Fatal("registration row must not allow leaflet search")
	}
}

func TestTemplatesAreFixedAndComplete(t *testing.T) {
	catalog, err := LoadTemplates(DefaultLanguage)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range TemplateKeys {
		text, err := catalog.Text(key)
		if err != nil {
			t.Fatal(err)
		}
		if strings.ContainsAny(text, "{}") {
			t.Errorf("template %s contains a placeholder", key)
		}
	}
}

func TestTemplatesAvoidForbiddenWording(t *testing.T) {
	catalog, err := LoadTemplates(DefaultLanguage)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range TemplateKeys {
		text, _ := catalog.Text(key)
		lower := strings.ToLower(text)
		for _, forbidden := range []string{"cure", "treat your", "genuine", "authentic"} {
			if strings.Contains(lower, forbidden) {
				t.Errorf("template %s contains forbidden wording %q: %s", key, forbidden, text)
			}
		}
		// "safe" as a reassurance is forbidden. The fixed text deliberately
		// avoids the word entirely.
		if strings.Contains(lower, "safe") {
			t.Errorf("template %s contains forbidden wording %q: %s", key, "safe", text)
		}
	}
}

func TestDefaultTemplatesAreReviewedAndProductionReady(t *testing.T) {
	catalog, err := LoadTemplates(DefaultLanguage)
	if err != nil {
		t.Fatal(err)
	}
	if !catalog.Reviewed {
		t.Fatal("the approved English templates must be marked reviewed")
	}
	if err := catalog.ProductionReady(); err != nil {
		t.Fatalf("reviewed templates must pass the production gate: %v", err)
	}
}
