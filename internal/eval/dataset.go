// Package eval defines the versioned evaluation datasets, deterministic
// scoring helpers, report format, and release gates for MedVerify NG.
//
// The package never calls a provider. Component runners plug a fake, a recorded
// replay, or a live implementation behind an interface; scoring and gating stay
// deterministic and testable without network access.
//
// Real datasets contain human labels, not fixture data. A harness test proves
// the machinery works; it does not measure model quality.
package eval

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Component is the part of the system a dataset evaluates.
type Component string

// Supported components. A new component needs a runner and a schema in its own
// phase document before its dataset can be merged.
const (
	ComponentExtraction Component = "extraction"
	ComponentRetrieval  Component = "retrieval"
	ComponentAssistant  Component = "assistant"
	ComponentVerdict    Component = "verdict"
)

// Valid reports whether the component is known.
func (c Component) Valid() bool {
	switch c {
	case ComponentExtraction, ComponentRetrieval, ComponentAssistant, ComponentVerdict:
		return true
	default:
		return false
	}
}

// Split marks whether a case may be used for tuning or is held out.
type Split string

// Dataset splits. The test split is never used for prompt, model, or threshold
// tuning.
const (
	SplitDev  Split = "dev"
	SplitTest Split = "test"
)

// Valid reports whether the split is known.
func (s Split) Valid() bool { return s == SplitDev || s == SplitTest }

// File names inside one dataset directory.
const (
	MetaFile  = "dataset.json"
	CasesFile = "cases.jsonl"
)

// Tags are optional descriptive labels used for slicing reports.
type Tags struct {
	Language   string `json:"language,omitempty"`
	Difficulty string `json:"difficulty,omitempty"`
	Category   string `json:"category,omitempty"`
}

// Case is one labelled evaluation case in JSON Lines form.
type Case struct {
	ID        string          `json:"id"`
	Component Component       `json:"component"`
	Input     json.RawMessage `json:"input"`
	Expected  json.RawMessage `json:"expected"`
	Tags      Tags            `json:"tags,omitempty"`
	Source    string          `json:"source"`
	Labeler   string          `json:"labeler"`
	Date      string          `json:"date"`
	Split     Split           `json:"split"`
}

// Meta describes one versioned dataset.
type Meta struct {
	Name        string    `json:"name"`
	Version     string    `json:"version"`
	Component   Component `json:"component"`
	Description string    `json:"description,omitempty"`
	CreatedBy   string    `json:"created_by,omitempty"`
	CreatedAt   string    `json:"created_at,omitempty"`
}

// Dataset is one loaded and validated dataset directory.
type Dataset struct {
	Meta  Meta
	Cases []Case
	// SHA256 is the hash of cases.jsonl, recorded in every report so a score
	// can be tied to the exact cases that produced it.
	SHA256 string
	Dir    string
}

// CasesForSplit returns the cases in one split, preserving file order.
func (d Dataset) CasesForSplit(split Split) []Case {
	if split == "" {
		out := make([]Case, len(d.Cases))
		copy(out, d.Cases)
		return out
	}
	out := make([]Case, 0, len(d.Cases))
	for _, c := range d.Cases {
		if c.Split == split {
			out = append(out, c)
		}
	}
	return out
}

// LoadDataset reads a dataset directory containing dataset.json and
// cases.jsonl. Every case is validated before it can be scored, so a typo
// cannot silently become an unlabelled case.
func LoadDataset(dir string) (Dataset, error) {
	metaBytes, err := os.ReadFile(filepath.Join(dir, MetaFile))
	if err != nil {
		return Dataset{}, fmt.Errorf("eval: read dataset metadata: %w", err)
	}
	var meta Meta
	decoder := json.NewDecoder(bytes.NewReader(metaBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&meta); err != nil {
		return Dataset{}, fmt.Errorf("eval: decode dataset metadata: %w", err)
	}
	if err := validateMeta(meta); err != nil {
		return Dataset{}, err
	}

	casesBytes, err := os.ReadFile(filepath.Join(dir, CasesFile))
	if err != nil {
		return Dataset{}, fmt.Errorf("eval: read dataset cases: %w", err)
	}
	cases, err := parseCases(casesBytes)
	if err != nil {
		return Dataset{}, err
	}
	if len(cases) == 0 {
		return Dataset{}, fmt.Errorf("eval: dataset %s has no cases", meta.Name)
	}

	dataset := Dataset{Meta: meta, Cases: cases, SHA256: hash(casesBytes), Dir: dir}
	if err := validateCases(dataset); err != nil {
		return Dataset{}, err
	}
	return dataset, nil
}

func validateMeta(meta Meta) error {
	if strings.TrimSpace(meta.Name) == "" {
		return fmt.Errorf("eval: dataset metadata needs a name")
	}
	if strings.TrimSpace(meta.Version) == "" {
		return fmt.Errorf("eval: dataset %s needs a version", meta.Name)
	}
	if !meta.Component.Valid() {
		return fmt.Errorf("eval: dataset %s has unknown component %q", meta.Name, meta.Component)
	}
	if meta.CreatedAt != "" {
		if _, err := time.Parse("2006-01-02", meta.CreatedAt); err != nil {
			return fmt.Errorf("eval: dataset %s created_at must be YYYY-MM-DD", meta.Name)
		}
	}
	return nil
}

func parseCases(data []byte) ([]Case, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	// Cases can carry long passages; do not silently truncate them.
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	cases := make([]Case, 0, 64)
	line := 0
	for scanner.Scan() {
		line++
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var c Case
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&c); err != nil {
			return nil, fmt.Errorf("eval: decode case on line %d: %w", line, err)
		}
		cases = append(cases, c)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("eval: read cases: %w", err)
	}
	return cases, nil
}

