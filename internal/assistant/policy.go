package assistant

import "fmt"

// Intent is the closed set of question categories the router may return. An
// unknown model value never becomes a new category: it maps to IntentUnknown
// and the safest policy row.
type Intent string

// Router intents.
const (
	IntentRegistration    Intent = "registration"
	IntentIngredients     Intent = "ingredients"
	IntentSafetyFlags     Intent = "safety_flags"
	IntentLeaflet         Intent = "leaflet"
	IntentIndications     Intent = "indications"
	IntentPersonalSafety  Intent = "personal_safety"
	IntentDosing          Intent = "dosing"
	IntentDiagnosis       Intent = "diagnosis"
	IntentComparison      Intent = "comparison"
	IntentOffTopic        Intent = "off_topic"
	IntentPromptInjection Intent = "prompt_injection"
	IntentUnknown         Intent = "unknown"
)

// Intents lists every intent in policy order.
var Intents = []Intent{
	IntentRegistration,
	IntentIngredients,
	IntentSafetyFlags,
	IntentLeaflet,
	IntentIndications,
	IntentPersonalSafety,
	IntentDosing,
	IntentDiagnosis,
	IntentComparison,
	IntentOffTopic,
	IntentPromptInjection,
	IntentUnknown,
}

// Valid reports whether the intent is one this build understands.
func (i Intent) Valid() bool {
	for _, known := range Intents {
		if i == known {
			return true
		}
	}
	return false
}

// Action is what the policy gate does with a question.
type Action string

// Policy actions.
const (
	// ActionAnswer allows the orchestrator to answer only from tool results.
	ActionAnswer Action = "answer"
	// ActionRefuse returns a fixed refusal and never calls the model.
	ActionRefuse Action = "refuse"
	// ActionRedirect returns the fixed off-topic redirect.
	ActionRedirect Action = "redirect"
	// ActionFallback returns the fixed safe fallback.
	ActionFallback Action = "fallback"
)

// Valid reports whether the action is known.
func (a Action) Valid() bool {
	switch a {
	case ActionAnswer, ActionRefuse, ActionRedirect, ActionFallback:
		return true
	default:
		return false
	}
}

// Template keys are stable names in the reviewed templates file.
const (
	TemplateRefusePersonalSafety = "refuse_personal_safety"
	TemplateRefuseDosing         = "refuse_dosing"
	TemplateRefuseDiagnosis      = "refuse_diagnosis"
	TemplateRefuseComparison     = "refuse_comparison"
	TemplateRefuseInjection      = "refuse_injection"
	TemplateRedirectOffTopic     = "redirect_off_topic"
	TemplateFallbackUnsupported  = "fallback_unsupported"
	TemplateFallbackNotFound     = "fallback_not_found"
	TemplateFallbackNoLeaflet    = "fallback_no_leaflet"
	TemplateFallbackUnknown      = "fallback_unknown"
	TemplateDisclaimer           = "disclaimer"
)

// TemplateKeys lists every template the policy can return.
var TemplateKeys = []string{
	TemplateRefusePersonalSafety,
	TemplateRefuseDosing,
	TemplateRefuseDiagnosis,
	TemplateRefuseComparison,
	TemplateRefuseInjection,
	TemplateRedirectOffTopic,
	TemplateFallbackUnsupported,
	TemplateFallbackNotFound,
	TemplateFallbackNoLeaflet,
	TemplateFallbackUnknown,
	TemplateDisclaimer,
}

// Tool names exposed to the orchestrator. Tools are read-only and take their
// scope from the server-side session.
const (
	ToolRegistration  = "get_registration"
	ToolIngredients   = "get_ingredients"
	ToolSafetyFlags   = "get_safety_flags"
	ToolSearchLeaflet = "search_leaflet"
)

// PolicyRow is one row of the reviewed policy table.
type PolicyRow struct {
	Intent   Intent
	Action   Action
	Template string
	// Tools is the exact tool allowlist for this row. An empty list means the
	// model is not called at all.
	Tools []string
	// SectionCode narrows a leaflet search to the section the row is about.
	// It is a topic hint, never a product scope.
	SectionCode string
}

