package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"
)

// ParsePage decodes one Greenbook DataTables response. A malformed row is
// collected in Page.RowErrors and does not abort the page; a malformed envelope
// is an error because the source contract changed.
func ParsePage(data []byte, fetchedAt time.Time, normalizer *Normalizer) (Page, error) {
	if normalizer == nil {
		normalizer = NewNormalizer()
	}

	var envelope rawPage
	if err := json.Unmarshal(data, &envelope); err != nil {
		return Page{}, fmt.Errorf("decode Greenbook page: %w", err)
	}

	page := Page{
		RecordsTotal:    envelope.RecordsTotal,
		RecordsFiltered: envelope.RecordsFiltered,
		Products:        make([]Product, 0, len(envelope.Data)),
	}
	for i, raw := range envelope.Data {
		product, err := ParseProduct(raw, fetchedAt, normalizer)
		if err != nil {
			page.RowErrors = append(page.RowErrors, RowError{Index: i, Raw: raw, Err: err})
			continue
		}
		page.Products = append(page.Products, product)
	}
	return page, nil
}

// ParseProduct decodes and normalizes one row of the Greenbook list payload.
func ParseProduct(raw []byte, fetchedAt time.Time, normalizer *Normalizer) (Product, error) {
	if normalizer == nil {
		normalizer = NewNormalizer()
	}

	var row rawProduct
	if err := json.Unmarshal(raw, &row); err != nil {
		return Product{}, fmt.Errorf("decode Greenbook product: %w", err)
	}

	product := Product{
		SourceID:             string(row.ProductID),
		NRN:                  NormalizeNRN(row.NAFDAC),
		Name:                 cleanLine(row.ProductName),
		Category:             cleanLine(firstNonEmpty(row.CategoryName, row.ProductCategory.Name)),
		Status:               cleanLine(row.Status),
		Applicant:            cleanLine(firstNonEmpty(row.ApplicantName, row.Applicant.Name)),
		ManufacturerSourceID: string(row.ManufacturerID),
		CompositionRaw:       cleanText(row.Composition),
		Form:                 cleanLine(firstNonEmpty(row.FormName, row.Form.Name)),
		Route:                cleanLine(firstNonEmpty(row.RouteName, row.Route.Name)),
		StrengthRaw:          cleanLine(row.Strength),
		PackSize:             cleanLine(row.PackSize),
		ATCCode:              cleanLine(row.ATC),
		ApprovedAt:           parseDate(row.ApprovalDate),
		ExpiresAt:            parseDate(row.ExpiryDate),
		SmpcURL:              cleanLine(row.SMPC),
		Description:          cleanText(row.Description),
		Raw:                  append([]byte(nil), raw...),
		FetchedAt:            fetchedAt,
	}
	product.Ingredients = parseIngredients(row, normalizer)
	return product, nil
}

// PayloadHash is the SHA-256 of the row exactly as received, used to skip
// unchanged products.
func (p Product) PayloadHash() string {
	hash := sha256.Sum256(p.Raw)
	return hex.EncodeToString(hash[:])
}

func parseIngredients(row rawProduct, normalizer *Normalizer) []Ingredient {
	names := splitList(firstNonEmpty(row.IngredientName, row.Ingredient.IngredientName))
	strengths := ParseStrengths(row.Strength)

	ingredients := make([]Ingredient, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for i, name := range names {
		rawName := cleanLine(name)
		if rawName == "" {
			continue
		}

		var strength Strength
		strengthRaw := ""
		if i < len(strengths) {
			strength = strengths[i]
			strengthRaw = strength.Raw
		}

		normalized := normalizer.Ingredient(rawName)
		key := ingredientKey(normalized, strength)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		ingredients = append(ingredients, Ingredient{
			NameRaw:     rawName,
			NameNorm:    normalized,
			Strength:    strength,
			StrengthRaw: strengthRaw,
		})
	}
	return ingredients
}

func ingredientKey(name string, strength Strength) string {
	if strength.Value == nil {
		return name + "|raw:" + strength.Raw
	}
	return name + "|" + strconv.FormatFloat(*strength.Value, 'f', -1, 64) + "|" + strength.Unit
}

func splitList(value string) []string {
	parts := strings.Split(value, ";")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func parseDate(value string) *time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return nil
	}
	return &parsed
}

// cleanLine decodes HTML entities, drops control characters, and collapses
// whitespace for a single-line field.
func cleanLine(value string) string {
	value = html.UnescapeString(value)
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, value)
	return strings.Join(strings.Fields(value), " ")
}

// cleanText decodes HTML entities and normalizes line endings while preserving
// the paragraph structure of free text.
func cleanText(value string) string {
	value = html.UnescapeString(value)
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// flexString accepts either a JSON string or a number for fields that change
// type between Greenbook payloads (for example form_id and product_id).
type flexString string

func (f *flexString) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*f = ""
		return nil
	}
	if len(data) > 0 && data[0] == '"' {
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		*f = flexString(value)
		return nil
	}
	*f = flexString(string(data))
	return nil
}

type rawPage struct {
	RecordsTotal    int               `json:"recordsTotal"`
	RecordsFiltered int               `json:"recordsFiltered"`
	Data            []json.RawMessage `json:"data"`
}

type named struct {
	Name string `json:"name"`
}

type rawProduct struct {
	ProductID      flexString `json:"product_id"`
	ProductName    string     `json:"product_name"`
	NAFDAC         string     `json:"NAFDAC"`
	Status         string     `json:"status"`
	CategoryName   string     `json:"category_name"`
	ApplicantName  string     `json:"applicant_name"`
	IngredientName string     `json:"ingredient_name"`
	FormName       string     `json:"form_name"`
	RouteName      string     `json:"route_name"`
	Strength       string     `json:"strength"`
	Composition    string     `json:"composition"`
	PackSize       string     `json:"pack_size"`
	ATC            string     `json:"atc"`
	ApprovalDate   string     `json:"approval_date"`
	ExpiryDate     string     `json:"expiry_date"`
	SMPC           string     `json:"smpc"`
	Description    string     `json:"product_description"`
	ManufacturerID flexString `json:"manufacturer_id"`

	ProductCategory named `json:"product_category"`
	Applicant       named `json:"applicant"`
	Form            named `json:"form"`
	Route           named `json:"route"`
	Ingredient      struct {
		IngredientName string `json:"ingredient_name"`
	} `json:"ingredient"`
}
