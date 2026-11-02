package safety

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// WordingVersion identifies the reviewed wording file. The language and version
// belong in the result so a client can pick a translation deliberately.
const WordingVersion = "wording-v1"

//go:embed wording/*.yaml
var wordingFiles embed.FS

// Wording is a reviewed catalog of flag templates for one language.
type Wording struct {
	Version   string            `yaml:"version"`
	Language  string            `yaml:"language"`
	Templates map[string]string `yaml:"templates"`
}

var placeholderPattern = regexp.MustCompile(`\{([a-z_]+)\}`)

// LoadWording reads a catalog. An empty path loads the embedded English file.
// Every reason code the checker can emit must have a template, so a new flag
// cannot ship without wording.
func LoadWording(path string) (Wording, error) {
	var data []byte
	var err error
	if path == "" {
		data, err = wordingFiles.ReadFile("wording/en.yaml")
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return Wording{}, fmt.Errorf("safety: read wording: %w", err)
	}

	var wording Wording
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&wording); err != nil {
		return Wording{}, fmt.Errorf("safety: decode wording: %w", err)
	}
	if wording.Version == "" || wording.Language == "" {
		return Wording{}, fmt.Errorf("safety: wording version and language are required")
	}
	for _, code := range ReasonCodes {
		template := strings.TrimSpace(wording.Templates[code])
		if template == "" {
			return Wording{}, fmt.Errorf("safety: wording is missing a template for %s", code)
		}
		if !placeholderPattern.MatchString(template) && strings.Contains(template, "{") {
			return Wording{}, fmt.Errorf("safety: wording template %s has a malformed placeholder", code)
		}
	}
	for code, template := range wording.Templates {
		if !knownReasonCode(code) {
			return Wording{}, fmt.Errorf("safety: wording template %q has no reason code", code)
		}
		if strings.TrimSpace(template) == "" {
			return Wording{}, fmt.Errorf("safety: wording template %q is empty", code)
		}
	}
	return wording, nil
}

// Render fills a template from flag evidence. A missing value is an error: it
// is better to show no sentence than a sentence with a hole in it.
func (w Wording) Render(reasonCode string, evidence map[string]string) (string, error) {
	template, ok := w.Templates[reasonCode]
	if !ok {
		return "", fmt.Errorf("safety: no wording for reason %q", reasonCode)
	}

	var missing []string
	rendered := placeholderPattern.ReplaceAllStringFunc(template, func(token string) string {
		key := strings.TrimSuffix(strings.TrimPrefix(token, "{"), "}")
		value, ok := evidence[key]
		if !ok || value == "" {
			missing = append(missing, key)
			return token
		}
		return value
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("safety: wording for %s is missing evidence %s", reasonCode, strings.Join(missing, ", "))
	}
	return rendered, nil
}

func knownReasonCode(code string) bool {
	for _, known := range ReasonCodes {
		if code == known {
			return true
		}
	}
	return false
}