// Policy is the ordered policy table. Unknown and injection rows are last so a
// malformed or surprising route can only ever fall back, never answer.
type Policy struct {
	rows map[Intent]PolicyRow
}

// DefaultPolicy returns the phase 09 policy table.
func DefaultPolicy() Policy {
	rows := []PolicyRow{
		{Intent: IntentRegistration, Action: ActionAnswer, Tools: []string{ToolRegistration}},
		{Intent: IntentIngredients, Action: ActionAnswer, Tools: []string{ToolIngredients}},
		{Intent: IntentSafetyFlags, Action: ActionAnswer, Tools: []string{ToolSafetyFlags}},
		{Intent: IntentLeaflet, Action: ActionAnswer, Tools: []string{ToolSearchLeaflet}},
		{Intent: IntentIndications, Action: ActionAnswer, Tools: []string{ToolSearchLeaflet}, SectionCode: "indications"},
		{Intent: IntentPersonalSafety, Action: ActionRefuse, Template: TemplateRefusePersonalSafety},
		{Intent: IntentDosing, Action: ActionRefuse, Template: TemplateRefuseDosing},
		{Intent: IntentDiagnosis, Action: ActionRefuse, Template: TemplateRefuseDiagnosis},
		{Intent: IntentComparison, Action: ActionRefuse, Template: TemplateRefuseComparison},
		{Intent: IntentOffTopic, Action: ActionRedirect, Template: TemplateRedirectOffTopic},
		{Intent: IntentPromptInjection, Action: ActionRefuse, Template: TemplateRefuseInjection},
		{Intent: IntentUnknown, Action: ActionFallback, Template: TemplateFallbackUnknown},
	}
	policy := Policy{rows: make(map[Intent]PolicyRow, len(rows))}
	for _, row := range rows {
		policy.rows[row.Intent] = row
	}
	return policy
}

// Decide returns the policy row for an intent. An invalid intent can only
// produce the unknown row, whose action is a fixed fallback.
func (p Policy) Decide(intent Intent) PolicyRow {
	if row, ok := p.rows[intent]; ok {
		return row
	}
	return p.rows[IntentUnknown]
}

// Validate checks the table is complete and internally consistent.
func (p Policy) Validate() error {
	for _, intent := range Intents {
		row, ok := p.rows[intent]
		if !ok {
			return fmt.Errorf("assistant: policy is missing intent %s", intent)
		}
		if !row.Action.Valid() {
			return fmt.Errorf("assistant: policy intent %s has invalid action %q", intent, row.Action)
		}
		switch row.Action {
		case ActionAnswer:
			if len(row.Tools) == 0 {
				return fmt.Errorf("assistant: policy intent %s answers without a tool", intent)
			}
			if row.Template != "" {
				return fmt.Errorf("assistant: policy intent %s answers but has a template", intent)
			}
		default:
			if !knownTemplate(row.Template) {
				return fmt.Errorf("assistant: policy intent %s needs a known template", intent)
			}
			if len(row.Tools) != 0 {
				return fmt.Errorf("assistant: policy intent %s must not call tools", intent)
			}
		}
		for _, tool := range row.Tools {
			if !knownTool(tool) {
				return fmt.Errorf("assistant: policy intent %s lists unknown tool %q", intent, tool)
			}
		}
	}
	return nil
}

// Answerable reports whether the row may reach the orchestrator.
func (r PolicyRow) Answerable() bool { return r.Action == ActionAnswer }

// AllowsTool reports whether a row permits a tool.
func (r PolicyRow) AllowsTool(name string) bool {
	for _, tool := range r.Tools {
		if tool == name {
			return true
		}
	}
	return false
}

func knownTemplate(key string) bool {
	for _, known := range TemplateKeys {
		if key == known {
			return true
		}
	}
	return false
}

func knownTool(name string) bool {
	switch name {
	case ToolRegistration, ToolIngredients, ToolSafetyFlags, ToolSearchLeaflet:
		return true
	default:
		return false
	}
}
