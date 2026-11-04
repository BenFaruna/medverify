package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Metric is one scored value. Unit is "count" or "rate" so a gate can tell a
// threshold of zero leaks from a threshold of zero percent.
type Metric struct {
	Value float64 `json:"value"`
	Count int     `json:"count,omitempty"`
	Total int     `json:"total,omitempty"`
	Unit  string  `json:"unit"`
}

// Failure is one case-level failure kept for review. It never contains raw
// user text, images, or provider responses.
type Failure struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// Report is the deterministic result of one eval run. The version fields are
// required: a score without them cannot be compared across runs.
type Report struct {
	Component      Component         `json:"component"`
	Dataset        string            `json:"dataset"`
	DatasetVersion string            `json:"dataset_version"`
	DatasetSHA256  string            `json:"dataset_sha256"`
	Split          Split             `json:"split,omitempty"`
	Model          string            `json:"model"`
	SmallModel     string            `json:"small_model,omitempty"`
	PromptVersion  string            `json:"prompt_version"`
	RulesVersion   string            `json:"rules_version,omitempty"`
	StartedAt      time.Time         `json:"started_at"`
	DurationMS     int64             `json:"duration_ms"`
	CostUSD        float64           `json:"cost_usd,omitempty"`
	Cases          int               `json:"cases"`
	Metrics        map[string]Metric `json:"metrics"`
	Confusion      *ConfusionTable   `json:"confusion,omitempty"`
	Failures       []Failure         `json:"failures,omitempty"`
}

// RateMetric adds or replaces a rate metric and keeps its tally. A rate is
// based on the supplied tally, not on a floating-point value, so it cannot
// disagree with its own counts.
func (r *Report) RateMetric(name string, tally Tally) {
	r.ensureMetrics()
	r.Metrics[name] = Metric{Value: tally.Rate(), Count: tally.Hits, Total: tally.Total, Unit: "rate"}
}

// CountMetric adds or replaces a count metric, such as advice leaks, where one
// occurrence fails the gate regardless of how many cases passed.
func (r *Report) CountMetric(name string, count int) {
	r.ensureMetrics()
	r.Metrics[name] = Metric{Value: float64(count), Count: count, Unit: "count"}
}

func (r *Report) ensureMetrics() {
	if r.Metrics == nil {
		r.Metrics = make(map[string]Metric)
	}
}

// AddFailure records a case the reviewer should inspect.
func (r *Report) AddFailure(id, reason string) {
	r.Failures = append(r.Failures, Failure{ID: id, Reason: reason})
}

// ScoredAt fills the timing fields from a start time.
func (r *Report) ScoredAt(started time.Time) {
	r.StartedAt = started.UTC()
	r.DurationMS = time.Since(started).Milliseconds()
}

// Validate checks the reproducibility header before a report is written or
// gated.
func (r Report) Validate() error {
	if !r.Component.Valid() {
		return fmt.Errorf("eval: report has unknown component %q", r.Component)
	}
	if strings.TrimSpace(r.Dataset) == "" || strings.TrimSpace(r.DatasetVersion) == "" {
		return fmt.Errorf("eval: report needs a dataset name and version")
	}
	if strings.TrimSpace(r.DatasetSHA256) == "" {
		return fmt.Errorf("eval: report needs the dataset hash")
	}
	if strings.TrimSpace(r.Model) == "" {
		return fmt.Errorf("eval: report needs the model that produced the outputs")
	}
	if strings.TrimSpace(r.PromptVersion) == "" {
		return fmt.Errorf("eval: report needs a prompt version")
	}
	if r.StartedAt.IsZero() {
		return fmt.Errorf("eval: report needs a start time")
	}
	return nil
}

// MetricNames returns metric names in sorted order.
func (r Report) MetricNames() []string {
	names := make([]string, 0, len(r.Metrics))
	for name := range r.Metrics {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// JSON renders the report as indented JSON.
func (r Report) JSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("eval: encode report: %w", err)
	}
	return append(encoded, '\n'), nil
}

