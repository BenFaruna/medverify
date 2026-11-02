package verify

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/benfaruna/medverify/internal/registry"
	"github.com/benfaruna/medverify/internal/vision"
)

// Catalog is the only database seam. It fetches candidates; all comparison
// happens in pure functions over the returned structs.
type Catalog interface {
	GetByNRN(ctx context.Context, rawNRN string) (registry.Product, bool, error)
	SearchByName(ctx context.Context, query string, limit int) ([]registry.SearchResult, error)
	Ingredients(ctx context.Context, productID int64) ([]registry.Ingredient, error)
	CountProducts(ctx context.Context) (int, error)
	LatestSuccessfulSync(ctx context.Context) (time.Time, bool, error)
}

// Options configures a Matcher.
type Options struct {
	Catalog Catalog
	Config  Config
	Now     func() time.Time
}

// Matcher performs registry verification.
type Matcher struct {
	catalog Catalog
	config  Config
	now     func() time.Time
}

// NewMatcher builds a matcher. A nil clock uses time.Now.
func NewMatcher(opts Options) (*Matcher, error) {
	if opts.Catalog == nil {
		return nil, fmt.Errorf("verify: matcher requires a catalog")
	}
	if err := opts.Config.validate(); err != nil {
		return nil, err
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Matcher{catalog: opts.Catalog, config: opts.Config, now: now}, nil
}

// Config returns the thresholds in use. The version is also stored per result.
func (m *Matcher) Config() Config { return m.config }

// Match compares validated label data with the local registry.
func (m *Matcher) Match(ctx context.Context, label vision.LabelData) (MatchResult, error) {
	result := MatchResult{
		Fields: map[string]FieldComparison{},
		Dataset: DatasetInfo{
			ConfigVersion: m.config.Version,
		},
	}

	products, err := m.catalog.CountProducts(ctx)
	if err != nil {
		return MatchResult{}, err
	}
	result.Dataset.Products = products
	if syncedAt, ok, err := m.catalog.LatestSuccessfulSync(ctx); err != nil {
		return MatchResult{}, err
	} else if ok {
		result.Dataset.LastSuccessfulSync = &syncedAt
	}

	if !label.Identified() {
		result.Outcome = UnreadableInput
		result.Notes = append(result.Notes, Note{Code: ReasonInsufficientLabel})
		return result, nil
	}

	if label.NRN.Value != nil {
		product, found, err := m.catalog.GetByNRN(ctx, *label.NRN.Value)
		if err != nil {
			return MatchResult{}, err
		}
		if found {
			return m.matched(ctx, label, product, result)
		}
		result.Notes = append(result.Notes, Note{Code: ReasonNRNNotInDataset, Field: FieldNRN})

		variant, ambiguous, err := m.lookupVariants(ctx, label.NRN.Candidates)
		if err != nil {
			return MatchResult{}, err
		}
		if len(variant) == 1 && !ambiguous {
			result.Outcome = CandidateOnly
			result.Candidates = variant
			result.Notes = append(result.Notes, Note{
				Code:   ReasonNRNVariantMatch,
				Field:  FieldNRN,
				Detail: variant[0].NRN,
			})
			return result, nil
		}
		if ambiguous {
			result.Outcome = Ambiguous
			result.Candidates = trimCandidates(variant, m.config.MaxCandidates)
			result.Notes = append(result.Notes, Note{Code: ReasonAmbiguousCandidates, Field: FieldNRN})
			return result, nil
		}
	}

	if label.ProductName.Value == nil {
		result.Outcome = NotFoundInDataset
		m.addDatasetNotes(&result)
		return result, nil
	}

	candidates, err := m.lookupByName(ctx, label)
	if err != nil {
		return MatchResult{}, err
	}
	if len(candidates) == 0 {
		result.Outcome = NotFoundInDataset
		m.addDatasetNotes(&result)
		return result, nil
	}

	result.Candidates = trimCandidates(candidates, m.config.MaxCandidates)
	if len(candidates) > 1 && candidates[0].Score-candidates[1].Score < m.config.AmbiguityMargin {
		result.Outcome = Ambiguous
		result.Notes = append(result.Notes, Note{Code: ReasonAmbiguousCandidates, Field: FieldName})
		return result, nil
	}
	result.Outcome = CandidateOnly
	return result, nil
}

func (m *Matcher) matched(ctx context.Context, label vision.LabelData, product registry.Product, result MatchResult) (MatchResult, error) {
	ingredients, err := m.catalog.Ingredients(ctx, product.ID)
	if err != nil {
		return MatchResult{}, err
	}

	result.Product = ptr(m.ref(product, ingredients, 1, false))
	result.RegistryStatus = product.Status

	result.Fields[FieldNRN] = agree(FieldNRN, *label.NRN.Value, product.NRN, ReasonNRNExactMatch)
	nameField := compareName(label.ProductName, product.Name, m.config.NameThreshold)
	result.Fields[FieldName] = nameField

	manufacturerField, manufacturerNote := compareManufacturer(label.Manufacturer, product, m.config.ManufacturerThreshold)
	result.Fields[FieldManufacturer] = manufacturerField
	if manufacturerNote != nil {
		result.Notes = append(result.Notes, *manufacturerNote)
	}

	ingredientField, strengthField, notes := compareIngredients(label.Ingredients, ingredients, m.config.StrengthTolerance)
	result.Fields[FieldIngredients] = ingredientField
	result.Fields[FieldStrength] = strengthField
	result.Notes = append(result.Notes, notes...)

	expiryField, expiryNote := compareExpiry(label.ExpiryDate, m.now())
	result.Fields[FieldExpiry] = expiryField
	if expiryNote != nil {
		result.Notes = append(result.Notes, *expiryNote)
	}

	statusField, statusNote := compareRegistryStatus(product, m.now())
	result.Fields[FieldRegistryStatus] = statusField
	if statusNote != nil {
		result.Notes = append(result.Notes, *statusNote)
	}

	if hasDisagreement(result.Fields) {
		result.Outcome = MatchedWithMismatch
	} else {
		result.Outcome = MatchedConsistent
	}
	return result, nil
}

func (m *Matcher) lookupVariants(ctx context.Context, variants []string) ([]ProductRef, bool, error) {
	limit := m.config.MaxVariantLookups
	if len(variants) > limit {
		variants = variants[:limit]
	}
	seen := make(map[string]struct{}, len(variants))
	found := make([]ProductRef, 0, 1)
	for _, variant := range variants {
		if _, exists := seen[variant]; exists {
			continue
		}
		seen[variant] = struct{}{}
		product, ok, err := m.catalog.GetByNRN(ctx, variant)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			continue
		}
		found = append(found, m.ref(product, nil, 0.5, true))
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].NRN < found[j].NRN })
	return found, len(found) > 1, nil
}

