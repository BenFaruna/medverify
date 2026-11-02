// Package safety flags packs whose product or ingredients appear on a curated,
// sourced watchlist, and records label-versus-registry ingredient problems.
//
// It never assesses harm. A flag says "this matches a cited source" or "this
// printed value differs from the registered record"; the wording catalog is the
// only place human-facing text lives, and it is stored as a reviewed file.
package safety

import (
	"time"

	"github.com/benfaruna/medverify/internal/verify"
)

// FlagType is the closed set of safety flags. Adding one requires updating this
// package, the wording catalog, the severity config, and the OpenAPI enum.
type FlagType string

// Flag types. None of them is a safety or authenticity claim.
const (
	// WatchlistProduct means the product (by NRN, or by name and manufacturer)
	// appears on a sourced alert or withdrawal list.
	WatchlistProduct FlagType = "watchlist_product"
	// WatchlistIngredient means a printed or registered ingredient appears on a
	// banned or restricted list.
	WatchlistIngredient FlagType = "watchlist_ingredient"
	// UndeclaredIngredient means the label lists an ingredient the registered
	// composition does not contain.
	UndeclaredIngredient FlagType = "undeclared_ingredient"
	// MissingIngredient means the registered composition lists an ingredient
	// the label did not show.
	MissingIngredient FlagType = "missing_ingredient"
	// StrengthDeviation means a printed strength differs from the registered
	// strength beyond the configured tolerance.
	StrengthDeviation FlagType = "strength_deviation"
	// RegistrationProblem means the registry status is expired, withdrawn, or
	// inactive.
	RegistrationProblem FlagType = "registration_problem"
)

// FlagTypes lists every flag type in display order.
var FlagTypes = []FlagType{
	WatchlistProduct,
	WatchlistIngredient,
	UndeclaredIngredient,
	MissingIngredient,
	StrengthDeviation,
	RegistrationProblem,
}

// Valid reports whether the type is one this build understands.
func (t FlagType) Valid() bool {
	for _, known := range FlagTypes {
		if t == known {
			return true
		}
	}
	return false
}

// Severity feeds the verdict engine. It never appears as a health claim.
type Severity string

// Severity levels.
const (
	SeverityHigh   Severity = "high"
	SeverityMedium Severity = "medium"
	SeverityLow    Severity = "low"
)

// Valid reports whether the severity is known.
func (s Severity) Valid() bool {
	return s == SeverityHigh || s == SeverityMedium || s == SeverityLow
}

// Reason codes are stable identifiers and keys in the wording catalog. They are
// never rendered as-is.
const (
	ReasonWatchlistAlert                = "watchlist_alert"
	ReasonWatchlistWithdrawal           = "watchlist_withdrawal"
	ReasonWatchlistBannedIngredient     = "watchlist_banned_ingredient"
	ReasonWatchlistRestrictedIngredient = "watchlist_restricted_ingredient"
	ReasonUndeclaredIngredient          = "undeclared_ingredient"
	ReasonMissingIngredient             = "missing_ingredient"
	ReasonStrengthDeviation             = "strength_deviation"
)

// ReasonCodes lists every reason code the checker can emit, including the
// registry status codes shared with phase 05.
var ReasonCodes = []string{
	ReasonWatchlistAlert,
	ReasonWatchlistWithdrawal,
	ReasonWatchlistBannedIngredient,
	ReasonWatchlistRestrictedIngredient,
	ReasonUndeclaredIngredient,
	ReasonMissingIngredient,
	ReasonStrengthDeviation,
	verify.ReasonRegistryExpired,
	verify.ReasonRegistryWithdrawn,
	verify.ReasonRegistryInactive,
}

// EntryKind is the kind of watchlist entry. Product kinds carry an NRN or a
// product name; ingredient kinds carry a normalized ingredient name.
type EntryKind string

// Entry kinds.
const (
	EntryAlert                EntryKind = "alert"
	EntryWithdrawal           EntryKind = "withdrawal"
	EntryBannedIngredient     EntryKind = "banned_ingredient"
	EntryRestrictedIngredient EntryKind = "restricted_ingredient"
)

// Valid reports whether the kind is known.
func (k EntryKind) Valid() bool {
	switch k {
	case EntryAlert, EntryWithdrawal, EntryBannedIngredient, EntryRestrictedIngredient:
		return true
	}
	return false
}

// IsIngredient reports whether the kind targets an ingredient rather than a
// product.
func (k EntryKind) IsIngredient() bool {
	return k == EntryBannedIngredient || k == EntryRestrictedIngredient
}

// FlagReason maps an entry kind to the reason code used in wording.
func (k EntryKind) FlagReason() string {
	switch k {
	case EntryAlert:
		return ReasonWatchlistAlert
	case EntryWithdrawal:
		return ReasonWatchlistWithdrawal
	case EntryBannedIngredient:
		return ReasonWatchlistBannedIngredient
	case EntryRestrictedIngredient:
		return ReasonWatchlistRestrictedIngredient
	default:
		return ""
	}
}

// SourceDocument is the cited document behind a watchlist entry. Provenance is
// required by the database: an entry cannot exist without one.
type SourceDocument struct {
	ID          int64
	IssuingBody string
	Title       string
	URL         string
	IssuedAt    time.Time
}

// Entry is one watchlist entry plus its source. Product entries match on NRN
// first; when the source gives no NRN they match on normalized name and
// manufacturer. Ingredient entries match on normalized ingredient name.
type Entry struct {
	ID               int64
	Kind             EntryKind
	NRN              string
	ProductName      string
	ProductNameNorm  string
	Manufacturer     string
	ManufacturerNorm string
	IngredientNorm   string
	Reason           string
	EffectiveUntil   *time.Time
	Active           bool
	ReviewedBy       string
	ReviewDueAt      *time.Time
	CreatedAt        time.Time
	Source           SourceDocument
}

// Live reports whether the entry may raise a flag at now. Inactive entries and
// entries whose effective window has closed never do.
func (e Entry) Live(now time.Time) bool {
	if !e.Active {
		return false
	}
	if e.EffectiveUntil != nil && e.EffectiveUntil.Before(DateOnly(now)) {
		return false
	}
	return true
}

// Source kinds used in a flag.
const (
	SourceWatchlistEntry = "watchlist_entry"
	SourceRegistry       = "registry"
)

// Source is the evidence citation on a flag: either the watchlist entry with
// its document, or the registry record that was compared.
type Source struct {
	Kind             string     `json:"kind"`
	WatchlistEntryID int64      `json:"watchlist_entry_id,omitempty"`
	IssuingBody      string     `json:"issuing_body,omitempty"`
	Title            string     `json:"title,omitempty"`
	URL              string     `json:"url,omitempty"`
	IssuedAt         *time.Time `json:"issued_at,omitempty"`
	RegistrySourceID string     `json:"registry_source_id,omitempty"`
	NRN              string     `json:"nrn,omitempty"`
	Status           string     `json:"status,omitempty"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
}

// Flag is one explainable safety finding. Evidence holds the compared values as
// strings so the wording catalog and the UI share one shape.
type Flag struct {
	Type       FlagType          `json:"type"`
	Severity   Severity          `json:"severity"`
	ReasonCode string            `json:"reason_code"`
	Evidence   map[string]string `json:"evidence,omitempty"`
	Source     Source            `json:"source"`
}

// DateOnly truncates a time to its UTC calendar date.
func DateOnly(t time.Time) time.Time {
	utc := t.UTC()
	return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
}
