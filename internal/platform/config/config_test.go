package config_test

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/config"
)

func baseEnv() []string {
	return []string{
		"DATABASE_URL=postgres://medverify:s3cret@localhost:5432/medverify?sslmode=disable",
		"STORAGE_ENDPOINT=localhost:9000",
		"STORAGE_ACCESS_KEY=minioadmin",
		"STORAGE_SECRET_KEY=minioadmin-secret",
		"STORAGE_BUCKET=medverify",
	}
}

func TestLoadFromValidEnvironment(t *testing.T) {
	t.Parallel()

	cfg, err := config.LoadFrom(baseEnv())
	require.NoError(t, err)

	assert.Equal(t, config.EnvDevelopment, cfg.Env)
	assert.Equal(t, "info", cfg.LogLevel)
	assert.Equal(t, ":8080", cfg.HTTP.Addr)
	assert.Equal(t, 15*time.Second, cfg.HTTP.RequestTimeout)
	assert.Equal(t, int64(1<<20), cfg.HTTP.MaxBodyBytes)
	assert.Equal(t, int32(10), cfg.Database.MaxConns)
	assert.Equal(t, "medverify", cfg.Storage.Bucket)
	assert.False(t, cfg.Storage.UseSSL)
	assert.Equal(t, 4, cfg.Jobs.Concurrency)

	assert.Equal(t, "dev-api-key", cfg.API.LegacyKey)
	assert.Equal(t, 4, cfg.Scan.MaxImages)
	assert.Equal(t, int64(10<<20), cfg.Scan.MaxImageBytes)
	assert.Equal(t, int64(45<<20), cfg.Scan.MaxRequestBytes)
	assert.Equal(t, int64(40_000_000), cfg.Scan.MaxPixels)
	assert.Equal(t, 5*time.Second, cfg.Scan.SyncWait)
	assert.Equal(t, 100*time.Millisecond, cfg.Scan.PollInterval)
	assert.Equal(t, 0.5, cfg.Scan.MinConfidence)

	assert.Equal(t, "fake", cfg.Vision.Provider)
	assert.Equal(t, 4096, cfg.Vision.MaxTokens)
	assert.Equal(t, 45*time.Second, cfg.Vision.Timeout)
	assert.Equal(t, 2, cfg.Vision.Retries)
	assert.Equal(t, 1568, cfg.Vision.MaxImageDimension)
	assert.Equal(t, 3.0, cfg.Vision.InputPricePerMToken)
	assert.Equal(t, 15.0, cfg.Vision.OutputPricePerMToken)

	assert.Equal(t, "fake", cfg.Assistant.Provider)
	assert.Equal(t, 4, cfg.Assistant.MaxSteps)
	assert.Equal(t, 6, cfg.Assistant.MaxTurns)
	assert.Equal(t, []string{"en"}, cfg.Assistant.Languages)

	assert.True(t, cfg.Greenbook.Enabled)
	assert.Equal(t, "https://greenbook.nafdac.gov.ng", cfg.Greenbook.BaseURL)
	assert.Equal(t, 1.0, cfg.Greenbook.RequestsPerSecond)
	assert.Equal(t, 100, cfg.Greenbook.PageSize)
	assert.Equal(t, 3, cfg.Greenbook.Retries)
	assert.Equal(t, time.Hour, cfg.Greenbook.CacheTTL)
	assert.Equal(t, int64(50<<20), cfg.Greenbook.MaxPDFBytes)
	assert.Equal(t, 100, cfg.Greenbook.MaxPDFPages)
	assert.ElementsMatch(t,
		[]string{"greenbook.nafdac.gov.ng", "admin.greenbook.nafdac.gov.ng"},
		cfg.Greenbook.AllowedHosts,
	)
	assert.Equal(t, 24*time.Hour, cfg.Greenbook.SyncInterval)
	assert.Equal(t, 30.0, cfg.Greenbook.AnomalyMaxDropPercent)
	assert.Equal(t, 10.0, cfg.Greenbook.AnomalyMaxParseFailurePercent)
}