// Markdown renders a human-readable report with the full version header.
func (r Report) Markdown() (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Eval report: %s\n\n", r.Dataset)
	fmt.Fprintf(&b, "| Field | Value |\n| --- | --- |\n")
	fmt.Fprintf(&b, "| Component | %s |\n", r.Component)
	fmt.Fprintf(&b, "| Dataset | %s |\n", r.Dataset)
	fmt.Fprintf(&b, "| Dataset version | %s |\n", r.DatasetVersion)
	fmt.Fprintf(&b, "| Dataset SHA256 | `%s` |\n", r.DatasetSHA256)
	if r.Split != "" {
		fmt.Fprintf(&b, "| Split | %s |\n", r.Split)
	}
	fmt.Fprintf(&b, "| Model | %s |\n", r.Model)
	if r.SmallModel != "" {
		fmt.Fprintf(&b, "| Small model | %s |\n", r.SmallModel)
	}
	fmt.Fprintf(&b, "| Prompt version | %s |\n", r.PromptVersion)
	if r.RulesVersion != "" {
		fmt.Fprintf(&b, "| Rules version | %s |\n", r.RulesVersion)
	}
	fmt.Fprintf(&b, "| Started at | %s |\n", r.StartedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "| Duration | %d ms |\n", r.DurationMS)
	fmt.Fprintf(&b, "| Cases | %d |\n", r.Cases)
	if r.CostUSD > 0 {
		fmt.Fprintf(&b, "| Estimated cost | $%.4f |\n", r.CostUSD)
	}
	b.WriteString("\n## Metrics\n\n| Metric | Value | Count | Unit |\n| --- | ---: | ---: | --- |\n")
	for _, name := range r.MetricNames() {
		metric := r.Metrics[name]
		var value string
		if metric.Unit == "count" {
			value = fmt.Sprintf("%d", metric.Count)
		} else {
			value = fmt.Sprintf("%.2f%%", metric.Value*100)
		}
		count := ""
		if metric.Total > 0 {
			count = fmt.Sprintf("%d/%d", metric.Count, metric.Total)
		} else if metric.Unit == "count" {
			count = fmt.Sprintf("%d", metric.Count)
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", name, value, count, metric.Unit)
	}
	if len(r.Failures) > 0 {
		b.WriteString("\n## Failures\n\n")
		for _, failure := range r.Failures {
			fmt.Fprintf(&b, "- `%s`: %s\n", failure.ID, failure.Reason)
		}
	}
	if r.Confusion != nil {
		b.WriteString("\n## Confusion\n\n| Expected \\ Got |")
		for _, label := range r.Confusion.Labels {
			fmt.Fprintf(&b, " %s |", label)
		}
		b.WriteString("\n| --- |")
		for range r.Confusion.Labels {
			b.WriteString(" ---: |")
		}
		b.WriteString("\n")
		for row, label := range r.Confusion.Labels {
			fmt.Fprintf(&b, "| %s |", label)
			for _, count := range r.Confusion.Counts[row] {
				fmt.Fprintf(&b, " %d |", count)
			}
			b.WriteString("\n")
		}
		if unknown := r.Confusion.Totals["(unknown)"]; unknown > 0 {
			fmt.Fprintf(&b, "\n%d output(s) had a label outside the confusion set.\n", unknown)
		}
	}
	return b.String(), nil
}

// WriteJSON writes the report to path, creating parent directories.
func (r Report) WriteJSON(path string) error {
	encoded, err := r.JSON()
	if err != nil {
		return err
	}
	return writeFile(path, encoded)
}

// WriteMarkdown writes the rendered report to path, creating parent
// directories.
func (r Report) WriteMarkdown(path string) error {
	rendered, err := r.Markdown()
	if err != nil {
		return err
	}
	return writeFile(path, []byte(rendered))
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("eval: create report directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("eval: write report: %w", err)
	}
	return nil
}
