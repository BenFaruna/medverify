package verify

import (
	"math"
	"strconv"
	"strings"

	"github.com/benfaruna/medverify/internal/registry"
)

// strengthAmount is a strength converted to a base unit per base unit. Simple
// strengths such as "500 mg" have an empty denominator.
type strengthAmount struct {
	value float64
	unit  string
	per   string
	ok    bool
}

var massToMG = map[string]float64{
	"mg":  1,
	"g":   1000,
	"kg":  1_000_000,
	"mcg": 0.001,
}

var volumeToML = map[string]float64{
	"mL": 1,
	"L":  1000,
}

func normalizeStrength(strength registry.Strength) strengthAmount {
	if !strength.Parsed() {
		return strengthAmount{}
	}
	value, unit, ok := convertUnit(*strength.Value, strength.Unit)
	if !ok {
		return strengthAmount{}
	}
	per := normalizePerUnit(strength.PerUnit)
	if per == "" && strength.PerUnit != "" {
		// An unparseable denominator cannot be compared reliably.
		return strengthAmount{}
	}
	if per != "" {
		perValue, perUnit, ok := parsePerUnit(strength.PerUnit)
		if !ok || perUnit == "" {
			return strengthAmount{}
		}
		if perValue != 0 {
			value /= perValue
		}
		per = perUnit
	}
	return strengthAmount{value: value, unit: unit, per: per, ok: true}
}

// strengthsAgree compares two parsed strengths after unit conversion. It
// returns ok=false when either cannot be compared; callers must treat that as
// unknown, never as a mismatch.
// StrengthsAgree reports whether two strengths are comparable and within a
// relative tolerance. It is exported so the safety checker asks exactly the
// same question as phase 05 instead of growing a second comparison.
func StrengthsAgree(label, record registry.Strength, tolerance float64) (agree bool, comparable bool) {
	return strengthsAgree(label, record, tolerance)
}

func strengthsAgree(label, record registry.Strength, tolerance float64) (agree bool, comparable bool) {
	left := normalizeStrength(label)
	right := normalizeStrength(record)
	if !left.ok || !right.ok || left.unit != right.unit || left.per != right.per {
		return false, false
	}
	scale := math.Max(math.Abs(left.value), math.Abs(right.value))
	if scale == 0 {
		return left.value == right.value, true
	}
	return math.Abs(left.value-right.value)/scale <= tolerance, true
}

func convertUnit(value float64, unit string) (float64, string, bool) {
	if factor, ok := massToMG[unit]; ok {
		return value * factor, "mg", true
	}
	if factor, ok := volumeToML[unit]; ok {
		return value * factor, "mL", true
	}
	switch unit {
	case "IU", "unit", "mEq", "mmol", "%":
		return value, unit, true
	default:
		return 0, "", false
	}
}

// normalizePerUnit returns a comparable token for a denominator, or the empty
// string when there is none. It is used only to decide whether two strengths
// share a shape.
func normalizePerUnit(per string) string {
	per = strings.TrimSpace(strings.ToLower(per))
	if per == "" {
		return ""
	}
	if _, unit, ok := parsePerUnit(per); ok {
		return unit
	}
	return ""
}

// parsePerUnit parses denominators such as "5 mL", "1mL", or "mL".
func parsePerUnit(per string) (float64, string, bool) {
	per = strings.TrimSpace(per)
	if per == "" {
		return 0, "", false
	}
	fields := strings.Fields(per)
	if len(fields) == 1 {
		if value, unit, ok := splitNumberUnit(fields[0]); ok {
			return value, unit, true
		}
		if value, unit, ok := convertUnit(1, canonicalUnit(fields[0])); ok {
			return value, unit, true
		}
		return 0, "", false
	}
	if len(fields) == 2 {
		amount, err := strconv.ParseFloat(strings.ReplaceAll(fields[0], ",", ""), 64)
		if err != nil {
			return 0, "", false
		}
		value, unit, ok := convertUnit(amount, canonicalUnit(fields[1]))
		return value, unit, ok
	}
	return 0, "", false
}

func splitNumberUnit(value string) (float64, string, bool) {
	index := 0
	for index < len(value) && (value[index] >= '0' && value[index] <= '9' || value[index] == '.') {
		index++
	}
	if index == 0 {
		return 0, "", false
	}
	amount, err := strconv.ParseFloat(value[:index], 64)
	if err != nil {
		return 0, "", false
	}
	converted, unit, ok := convertUnit(amount, canonicalUnit(value[index:]))
	return converted, unit, ok
}

func canonicalUnit(unit string) string {
	unit = strings.TrimSpace(unit)
	switch strings.ToLower(unit) {
	case "mg":
		return "mg"
	case "g":
		return "g"
	case "kg":
		return "kg"
	case "mcg", "ug", "µg", "μg":
		return "mcg"
	case "ml":
		return "mL"
	case "l":
		return "L"
	case "iu":
		return "IU"
	case "unit", "units", "u":
		return "unit"
	case "meq":
		return "mEq"
	case "mmol":
		return "mmol"
	case "%":
		return "%"
	default:
		return unit
	}
}