func TestLoadFromOverridesDefaults(t *testing.T) {
	t.Parallel()

	env := append(baseEnv(),
		"APP_ENV=production",
		"LOG_LEVEL=debug",
		"HTTP_ADDR=0.0.0.0:9090",
		"API_CORS_ORIGINS=https://app.medverify.test",
		"HTTP_REQUEST_TIMEOUT=30s",
		"HTTP_MAX_BODY_BYTES=4194304",
		"DATABASE_MAX_CONNS=25",
		"DATABASE_MIN_CONNS=5",
		"STORAGE_USE_SSL=true",
		"JOBS_CONCURRENCY=8",
		"GREENBOOK_ENABLED=false",
		"GREENBOOK_BASE_URL=http://localhost:9001",
		"GREENBOOK_RPS=0.5",
		"GREENBOOK_PAGE_SIZE=50",
		"GREENBOOK_RETRIES=1",
		"GREENBOOK_CACHE_TTL=10m",
		"GREENBOOK_ALLOWED_HOSTS=example.test",
		"GREENBOOK_SYNC_INTERVAL=6h",
		"GREENBOOK_ANOMALY_MAX_DROP_PERCENT=50",
		"VISION_PROVIDER=claude",
		"VISION_MODEL=claude-sonnet-4-5-20250929",
		"VISION_API_KEY=test-vision-key",
		"ASSISTANT_LLM_PROVIDER=claude",
		"ASSISTANT_MODEL=claude-sonnet-4-5-20250929",
		"ASSISTANT_SMALL_MODEL=claude-haiku-4-5-20251001",
		"ASSISTANT_API_KEY=test-assistant-key",
	)

	cfg, err := config.LoadFrom(env)
	require.NoError(t, err)

	assert.Equal(t, config.EnvProduction, cfg.Env)
	assert.Equal(t, slog.LevelDebug, cfg.LogLevelValue())
	assert.Equal(t, "0.0.0.0:9090", cfg.HTTP.Addr)
	assert.Equal(t, 30*time.Second, cfg.HTTP.RequestTimeout)
	assert.Equal(t, int64(4194304), cfg.HTTP.MaxBodyBytes)
	assert.Equal(t, int32(25), cfg.Database.MaxConns)
	assert.Equal(t, int32(5), cfg.Database.MinConns)
	assert.True(t, cfg.Storage.UseSSL)
	assert.Equal(t, 8, cfg.Jobs.Concurrency)
	assert.Empty(t, cfg.API.LegacyKey, "production must not accept a shared legacy key")
	assert.Equal(t, []string{"https://app.medverify.test"}, cfg.API.CORSOrigins)
	assert.Equal(t, "claude", cfg.Vision.Provider)
	assert.Equal(t, "claude-sonnet-4-5-20250929", cfg.Vision.Model)
	assert.Contains(t, cfg.Secrets(), "test-vision-key")
	assert.Contains(t, cfg.Secrets(), "test-assistant-key")
	assert.Equal(t, "claude", cfg.Assistant.Provider)
	assert.Equal(t, "claude-sonnet-4-5-20250929", cfg.Assistant.Model)
	assert.Equal(t, "claude-haiku-4-5-20251001", cfg.Assistant.SmallModel)

	assert.False(t, cfg.Greenbook.Enabled)
	assert.Equal(t, "http://localhost:9001", cfg.Greenbook.BaseURL)
	assert.Equal(t, 0.5, cfg.Greenbook.RequestsPerSecond)
	assert.Equal(t, 50, cfg.Greenbook.PageSize)
	assert.Equal(t, 1, cfg.Greenbook.Retries)
	assert.Equal(t, 10*time.Minute, cfg.Greenbook.CacheTTL)
	assert.Equal(t, []string{"example.test"}, cfg.Greenbook.AllowedHosts)
	assert.Equal(t, 6*time.Hour, cfg.Greenbook.SyncInterval)
	assert.Equal(t, 50.0, cfg.Greenbook.AnomalyMaxDropPercent)
}

func TestLoadFromMissingRequiredListsEveryProblem(t *testing.T) {
	t.Parallel()

	_, err := config.LoadFrom(nil)
	require.Error(t, err)

	validation, ok := config.IsValidationError(err)
	require.True(t, ok, "expected a validation error, got %T", err)

	fields := make([]string, 0, len(validation.Problems))
	for _, p := range validation.Problems {
		fields = append(fields, p.Field)
	}
	assert.ElementsMatch(t,
		[]string{"DATABASE_URL", "STORAGE_ENDPOINT", "STORAGE_ACCESS_KEY", "STORAGE_SECRET_KEY", "STORAGE_BUCKET"},
		fields,
	)
	assert.Contains(t, err.Error(), "5 problems")
}

