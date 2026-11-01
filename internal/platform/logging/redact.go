package logging

import (
	"log/slog"
	"regexp"
	"sort"
	"strings"
)

// Redacted replaces a sensitive value in log output.
const Redacted = "[REDACTED]"

// minSecretLength ignores trivially short "secrets" (such as an empty string)
// so the redactor never mangles ordinary text.
const minSecretLength = 4

// base64Like matches long, unbroken runs of base64 or URL-safe base64
// characters. Image blobs, API tokens, and signed URLs look like this.
var base64Like = regexp.MustCompile(`[A-Za-z0-9+/=_-]{60,}`)

// Redactor masks configured secrets and long base64-like tokens in a string.
// The zero value is usable and masks only base64-like tokens.
type Redactor struct {
	secrets []string
}

// NewRedactor returns a Redactor that masks every provided secret. Secrets
// shorter than four characters are ignored: replacing them would corrupt
// ordinary log text.
func NewRedactor(secrets ...string) *Redactor {
	unique := make(map[string]struct{}, len(secrets))
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if len(secret) < minSecretLength {
			continue
		}
		unique[secret] = struct{}{}
	}

	ordered := make([]string, 0, len(unique))
	for secret := range unique {
		ordered = append(ordered, secret)
	}
	// Longest first so a secret that contains another secret is fully masked.
	sort.Slice(ordered, func(i, j int) bool {
		if len(ordered[i]) != len(ordered[j]) {
			return len(ordered[i]) > len(ordered[j])
		}
		return ordered[i] < ordered[j]
	})

	return &Redactor{secrets: ordered}
}

// Redact returns value with configured secrets and long base64-like tokens
// replaced. It is safe to call on a nil Redactor.
func (r *Redactor) Redact(value string) string {
	if value == "" {
		return value
	}
	if r != nil {
		for _, secret := range r.secrets {
			value = strings.ReplaceAll(value, secret, Redacted)
		}
	}
	return base64Like.ReplaceAllString(value, Redacted)
}

// replaceAttr is the slog ReplaceAttr hook that runs every string attribute and
// error through the redactor.
func (r *Redactor) replaceAttr(_ []string, attr slog.Attr) slog.Attr {
	switch attr.Value.Kind() {
	case slog.KindString:
		attr.Value = slog.StringValue(r.Redact(attr.Value.String()))
	case slog.KindAny:
		if err, ok := attr.Value.Any().(error); ok {
			attr.Value = slog.StringValue(r.Redact(err.Error()))
		}
	}
	return attr
}
