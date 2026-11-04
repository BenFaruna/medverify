package eval

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// JudgeRecord is one human label paired with one judge label for the same case.
// It deliberately carries no answer text: calibration is about agreement, not
// about storing user data.
type JudgeRecord struct {
	ID    string `json:"id"`
	Human string `json:"human"`
	Judge string `json:"judge"`
}

// JudgeCalibration is the measured agreement between a judge and human labels.
// It is only meaningful once real, two-person human labels exist.
type JudgeCalibration struct {
	Cases     int            `json:"cases"`
	Kappa     float64        `json:"kappa"`
	Agreement float64        `json:"agreement"`
	Labels    []string       `json:"labels"`
	Confusion ConfusionTable `json:"confusion"`
}

// LoadJudgeLabels reads JSONL human/judge label pairs. Unknown fields are
// rejected so a recording cannot quietly include raw answer text.
func LoadJudgeLabels(path string) ([]JudgeRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("eval: read judge labels: %w", err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	records := make([]JudgeRecord, 0, 64)
	line := 0
	for scanner.Scan() {
		line++
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var record JudgeRecord
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&record); err != nil {
			return nil, fmt.Errorf("eval: decode judge label on line %d: %w", line, err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("eval: read judge labels: %w", err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("eval: judge calibration needs at least one labelled pair")
	}
	return records, nil
}

// CalibrateJudge computes Cohen's kappa over the declared label set. A judge
// must not be used for release decisions until kappa is measured on real human
// labels and the project lead agrees the threshold.
func CalibrateJudge(records []JudgeRecord, labels []string) (JudgeCalibration, error) {
	if len(labels) == 0 {
		return JudgeCalibration{}, fmt.Errorf("eval: judge calibration needs the declared labels")
	}
	known := make(map[string]struct{}, len(labels))
	for _, label := range labels {
		if strings.TrimSpace(label) == "" {
			return JudgeCalibration{}, fmt.Errorf("eval: judge calibration has an empty label")
		}
		if _, exists := known[label]; exists {
			return JudgeCalibration{}, fmt.Errorf("eval: judge calibration has duplicate label %q", label)
		}
		known[label] = struct{}{}
	}

	seen := make(map[string]struct{}, len(records))
	pairs := make([]LabelPair, 0, len(records))
	for _, record := range records {
		if strings.TrimSpace(record.ID) == "" {
			return JudgeCalibration{}, fmt.Errorf("eval: judge label has no case id")
		}
		if _, exists := seen[record.ID]; exists {
			return JudgeCalibration{}, fmt.Errorf("eval: duplicate judge label for case %q", record.ID)
		}
		seen[record.ID] = struct{}{}
		if _, ok := known[record.Human]; !ok {
			return JudgeCalibration{}, fmt.Errorf("eval: case %q has human label %q outside the declared set", record.ID, record.Human)
		}
		if _, ok := known[record.Judge]; !ok {
			return JudgeCalibration{}, fmt.Errorf("eval: case %q has judge label %q outside the declared set", record.ID, record.Judge)
		}
		pairs = append(pairs, LabelPair{Expected: record.Human, Got: record.Judge})
	}

	kappa, err := CohensKappa(pairs)
	if err != nil {
		return JudgeCalibration{}, err
	}
	table, err := BuildConfusion(labels, pairs)
	if err != nil {
		return JudgeCalibration{}, err
	}
	hits := 0
	for _, pair := range pairs {
		if pair.Expected == pair.Got {
			hits++
		}
	}
	sortedLabels := append([]string(nil), labels...)
	sort.Strings(sortedLabels)
	return JudgeCalibration{
		Cases:     len(pairs),
		Kappa:     kappa,
		Agreement: float64(hits) / float64(len(pairs)),
		Labels:    sortedLabels,
		Confusion: table,
	}, nil
}

// Meets reports why a calibration is not yet usable. An empty result means the
// measured kappa reaches the agreed floor and the sample is large enough.
func (c JudgeCalibration) Meets(minKappa float64, minCases int) []string {
	var failures []string
	if c.Cases < minCases {
		failures = append(failures, fmt.Sprintf("only %d labelled pair(s); need %d", c.Cases, minCases))
	}
	if c.Kappa < minKappa {
		failures = append(failures, fmt.Sprintf("kappa %.4f is below the agreed floor %.4f", c.Kappa, minKappa))
	}
	return failures
}