func TestLoadFromMalformedValuesListsEveryProblem(t *testing.T) {
	t.Parallel()

	env := append(baseEnv(),
		"APP_ENV=staging",
		"LOG_LEVEL=verbose",
		"HTTP_REQUEST_TIMEOUT=soon",
		"HTTP_MAX_BODY_BYTES=lots",
		"DATABASE_MAX_CONNS=ten",

		"DATABASE_MIN_CONNS=11",
		"DATABASE_URL=mysql://localhost/db",
		"STORAGE_USE_SSL=maybe",
		"JOBS_CONCURRENCY=0",
		"GREENBOOK_BASE_URL=greenbook",
		"GREENBOOK_RPS=fast",
		"GREENBOOK_PAGE_SIZE=0",
		"GREENBOOK_RETRIES=-1",
		"GREENBOOK_CACHE_TTL=soon",
		"GREENBOOK_MAX_PDF_BYTES=big",
		"GREENBOOK_MAX_PDF_PAGES=0",
		"GREENBOOK_ALLOWED_HOSTS= , ",
		"GREENBOOK_SYNC_INTERVAL=nightly",
		"GREENBOOK_ANOMALY_MAX_DROP_PERCENT=100",
		"GREENBOOK_ANOMALY_MAX_PARSE_FAILURE_PERCENT=-1",
	)

	_, err := config.LoadFrom(env)
	require.Error(t, err)

	validation, ok := config.IsValidationError(err)
	require.True(t, ok)

	fields := make([]string, 0, len(validation.Problems))
	for _, p := range validation.Problems {
		fields = append(fields, p.Field)
	}
	assert.ElementsMatch(t, []string{
		"APP_ENV",
		"LOG_LEVEL",
		"HTTP_REQUEST_TIMEOUT",
		"HTTP_MAX_BODY_BYTES",
		"DATABASE_MAX_CONNS",
		"DATABASE_MIN_CONNS",
		"DATABASE_URL",
		"STORAGE_USE_SSL",
		"JOBS_CONCURRENCY",
		"GREENBOOK_BASE_URL",
		"GREENBOOK_RPS",
		"GREENBOOK_PAGE_SIZE",
		"GREENBOOK_RETRIES",
		"GREENBOOK_CACHE_TTL",
		"GREENBOOK_MAX_PDF_BYTES",
		"GREENBOOK_MAX_PDF_PAGES",
		"GREENBOOK_ALLOWED_HOSTS",
		"GREENBOOK_SYNC_INTERVAL",
		"GREENBOOK_ANOMALY_MAX_DROP_PERCENT",
		"GREENBOOK_ANOMALY_MAX_PARSE_FAILURE_PERCENT",
	}, fields)
}

func TestLoadFromNeverEchoesSecretValues(t *testing.T) {
	t.Parallel()

	env := append(baseEnv(), "STORAGE_USE_SSL=maybe")
	_, err := config.LoadFrom(env)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "minioadmin-secret")
	assert.NotContains(t, err.Error(), "s3cret")
}

func TestSecretsDoesNotExposeWholeURL(t *testing.T) {
	t.Parallel()

	cfg, err := config.LoadFrom(baseEnv())
	require.NoError(t, err)

	secrets := cfg.Secrets()
	assert.Contains(t, secrets, "minioadmin-secret")
	assert.Contains(t, secrets, "s3cret")
	for _, secret := range secrets {
		assert.False(t, strings.Contains(secret, "DATABASE_URL"))
	}
}

func TestLogLevelValue(t *testing.T) {
	t.Parallel()

	cases := map[string]slog.Level{
		"debug": slog.LevelDebug,
		"info":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
	}

	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := config.Config{LogLevel: name}
			assert.Equal(t, want, cfg.LogLevelValue())
		})
	}
}

func TestEnvironmentValid(t *testing.T) {
	t.Parallel()

	assert.True(t, config.EnvDevelopment.Valid())
	assert.True(t, config.EnvTest.Valid())
	assert.True(t, config.EnvProduction.Valid())
	assert.False(t, config.Environment("staging").Valid())
}

func TestProductionRejectsTheFakeAssistantProvider(t *testing.T) {
	t.Parallel()
	env := append(baseEnv(),
		"APP_ENV=production",
		"API_CORS_ORIGINS=https://app.medverify.test",
		"VISION_PROVIDER=claude",
		"VISION_MODEL=claude-sonnet-4-5-20250929",
		"VISION_API_KEY=test-vision-key",
	)
	_, err := config.LoadFrom(env)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ASSISTANT_LLM_PROVIDER")
}

func TestClaudeAssistantRequiresPinnedModels(t *testing.T) {
	t.Parallel()
	env := append(baseEnv(),
		"ASSISTANT_LLM_PROVIDER=claude",
		"ASSISTANT_API_KEY=test-key",
	)
	_, err := config.LoadFrom(env)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ASSISTANT_MODEL")
	assert.Contains(t, err.Error(), "ASSISTANT_SMALL_MODEL")
}
