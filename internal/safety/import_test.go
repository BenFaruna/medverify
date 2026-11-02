package safety_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/safety"
)

const importHeader = "kind,nrn,product_name,manufacturer,ingredient,reason,effective_until,review_due_at,issuing_body,source_title,source_url,issued_at,reviewed_by\n"

const validRow = "alert,TT4-7001,,,,recalled test batch,,2027-09-01,NAFDAC,Alert on a test batch,https://example.test/nafdac/alerts/1,2026-09-01,Test Reviewer\n"

func TestParseImportCSVAcceptsTheCommittedFixture(t *testing.T) {
	t.Parallel()

	file, err := os.Open("../../testdata/watchlist/entries.csv")
	require.NoError(t, err)
	defer file.Close()

	entries, err := safety.ParseImportCSV(file)
	require.NoError(t, err)
	require.Len(t, entries, 20)

	byNRN := make(map[string]safety.ImportEntry)
	byIngredient := make(map[string]safety.ImportEntry)
	for _, entry := range entries {
		if entry.NRN != "" {
			byNRN[entry.NRN] = entry
		}
		if entry.IngredientNorm != "" {
			byIngredient[entry.IngredientNorm] = entry
		}
	}

	alert, ok := byNRN["TT4-7001"]
	require.True(t, ok)
	assert.Equal(t, safety.EntryAlert, alert.Kind)
	assert.Equal(t, "https://example.test/nafdac/alerts/2026-09-01", alert.Source.URL)
	assert.Equal(t, "NAFDAC", alert.Source.IssuingBody)
	assert.Equal(t, "Test Reviewer", alert.ReviewedBy)

	withdrawal, ok := byNRN["TT4-7005"]
	require.True(t, ok)
	assert.Equal(t, safety.EntryWithdrawal, withdrawal.Kind)

	banned, ok := byIngredient["phenylpropanolamine"]
	require.True(t, ok)
	assert.Equal(t, safety.EntryBannedIngredient, banned.Kind)

	restricted, ok := byIngredient["codeine"]
	require.True(t, ok)
	assert.Equal(t, safety.EntryRestrictedIngredient, restricted.Kind)

	// Fictional brand entries keep both comparison keys.
	var brand safety.ImportEntry
	for _, entry := range entries {
		if entry.ProductName == "Kestrel Test Cold Relief Syrup" {
			brand = entry
			break
		}
	}
	// Dosage-form words are stripped for comparison, as in phase 05.
	assert.Equal(t, "kestrel test cold relief", brand.ProductNameNorm)
	assert.Equal(t, "kestrel", brand.ManufacturerNorm)
}

func TestParseImportCSVNormalizesNRNAndIngredient(t *testing.T) {
	t.Parallel()

	entries, err := safety.ParseImportCSV(strings.NewReader(importHeader +
		"alert, tt4-7010 ,,,,reason,,,NAFDAC,Alert,https://example.test/a,2026-09-01,Reviewer\n" +
		"banned_ingredient,,,,Phenylpropanolamine Hydrochloride,reason,,,NAFDAC,List,https://example.test/b,2026-09-01,Reviewer\n"))
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "TT4-7010", entries[0].NRN)
	// Salt forms and synonyms come from the shared phase 02 table.
	assert.Equal(t, "phenylpropanolamine", entries[1].IngredientNorm)
}

func TestParseImportCSVReportsEveryInvalidRowWithALineNumber(t *testing.T) {
	t.Parallel()

	body := importHeader +
		validRow + // line 2, valid
		"recall,TT4-7002,,,,reason,,,NAFDAC,Alert,https://example.test/a,2026-09-01,Reviewer\n" + // line 3, bad kind
		"alert,TT4-7003,,,,reason,,,NAFDAC,Alert,not-a-url,2026-09-01,Reviewer\n" + // line 4, bad URL
		"alert,TT4-7004,,,,reason,,,NAFDAC,Alert,https://example.test/a,01/09/2026,Reviewer\n" + // line 5, bad date
		"alert,,Brand Only,Company Ltd,,reason,,,NAFDAC,Alert,https://example.test/a,2026-09-01,Reviewer\n" // line 6, valid name entry
	// line 7 duplicates line 2.
	body += validRow

	entries, err := safety.ParseImportCSV(strings.NewReader(body))
	require.Error(t, err)
	assert.Nil(t, entries, "one invalid row must reject the whole file")

	var validation safety.ValidationError
	require.True(t, errors.As(err, &validation))
	require.Len(t, validation.Problems, 4)

	lines := make([]int, 0, len(validation.Problems))
	columns := make([]string, 0, len(validation.Problems))
	for _, problem := range validation.Problems {
		lines = append(lines, problem.Line)
		columns = append(columns, problem.Column)
	}
	assert.ElementsMatch(t, []int{3, 4, 5, 7}, lines)
	assert.Contains(t, columns, "kind")
	assert.Contains(t, columns, "source_url")
	assert.Contains(t, columns, "issued_at")
	assert.Contains(t, columns, "row")
}

func TestParseImportCSVRejectsBadHeaders(t *testing.T) {
	t.Parallel()

	_, err := safety.ParseImportCSV(strings.NewReader("kind,nrn,reason\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required column is missing")

	_, err = safety.ParseImportCSV(strings.NewReader(importHeader[:len(importHeader)-1] + ",surprise\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown column")
}

func TestParseImportCSVEnforcesEntryShape(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"ingredient entry with an NRN":     "banned_ingredient,TT4-7011,,,,reason,,,NAFDAC,List,https://example.test/a,2026-09-01,Reviewer\n",
		"product entry with an ingredient": "alert,TT4-7012,,,codeine,reason,,,NAFDAC,Alert,https://example.test/a,2026-09-01,Reviewer\n",
		"product entry with no identifier": "alert,,,,,reason,,,NAFDAC,Alert,https://example.test/a,2026-09-01,Reviewer\n",
		"manufacturer without a name":      "alert,,,,Kestrel Pharma Ltd,reason,,,NAFDAC,Alert,https://example.test/a,2026-09-01,Reviewer\n",
		"missing reviewer":                 "alert,TT4-7013,,,,reason,,,NAFDAC,Alert,https://example.test/a,2026-09-01,\n",
		"review due before issue":          "alert,TT4-7014,,,,reason,,2026-01-01,NAFDAC,Alert,https://example.test/a,2026-09-01,Reviewer\n",
		"invalid NRN shape":                "alert,TT4,,,reason,,,NAFDAC,Alert,https://example.test/a,2026-09-01,Reviewer\n",
	}
	for name, row := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			entries, err := safety.ParseImportCSV(strings.NewReader(importHeader + row))
			require.Error(t, err)
			assert.Nil(t, entries)
		})
	}
}

func TestParseImportCSVRequiresAtLeastOneRow(t *testing.T) {
	t.Parallel()

	_, err := safety.ParseImportCSV(strings.NewReader(importHeader))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no rows")
}