func (m *Matcher) lookupByName(ctx context.Context, label vision.LabelData) ([]ProductRef, error) {
	if label.ProductName.Value == nil {
		return nil, nil
	}
	name := NormalizeName(*label.ProductName.Value)
	if name == "" {
		return nil, nil
	}

	results, err := m.catalog.SearchByName(ctx, *label.ProductName.Value, m.config.MaxNameCandidates)
	if err != nil {
		return nil, err
	}
	manufacturer := ""
	if label.Manufacturer.Value != nil {
		manufacturer = NormalizeManufacturer(*label.Manufacturer.Value)
	}

	type scored struct {
		product ProductRef
		score   float64
	}
	accepted := make([]scored, 0, len(results))
	for _, hit := range results {
		nameScore := Similarity(name, NormalizeName(hit.Product.Name))
		threshold := m.config.NameOnlyThreshold
		score := nameScore
		if manufacturer != "" {
			threshold = m.config.NameThreshold
			manufacturerScore := manufacturerSimilarity(manufacturer, hit.Product)
			if manufacturerScore < m.config.ManufacturerThreshold {
				continue
			}
			score = 0.7*nameScore + 0.3*manufacturerScore
		}
		if nameScore < threshold {
			continue
		}
		accepted = append(accepted, scored{product: m.ref(hit.Product, nil, score, false), score: score})
	}
	sort.SliceStable(accepted, func(i, j int) bool {
		if accepted[i].score == accepted[j].score {
			return accepted[i].product.NRN < accepted[j].product.NRN
		}
		return accepted[i].score > accepted[j].score
	})

	candidates := make([]ProductRef, 0, len(accepted))
	for _, candidate := range accepted {
		candidates = append(candidates, candidate.product)
	}
	return candidates, nil
}

func (m *Matcher) addDatasetNotes(result *MatchResult) {
	if result.Dataset.Products == 0 {
		result.Notes = append(result.Notes, Note{Code: ReasonDatasetEmpty})
	}
	if result.Dataset.LastSuccessfulSync == nil {
		result.Notes = append(result.Notes, Note{Code: ReasonDatasetNeverSynced})
	}
}

func (m *Matcher) ref(product registry.Product, ingredients []registry.Ingredient, score float64, variant bool) ProductRef {
	ref := ProductRef{
		SourceID:     product.SourceID,
		NRN:          product.NRN,
		Name:         product.Name,
		Manufacturer: product.Manufacturer,
		Applicant:    product.Applicant,
		Status:       product.Status,
		ExpiresAt:    product.ExpiresAt,
		Score:        score,
		VariantMatch: variant,
	}
	for _, ingredient := range ingredients {
		ref.Ingredients = append(ref.Ingredients, IngredientRef{
			Name:     ingredient.NameNorm,
			Strength: ingredient.StrengthRaw,
		})
	}
	return ref
}

