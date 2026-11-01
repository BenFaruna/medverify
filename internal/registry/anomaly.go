package registry

import "fmt"

// AnomalyPolicy controls when a sync is too suspicious to commit.
type AnomalyPolicy struct {
	// MaxDropPercent aborts when the current row count is this much smaller
	// than the last successful run. 0 disables the check.
	MaxDropPercent float64
	// MaxParseFailurePercent aborts when more than this share of returned rows
	// could not be parsed. 0 disables the check.
	MaxParseFailurePercent float64
}

// Anomaly is the outcome of the guard. Empty Reasons means the sync is normal.
type Anomaly struct {
	Reasons []string
}

// Aborted reports whether the sync should stop before writing products.
func (a Anomaly) Aborted() bool { return len(a.Reasons) > 0 }

// Error joins the reasons into one line for sync_runs.error and admin output.
func (a Anomaly) Error() string {
	if len(a.Reasons) == 0 {
		return ""
	}
	out := "anomaly guard: "
	for i, reason := range a.Reasons {
		if i > 0 {
			out += "; "
		}
		out += reason
	}
	return out
}

// CheckAnomaly compares a finished page sweep with the last successful run.
// The product count and the parse-failure share are independent checks: either
// one can abort. hasPrevious is false on the first ever sync, when only the
// empty-response and parse-failure checks apply.
func CheckAnomaly(previousRows int, hasPrevious bool, currentRows, rowsSeen, rowsFailed int, policy AnomalyPolicy) Anomaly {
	var reasons []string

	if currentRows == 0 {
		reasons = append(reasons, "source returned no products")
	}

	if hasPrevious && previousRows > 0 && policy.MaxDropPercent > 0 {
		drop := float64(previousRows-currentRows) / float64(previousRows) * 100
		if drop > policy.MaxDropPercent {
			reasons = append(reasons, fmt.Sprintf(
				"row count dropped from %d to %d (%.1f%%, limit %.1f%%)",
				previousRows, currentRows, drop, policy.MaxDropPercent,
			))
		}
	}

	if rowsSeen > 0 && policy.MaxParseFailurePercent > 0 {
		failureShare := float64(rowsFailed) / float64(rowsSeen) * 100
		if failureShare > policy.MaxParseFailurePercent {
			reasons = append(reasons, fmt.Sprintf(
				"%.1f%% of rows failed to parse (limit %.1f%%)",
				failureShare, policy.MaxParseFailurePercent,
			))
		}
	}

	return Anomaly{Reasons: reasons}
}
