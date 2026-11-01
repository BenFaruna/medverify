package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readLeafletFixture(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "greenbook", "leaflets", name))
	require.NoError(t, err)
	return string(body)
}

func TestSplitSectionsStructuredLeaflet(t *testing.T) {
	sections := SplitSections(readLeafletFixture(t, "structured.txt"))
	require.Len(t, sections, 5)

	assert.Equal(t, "1", sections[0].Code)
	assert.Equal(t, "NAME OF THE MEDICINAL PRODUCT", sections[0].Heading)
	assert.Equal(t, 1, sections[0].PageStart)
	assert.Contains(t, sections[0].Text, "Paracetamol 500 mg tablets")

	assert.Equal(t, "2", sections[1].Code)
	assert.Equal(t, "QUALITATIVE AND QUANTITATIVE COMPOSITION", sections[1].Heading)

	assert.Equal(t, "3", sections[2].Code)
	assert.Equal(t, 2, sections[2].PageStart)

	assert.Equal(t, "4.1", sections[3].Code)
	assert.Equal(t, "Therapeutic indications", sections[3].Heading)

	assert.Equal(t, "4.2", sections[4].Code)
	assert.Equal(t, "Posology and method of administration", sections[4].Heading)
	assert.Contains(t, sections[4].Text, "Take one tablet twice a day.")
}

func TestSplitSectionsOddHeadings(t *testing.T) {
	sections := SplitSections(readLeafletFixture(t, "odd_headings.txt"))
	require.Len(t, sections, 3)
	assert.Equal(t, "1", sections[0].Code)
	assert.Equal(t, "2", sections[1].Code)
	assert.Equal(t, "4.2", sections[2].Code)
	assert.Contains(t, sections[2].Text, "One tablet up to three times daily.")
}

func TestSplitSectionsWithoutHeadingsKeepsOneSection(t *testing.T) {
	sections := SplitSections(readLeafletFixture(t, "no_headings.txt"))
	require.Len(t, sections, 1)
	assert.Empty(t, sections[0].Code)
	assert.Empty(t, sections[0].Heading)
	assert.Contains(t, sections[0].Text, "PATIENT INFORMATION LEAFLET")
}

func TestSplitSectionsIgnoresNumberedBodyLines(t *testing.T) {
	text := "1. NAME OF THE MEDICINAL PRODUCT\nExample\n2. Take one tablet twice a day.\nStill body text.\n"
	sections := SplitSections(text)
	require.Len(t, sections, 1)
	assert.Equal(t, "1", sections[0].Code)
	assert.Contains(t, sections[0].Text, "2. Take one tablet twice a day.")
}

func TestSplitSectionsEmptyText(t *testing.T) {
	assert.Nil(t, SplitSections(" \n\f\n "))
}

func TestParseSectionHeading(t *testing.T) {
	tests := []struct {
		line    string
		code    string
		heading string
		ok      bool
	}{
		{"1. NAME OF THE MEDICINAL PRODUCT", "1", "NAME OF THE MEDICINAL PRODUCT", true},
		{"4.2 Posology and method of administration", "4.2", "Posology and method of administration", true},
		{"SECTION 4.2 Posology and method of administration", "4.2", "Posology and method of administration", true},
		{"2) QUALITATIVE AND QUANTITATIVE COMPOSITION", "2", "QUALITATIVE AND QUANTITATIVE COMPOSITION", true},
		{"1. Take one tablet twice a day.", "", "", false},
		{"21. NAME OF THE MEDICINAL PRODUCT", "", "", false},
		{"no number here", "", "", false},
	}
	for _, test := range tests {
		code, heading, ok := parseSectionHeading(test.line)
		assert.Equal(t, test.ok, ok, test.line)
		if test.ok {
			assert.Equal(t, test.code, code, test.line)
			assert.Equal(t, test.heading, heading, test.line)
		}
	}
}

func TestSplitSectionsNormalisesWindowsLineEndings(t *testing.T) {
	sections := SplitSections("1. NAME OF THE MEDICINAL PRODUCT\r\nExample\r\n")
	require.Len(t, sections, 1)
	assert.False(t, strings.Contains(sections[0].Text, "\r"))
}