func trimCandidates(candidates []ProductRef, limit int) []ProductRef {
	if len(candidates) <= limit {
		return candidates
	}
	return candidates[:limit]
}

func agree(field, label, record, reason string) FieldComparison {
	return FieldComparison{
		Outcome:  Agree,
		Label:    label,
		Registry: record,
		Reasons:  []string{reason},
	}
}

func hasDisagreement(fields map[string]FieldComparison) bool {
	for _, field := range fields {
		if field.Outcome == Disagree {
			return true
		}
	}
	return false
}

func compareName(label vision.Field[string], record string, threshold float64) FieldComparison {
	if label.Value == nil {
		return FieldComparison{Outcome: Unknown, Reasons: []string{ReasonUnknownField}}
	}
	labelName := *label.Value
	normalizedLabel := NormalizeName(labelName)
	normalizedRecord := NormalizeName(record)
	if normalizedLabel == "" || normalizedRecord == "" {
		return FieldComparison{Outcome: Unknown, Label: labelName, Registry: record, Reasons: []string{ReasonUnknownField}}
	}
	score := Similarity(normalizedLabel, normalizedRecord)
	if score >= threshold {
		return FieldComparison{
			Outcome: Agree, Label: labelName, Registry: record,
			Reasons: []string{ReasonNameMatch},
		}
	}
	return FieldComparison{
		Outcome: Disagree, Label: labelName, Registry: record,
		Reasons: []string{ReasonNameMismatch},
	}
}

func compareManufacturer(label vision.Field[string], product registry.Product, threshold float64) (FieldComparison, *Note) {
	if label.Value == nil {
		return FieldComparison{Outcome: Unknown, Reasons: []string{ReasonUnknownField}}, nil
	}
	labelName := *label.Value
	normalized := NormalizeManufacturer(labelName)
	if normalized == "" {
		return FieldComparison{Outcome: Unknown, Label: labelName, Reasons: []string{ReasonUnknownField}}, nil
	}

	best := 0.0
	bestRecord := ""
	for _, record := range []string{product.Manufacturer, product.Applicant} {
		normalizedRecord := NormalizeManufacturer(record)
		if normalizedRecord == "" {
			continue
		}
		if score := Similarity(normalized, normalizedRecord); score > best {
			best = score
			bestRecord = record
		}
	}
	if bestRecord == "" {
		return FieldComparison{Outcome: Unknown, Label: labelName, Reasons: []string{ReasonUnknownField}}, nil
	}
	if best >= threshold {
		return FieldComparison{
			Outcome: Agree, Label: labelName, Registry: bestRecord,
			Reasons: []string{ReasonManufacturerMatch},
		}, nil
	}
	return FieldComparison{
		Outcome: Disagree, Label: labelName, Registry: bestRecord,
		Reasons: []string{ReasonManufacturerMismatch},
	}, &Note{Code: ReasonManufacturerMismatch, Field: FieldManufacturer, Detail: labelName + " / " + bestRecord}
}

