package verify

import "time"

// Outcome is the terminal result of registry verification. The set is closed:
// adding a value requires updating this package, the verdict mapping, and the
// OpenAPI enum.
type Outcome string

// Match outcomes. No outcome means a medicine is safe or genuine.
const (
	// MatchedConsistent means the NRN was found and no printed field materially
	// disagreed with the registry record.
	MatchedConsistent Outcome = "matched_consistent"
	// MatchedWithMismatch means the NRN was found but at least one printed field
	// disagreed; the reasons say which.
	MatchedWithMismatch Outcome = "matched_with_mismatch"
	// CandidateOnly means the NRN was unreadable and fuzzy matching found one
	// plausible product. It is not a confirmed match.
	CandidateOnly Outcome = "candidate_only"
	// Ambiguous means more than one product was plausible. The caller must not
	// choose one.
	Ambiguous Outcome = "ambiguous"
	// NotFoundInDataset means the local dataset has no match. The dataset may be
	// incomplete or stale; this is never proof that a pack is fake.
	NotFoundInDataset Outcome = "not_found_in_dataset"
	// UnreadableInput means there was not enough label data to search.
	UnreadableInput Outcome = "unreadable_input"
)

// FieldOutcome is the result of comparing one label field with the registry.
type FieldOutcome string

// Field outcomes. Unknown is never counted as disagreement.
const (
	Agree    FieldOutcome = "agree"
	Disagree FieldOutcome = "disagree"
	Unknown  FieldOutcome = "unknown"
)

// Stable field names used in MatchResult.Fields and reason codes.
const (
	FieldNRN            = "nrn"
	FieldName           = "name"
	FieldManufacturer   = "manufacturer"
	FieldIngredients    = "ingredients"
	FieldStrength       = "strength"
	FieldExpiry         = "expiry"
	FieldRegistryStatus = "registry_status"
)

// Stable reason codes. The UI translates these; it must not parse free text.
const (
	ReasonNRNExactMatch        = "nrn_exact_match"
	ReasonNRNVariantMatch      = "nrn_variant_match"
	ReasonNRNNotInDataset      = "nrn_not_in_dataset"
	ReasonNameMatch            = "name_match"
	ReasonNameMismatch         = "name_mismatch"
	ReasonManufacturerMatch    = "manufacturer_match"
	ReasonManufacturerMismatch = "manufacturer_mismatch"
	ReasonIngredientsMatch     = "ingredients_match"
	ReasonMissingIngredient    = "missing_ingredient"
	ReasonExtraIngredient      = "extra_ingredient"
	ReasonStrengthMatch        = "strength_match"
	ReasonStrengthMismatch     = "strength_mismatch"
	ReasonNotExpired           = "not_expired"
	ReasonExpiredOnLabel       = "expired_on_label"
	ReasonRegistryActive       = "registry_active"
	ReasonRegistryInactive     = "registry_inactive"
	ReasonRegistryWithdrawn    = "registry_withdrawn"
	ReasonRegistryExpired      = "registry_expired"
	ReasonAmbiguousCandidates  = "ambiguous_candidates"
	ReasonDatasetEmpty         = "dataset_empty"
	ReasonDatasetNeverSynced   = "dataset_never_synced"
	ReasonInsufficientLabel    = "insufficient_label_data"
	ReasonUnknownField         = "unknown_field"
)

// FieldComparison records what was compared and why it agreed or disagreed.
// Reasons are stable codes; Note carries any human-readable detail separately.
type FieldComparison struct {
	Outcome  FieldOutcome `json:"outcome"`
	Label    string       `json:"label,omitempty"`
	Registry string       `json:"registry,omitempty"`
	Reasons  []string     `json:"reasons,omitempty"`
}

// Note is an explainable, translatable event. Code is stable; Detail is data.
type Note struct {
	Code   string `json:"code"`
	Field  string `json:"field,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// ProductRef is the subset of a registry product needed to explain a result.
// Ingredients are included for a confirmed match and omitted for lightweight
// candidates.
type ProductRef struct {
	SourceID     string          `json:"source_id"`
	NRN          string          `json:"nrn,omitempty"`
	Name         string          `json:"name"`
	Manufacturer string          `json:"manufacturer,omitempty"`
	Applicant    string          `json:"applicant,omitempty"`
	Status       string          `json:"status"`
	ExpiresAt    *time.Time      `json:"expires_at,omitempty"`
	Ingredients  []IngredientRef `json:"ingredients,omitempty"`
	Score        float64         `json:"score"`
	VariantMatch bool            `json:"variant_match,omitempty"`
}

// IngredientRef is one registry ingredient as shown in a match result.
type IngredientRef struct {
	Name     string `json:"name"`
	Strength string `json:"strength,omitempty"`
}

// DatasetInfo records how much data the answer was based on, so a not-found
// result can be understood as a dataset limit rather than a product judgment.
type DatasetInfo struct {
	ConfigVersion      string     `json:"config_version"`
	Products           int        `json:"products"`
	LastSuccessfulSync *time.Time `json:"last_successful_sync,omitempty"`
}

// MatchResult is the complete, explainable comparison result.
type MatchResult struct {
	Outcome        Outcome                    `json:"outcome"`
	Product        *ProductRef                `json:"product,omitempty"`
	Candidates     []ProductRef               `json:"candidates,omitempty"`
	Fields         map[string]FieldComparison `json:"fields"`
	RegistryStatus string                     `json:"registry_status,omitempty"`
	Notes          []Note                     `json:"notes,omitempty"`
	Dataset        DatasetInfo                `json:"dataset"`
}
