package eval

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Threshold is one release gate. Min and Max are optional but at least one must
// be set. A count gate such as advice leaks normally uses Max: 0.
type Threshold struct {
	Min *float64 `yaml:"min,omitempty" json:"min,omitempty"`
	Max *float64 `yaml:"max,omitempty" json:"max,omitempty"`
}

// GateFile is the versioned set of thresholds for one component. Thresholds
// are proposed by engineering and agreed with the project lead before a release
// gate may be cited as passed.
type GateFile struct {
	Version    string               `yaml:"version" json:"version"`
	Component  Component            `yaml:"component" json:"component"`
	Thresholds map[string]Threshold `yaml:"thresholds" json:"thresholds"`
}

// GateFailure explains one failed threshold.
type GateFailure struct {
	Metric string   `json:"metric"`
	Value  float64  `json:"value"`
	Min    *float64 `json:"min,omitempty"`
	Max    *float64 `json:"max,omitempty"`
	Reason string   `json:"reason"`
}

func (f GateFailure) String() string {
	return fmt.Sprintf("%s: %s", f.Metric, f.Reason)
}

// LoadGates reads a strict YAML gate file. Unknown fields are rejected so a
// misspelled metric cannot silently go unenforced.
func LoadGates(path string) (GateFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return GateFile{}, fmt.Errorf("eval: read gates: %w", err)
	}
	var gates GateFile
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&gates); err != nil {
		return GateFile{}, fmt.Errorf("eval: decode gates: %w", err)
	}
	if err := gates.Validate(); err != nil {
		return GateFile{}, err
	}
	return gates, nil
}

// Validate checks a gate file before it is used.
func (g GateFile) Validate() error {
	if strings.TrimSpace(g.Version) == "" {
		return fmt.Errorf("eval: gate file needs a version")
	}
	if !g.Component.Valid() {
		return fmt.Errorf("eval: gate file has unknown component %q", g.Component)
	}
	if len(g.Thresholds) == 0 {
		return fmt.Errorf("eval: gate file %s has no thresholds", g.Version)
	}
	for name, threshold := range g.Thresholds {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("eval: gate file has an empty metric name")
		}
		if threshold.Min == nil && threshold.Max == nil {
			return fmt.Errorf("eval: gate %s needs min or max", name)
		}
		if threshold.Min != nil && threshold.Max != nil && *threshold.Min > *threshold.Max {
			return fmt.Errorf("eval: gate %s has min greater than max", name)
		}
	}
	return nil
}

// Evaluate compares every threshold with the report. A threshold naming a
// metric the run did not produce fails: an absent metric cannot prove safety.
// Component mismatch also fails rather than silently applying the wrong gates.
func (g GateFile) Evaluate(report Report) []GateFailure {
	if g.Component != report.Component {
		return []GateFailure{{
			Metric: "(component)",
			Reason: fmt.Sprintf("gate file is for %s but the report is %s", g.Component, report.Component),
		}}
	}
	names := make([]string, 0, len(g.Thresholds))
	for name := range g.Thresholds {
		names = append(names, name)
	}
	sort.Strings(names)

	failures := make([]GateFailure, 0, len(names))
	for _, name := range names {
		threshold := g.Thresholds[name]
		metric, ok := report.Metrics[name]
		if !ok {
			failures = append(failures, GateFailure{
				Metric: name, Min: threshold.Min, Max: threshold.Max,
				Reason: "metric is missing from the report",
			})
			continue
		}
		if threshold.Min != nil && metric.Value < *threshold.Min {
			failures = append(failures, GateFailure{
				Metric: name, Value: metric.Value, Min: threshold.Min, Max: threshold.Max,
				Reason: fmt.Sprintf("%.4f is below the minimum %.4f", metric.Value, *threshold.Min),
			})
			continue
		}
		if threshold.Max != nil && metric.Value > *threshold.Max {
			failures = append(failures, GateFailure{
				Metric: name, Value: metric.Value, Min: threshold.Min, Max: threshold.Max,
				Reason: fmt.Sprintf("%.4f is above the maximum %.4f", metric.Value, *threshold.Max),
			})
		}
	}
	return failures
}
