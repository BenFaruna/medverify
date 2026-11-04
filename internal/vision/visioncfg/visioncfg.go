// Package visioncfg builds a vision provider from configuration. It is the
// only place that maps settings to a concrete provider, so worker and eval
// select the same implementation.
package visioncfg

import (
	"fmt"

	"github.com/benfaruna/medverify/internal/platform/config"
	"github.com/benfaruna/medverify/internal/vision"
)

// New returns the configured provider. The fake is for development and tests;
// production configuration rejects it. A fallback model is tried only when the
// primary provider is unavailable, never when an image is unreadable.
//
// "claude" and "openai" are wire formats, not vendor commitments: "openai"
// accepts any OpenAI-compatible chat completions endpoint, including Gemini's
// compatibility API, Azure OpenAI, OpenRouter, Ollama, and vLLM.
func New(cfg config.Vision) (vision.Provider, error) {
	if cfg.Provider == "fake" {
		return vision.NewFake(), nil
	}
	if cfg.Provider != "claude" && cfg.Provider != "openai" {
		return nil, fmt.Errorf("visioncfg: unsupported provider %q", cfg.Provider)
	}

	prompt, err := vision.LoadPrompt(vision.PromptVersion)
	if err != nil {
		return nil, err
	}
	resilientOptions := vision.ResilientOptions{
		Timeout:          cfg.Timeout,
		Retries:          cfg.Retries,
		Backoff:          cfg.RetryBackoff,
		FailureThreshold: cfg.CircuitFailureThreshold,
		Cooldown:         cfg.CircuitCooldown,
	}

	build := func(model string) (vision.Provider, error) {
		return newProvider(cfg, model, prompt)
	}

	primary, err := build(cfg.Model)
	if err != nil {
		return nil, err
	}
	provider := vision.NewResilient(primary, resilientOptions)

	if cfg.FallbackModel == "" || cfg.FallbackModel == cfg.Model {
		return provider, nil
	}
	fallback, err := build(cfg.FallbackModel)
	if err != nil {
		return nil, err
	}
	return vision.Fallback{
		Primary:   provider,
		Secondary: vision.NewResilient(fallback, resilientOptions),
	}, nil
}

func newProvider(cfg config.Vision, model string, prompt vision.Prompt) (vision.Provider, error) {
	switch cfg.Provider {
	case "claude":
		return vision.NewClaude(vision.ClaudeOptions{
			APIKey:      cfg.APIKey,
			BaseURL:     cfg.BaseURL,
			Model:       model,
			MaxTokens:   cfg.MaxTokens,
			Prompt:      prompt,
			InputPrice:  cfg.InputPricePerMToken,
			OutputPrice: cfg.OutputPricePerMToken,
		})
	case "openai":
		return vision.NewOpenAI(vision.OpenAIOptions{
			APIKey:      cfg.APIKey,
			BaseURL:     cfg.BaseURL,
			Model:       model,
			MaxTokens:   cfg.MaxTokens,
			Prompt:      prompt,
			InputPrice:  cfg.InputPricePerMToken,
			OutputPrice: cfg.OutputPricePerMToken,
		})
	default:
		return nil, fmt.Errorf("visioncfg: unsupported provider %q", cfg.Provider)
	}
}
