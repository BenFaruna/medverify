package registry

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// sectionHeading matches "1. Name", "4.1 Therapeutic indications",
// "2) Composition", and "SECTION 4.2 ...". The heading text is checked
// separately so numbered lists inside body text are not mistaken for headings.
var sectionHeading = regexp.MustCompile(`(?i)^(?:section\s+)?(\d{1,2}(?:\.\d{1,2})*)[.):]?\s+(.+)$`)

// headingKeywords are common words in a medicines leaflet heading. A numbered
// line must either look like a title (mostly uppercase) or contain one of these
// words; this keeps "1. Take one tablet twice a day." in the body.
var headingKeywords = []string{
	"name", "composition", "pharmaceutical", "clinical", "indication",
	"dosage", "administration", "contra", "warning", "precaution",
	"interaction", "pregnancy", "lactation", "fertility", "effect",
	"overdose", "property", "storage", "shelf", "holder", "manufacturer",
	"description", "presentation", "leaflet", "qualitative", "quantitative",
	"therapeutic", "posology", "method", "special", "adverse", "pharmaco",
	"instruction", "information", "package", "marketing", "authorisation",
}

// SplitSections splits extracted leaflet text into numbered sections. Text
// before the first heading is kept as a preamble section. If the document has
// no recognisable heading, it becomes one section with no code.
func SplitSections(text string) []SMPCSection {
	pages := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\f")
	var sections []SMPCSection
	var current *SMPCSection
	var body []string

	flush := func() {
		if current == nil {
			return
		}
		trimmed := trimBlankEdges(body)
		if current.Code == "" && current.Heading == "" && len(trimmed) == 0 {
			return
		}
		current.Text = strings.Join(trimmed, "\n")
		sections = append(sections, *current)
		current = nil
		body = nil
	}

	for pageIndex, page := range pages {
		for _, line := range strings.Split(page, "\n") {
			if code, heading, ok := parseSectionHeading(line); ok {
				flush()
				current = &SMPCSection{
					Code:      code,
					Heading:   heading,
					PageStart: pageIndex + 1,
				}
				continue
			}
			if current == nil {
				current = &SMPCSection{PageStart: pageIndex + 1}
			}
			body = append(body, line)
		}
	}
	flush()

	if len(sections) == 0 {
		trimmed := strings.TrimSpace(normalizePageBreaks(text))
		if trimmed == "" {
			return nil
		}
		return []SMPCSection{{Text: trimmed}}
	}
	return sections
}

// parseSectionHeading returns a section code and title when the line is a
// heading. Leading indentation is allowed; numbered body lines that end like a
// sentence are rejected.
func parseSectionHeading(line string) (code, heading string, ok bool) {
	match := sectionHeading.FindStringSubmatch(strings.TrimSpace(line))
	if match == nil {
		return "", "", false
	}
	heading = strings.TrimSpace(match[2])
	if !looksLikeHeading(heading) {
		return "", "", false
	}
	first := match[1]
	if dot := strings.IndexByte(first, '.'); dot >= 0 {
		first = first[:dot]
	}
	number, err := strconv.Atoi(first)
	if err != nil || number < 1 || number > 20 {
		return "", "", false
	}
	return match[1], heading, true
}

func looksLikeHeading(heading string) bool {
	if len(heading) < 2 || len(heading) > 160 {
		return false
	}
	last := heading[len(heading)-1]
	if last == '.' || last == ',' || last == ';' || last == '!' || last == '?' {
		return false
	}
	first, _ := utf8FirstRune(heading)
	if !unicode.IsUpper(first) && !unicode.IsDigit(first) {
		return false
	}
	if len(strings.Fields(heading)) > 16 {
		return false
	}
	if mostlyUppercase(heading) {
		return true
	}
	lower := strings.ToLower(heading)
	for _, keyword := range headingKeywords {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}

func mostlyUppercase(text string) bool {
	letters, upper := 0, 0
	for _, r := range text {
		if unicode.IsLetter(r) {
			letters++
			if unicode.IsUpper(r) {
				upper++
			}
		}
	}
	return letters > 0 && float64(upper)/float64(letters) >= 0.6
}

func trimBlankEdges(lines []string) []string {
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	end := len(lines)
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return lines[start:end]
}

func utf8FirstRune(text string) (rune, int) {
	for _, r := range text {
		return r, len(string(r))
	}
	return 0, 0
}
