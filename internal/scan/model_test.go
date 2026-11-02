package scan_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/scan"
)

func TestStateMachineAllowsOnlyDocumentedTransitions(t *testing.T) {
	t.Parallel()

	allowed := map[scan.Status][]scan.Status{
		scan.StatusQueued:     {scan.StatusProcessing},
		scan.StatusProcessing: {scan.StatusCompleted, scan.StatusUnreadable, scan.StatusFailed},
		scan.StatusFailed:     {scan.StatusQueued},
		scan.StatusCompleted:  nil,
		scan.StatusUnreadable: nil,
	}

	for from, tos := range allowed {
		for _, to := range []scan.Status{
			scan.StatusQueued, scan.StatusProcessing, scan.StatusCompleted, scan.StatusUnreadable, scan.StatusFailed,
		} {
			want := false
			for _, candidate := range tos {
				if candidate == to {
					want = true
				}
			}
			assert.Equal(t, want, scan.CanTransition(from, to), "%s -> %s", from, to)
		}
	}
}

func TestTransitionRejectsIllegalChange(t *testing.T) {
	t.Parallel()

	_, err := scan.Transition(scan.StatusCompleted, scan.StatusProcessing)
	assert.ErrorIs(t, err, scan.ErrInvalidTransition)

	next, err := scan.Transition(scan.StatusFailed, scan.StatusQueued)
	require.NoError(t, err)
	assert.Equal(t, scan.StatusQueued, next)
}

func TestCoarseCellDropsPrecision(t *testing.T) {
	t.Parallel()

	cell, err := scan.CoarseCell(6.453123, 3.394321, 1)
	require.NoError(t, err)
	assert.Equal(t, "6.5,3.4", cell)
	assert.NotContains(t, cell, "453123")
	assert.NotContains(t, cell, "394321")
}

func TestParseLocationRequiresBothCoordinates(t *testing.T) {
	t.Parallel()

	cell, err := scan.ParseLocation("", "")
	require.NoError(t, err)
	assert.Empty(t, cell)

	_, err = scan.ParseLocation("6.45", "")
	assert.ErrorIs(t, err, scan.ErrInvalidLocation)

	_, err = scan.ParseLocation("not-a-number", "3.39")
	assert.ErrorIs(t, err, scan.ErrInvalidLocation)

	_, err = scan.ParseLocation("91", "3.39")
	assert.ErrorIs(t, err, scan.ErrInvalidLocation)

	cell, err = scan.ParseLocation("6.453123", "3.394321")
	require.NoError(t, err)
	assert.Equal(t, "6.5,3.4", cell)
}

func TestNormalizeLanguage(t *testing.T) {
	t.Parallel()

	for _, valid := range []string{"en", "ha", "yo"} {
		got, err := scan.NormalizeLanguage(valid)
		require.NoError(t, err, valid)
		assert.Equal(t, valid, got)
	}

	got, err := scan.NormalizeLanguage("en-GB")
	require.NoError(t, err)
	assert.Equal(t, "en-gb", got)

	for _, invalid := range []string{"english language", "e", "en_GB", "<script>"} {
		_, err := scan.NormalizeLanguage(invalid)
		assert.ErrorIs(t, err, scan.ErrInvalidLanguage, invalid)
	}
}
