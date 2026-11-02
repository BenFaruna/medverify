package vision

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// dayLayouts are unambiguous formats: they always name the month, or put the
// year first. A format that could mean either day-first or month-first is not
// in this list on purpose.
var dayLayouts = []string{
	"02 Jan 2006",
	"2 Jan 2006",
	"02 January 2006",
	"2 January 2006",
	"Jan 02 2006",
	"Jan 2 2006",
	"January 02 2006",
	"January 2 2006",
	"2006-01-02",
	"2006/01/02",
	"2006.01.02",
}

// monthLayouts carry no day. The parsed date is the first of the month and the
// precision records that the day is unknown.
var monthLayouts = []string{
	"Jan 2006",
	"January 2006",
	"2006 Jan",
	"2006 January",
	"2006-01",
	"2006/01",
	"01/2006",
	"01-2006",
	"01.2006",
}

var (
	slashDatePattern = regexp.MustCompile(`^([0-9]{1,2})[/.-]([0-9]{1,2})[/.-]([0-9]{4})$`)
	monthYearPattern = regexp.MustCompile(`^([0-9]{1,2})[/.-]([0-9]{4})$`)
)

// ParseLabelDate parses the date formats that appear on Nigerian medicine
// packs. Ambiguous numeric dates such as 03/04/2026 are rejected: a wrong
// expiry date is worse than an unreadable one.
func ParseLabelDate(raw string) (LabelDate, bool) {
	value := normalizeDateText(raw)
	if value == "" {
		return LabelDate{}, false
	}

	for _, layout := range dayLayouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return LabelDate{
				Year:      parsed.Year(),
				Month:     int(parsed.Month()),
				Day:       parsed.Day(),
				Precision: "day",
				Raw:       strings.TrimSpace(raw),
			}, true
		}
	}

	for _, layout := range monthLayouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return LabelDate{
				Year:      parsed.Year(),
				Month:     int(parsed.Month()),
				Precision: "month",
				Raw:       strings.TrimSpace(raw),
			}, true
		}
	}

	// A numeric day/month/year date is unambiguous only when the first
	// component cannot be a month. If the second component cannot be a month,
	// the label used month-first ordering; we still reject it because locale is
	// unknown.
	if matches := slashDatePattern.FindStringSubmatch(value); matches != nil {
		first, _ := strconv.Atoi(matches[1])
		second, _ := strconv.Atoi(matches[2])
		year, _ := strconv.Atoi(matches[3])
		if first > 12 && second >= 1 && second <= 12 {
			return dateFromParts(year, second, first, raw, "day")
		}
		return LabelDate{}, false
	}

	if matches := monthYearPattern.FindStringSubmatch(value); matches != nil {
		month, _ := strconv.Atoi(matches[1])
		year, _ := strconv.Atoi(matches[2])
		if month >= 1 && month <= 12 && year >= 1900 && year <= 2200 {
			return LabelDate{Year: year, Month: month, Precision: "month", Raw: strings.TrimSpace(raw)}, true
		}
	}

	return LabelDate{}, false
}

// Format renders a date in ISO form. Month precision renders YYYY-MM; day
// precision renders YYYY-MM-DD.
func (d LabelDate) Format() string {
	if d.Precision == "month" {
		return time.Date(d.Year, time.Month(d.Month), 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
	}
	return time.Date(d.Year, time.Month(d.Month), d.Day, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
}

func dateFromParts(year, month, day int, raw, precision string) (LabelDate, bool) {
	if year < 1900 || year > 2200 || month < 1 || month > 12 || day < 1 || day > 31 {
		return LabelDate{}, false
	}
	// time.Date normalizes impossible dates (31 Feb becomes 3 Mar), so check
	// that the components survived normalization.
	candidate := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if candidate.Year() != year || int(candidate.Month()) != month || candidate.Day() != day {
		return LabelDate{}, false
	}
	return LabelDate{Year: year, Month: month, Day: day, Precision: precision, Raw: strings.TrimSpace(raw)}, true
}

func normalizeDateText(raw string) string {
	value := strings.TrimSpace(raw)
	value = strings.ReplaceAll(value, "\u00a0", " ")
	value = strings.Join(strings.Fields(value), " ")
	value = strings.TrimSuffix(value, ".")
	// Collapse separators such as "03 - 2027" without touching month names.
	value = strings.ReplaceAll(value, " / ", "/")
	value = strings.ReplaceAll(value, " - ", "-")
	value = strings.ReplaceAll(value, " . ", ".")
	return value
}