func compareIngredients(label []vision.IngredientClaim, record []registry.Ingredient, tolerance float64) (FieldComparison, FieldComparison, []Note) {
	ingredientField := FieldComparison{Outcome: Unknown, Reasons: []string{ReasonUnknownField}}
	strengthField := FieldComparison{Outcome: Unknown, Reasons: []string{ReasonUnknownField}}
	if len(label) == 0 || len(record) == 0 {
		return ingredientField, strengthField, nil
	}

	recordByName := make(map[string]registry.Ingredient, len(record))
	for _, ingredient := range record {
		recordByName[ingredient.NameNorm] = ingredient
	}
	labelByName := make(map[string]vision.IngredientClaim, len(label))
	for _, ingredient := range label {
		labelByName[ingredient.NameNorm] = ingredient
	}

	var notes []Note
	var missing, extra []string
	for name := range recordByName {
		if _, ok := labelByName[name]; !ok {
			missing = append(missing, name)
		}
	}
	for name := range labelByName {
		if _, ok := recordByName[name]; !ok {
			extra = append(extra, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)

	reasons := make([]string, 0, 3)
	if len(missing) == 0 && len(extra) == 0 {
		ingredientField.Outcome = Agree
		reasons = append(reasons, ReasonIngredientsMatch)
	} else {
		ingredientField.Outcome = Disagree
	}
	if len(missing) > 0 {
		reasons = append(reasons, ReasonMissingIngredient)
		for _, name := range missing {
			notes = append(notes, Note{Code: ReasonMissingIngredient, Field: FieldIngredients, Detail: name})
		}
	}
	if len(extra) > 0 {
		reasons = append(reasons, ReasonExtraIngredient)
		for _, name := range extra {
			notes = append(notes, Note{Code: ReasonExtraIngredient, Field: FieldIngredients, Detail: name})
		}
	}
	ingredientField.Reasons = reasons

	comparable := 0
	matched := 0
	mismatched := 0
	for name, claim := range labelByName {
		recordIngredient, ok := recordByName[name]
		if !ok {
			continue
		}
		matched++
		agree, ok := strengthsAgree(claim.Strength, recordIngredient.Strength, tolerance)
		if !ok {
			continue
		}
		comparable++
		if !agree {
			mismatched++
			notes = append(notes, Note{
				Code:   ReasonStrengthMismatch,
				Field:  FieldStrength,
				Detail: name + ": " + claim.StrengthRaw + " / " + recordIngredient.StrengthRaw,
			})
		}
	}
	switch {
	case mismatched > 0:
		strengthField.Outcome = Disagree
		strengthField.Reasons = []string{ReasonStrengthMismatch}
	case comparable > 0:
		strengthField.Outcome = Agree
		strengthField.Reasons = []string{ReasonStrengthMatch}
	case matched > 0:
		strengthField.Outcome = Unknown
		strengthField.Reasons = []string{ReasonUnknownField}
	default:
		strengthField.Outcome = Unknown
		strengthField.Reasons = []string{ReasonUnknownField}
	}
	return ingredientField, strengthField, notes
}

func compareExpiry(field vision.Field[vision.LabelDate], now time.Time) (FieldComparison, *Note) {
	if field.Value == nil {
		return FieldComparison{Outcome: Unknown, Reasons: []string{ReasonUnknownField}}, nil
	}
	label := field.Value
	expired := expiryBefore(*label, now)
	if expired {
		return FieldComparison{
			Outcome: Disagree, Label: label.Format(), Reasons: []string{ReasonExpiredOnLabel},
		}, &Note{Code: ReasonExpiredOnLabel, Field: FieldExpiry, Detail: label.Format()}
	}
	return FieldComparison{
		Outcome: Agree, Label: label.Format(), Reasons: []string{ReasonNotExpired},
	}, nil
}

func expiryBefore(date vision.LabelDate, now time.Time) bool {
	if date.Precision == "month" {
		// A pack marked 01/2027 is usable through the end of January 2027.
		end := time.Date(date.Year, time.Month(date.Month)+1, 1, 0, 0, 0, 0, time.UTC)
		return !now.UTC().Before(end)
	}
	end := time.Date(date.Year, time.Month(date.Month), date.Day+1, 0, 0, 0, 0, time.UTC)
	return !now.UTC().Before(end)
}

func compareRegistryStatus(product registry.Product, now time.Time) (FieldComparison, *Note) {
	status := strings.ToLower(strings.TrimSpace(product.Status))
	record := product.Status
	switch {
	case status == "active":
		if product.ExpiresAt != nil && product.ExpiresAt.Before(now.UTC()) {
			return FieldComparison{
				Outcome: Disagree, Registry: record, Reasons: []string{ReasonRegistryExpired},
			}, &Note{Code: ReasonRegistryExpired, Field: FieldRegistryStatus, Detail: record}
		}
		return FieldComparison{
			Outcome: Agree, Registry: record, Reasons: []string{ReasonRegistryActive},
		}, nil
	case strings.Contains(status, "withdraw"):
		return FieldComparison{
			Outcome: Disagree, Registry: record, Reasons: []string{ReasonRegistryWithdrawn},
		}, &Note{Code: ReasonRegistryWithdrawn, Field: FieldRegistryStatus, Detail: record}
	case status == "inactive":
		return FieldComparison{
			Outcome: Disagree, Registry: record, Reasons: []string{ReasonRegistryInactive},
		}, &Note{Code: ReasonRegistryInactive, Field: FieldRegistryStatus, Detail: record}
	case status == "expired":
		return FieldComparison{
			Outcome: Disagree, Registry: record, Reasons: []string{ReasonRegistryExpired},
		}, &Note{Code: ReasonRegistryExpired, Field: FieldRegistryStatus, Detail: record}
	default:
		return FieldComparison{Outcome: Unknown, Registry: record, Reasons: []string{ReasonUnknownField}}, nil
	}
}

func manufacturerSimilarity(label string, product registry.Product) float64 {
	best := 0.0
	for _, record := range []string{product.Manufacturer, product.Applicant} {
		normalized := NormalizeManufacturer(record)
		if normalized == "" {
			continue
		}
		if score := Similarity(label, normalized); score > best {
			best = score
		}
	}
	return best
}

func ptr[T any](value T) *T { return &value }
