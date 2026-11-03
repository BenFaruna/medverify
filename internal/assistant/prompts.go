package assistant

import (
	"embed"
	"fmt"
	"strings"
)

// PromptVersion is stored on every conversation and call so an answer can be
// traced to the exact instructions that produced it.
const PromptVersion = "v1"

//go:embed prompts/v1/system.md prompts/v1/router.md prompts/v1/verifier.md
var promptFiles embed.FS

// Prompts is one versioned instruction set.
type Prompts struct {
	Version  string
	System   string
	Router   string
	Verifier string
}

// LoadPrompts reads a versioned prompt set. An unknown version is an error
// rather than a silent fallback.
func LoadPrompts(version string) (Prompts, error) {
	if strings.TrimSpace(version) == "" {
		version = PromptVersion
	}
	if version != PromptVersion {
		return Prompts{}, fmt.Errorf("assistant: unknown prompt version %q", version)
	}
	read := func(name string) (string, error) {
		data, err := promptFiles.ReadFile("prompts/" + version + "/" + name)
		if err != nil {
			return "", fmt.Errorf("assistant: read %s prompt: %w", name, err)
		}
		text := strings.TrimSpace(string(data))
		if text == "" {
			return "", fmt.Errorf("assistant: %s prompt is empty", name)
		}
		return text, nil
	}
	system, err := read("system.md")
	if err != nil {
		return Prompts{}, err
	}
	router, err := read("router.md")
	if err != nil {
		return Prompts{}, err
	}
	verifier, err := read("verifier.md")
	if err != nil {
		return Prompts{}, err
	}
	return Prompts{Version: version, System: system, Router: router, Verifier: verifier}, nil
}
