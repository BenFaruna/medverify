package registry

import (
	_ "embed"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"go.yaml.in/yaml/v3"
	"golang.org/x/text/unicode/norm"
)

// NRNPattern is the NAFDAC registration number shape observed in the phase 02
// fixtures: an optional one- or two-letter prefix, a one- to three-digit class
// code, a dash, four to six digits, and an optional trailing letter.
//
// Keep this in one place: phase 05 uses it to decide whether a label value
// looks like an NRN before lookup.
var NRNPattern = regexp.MustCompile(`^[A-Z]{0,2}[0-9]{1,3}-[0-9]{3,6}[A-Z]?$`)

// byteOrderMark and the various dash code points found in real payloads.
const byteOrderMark = '\uFEFF'

var dashReplacer = strings.NewReplacer(
	"\u2010", "-", // hyphen
	"\u2011", "-", // non-breaking hyphen
	"\u2012", "-", // figure dash
	"\u2013", "-", // en dash
	"\u2014", "-", // em dash
	"\u2015", "-", // horizontal bar
	"\u2212", "-", // minus sign
	"\uFE63", "-", // small hyphen-minus
	"\uFF0D", "-", // fullwidth hyphen-minus
)

// NormalizeNRN trims, upper-cases, unifies dashes, and removes all whitespace
// from a Greenbook or label NRN.
//
// It is idempotent and deliberately does not validate: callers use ValidNRN
// when they need to reject a value. A placeholder such as "00-0000" normalizes
// to itself so phase 05 can flag it.
func NormalizeNRN(raw string) string {
	value := norm.NFKC.String(raw)
	value = strings.ReplaceAll(value, string(byteOrderMark), "")
	value = dashReplacer.Replace(value)
	value = strings.ToUpper(value)
	// Remove punctuation and all whitespace; NRNs are alphanumeric plus a dash.
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		switch {
		case r == '-':
			b.WriteRune(r)
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ValidNRN reports whether raw normalizes to the observed NRN shape.
func ValidNRN(raw string) bool {
	return NRNPattern.MatchString(NormalizeNRN(raw))
}

// Strength is a parsed strength expression. Raw always holds the input. Value,
// Unit, and PerUnit are nil/empty when the expression could not be parsed.
type Strength struct {
	Raw     string   `json:"raw"`
	Value   *float64 `json:"value,omitempty"`
	Unit    string   `json:"unit,omitempty"`
	PerUnit string   `json:"per_unit,omitempty"`
}

// Parsed reports whether the numeric part was understood.
func (s Strength) Parsed() bool { return s.Value != nil }

var parentheticalPattern = regexp.MustCompile(`\([^()]*\)`)

var strengthPattern = regexp.MustCompile(
	`^([0-9]+(?:[.,][0-9]+)*)\s*([[:alpha:]µμ%]+)\s*(?:/\s*([0-9]+(?:[.,][0-9]+)*)?\s*([[:alpha:]µμ%]+))?$`,
)

var unitCanonical = map[string]string{
	"mg":    "mg",
	"mcg":   "mcg",
	"µg":    "mcg",
	"μg":    "mcg",
	"ug":    "mcg",
	"g":     "g",
	"kg":    "kg",
	"ml":    "mL",
	"l":     "L",
	"iu":    "IU",
	"u":     "unit",
	"unit":  "unit",
	"units": "unit",
	"meq":   "mEq",
	"mmol":  "mmol",
	"%":     "%",
}

// ParseStrength parses one strength expression such as "500 mg", "120 mg/5 mL",
// "10,000 IU/1 mL", or "0.05%". Unparseable expressions keep Raw and leave the
// numeric fields empty; they are never dropped.
func ParseStrength(raw string) Strength {
	strength := Strength{Raw: strings.TrimSpace(raw)}
	compact := strings.TrimSpace(norm.NFKC.String(raw))
	if compact == "" {
		return strength
	}

	matches := strengthPattern.FindStringSubmatch(compact)
	if matches == nil {
		return strength
	}

	value, err := parseDecimal(matches[1])
	if err != nil {
		return strength
	}
	strength.Value = &value
	strength.Unit = canonicalUnit(matches[2])

	if matches[4] != "" {
		denominator := ""
		if matches[3] != "" {
			denominator = strings.TrimSpace(matches[3]) + " "
		}
		strength.PerUnit = denominator + canonicalUnit(matches[4])
	}
	return strength
}

// ParseStrengths splits a Greenbook strength list and parses each component.
// Empty components are skipped.
func ParseStrengths(raw string) []Strength {
	parts := strings.Split(raw, ";")
	strengths := make([]Strength, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		strengths = append(strengths, ParseStrength(part))
	}
	return strengths
}

func parseDecimal(value string) (float64, error) {
	// NAFDAC uses commas as thousands separators, never as decimal points in
	// the captured data. "10,000" means ten thousand.
	value = strings.ReplaceAll(value, ",", "")
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("parse strength value %q: %w", value, err)
	}
	return parsed, nil
}

func canonicalUnit(unit string) string {
	lower := strings.ToLower(strings.TrimSpace(unit))
	if canonical, ok := unitCanonical[lower]; ok {
		return canonical
	}
	return lower
}

// Normalizer normalizes ingredient names using the embedded synonym table and
// records names it does not know so the ingestion report can show them.
//
// A Normalizer is safe for concurrent use, but a sync uses one instance so the
// unknown-name counts are complete.
type Normalizer struct {
	table *synonymTable

	mu      sync.Mutex
	unknown map[string]int
}

// NewNormalizer builds a Normalizer from the embedded table. It does not fail:
// the table is parsed at init, and a malformed embedded file is a programming
// error.
func NewNormalizer() *Normalizer {
	return &Normalizer{table: defaultTable, unknown: make(map[string]int)}
}

// Ingredient returns the canonical form of one ingredient name. Unknown names
// pass through unchanged and are counted for the report.
func (n *Normalizer) Ingredient(raw string) string {
	name := normalizeIngredient(n.table, raw)
	if name == "" {
		return ""
	}
	if !n.table.known(name) {
		n.mu.Lock()
		n.unknown[name]++
		n.mu.Unlock()
	}
	return name
}

// Unknown returns a copy of the unknown ingredient counts, keyed by normalized
// name.
func (n *Normalizer) Unknown() map[string]int {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make(map[string]int, len(n.unknown))
	for name, count := range n.unknown {
		out[name] = count
	}
	return out
}

// NormalizeIngredient returns the canonical form of one ingredient name using
// the embedded table. It is a convenience for callers that do not need the
// unknown-name report.
func NormalizeIngredient(raw string) string {
	return normalizeIngredient(defaultTable, raw)
}

// KnownIngredient reports whether a normalized ingredient name is in the
// synonym table. The ingestion report uses it to show which names to add next.
func KnownIngredient(normalized string) bool {
	return defaultTable.known(normalized)
}

func normalizeIngredient(table *synonymTable, raw string) string {
	name := cleanIngredient(raw)
	if name == "" {
		return ""
	}
	name = stripTrailingQualifiers(table, name)
	name = stripTrailingQualifiers(table, name)
	if canonical, ok := table.synonyms[name]; ok {
		name = canonical
	}
	return name
}

func cleanIngredient(raw string) string {
	value := html.UnescapeString(raw)
	value = norm.NFKC.String(value)
	value = strings.ReplaceAll(value, string(byteOrderMark), "")
	value = dashReplacer.Replace(value)
	value = strings.ToLower(value)

	// Drop parenthetical qualifiers but keep the text around them:
	// "Vitamin B12 (Cyanocobalamin)" is vitamin B12 for matching, and
	// "Human papillomavirus (types 16 & 18) L1 protein vaccine" keeps its tail.
	// Repeat for simple nesting; the raw name always keeps the detail.
	for range 3 {
		cleaned := parentheticalPattern.ReplaceAllString(value, " ")
		if cleaned == value {
			break
		}
		value = cleaned
	}

	var b strings.Builder
	b.Grow(len(value))
	lastSpace := true
	for _, r := range value {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
			lastSpace = false
		default:
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		}
	}
	return strings.TrimSpace(strings.Trim(b.String(), " ,.;:"))
}

