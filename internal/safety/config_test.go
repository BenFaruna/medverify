package safety_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/safety"
	"github.com/benfaruna/medverify/internal/verify"
)

func TestEmbeddedConfigIsComplete(t *testing.T) {
	t.Parallel()

	config, err := safety.LoadConfig("")
	require.NoError(t, err)
	assert.Equal(t, safety.ConfigVersion, config.Version)
	assert.Positive(t, config.StrengthTolerance)
	assert.Len(t, config.Severity, len(safety.FlagTypes))
	for _, flagType := range safety.FlagTypes {
		assert.True(t, config.Severity[flagType].Valid(), "severity for %s", flagType)
	}
}

func TestConfigRejectsUnknownFieldsAndBadValues(t *testing.T) {
	t.Parallel()

	write := func(t *testing.T, body string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "safety.yaml")
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
		return path
	}

	base := `
version: safety-v1
strength_tolerance: 0.05
min_ingredient_confidence: 0.7
watchlist_name_threshold: 0.85
watchlist_manufacturer_threshold: 0.7
severity:
  watchlist_product: high
  watchlist_ingredient: high
  undeclared_ingredient: medium
  missing_ingredient: medium
  strength_deviation: medium
  registration_problem: high
`

	_, err := safety.LoadConfig(write(t, base))
	require.NoError(t, err, "the baseline fixture must be valid")

	_, err = safety.LoadConfig(write(t, base+"extra_threshold: 1\n"))
	require.Error(t, err, "an unknown field must be rejected, not ignored")

	_, err = safety.LoadConfig(write(t, strings.Replace(base, "strength_tolerance: 0.05", "strength_tolerance: 0", 1)))
	require.Error(t, err)

	_, err = safety.LoadConfig(write(t, strings.Replace(base, "  registration_problem: high\n", "", 1)))
	require.Error(t, err, "every flag type needs a severity")

	_, err = safety.LoadConfig(write(t, strings.Replace(base, "missing_ingredient: medium", "missing_ingredient: critical", 1)))
	require.Error(t, err)
}

// TestWordingCatalogCoversEveryCodeAndMakesNoClaim is the phase 06 unit test:
// every reason code has a template in every language, and no template claims a
// product is safe, harmful, fake, or counterfeit.
func TestWordingCatalogCoversEveryCodeAndMakesNoClaim(t *testing.T) {
	t.Parallel()

	wording, err := safety.LoadWording("")
	require.NoError(t, err)
	assert.Equal(t, safety.WordingVersion, wording.Version)
	assert.Equal(t, "en", wording.Language)

	forbidden := regexp.MustCompile(`(?i)\b(safe|harmless|fake|counterfeit|poison)\b`)
	for _, code := range safety.ReasonCodes {
		template, ok := wording.Templates[code]
		require.True(t, ok, "reason %s has no wording", code)
		assert.NotEmpty(t, strings.TrimSpace(template), "reason %s has empty wording", code)
		assert.NotRegexp(t, forbidden, template, "reason %s makes a claim", code)
	}
}

func TestWordingRendersEveryCodeWithItsEvidence(t *testing.T) {
	t.Parallel()

	wording, err := safety.LoadWording("")
	require.NoError(t, err)

	evidence := map[string]map[string]string{
		safety.ReasonWatchlistAlert:                {"issuing_body": "NAFDAC", "issued_at": "2026-09-01", "source_url": "https://example.test/a"},
		safety.ReasonWatchlistWithdrawal:           {"issuing_body": "NAFDAC", "issued_at": "2026-09-01", "source_url": "https://example.test/a"},
		safety.ReasonWatchlistBannedIngredient:     {"issuing_body": "NAFDAC", "ingredient": "paracetamol", "source_url": "https://example.test/a"},
		safety.ReasonWatchlistRestrictedIngredient: {"issuing_body": "NAFDAC", "ingredient": "codeine", "source_url": "https://example.test/a"},
		safety.ReasonUndeclaredIngredient:          {"ingredient": "caffeine"},
		safety.ReasonMissingIngredient:             {"ingredient": "caffeine"},
		safety.ReasonStrengthDeviation:             {"ingredient": "paracetamol", "label_strength": "250 mg", "registry_strength": "500 mg"},
		verify.ReasonRegistryExpired:               {"status": "Expired"},
		verify.ReasonRegistryWithdrawn:             {"status": "Withdrawn"},
		verify.ReasonRegistryInactive:              {"status": "Inactive"},
	}

	for _, code := range safety.ReasonCodes {
		codeEvidence, ok := evidence[code]
		require.True(t, ok, "test has no evidence for %s", code)
		rendered, err := wording.Render(code, codeEvidence)
		require.NoError(t, err, "render %s", code)
		assert.NotContains(t, rendered, "{", "render %s left a placeholder", code)
	}
}

func TestWordingRenderRefusesToLeaveHoles(t *testing.T) {
	t.Parallel()

	wording, err := safety.LoadWording("")
	require.NoError(t, err)

	_, err = wording.Render(safety.ReasonWatchlistAlert, map[string]string{"issuing_body": "NAFDAC"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "issued_at")

	_, err = wording.Render("no_such_reason", nil)
	require.Error(t, err)
}

func TestWordingRejectsAnIncompleteCatalog(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "en.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: wording-v1\nlanguage: en\ntemplates:\n  watchlist_alert: \"Alert from {issuing_body}.\"\n"), 0o600))

	_, err := safety.LoadWording(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "template for")
}
