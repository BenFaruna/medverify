package logging_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/logging"
)

func TestRedactMasksConfiguredSecrets(t *testing.T) {
	t.Parallel()

	redactor := logging.NewRedactor("minioadmin-secret", "s3cret")

	got := redactor.Redact("dial postgres://medverify:s3cret@db with key minioadmin-secret")
	assert.Equal(t, "dial postgres://medverify:[REDACTED]@db with key [REDACTED]", got)
	assert.NotContains(t, got, "s3cret")
}

func TestRedactMasksLongBase64LikeTokens(t *testing.T) {
	t.Parallel()

	blob := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("pack-photo"), 40))
	require.Greater(t, len(blob), 60)

	redactor := logging.NewRedactor()
	got := redactor.Redact("payload " + blob + " end")

	assert.Equal(t, "payload [REDACTED] end", got)
	assert.NotContains(t, got, blob)
}

func TestRedactLeavesOrdinaryTextAlone(t *testing.T) {
	t.Parallel()

	redactor := logging.NewRedactor("a-secret-value")
	text := "request id 9f8c1d2e method GET path /readyz status 200"

	assert.Equal(t, text, redactor.Redact(text))
}

func TestRedactMasksNestedSecretBeforeItsParent(t *testing.T) {
	t.Parallel()

	redactor := logging.NewRedactor("token", "token-with-suffix")

	got := redactor.Redact("value=token-with-suffix")
	assert.Equal(t, "value=[REDACTED]", got)
}

func TestRedactNilRedactorIsSafe(t *testing.T) {
	t.Parallel()

	var redactor *logging.Redactor
	assert.Equal(t, "plain text", redactor.Redact("plain text"))
	assert.Equal(t, "", redactor.Redact(""))
}

func TestNewLoggerEmitsJSONAndRedactsAttributes(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger, redactor := logging.New(logging.Options{
		Level:   slog.LevelInfo,
		Service: "medverify-test",
		Secrets: []string{"super-secret-key"},
		Writer:  &buf,
	})
	require.NotNil(t, redactor)

	logger.Info("storage failed",
		slog.String("endpoint", "localhost:9000"),
		slog.String("key", "super-secret-key"),
		slog.Any("err", errors.New("signature rejected for super-secret-key")),
	)

	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line))
	assert.Equal(t, "storage failed", line["msg"])
	assert.Equal(t, "medverify-test", line["service"])
	assert.Equal(t, "[REDACTED]", line["key"])
	assert.Equal(t, "signature rejected for [REDACTED]", line["err"])
	assert.NotContains(t, buf.String(), "super-secret-key")
}

func TestNewLoggerRespectsLevel(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger, _ := logging.New(logging.Options{Level: slog.LevelWarn, Writer: &buf})

	logger.Info("dropped")
	logger.Warn("kept")

	assert.NotContains(t, buf.String(), "dropped")
	assert.Contains(t, buf.String(), "kept")
	assert.True(t, strings.HasPrefix(strings.TrimSpace(buf.String()), "{"))
}
