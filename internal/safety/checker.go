package safety

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/benfaruna/medverify/internal/registry"
	"github.com/benfaruna/medverify/internal/verify"
	"github.com/benfaruna/medverify/internal/vision"
)

// Watchlist is the read seam the checker needs. The store implements it.
type Watchlist interface {
	ProductEntries(ctx context.Context, query ProductQuery) ([]Entry, error)
	IngredientEntries(ctx context.Context, names []string, now time.Time) ([]Entry, error)
}

// CheckerOptions configures a Checker.
type CheckerOptions struct {
	Watchlist Watchlist
	Config    Config
	Now       func() time.Time
}

// Checker turns a registry match plus label data into sourced safety flags. It
// never decides whether a medicine is safe; every flag cites the document or
// record it came from.
type Checker struct {
	watchlist Watchlist
	config    Config
	now       func() time.Time
}

// NewChecker builds a checker. A nil clock uses time.Now.
func NewChecker(opts CheckerOptions) (*Checker, error) {
	if opts.Watchlist == nil {
		return nil, fmt.Errorf("safety: checker requires a watchlist")
	}
	if err := opts.Config.validate(); err != nil {
		return nil, err
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Checker{watchlist: opts.Watchlist, config: opts.Config, now: now}, nil
}

// Config returns the thresholds and severity map in use.
func (c *Checker) Config() Config { return c.config }

// productLookupLimit bounds the name candidates fetched per scan.
const productLookupLimit = 20

// Check compares a match result and its label with the watchlist and the
// registered composition. Flags are ordered by type and then by their evidence,
// so the same input always produces the same output.
func (c *Checker) Check(ctx context.Context, match verify.MatchResult, label vision.LabelData) ([]Flag, error) {
	now := c.now().UTC()

	productFlags, err := c.checkWatchlistProduct(ctx, match, label, now)
	if err != nil {
		return nil, err
	}

	ingredientFlags, err := c.checkWatchlistIngredients(ctx, match, label, now)
	if err != nil {
		return nil, err
	}

	flags := make([]Flag, 0, len(productFlags)+len(ingredientFlags)+4)
	flags = append(flags, productFlags...)
	flags = append(flags, ingredientFlags...)
	flags = append(flags, c.checkLabelIngredients(match, label)...)
	if flag, ok := c.checkRegistration(match); ok {
		flags = append(flags, flag)
	}

	sortFlags(flags)
	return flags, nil
}

func (c *Checker) checkWatchlistProduct(ctx context.Context, match verify.MatchResult, label vision.LabelData, now time.Time) ([]Flag, error) {
	nrn := normalizedLabelNRN(label)
	if nrn == "" && match.Product != nil {
		nrn = registry.NormalizeNRN(match.Product.NRN)
	}

	nameNorm := normalizedLabelName(label)
	if nameNorm == "" && match.Product != nil {
		nameNorm = verify.NormalizeName(match.Product.Name)
	}

	manufacturerNorm := normalizedLabelManufacturer(label)
	if manufacturerNorm == "" && match.Product != nil {
		manufacturerNorm = verify.NormalizeManufacturer(match.Product.Manufacturer)
	}

	if nrn == "" && nameNorm == "" {
		return nil, nil
	}

	entries, err := c.watchlist.ProductEntries(ctx, ProductQuery{
		NRN:      nrn,
		NameNorm: nameNorm,
		Limit:    productLookupLimit,
		Now:      now,
	})
	if err != nil {
		return nil, err
	}

	seen := make(map[int64]struct{}, len(entries))
	flags := make([]Flag, 0, len(entries))
	for _, entry := range entries {
		if _, duplicate := seen[entry.ID]; duplicate {
			continue
		}
		if !entry.Live(now) {
			continue
		}

		switch {
		case entry.NRN != "":
			if entry.NRN != nrn {
				continue
			}
		case nameNorm != "":
			if verify.Similarity(entry.ProductNameNorm, nameNorm) < c.config.WatchlistNameThreshold {
				continue
			}
			// A name-only entry that names a manufacturer is only reported when
			// the label also supports that attribution.
			if entry.ManufacturerNorm != "" {
				if manufacturerNorm == "" ||
					verify.Similarity(entry.ManufacturerNorm, manufacturerNorm) < c.config.WatchlistManufacturerThreshold {
					continue
				}
			}
		default:
			continue
		}

		seen[entry.ID] = struct{}{}
		flags = append(flags, c.entryFlag(entry, nil))
	}
	return flags, nil
}

func (c *Checker) checkWatchlistIngredients(ctx context.Context, match verify.MatchResult, label vision.LabelData, now time.Time) ([]Flag, error) {
	names := make([]string, 0, len(label.Ingredients))
	origin := make(map[string]string, len(label.Ingredients))

	for _, claim := range label.Ingredients {
		name := registry.NormalizeIngredient(firstNonEmpty(claim.NameNorm, claim.NameRaw))
		if name == "" || claim.Confidence < c.config.MinIngredientConfidence {
			continue
		}
		if _, ok := origin[name]; !ok {
			names = append(names, name)
			origin[name] = "label"
		}
	}

	if confirmedProduct(match) {
		for _, ingredient := range match.Product.Ingredients {
			name := registry.NormalizeIngredient(ingredient.Name)
			if name == "" {
				continue
			}
			if _, ok := origin[name]; !ok {
				names = append(names, name)
				origin[name] = "registry"
			}
		}
	}

	if len(names) == 0 {
		return nil, nil
	}

	entries, err := c.watchlist.IngredientEntries(ctx, names, now)
	if err != nil {
		return nil, err
	}

	seen := make(map[int64]struct{}, len(entries))
	flags := make([]Flag, 0, len(entries))
	for _, entry := range entries {
		if _, duplicate := seen[entry.ID]; duplicate {
			continue
		}
		if !entry.Live(now) {
			continue
		}
		seen[entry.ID] = struct{}{}
		flags = append(flags, c.entryFlag(entry, map[string]string{
			"ingredient": entry.IngredientNorm,
			"matched_on": origin[entry.IngredientNorm],
		}))
	}
	return flags, nil
}

// checkLabelIngredients compares the printed composition with the registered
// one. It reports each differing ingredient once and stays silent when the label
// ingredient list was not read well enough to make a claim.
func (c *Checker) checkLabelIngredients(match verify.MatchResult, label vision.LabelData) []Flag {
	if !confirmedProduct(match) || len(match.Product.Ingredients) == 0 {
		return nil
	}

	registered := make(map[string]verify.IngredientRef, len(match.Product.Ingredients))
	for _, ingredient := range match.Product.Ingredients {
		name := registry.NormalizeIngredient(ingredient.Name)
		if name == "" {
			continue
		}
		registered[name] = ingredient
	}
	if len(registered) == 0 {
		return nil
	}

	printed := make(map[string]vision.IngredientClaim, len(label.Ingredients))
	adequate := false
	for _, claim := range label.Ingredients {
		name := registry.NormalizeIngredient(firstNonEmpty(claim.NameNorm, claim.NameRaw))
		if name == "" {
			continue
		}
		printed[name] = claim
		if claim.Confidence >= c.config.MinIngredientConfidence {
			adequate = true
		}
	}

	var flags []Flag

	extra := make([]string, 0)
	for name, claim := range printed {
		if _, ok := registered[name]; ok {
			continue
		}
		if claim.Confidence < c.config.MinIngredientConfidence {
			continue
		}
		extra = append(extra, name)
	}
	sort.Strings(extra)
	for _, name := range extra {
		flags = append(flags, Flag{
			Type:       UndeclaredIngredient,
			Severity:   c.config.Severity[UndeclaredIngredient],
			ReasonCode: ReasonUndeclaredIngredient,
			Evidence:   map[string]string{"ingredient": name},
			Source: Source{
				Kind: SourceRegistry,
				NRN:  match.Product.NRN,
			},
		})
	}

	flags = append(flags, c.strengthFlags(match, printed, registered)...)

	// A photo that produced no readable ingredient list cannot prove an
	// ingredient is absent.
	if !adequate {
		return flags
	}

	missing := make([]string, 0)
	for name := range registered {
		if _, ok := printed[name]; ok {
			continue
		}
		missing = append(missing, name)
	}
	sort.Strings(missing)
	for _, name := range missing {
		flags = append(flags, Flag{
			Type:       MissingIngredient,
			Severity:   c.config.Severity[MissingIngredient],
			ReasonCode: ReasonMissingIngredient,
			Evidence:   map[string]string{"ingredient": name},
			Source: Source{
				Kind: SourceRegistry,
				NRN:  match.Product.NRN,
			},
		})
	}
	return flags
}

// strengthFlags reports printed strengths that differ from the registered
// strength beyond the configured tolerance. The tolerance itself is included in
// the evidence so a past result never hides the threshold it used.
func (c *Checker) strengthFlags(match verify.MatchResult, printed map[string]vision.IngredientClaim, registered map[string]verify.IngredientRef) []Flag {
	names := make([]string, 0, len(printed))
	for name := range printed {
		if _, ok := registered[name]; ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	flags := make([]Flag, 0, len(names))
	for _, name := range names {
		claim := printed[name]
		if claim.Confidence < c.config.MinIngredientConfidence {
			continue
		}
		record := registered[name]
		agree, comparable := verify.StrengthsAgree(
			claim.Strength, registry.ParseStrength(record.Strength), c.config.StrengthTolerance)
		if !comparable || agree {
			continue
		}
		flags = append(flags, Flag{
			Type:       StrengthDeviation,
			Severity:   c.config.Severity[StrengthDeviation],
			ReasonCode: ReasonStrengthDeviation,
			Evidence: map[string]string{
				"ingredient":        name,
				"label_strength":    firstNonEmpty(claim.StrengthRaw, claim.Strength.Raw),
				"registry_strength": record.Strength,
				"tolerance":         strconv.FormatFloat(c.config.StrengthTolerance, 'f', -1, 64),
			},
			Source: Source{
				Kind: SourceRegistry,
				NRN:  match.Product.NRN,
			},
		})
	}
	return flags
}

func (c *Checker) checkRegistration(match verify.MatchResult) (Flag, bool) {
	comparison, ok := match.Fields[verify.FieldRegistryStatus]
	if !ok || comparison.Outcome != verify.Disagree {
		return Flag{}, false
	}

	reason := ""
	for _, candidate := range comparison.Reasons {
		switch candidate {
		case verify.ReasonRegistryExpired, verify.ReasonRegistryWithdrawn, verify.ReasonRegistryInactive:
			reason = candidate
		}
		if reason != "" {
			break
		}
	}
	if reason == "" {
		return Flag{}, false
	}

	source := Source{Kind: SourceRegistry, Status: match.RegistryStatus}
	evidence := map[string]string{"status": match.RegistryStatus}
	if match.Product != nil {
		source.RegistrySourceID = match.Product.SourceID
		source.NRN = match.Product.NRN
		source.ExpiresAt = match.Product.ExpiresAt
		if match.Product.ExpiresAt != nil {
			evidence["expires_at"] = DateOnly(*match.Product.ExpiresAt).Format(time.DateOnly)
		}
	} else if match.RegistryStatus == "" {
		return Flag{}, false
	}

	return Flag{
		Type:       RegistrationProblem,
		Severity:   c.config.Severity[RegistrationProblem],
		ReasonCode: reason,
		Evidence:   evidence,
		Source:     source,
	}, true
}

func (c *Checker) entryFlag(entry Entry, extra map[string]string) Flag {
	flagType := WatchlistProduct
	if entry.Kind.IsIngredient() {
		flagType = WatchlistIngredient
	}

	evidence := map[string]string{
		"issuing_body": entry.Source.IssuingBody,
		"issued_at":    DateOnly(entry.Source.IssuedAt).Format(time.DateOnly),
		"source_url":   entry.Source.URL,
	}
	for key, value := range extra {
		evidence[key] = value
	}

	issued := DateOnly(entry.Source.IssuedAt)
	return Flag{
		Type:       flagType,
		Severity:   c.config.Severity[flagType],
		ReasonCode: entry.Kind.FlagReason(),
		Evidence:   evidence,
		Source: Source{
			Kind:             SourceWatchlistEntry,
			WatchlistEntryID: entry.ID,
			IssuingBody:      entry.Source.IssuingBody,
			Title:            entry.Source.Title,
			URL:              entry.Source.URL,
			IssuedAt:         &issued,
		},
	}
}

func confirmedProduct(match verify.MatchResult) bool {
	if match.Product == nil {
		return false
	}
	return match.Outcome == verify.MatchedConsistent || match.Outcome == verify.MatchedWithMismatch
}

func normalizedLabelNRN(label vision.LabelData) string {
	if label.NRN.Value == nil {
		return ""
	}
	nrn := registry.NormalizeNRN(*label.NRN.Value)
	if nrn == "" || !registry.ValidNRN(nrn) {
		return ""
	}
	return nrn
}

func normalizedLabelName(label vision.LabelData) string {
	if label.ProductName.Value == nil {
		return ""
	}
	return verify.NormalizeName(*label.ProductName.Value)
}

func normalizedLabelManufacturer(label vision.LabelData) string {
	if label.Manufacturer.Value == nil {
		return ""
	}
	return verify.NormalizeManufacturer(*label.Manufacturer.Value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func sortFlags(flags []Flag) {
	order := make(map[FlagType]int, len(FlagTypes))
	for index, flagType := range FlagTypes {
		order[flagType] = index
	}
	sort.SliceStable(flags, func(i, j int) bool {
		left, right := flags[i], flags[j]
		if order[left.Type] != order[right.Type] {
			return order[left.Type] < order[right.Type]
		}
		leftKey := strings.Join([]string{left.ReasonCode, left.Evidence["ingredient"], left.Evidence["source_url"]}, "|")
		rightKey := strings.Join([]string{right.ReasonCode, right.Evidence["ingredient"], right.Evidence["source_url"]}, "|")
		return leftKey < rightKey
	})
}
