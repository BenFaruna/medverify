package registry

import "time"

// Product is one Greenbook product after parsing and normalization. The raw
// payload is kept on every row so nothing published by the source is lost.
type Product struct {
	SourceID             string     `json:"source_id"`
	NRN                  string     `json:"nrn"`
	Name                 string     `json:"name"`
	Category             string     `json:"category,omitempty"`
	Status               string     `json:"status"`
	Applicant            string     `json:"applicant,omitempty"`
	Manufacturer         string     `json:"manufacturer,omitempty"`
	ManufacturerSourceID string     `json:"manufacturer_source_id,omitempty"`
	CompositionRaw       string     `json:"composition_raw,omitempty"`
	Form                 string     `json:"form,omitempty"`
	Route                string     `json:"route,omitempty"`
	StrengthRaw          string     `json:"strength_raw,omitempty"`
	PackSize             string     `json:"pack_size,omitempty"`
	ATCCode              string     `json:"atc_code,omitempty"`
	ApprovedAt           *time.Time `json:"approved_at,omitempty"`
	ExpiresAt            *time.Time `json:"expires_at,omitempty"`
	SmpcURL              string     `json:"smpc_url,omitempty"`
	Description          string     `json:"description,omitempty"`

	Ingredients []Ingredient `json:"ingredients"`

	Raw       []byte    `json:"-"`
	FetchedAt time.Time `json:"-"`
}

// Ingredient is one active ingredient with its parsed strength, when the
// source expressed one. NameRaw and StrengthRaw always survive parsing.
type Ingredient struct {
	NameRaw     string   `json:"name_raw"`
	NameNorm    string   `json:"name_norm"`
	Strength    Strength `json:"strength"`
	StrengthRaw string   `json:"strength_raw,omitempty"`
}

// Page is one parsed Greenbook list response.
type Page struct {
	RecordsTotal    int        `json:"records_total"`
	RecordsFiltered int        `json:"records_filtered"`
	Products        []Product  `json:"products"`
	RowErrors       []RowError `json:"-"`
}

// RowError records one row that could not be decoded. A bad row never aborts a
// whole page; the anomaly guard sees the failure share instead.
type RowError struct {
	Index int    `json:"index"`
	Raw   []byte `json:"raw"`
	Err   error  `json:"-"`
}
