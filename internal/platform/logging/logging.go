// Package logging builds the structured logger every process uses and keeps
// sensitive values out of log output.
//
// Two rules from the build tracker drive this package:
//
//   - Logs are JSON, one object per line.
//   - No images, API keys, passwords, or raw user text may be logged.
//
// The Redactor is the second rule's enforcement point. Register configured
// secrets once at startup and every log call passes through it.
package logging

import (
	"io"
	"log/slog"
	"os"
)

// Options configures the process logger.
type Options struct {
	// Level is the minimum level to emit.
	Level slog.Level
	// Service labels every line, for example "medverify-api".
	Service string
	// Secrets are values that must never appear in log output.
	Secrets []string
	// Writer receives log output. Defaults to os.Stdout.
	Writer io.Writer
}

// New returns a JSON logger writing to opts.Writer and the Redactor bound to
// it. Callers should keep the Redactor for any log values they build by hand.
func New(opts Options) (*slog.Logger, *Redactor) {
	writer := opts.Writer
	if writer == nil {
		writer = os.Stdout
	}

	redactor := NewRedactor(opts.Secrets...)
	handler := slog.NewJSONHandler(writer, &slog.HandlerOptions{
		Level:       opts.Level,
		ReplaceAttr: redactor.replaceAttr,
	})

	logger := slog.New(handler)
	if opts.Service != "" {
		logger = logger.With(slog.String("service", opts.Service))
	}
	return logger, redactor
}