func stripTrailingQualifiers(table *synonymTable, name string) string {
	tokens := strings.Fields(name)
	for len(tokens) > 1 {
		last := strings.Trim(tokens[len(tokens)-1], ".,")
		if _, ok := table.qualifiers[last]; !ok {
			break
		}
		candidate := strings.TrimSpace(strings.Join(tokens[:len(tokens)-1], " "))
		if candidate == "" {
			break
		}
		if _, ok := table.standalone[candidate]; ok {
			// "sodium chloride" is a compound, not a salt of sodium.
			break
		}
		tokens = strings.Fields(candidate)
	}
	return strings.Join(tokens, " ")
}

type synonymFile struct {
	Synonyms   map[string][]string `yaml:"synonyms"`
	Qualifiers []string            `yaml:"qualifiers"`
}

type synonymTable struct {
	synonyms   map[string]string
	qualifiers map[string]struct{}
	standalone map[string]struct{}
}

func (t *synonymTable) known(name string) bool {
	_, ok := t.synonyms[name]
	return ok
}

//go:embed synonyms.yaml
var synonymsYAML []byte

var defaultTable = mustLoadTable(synonymsYAML)

func mustLoadTable(data []byte) *synonymTable {
	var file synonymFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		panic("registry: parse embedded synonyms.yaml: " + err.Error())
	}
	table := &synonymTable{
		synonyms:   make(map[string]string, len(file.Synonyms)),
		qualifiers: make(map[string]struct{}, len(file.Qualifiers)),
		standalone: map[string]struct{}{
			"aluminium": {}, "aluminum": {}, "ammonium": {}, "calcium": {},
			"copper": {}, "ferric": {}, "ferrous": {}, "iron": {}, "lithium": {},
			"magnesium": {}, "potassium": {}, "sodium": {}, "zinc": {},
		},
	}
	for canonical, variants := range file.Synonyms {
		key := cleanIngredient(canonical)
		table.synonyms[key] = key
		for _, variant := range variants {
			table.synonyms[cleanIngredient(variant)] = key
		}
	}
	for _, qualifier := range file.Qualifiers {
		table.qualifiers[strings.ToLower(strings.TrimSpace(qualifier))] = struct{}{}
	}
	return table
}
