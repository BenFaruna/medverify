package registry_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/benfaruna/medverify/internal/registry"
)

func TestCheckAnomaly(t *testing.T) {
	t.Parallel()

	policy := registry.AnomalyPolicy{MaxDropPercent: 30, MaxParseFailurePercent: 10}

	cases := []struct {
		name         string
		previousRows int
		hasPrevious  bool
		currentRows  int
		rowsSeen     int
		rowsFailed   int
		policy       registry.AnomalyPolicy
		wantAborted  bool
	}{
		{
			name:        "first sync with rows is fine",
			hasPrevious: false,
			currentRows: 10,
			rowsSeen:    10,
			policy:      policy,
			wantAborted: false,
		},
		{
			name:        "empty source aborts even on first sync",
			hasPrevious: false,
			currentRows: 0,
			rowsSeen:    0,
			policy:      policy,
			wantAborted: true,
		},
		{
			name:         "thirty percent fewer aborts",
			previousRows: 100,
			hasPrevious:  true,
			currentRows:  69,
			rowsSeen:     69,
			policy:       policy,
			wantAborted:  true,
		},
		{
			name:         "exactly the limit is not a drop over the limit",
			previousRows: 100,
			hasPrevious:  true,
			currentRows:  70,
			rowsSeen:     70,
			policy:       policy,
			wantAborted:  false,
		},
		{
			name:         "more rows is fine",
			previousRows: 100,
			hasPrevious:  true,
			currentRows:  150,
			rowsSeen:     150,
			policy:       policy,
			wantAborted:  false,
		},
		{
			name:         "too many parse failures aborts",
			previousRows: 100,
			hasPrevious:  true,
			currentRows:  90,
			rowsSeen:     100,
			rowsFailed:   11,
			policy:       policy,
			wantAborted:  true,
		},
		{
			name:         "parse failure share at the limit is fine",
			previousRows: 100,
			hasPrevious:  true,
			currentRows:  90,
			rowsSeen:     100,
			rowsFailed:   10,
			policy:       policy,
			wantAborted:  false,
		},
		{
			name:         "zero thresholds disable both checks",
			previousRows: 100,
			hasPrevious:  true,
			currentRows:  1,
			rowsSeen:     100,
			rowsFailed:   99,
			policy:       registry.AnomalyPolicy{},
			wantAborted:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			anomaly := registry.CheckAnomaly(tc.previousRows, tc.hasPrevious, tc.currentRows, tc.rowsSeen, tc.rowsFailed, tc.policy)
			assert.Equal(t, tc.wantAborted, anomaly.Aborted())
			if tc.wantAborted {
				assert.NotEmpty(t, anomaly.Error())
			} else {
				assert.Empty(t, anomaly.Error())
			}
		})
	}
}
