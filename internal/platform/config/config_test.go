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
}

func TestLoadFromOverridesDefaults(t *testing.T) {
	t.Parallel()

	env := append(baseEnv(),
		"APP_ENV=production",
		"LOG_LEVEL=debug",
		"HTTP_ADDR=0.0.0.0:9090",
		"HTTP_REQUEST_TIMEOUT=30s",
		"HTTP_MAX_BODY_BYTES=4194304",
		"DATABASE_MAX_CONNS=25",
		"DATABASE_MIN_CONNS=5",
		"STORAGE_USE_SSL=true",
		"JOBS_CONCURRENCY=8",
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
