package visioncfg

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/config"
	"github.com/benfaruna/medverify/internal/vision"
)

func TestNewSelectsEachProvider(t *testing.T) {
	t.Parallel()

	fake, err := New(config.Vision{Provider: "fake"})
	require.NoError(t, err)
	assert.IsType(t, &vision.Fake{}, fake)

	for _, provider := range []string{"claude", "openai"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			built, err := New(config.Vision{
				Provider: provider,
				Model:    "pinned-model",
				APIKey:   "test-key",
			})
			require.NoError(t, err)
			assert.NotNil(t, built)
		})
	}
}

func TestNewWrapsTheFallbackModel(t *testing.T) {
	t.Parallel()

	built, err := New(config.Vision{
		Provider:      "openai",
		Model:         "primary-model",
		FallbackModel: "fallback-model",
		APIKey:        "test-key",
	})
	require.NoError(t, err)
	assert.IsType(t, vision.Fallback{}, built,
		"a second model must be used only when the primary is unavailable")
}

func TestNewRejectsUnknownAndIncompleteProviders(t *testing.T) {
	t.Parallel()

	_, err := New(config.Vision{Provider: "gemini-native"})
	assert.Error(t, err)

	_, err = New(config.Vision{Provider: "openai", Model: "m"})
	assert.Error(t, err, "a real provider needs a key")

	_, err = New(config.Vision{Provider: "claude", APIKey: "k"})
	assert.Error(t, err, "a real provider needs a pinned model")
}
