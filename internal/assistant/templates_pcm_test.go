package assistant

import (
	"strings"
	"testing"
)

// The Pidgin catalog was approved by the project owner on 2026-11-05. It must
// stay complete and keep passing the production review gate.
func TestPidginTemplatesAreCompleteAndApproved(t *testing.T) {
	t.Parallel()

	catalog, err := LoadTemplates("pcm")
	if err != nil {
		t.Fatalf("LoadTemplates(pcm): %v", err)
	}
	if catalog.Language != "pcm" {
		t.Fatalf("language = %q, want pcm", catalog.Language)
	}
	if !catalog.Reviewed {
		t.Fatal("the approved Pidgin catalog must be marked reviewed")
	}
	if err := catalog.ProductionReady(); err != nil {
		t.Fatalf("approved Pidgin templates must pass the production gate: %v", err)
	}

	for _, key := range TemplateKeys {
		text, err := catalog.Text(key)
		if err != nil {
			t.Fatalf("template %s: %v", key, err)
		}
		if strings.ContainsAny(text, "{}") {
			t.Errorf("template %s contains a placeholder", key)
		}
	}
}

func TestPidginTemplatesAvoidForbiddenWording(t *testing.T) {
	t.Parallel()

	catalog, err := LoadTemplates("pcm")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range TemplateKeys {
		text, _ := catalog.Text(key)
		lower := strings.ToLower(text)
		for _, forbidden := range []string{"cure", "genuine", "authentic", "safe"} {
			if strings.Contains(lower, forbidden) {
				t.Errorf("template %s contains forbidden wording %q: %s", key, forbidden, text)
			}
		}
	}
}
