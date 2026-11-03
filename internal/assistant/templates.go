package assistant

import (
	"bytes"
	"embed"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// TemplateVersion identifies the reviewed fixed-text file.
const TemplateVersion = "templates-v1"

// DefaultLanguage is the language whose templates are always present. Other
// languages are added only with native-speaker, pharmacist, and legal review.
const DefaultLanguage = "en"

//go:embed templates/*.yaml
var templateFiles embed.FS

// Catalog is the fixed refusal, fallback, and disclaimer text for one
// language. Reviewed records whether the required human review is recorded.
type Catalog struct {
	Version   string            `yaml:"version"`
	Language  string            `yaml:"language"`
	Reviewed  bool              `yaml:"reviewed"`
	Templates map[string]string `yaml:"templates"`
}

// LoadTemplates reads the embedded catalog for a language. An unknown language
// is an error: silently borrowing English would break the language-switch
// policy tests.
func LoadTemplates(language string) (Catalog, error) {
	language = strings.TrimSpace(strings.ToLower(language))
	if language == "" {
		language = DefaultLanguage
	}
	data, err := templateFiles.ReadFile("templates/" + language + ".yaml")
	if err != nil {
		return Catalog{}, fmt.Errorf("assistant: no fixed-text catalog for language %q", language)
	}
	var catalog Catalog
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&catalog); err != nil {
		return Catalog{}, fmt.Errorf("assistant: decode templates for %q: %w", language, err)
	}
	if err := catalog.Validate(); err != nil {
		return Catalog{}, err
	}
	return catalog, nil
}

// Validate checks that every required template exists and is fixed text.
func (c Catalog) Validate() error {
	if c.Version == "" || c.Language == "" {
		return fmt.Errorf("assistant: template catalog version and language are required")
	}
	if c.Version != TemplateVersion {
		return fmt.Errorf("assistant: unknown template version %q", c.Version)
	}
	for _, key := range TemplateKeys {
		text := strings.TrimSpace(c.Templates[key])
		if text == "" {
			return fmt.Errorf("assistant: template catalog is missing %s", key)
		}
		if strings.ContainsAny(text, "{}") {
			return fmt.Errorf("assistant: template %s must be fixed text, not a placeholder template", key)
		}
	}
	for key := range c.Templates {
		if !knownTemplate(key) {
			return fmt.Errorf("assistant: template %q has no policy key", key)
		}
	}
	return nil
}

// Text returns a fixed template. It never renders model output.
func (c Catalog) Text(key string) (string, error) {
	text, ok := c.Templates[key]
	if !ok || strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("assistant: no template for %q", key)
	}
	return strings.TrimSpace(text), nil
}

// ProductionReady reports whether the required human review is recorded.
func (c Catalog) ProductionReady() error {
	if !c.Reviewed {
		return fmt.Errorf("assistant: templates for %q are not marked reviewed; pharmacist and legal review is required", c.Language)
	}
	return nil
}
