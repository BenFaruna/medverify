package verdict_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/verdict"
)

// forbiddenWords is the phase 07 wording rule. A verdict may report agreement,
// differences, or not-found, but it may never make an authenticity or safety
// claim in either direction. Word boundaries keep "safety check" usable.
var forbiddenWords = regexp.MustCompile(`(?i)\b(safe|genuine|authentic|fake|counterfeit|harmless|dangerous)\b`)

func TestReasonCatalogCoversEveryCodeAndAvoidsForbiddenWords(t *testing.T) {
	catalog, err := verdict.LoadCatalog("")
	require.NoError(t, err)

	assert.Equal(t, verdict.CatalogVersion, catalog.Version)
	assert.Equal(t, "en", catalog.Language)
	assert.Len(t, catalog.Templates, len(verdict.ReasonCodes))

	for _, code := range verdict.ReasonCodes {
		template := catalog.Templates[code]
		assert.NotEmpty(t, template, "missing template for %s", code)

		assert.NotRegexp(t, forbiddenWords, template, "template %s makes a claim", code)
	}
}

func TestCatalogRejectsUnknownAndEmptyTemplates(t *testing.T) {
	base := `version: reasons-test
language: en
templates:
`
	for _, code := range verdict.ReasonCodes {
		base += "  " + code + ": \"Text for " + code + ".\"\n"
	}

	_, err := verdict.LoadCatalog(writeTempFile(t, "catalog-ok.yaml", base))
	require.NoError(t, err)

	_, err = verdict.LoadCatalog(writeTempFile(t, "catalog-unknown.yaml", base+"  invented_reason: \"Something.\"\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invented_reason")

	_, err = verdict.LoadCatalog(writeTempFile(t, "catalog-empty.yaml", strings.Replace(base,
		"  label_consistent: \"Text for label_consistent.\"\n", "  label_consistent: \"\"\n", 1)))
	require.Error(t, err)
}

func TestRenderRequiresEveryPlaceholder(t *testing.T) {
	catalog, err := verdict.LoadCatalog("")
	require.NoError(t, err)

	rendered, err := catalog.Render(verdict.Reason{Code: verdict.ReasonUnknownFields, Params: map[string]string{"fields": "expiry"}})
	require.NoError(t, err)
	assert.Contains(t, rendered, "expiry")

	_, err = catalog.Render(verdict.Reason{Code: verdict.ReasonUnknownFields})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fields")

	_, err = catalog.Render(verdict.Reason{Code: "not_a_code"})
	require.Error(t, err)
}
