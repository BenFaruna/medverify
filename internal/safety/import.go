package safety

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/benfaruna/medverify/internal/registry"
	"github.com/benfaruna/medverify/internal/verify"
)

// importColumns is the exact CSV header the admin import accepts. A human
// reviewer supplies the file; no scraper writes here directly.
var importColumns = []string{
	"kind",
	"nrn",
	"product_name",
	"manufacturer",
	"ingredient",
	"reason",
	"effective_until",
	"review_due_at",
	"issuing_body",
	"source_title",
	"source_url",
	"issued_at",
	"reviewed_by",
}

// ImportError is one invalid value, tied to a CSV line so an operator can fix
// the file without guessing.
type ImportError struct {
	Line    int
	Column  string
	Message string
}

func (e ImportError) Error() string {
	if e.Line <= 1 {
		return fmt.Sprintf("header: %s: %s", e.Column, e.Message)
	}
	return fmt.Sprintf("line %d: %s: %s", e.Line, e.Column, e.Message)
}

// ValidationError collects every problem in a file. Nothing is imported when
// it is returned: a partially loaded watchlist is worse than a rejected one.
type ValidationError struct {
	Problems []ImportError
}

func (e ValidationError) Error() string {
	lines := make([]string, 0, len(e.Problems)+1)
	lines = append(lines, fmt.Sprintf("%d invalid watchlist rows:", len(e.Problems)))
	for _, problem := range e.Problems {
		lines = append(lines, "  "+problem.Error())
	}
	return strings.Join(lines, "\n")
}

// ParseImportCSV validates a whole watchlist CSV. It returns entries only when
// every row is valid. Values are normalized with the same functions the
// matcher uses, so what is stored is what is compared.
func ParseImportCSV(r io.Reader) ([]ImportEntry, error) {
	reader := csv.NewReader(r)
	reader.TrimLeadingSpace = true

	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("read watchlist CSV header: %w", err)
	}
	positions, err := headerPositions(header)
	if err != nil {
		return nil, err
	}

	var entries []ImportEntry
	var problems []ImportError
	seen := make(map[string]int)

	for line := 2; ; line++ {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// A quoting error can desynchronise the remaining rows; stop and
			// report it rather than guessing where the next row starts.
			problems = append(problems, ImportError{Line: line, Column: "csv", Message: err.Error()})
			break
		}
		if blankRecord(record) {
			continue
		}

		row := rowReader{record: record, positions: positions, line: line}
		entry, rowProblems := parseImportRow(row)
		if len(rowProblems) > 0 {
			problems = append(problems, rowProblems...)
			continue
		}

		key := importKey(entry)
		if previous, ok := seen[key]; ok {
			problems = append(problems, ImportError{
				Line: line, Column: "row",
				Message: fmt.Sprintf("duplicate of line %d", previous),
			})
			continue
		}
		seen[key] = line
		entries = append(entries, entry)
	}

	if len(problems) > 0 {
		return nil, ValidationError{Problems: problems}
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("watchlist CSV has no rows")
	}
	return entries, nil
}

func headerPositions(header []string) (map[string]int, error) {
	positions := make(map[string]int, len(header))
	var problems []ImportError
	for index, raw := range header {
		name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff")))
		if name == "" {
			problems = append(problems, ImportError{Column: "header", Message: "empty column name"})
			continue
		}
		if _, duplicate := positions[name]; duplicate {
			problems = append(problems, ImportError{Column: name, Message: "duplicate column"})
			continue
		}
		positions[name] = index
	}
	for _, required := range importColumns {
		if _, ok := positions[required]; !ok {
			problems = append(problems, ImportError{Column: required, Message: "required column is missing"})
		}
	}
	for name := range positions {
		if !knownImportColumn(name) {
			problems = append(problems, ImportError{Column: name, Message: "unknown column"})
		}
	}
	if len(problems) > 0 {
		return nil, ValidationError{Problems: problems}
	}
	return positions, nil
}

func knownImportColumn(name string) bool {
	for _, column := range importColumns {
		if name == column {
			return true
		}
	}
	return false
}

type rowReader struct {
	record    []string
	positions map[string]int
	line      int
}

func (r rowReader) value(column string) string {
	index, ok := r.positions[column]
	if !ok || index >= len(r.record) {
		return ""
	}
	return strings.TrimSpace(r.record[index])
}

func (r rowReader) fail(column, message string) ImportError {
	return ImportError{Line: r.line, Column: column, Message: message}
}