func validateCases(dataset Dataset) error {
	seen := make(map[string]struct{}, len(dataset.Cases))
	// fingerprint catches the same case copied into both splits under a new id.
	fingerprints := make(map[string]Split, len(dataset.Cases))
	for index, c := range dataset.Cases {
		where := fmt.Sprintf("case %d", index+1)
		if strings.TrimSpace(c.ID) == "" {
			return fmt.Errorf("eval: %s has no id", where)
		}
		if _, exists := seen[c.ID]; exists {
			return fmt.Errorf("eval: duplicate case id %q", c.ID)
		}
		seen[c.ID] = struct{}{}
		if c.Component != dataset.Meta.Component {
			return fmt.Errorf("eval: case %q component %q does not match dataset component %q",
				c.ID, c.Component, dataset.Meta.Component)
		}
		if !c.Split.Valid() {
			return fmt.Errorf("eval: case %q needs split dev or test", c.ID)
		}
		if len(c.Input) == 0 || !json.Valid(c.Input) || string(bytes.TrimSpace(c.Input)) == "null" {
			return fmt.Errorf("eval: case %q has no valid input JSON", c.ID)
		}
		if len(c.Expected) == 0 || !json.Valid(c.Expected) || string(bytes.TrimSpace(c.Expected)) == "null" {
			return fmt.Errorf("eval: case %q has no valid expected JSON", c.ID)
		}
		if strings.TrimSpace(c.Source) == "" {
			return fmt.Errorf("eval: case %q needs a source", c.ID)
		}
		if strings.TrimSpace(c.Labeler) == "" {
			return fmt.Errorf("eval: case %q needs a labeler", c.ID)
		}
		if _, err := time.Parse("2006-01-02", c.Date); err != nil {
			return fmt.Errorf("eval: case %q date must be YYYY-MM-DD", c.ID)
		}
		fingerprint := caseFingerprint(c)
		if previous, exists := fingerprints[fingerprint]; exists && previous != c.Split {
			return fmt.Errorf("eval: case %q duplicates a case in the %s split", c.ID, previous)
		}
		fingerprints[fingerprint] = c.Split
	}
	return nil
}

// caseFingerprint identifies the same labelled input regardless of its id, so
// copying a case into the held-out split is rejected.
func caseFingerprint(c Case) string {
	var input, expected any
	_ = json.Unmarshal(c.Input, &input)
	_ = json.Unmarshal(c.Expected, &expected)
	normalizedInput, _ := json.Marshal(input)
	normalizedExpected, _ := json.Marshal(expected)
	return string(c.Component) + "\x00" + string(normalizedInput) + "\x00" + string(normalizedExpected)
}

func hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// SplitCounts returns case counts per split, for dataset hygiene tests and
// report headers.
func (d Dataset) SplitCounts() map[Split]int {
	counts := map[Split]int{SplitDev: 0, SplitTest: 0}
	for _, c := range d.Cases {
		counts[c.Split]++
	}
	return counts
}

// SortedIDs is a deterministic key list, useful in diagnostics.
func (d Dataset) SortedIDs() []string {
	ids := make([]string, 0, len(d.Cases))
	for _, c := range d.Cases {
		ids = append(ids, c.ID)
	}
	sort.Strings(ids)
	return ids
}
