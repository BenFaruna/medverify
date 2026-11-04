package scan

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/benfaruna/medverify/internal/safety"
	"github.com/benfaruna/medverify/internal/verdict"
)

// Status is the lifecycle state of a scan. The database CHECK constraint and
// the guarded updates both enforce this set.
type Status string

// Scan states.
const (
	StatusQueued     Status = "queued"
	StatusProcessing Status = "processing"
	StatusCompleted  Status = "completed"
	StatusUnreadable Status = "unreadable"
	StatusFailed     Status = "failed"
)

// ErrInvalidTransition marks a state change the pipeline does not allow.
var ErrInvalidTransition = errors.New("invalid scan state transition")

// ErrInvalidLocation is returned for missing, malformed, or out-of-range
// coordinates.
var ErrInvalidLocation = errors.New("invalid location")

// ErrInvalidLanguage is returned for a language tag that is not a short BCP 47
// style value.
var ErrInvalidLanguage = errors.New("invalid language")

// Terminal reports whether no further work is expected for this state.
func (s Status) Terminal() bool {
	return s == StatusCompleted || s == StatusUnreadable || s == StatusFailed
}

// CanTransition reports whether the state machine allows from -> to.
func CanTransition(from, to Status) bool {
	switch from {
	case StatusQueued:
		return to == StatusProcessing
	case StatusProcessing:
		return to == StatusCompleted || to == StatusUnreadable || to == StatusFailed
	case StatusFailed:
		// River may retry a failed job; the retry requeues the scan first.
		return to == StatusQueued
	default:
		return false
	}
}

// Transition validates a state change.
func Transition(from, to Status) (Status, error) {
	if !CanTransition(from, to) {
		return from, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, from, to)
	}
	return to, nil
}

// Image is one stored, re-encoded scan image. StorageKey is random and never
// contains the client filename.
type Image struct {
	ID          int64
	Position    int
	StorageKey  string
	ContentType string
	ByteSize    int
	Width       int
	Height      int
	SHA256      string
	CreatedAt   time.Time
}

// Result is the stored result row. Verdict is the typed phase 07 output,
// Signals is the exact input it used (so the scan can be replayed), Extracted
// carries validated label data, Features carries registry verification, and
// Flags carries the sourced safety findings from phase 06.
type Result struct {
	Verdict   *verdict.Result
	Signals   json.RawMessage
	Reasons   json.RawMessage
	Extracted json.RawMessage
	Features  json.RawMessage
	Flags     []safety.Flag
	Provider  string
	Error     string
}

// Scan is a scan with its images and optional result.
type Scan struct {
	ID           uuid.UUID
	ClientKey    string
	Status       Status
	Language     string
	LocationCell string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	CompletedAt  *time.Time
	Images       []Image
	Result       *Result
	Flags        []safety.Flag
}

// Disclaimer is shown with every result. It states what the tool does and does
// not do; it must never turn into advice, and it must never claim a pack is
// safe, genuine, or authentic.
const Disclaimer = "MedVerify compares the pack photo with NAFDAC records. It is not medical " +
	"advice: talk to a pharmacist or doctor about your medicine."

// VerdictView is the public verdict. It deliberately has no numeric score: the
// internal score is a ranking for analytics and would read as a probability.
type VerdictView struct {
	Value        string `json:"value"`
	Confidence   string `json:"confidence"`
	RulesVersion string `json:"rules_version"`
}

// View is the API response shape. Field names are stable for clients.
//
// It is the public contract: no numeric score, no internal row IDs, and no
// wording that calls a medicine safe, genuine, or authentic.
type View struct {
	ID          string             `json:"id"`
	Status      Status             `json:"status"`
	Verdict     *VerdictView       `json:"verdict"`
	Reasons     []verdict.Rendered `json:"reasons"`
	Extracted   json.RawMessage    `json:"extracted"`
	Features    json.RawMessage    `json:"features"`
	Flags       json.RawMessage    `json:"flags"`
	Disclaimer  string             `json:"disclaimer"`
	CreatedAt   time.Time          `json:"created_at"`
	CompletedAt *time.Time         `json:"completed_at"`
}