func parseImportRow(row rowReader) (ImportEntry, []ImportError) {
	var problems []ImportError

	kind := EntryKind(strings.ToLower(row.value("kind")))
	if !kind.Valid() {
		problems = append(problems, row.fail("kind", "must be alert, withdrawal, banned_ingredient, or restricted_ingredient"))
	}

	entry := ImportEntry{
		Kind:        kind,
		Reason:      row.value("reason"),
		ReviewedBy:  row.value("reviewed_by"),
		Source:      SourceDocument{},
		ProductName: row.value("product_name"),
		NRN:         row.value("nrn"),
	}

	if entry.Reason == "" {
		problems = append(problems, row.fail("reason", "is required"))
	}
	if entry.ReviewedBy == "" {
		problems = append(problems, row.fail("reviewed_by", "is required"))
	}

	if kind.IsIngredient() {
		if entry.NRN != "" || entry.ProductName != "" || row.value("manufacturer") != "" {
			problems = append(problems, row.fail("kind", "ingredient entries must not name a product or NRN"))
		}
		rawIngredient := row.value("ingredient")
		entry.IngredientNorm = registry.NormalizeIngredient(rawIngredient)
		if entry.IngredientNorm == "" {
			problems = append(problems, row.fail("ingredient", "is required for this kind"))
		}
	} else if kind.Valid() {
		if row.value("ingredient") != "" {
			problems = append(problems, row.fail("ingredient", "is only allowed for ingredient kinds"))
		}
		if entry.NRN != "" {
			entry.NRN = registry.NormalizeNRN(entry.NRN)
			if !registry.NRNPattern.MatchString(entry.NRN) {
				problems = append(problems, row.fail("nrn", "is not a valid NRN shape"))
			}
		}
		entry.ProductNameNorm = verify.NormalizeName(entry.ProductName)
		if entry.ProductName != "" && entry.ProductNameNorm == "" {
			problems = append(problems, row.fail("product_name", "has no comparable text"))
		}
		entry.Manufacturer = row.value("manufacturer")
		entry.ManufacturerNorm = verify.NormalizeManufacturer(entry.Manufacturer)
		if entry.Manufacturer != "" && entry.ProductName == "" {
			problems = append(problems, row.fail("manufacturer", "requires a product_name"))
		}
		if entry.NRN == "" && entry.ProductName == "" {
			problems = append(problems, row.fail("nrn", "a product entry needs an NRN or a product_name"))
		}
	}

	issuedAt, problem := parseDateColumn(row, "issued_at", true)
	if problem != nil {
		problems = append(problems, *problem)
	} else if issuedAt != nil {
		entry.Source.IssuedAt = *issuedAt
	}

	entry.Source.IssuingBody = row.value("issuing_body")
	entry.Source.Title = row.value("source_title")
	entry.Source.URL = row.value("source_url")
	if entry.Source.IssuingBody == "" {
		problems = append(problems, row.fail("issuing_body", "is required"))
	}
	if entry.Source.Title == "" {
		problems = append(problems, row.fail("source_title", "is required"))
	}
	if message := validateSourceURL(entry.Source.URL); message != "" {
		problems = append(problems, row.fail("source_url", message))
	}

	if effectiveUntil, problem := parseDateColumn(row, "effective_until", false); problem != nil {
		problems = append(problems, *problem)
	} else {
		entry.EffectiveUntil = effectiveUntil
	}
	if reviewDue, problem := parseDateColumn(row, "review_due_at", false); problem != nil {
		problems = append(problems, *problem)
	} else {
		entry.ReviewDueAt = reviewDue
		if reviewDue != nil && issuedAt != nil && reviewDue.Before(*issuedAt) {
			problems = append(problems, row.fail("review_due_at", "must not be before issued_at"))
		}
	}

	return entry, problems
}

func parseDateColumn(row rowReader, column string, required bool) (*time.Time, *ImportError) {
	raw := row.value(column)
	if raw == "" {
		if required {
			problem := row.fail(column, "is required (YYYY-MM-DD)")
			return nil, &problem
		}
		return nil, nil
	}
	parsed, err := time.Parse(time.DateOnly, raw)
	if err != nil {
		problem := row.fail(column, "must be a date in YYYY-MM-DD form")
		return nil, &problem
	}
	date := DateOnly(parsed)
	return &date, nil
}

func validateSourceURL(raw string) string {
	if raw == "" {
		return "is required"
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "is not a valid URL"
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "must use http or https"
	}
	if parsed.Host == "" {
		return "must include a host"
	}
	return ""
}

func importKey(entry ImportEntry) string {
	switch {
	case entry.IngredientNorm != "":
		return "ingredient|" + entry.IngredientNorm + "|" + entry.Source.URL
	case entry.NRN != "":
		return "nrn|" + entry.NRN + "|" + entry.Source.URL
	default:
		return "name|" + entry.ProductNameNorm + "|" + entry.ManufacturerNorm + "|" + entry.Source.URL
	}
}

func blankRecord(record []string) bool {
	for _, value := range record {
		if strings.TrimSpace(value) != "" {
			return false
		}
	}
	return true
}
