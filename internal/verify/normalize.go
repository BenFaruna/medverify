package verify

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// dosageAndPackWords are stripped before name comparison. They describe the
// presentation, not the product, and often appear on one face but not another.
var dosageAndPackWords = map[string]struct{}{
	"tablet": {}, "tablets": {}, "capsule": {}, "capsules": {},
	"syrup": {}, "suspension": {}, "solution": {}, "injection": {},
	"injections": {}, "infusion": {}, "cream": {}, "ointment": {},
	"gel": {}, "drops": {}, "drop": {}, "powder": {}, "sachet": {},
	"sachets": {}, "pack": {}, "packs": {}, "bottle": {}, "bottles": {},
	"blister": {}, "film": {}, "coated": {}, "oral": {}, "bp": {},
	"usp": {}, "ip": {},
}

// companySuffixes are stripped from manufacturer and applicant names. They do
// not distinguish one company from another.
var companySuffixes = map[string]struct{}{
	"ltd": {}, "limited": {}, "plc": {}, "inc": {}, "incorporated": {},
	"corp": {}, "corporation": {}, "company": {}, "co": {}, "gmbh": {},
	"sa": {}, "bv": {}, "nigeria": {}, "nig": {}, "pharmaceuticals": {},
	"pharmaceutical": {}, "pharma": {}, "laboratories": {}, "labs": {},
	"industries": {}, "enterprise": {}, "enterprises": {},
}

// NormalizeName prepares a product name for comparison. It is deliberately
// separate from storage normalization: it removes presentation words that would
// otherwise make the same product look different on two label faces.
func NormalizeName(raw string) string {
	return normalizeTokens(raw, dosageAndPackWords)
}

// NormalizeManufacturer prepares a company name for comparison.
func NormalizeManufacturer(raw string) string {
	return normalizeTokens(raw, companySuffixes)
}

func normalizeTokens(raw string, drop map[string]struct{}) string {
	value := norm.NFKC.String(raw)
	value = strings.ToLower(value)

	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}

	kept := make([]string, 0, 8)
	for _, token := range strings.Fields(b.String()) {
		if _, skip := drop[token]; skip {
			continue
		}
		kept = append(kept, token)
	}
	return strings.Join(kept, " ")
}

// Similarity is a Sørensen–Dice coefficient over character trigrams. It is the
// same shape as pg_trgm similarity while remaining deterministic and usable on
// plain structs in tests. Identical strings score 1.
func Similarity(a, b string) float64 {
	if a == b {
		return 1
	}
	left := trigrams(a)
	right := trigrams(b)
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	intersection := 0
	for gram := range left {
		if _, ok := right[gram]; ok {
			intersection++
		}
	}
	return 2 * float64(intersection) / float64(len(left)+len(right))
}

func trigrams(value string) map[string]struct{} {
	runes := []rune(value)
	if len(runes) < 3 {
		return nil
	}
	set := make(map[string]struct{}, len(runes)-2)
	for i := 0; i+2 < len(runes); i++ {
		set[string(runes[i:i+3])] = struct{}{}
	}
	return set
}