// View converts a scan to its public API representation. reasons supplies the
// reviewed wording catalog; a nil catalog still returns the stable reason codes
// but no sentences.
func (s Scan) View(reasons *verdict.Catalog) View {
	view := View{
		ID:          s.ID.String(),
		Status:      s.Status,
		Reasons:     []verdict.Rendered{},
		Flags:       json.RawMessage("[]"),
		Disclaimer:  Disclaimer,
		CreatedAt:   s.CreatedAt,
		CompletedAt: s.CompletedAt,
	}
	if encoded, err := json.Marshal(s.Flags); err == nil && len(s.Flags) > 0 {
		view.Flags = encoded
	}
	if s.Result == nil {
		return view
	}
	if s.Result.Verdict != nil {
		view.Verdict = &VerdictView{
			Value:        string(s.Result.Verdict.Verdict),
			Confidence:   string(s.Result.Verdict.Confidence),
			RulesVersion: s.Result.Verdict.RulesVersion,
		}
		view.Reasons = renderReasons(reasons, s.Result.Verdict.Reasons)
	} else {
		view.Reasons = renderStoredReasons(reasons, s.Result.Reasons)
	}
	if len(s.Result.Extracted) > 0 {
		view.Extracted = s.Result.Extracted
	}
	if len(s.Result.Features) > 0 {
		view.Features = s.Result.Features
	}
	return view
}

func renderReasons(catalog *verdict.Catalog, reasons []verdict.Reason) []verdict.Rendered {
	if len(reasons) == 0 {
		return []verdict.Rendered{}
	}
	if catalog == nil {
		// Without reviewed wording a reason a client cannot display is better
		// than a code a client is tempted to print.
		return []verdict.Rendered{}
	}
	return catalog.RenderAll(reasons)
}

// renderStoredReasons handles results written before the typed verdict existed:
// the codes are in JSON but there is no parsed result to read.
func renderStoredReasons(catalog *verdict.Catalog, raw json.RawMessage) []verdict.Rendered {
	if len(raw) == 0 {
		return []verdict.Rendered{}
	}
	var reasons []verdict.Reason
	if err := json.Unmarshal(raw, &reasons); err != nil {
		return []verdict.Rendered{}
	}
	return renderReasons(catalog, reasons)
}

// CoarseCell converts coordinates to a low-precision grid identifier. Only the
// cell is stored; the raw coordinates must never be persisted or logged.
func CoarseCell(latitude, longitude float64, precision int) (string, error) {
	if math.IsNaN(latitude) || math.IsNaN(longitude) ||
		math.IsInf(latitude, 0) || math.IsInf(longitude, 0) {
		return "", ErrInvalidLocation
	}
	if latitude < -90 || latitude > 90 || longitude < -180 || longitude > 180 {
		return "", ErrInvalidLocation
	}
	if precision < 0 || precision > 3 {
		precision = 1
	}

	factor := math.Pow10(precision)
	lat := math.Round(latitude*factor) / factor
	lon := math.Round(longitude*factor) / factor
	// Normalize negative zero so "-0.0" does not create a second cell.
	if lat == 0 {
		lat = 0
	}
	if lon == 0 {
		lon = 0
	}
	return fmt.Sprintf("%.*f,%.*f", precision, lat, precision, lon), nil
}

// ParseLocation turns optional latitude and longitude form fields into a grid
// cell. Both fields must be present, or neither. The raw values are returned
// only as the cell; callers must not retain them.
func ParseLocation(latitude, longitude string) (string, error) {
	latitude = strings.TrimSpace(latitude)
	longitude = strings.TrimSpace(longitude)
	if latitude == "" && longitude == "" {
		return "", nil
	}
	if latitude == "" || longitude == "" {
		return "", ErrInvalidLocation
	}
	lat, err := strconv.ParseFloat(latitude, 64)
	if err != nil {
		return "", ErrInvalidLocation
	}
	lon, err := strconv.ParseFloat(longitude, 64)
	if err != nil {
		return "", ErrInvalidLocation
	}
	return CoarseCell(lat, lon, 1)
}

var languagePattern = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

// NormalizeLanguage validates an optional BCP 47 style tag and returns it in a
// canonical lower-case form.
func NormalizeLanguage(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	if len(value) > 32 || !languagePattern.MatchString(value) {
		return "", ErrInvalidLanguage
	}
	return strings.ToLower(value), nil
}
