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
func New(cfg config.Vision) (vision.Provider, error) {
	if cfg.Provider == "fake" {
		return vision.NewFake(), nil
	}
	if cfg.Provider != "claude" {
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

	primary, err := newClaude(cfg, prompt)
	if err != nil {
		return nil, err
	}
	provider := vision.NewResilient(primary, resilientOptions)

	if cfg.FallbackModel == "" || cfg.FallbackModel == cfg.Model {
		return provider, nil
	}
	fallback, err := newClaude(config.Vision{
		Provider:             cfg.Provider,
		Model:                cfg.FallbackModel,
		APIKey:               cfg.APIKey,
		BaseURL:              cfg.BaseURL,
		MaxTokens:            cfg.MaxTokens,
		InputPricePerMToken:  cfg.InputPricePerMToken,
		OutputPricePerMToken: cfg.OutputPricePerMToken,
	}, prompt)
	if err != nil {
		return nil, err
	}
	return vision.Fallback{
		Primary:   provider,
		Secondary: vision.NewResilient(fallback, resilientOptions),
	}, nil
}

func newClaude(cfg config.Vision, prompt vision.Prompt) (vision.Provider, error) {
	return vision.NewClaude(vision.ClaudeOptions{
		APIKey:      cfg.APIKey,
		BaseURL:     cfg.BaseURL,
		Model:       cfg.Model,
		MaxTokens:   cfg.MaxTokens,
		Prompt:      prompt,
		InputPrice:  cfg.InputPricePerMToken,
		OutputPrice: cfg.OutputPricePerMToken,
	})
}
