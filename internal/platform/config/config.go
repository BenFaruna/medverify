// Package config loads and validates every setting MedVerify NG needs at
// startup. It is the only package allowed to read environment variables.
//
// Configuration is parsed once in main and passed down explicitly. Validation
// collects every problem it finds so an operator sees the full list in one go
// instead of fixing one variable per restart.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Environment is the deployment environment the process is running in.
type Environment string

// Supported environments.
const (
	EnvDevelopment Environment = "development"
	EnvTest        Environment = "test"
	EnvProduction  Environment = "production"
)

// Valid reports whether the environment is one of the known values.
func (e Environment) Valid() bool {
	switch e {
	case EnvDevelopment, EnvTest, EnvProduction:
		return true
	default:
		return false
	}
}

// Config is the fully parsed configuration for one process.
type Config struct {
	Env        Environment
	LogLevel   string
	HTTP       HTTP
	Database   Database
	Storage    Storage
	Jobs       Jobs
	Greenbook  Greenbook
	API        API
	Scan       Scan
	Vision     Vision
	Verify     Verify
	Safety     Safety
	Verdict    Verdict
	Embeddings Embeddings
	Assistant  Assistant
	Webhook    Webhook
	Web        Web
}

// Web holds the static MVP client settings. An empty Dir disables serving it,
// which a deployment that hosts the client elsewhere wants.
type Web struct {
	Dir string
}

// Webhook holds outbound delivery settings. A per-endpoint secret signs each
// payload; retries use exponential backoff until the attempt limit, then the
// delivery is dead-lettered for replay.
type Webhook struct {
	// Interval is how often the worker drains due deliveries.
	Interval time.Duration
	// Timeout bounds a single delivery attempt.
	Timeout time.Duration
}

// HTTP holds the HTTP server settings.
type HTTP struct {
	Addr            string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	RequestTimeout  time.Duration
	ShutdownTimeout time.Duration
	MaxBodyBytes    int64
}

// Database holds the Postgres connection settings.
type Database struct {
	URL            string
	MaxConns       int32
	MinConns       int32
	ConnectTimeout time.Duration
}

// Storage holds the S3-compatible object storage settings.
type Storage struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	Region    string
	UseSSL    bool
}

// Jobs holds the background worker settings.
type Jobs struct {
	Concurrency     int
	ShutdownTimeout time.Duration
}

// Greenbook holds the NAFDAC Greenbook ingestion settings. The kill switch is
// Enabled=false: the fetcher refuses every outbound call, including retries.
type Greenbook struct {
	Enabled                       bool
	BaseURL                       string
	UserAgent                     string
	RequestsPerSecond             float64
	MaxConcurrency                int
	PageSize                      int
	RequestTimeout                time.Duration
	Retries                       int
	CacheDir                      string
	CacheTTL                      time.Duration
	MaxPDFBytes                   int64
	MaxPDFPages                   int
	AllowedHosts                  []string
	SyncInterval                  time.Duration
	AnomalyMaxDropPercent         float64
	AnomalyMaxParseFailurePercent float64
}

// API holds the integrator API settings.
type API struct {
	// LegacyKey is the single shared key accepted by local development. It is
	// rejected in production: every production caller must have a
	// database-backed identity with its own scopes and quotas.
	LegacyKey string
	// CORSOrigins allowlists browser origins for /v1. An empty list sends no
	// CORS headers at all, which blocks browser callers rather than allowing
	// every origin. A wildcard is rejected in production.
	CORSOrigins []string
	// RatePerMin bounds requests per key per minute; IPRatePerMin bounds
	// requests per calling address. RateLimitBurst is the bucket size.
	RatePerMin     int
	IPRatePerMin   int
	RateLimitBurst int
}

// Vision holds provider selection, resilience, and pricing for label
// extraction. Model identifiers are pinned; floating aliases are not accepted.
type Vision struct {
	Provider                string
	Model                   string
	FallbackModel           string
	APIKey                  string
	BaseURL                 string
	MaxTokens               int
	Timeout                 time.Duration
	Retries                 int
	RetryBackoff            time.Duration
	CircuitFailureThreshold int
	CircuitCooldown         time.Duration
	MaxImageDimension       int
	InputPricePerMToken     float64
	OutputPricePerMToken    float64
}

// Verify holds registry-verification settings. Matching thresholds live in a
// versioned YAML file, not in environment variables, so the file's version can
// be stored with every result. ConfigFile may point at an override.
type Verify struct {
	ConfigFile string
}

// Safety holds the phase 06 checker settings. Thresholds and the severity map
// live in the versioned YAML file named by ConfigFile; an empty value selects
// the embedded default.
type Safety struct {
	ConfigFile string
}

// Verdict holds the phase 07 engine settings. Bands, weights, and the gate live
// in the versioned YAML file named by ConfigFile; an empty value selects the
// embedded default.
type Verdict struct {
	ConfigFile string
}

// Embeddings holds the phase 08 retrieval settings. The provider decision is
// still open, so "http" is a generic OpenAI-compatible client rather than a
// vendor-specific integration, and "fake" is only for local development and
// tests. Dimension must match the smpc_chunks column.
type Embeddings struct {
	Provider      string
	BaseURL       string
	APIKey        string
	Model         string
	Dimension     int
	BatchSize     int
	Timeout       time.Duration
	BackfillLimit int
	Delay         time.Duration
	// IndexInterval schedules a worker indexing pass. Zero leaves it to the
	// operator, which is the default until the provider decision is made.
	IndexInterval time.Duration
}

// Assistant holds the grounded Q&A settings. The router and verifier use the
// small model; the orchestrator uses the strong model. Model identifiers are
// pinned values supplied by the operator.
type Assistant struct {
	Provider         string
	Model            string
	SmallModel       string
	APIKey           string
	BaseURL          string
	MaxTokens        int
	Timeout          time.Duration
	MaxSteps         int
	MaxOutputTokens  int
	MaxToolCalls     int
	MaxTurns         int
	ConversationTTL  time.Duration
	MaxQuestionBytes int
	Languages        []string
}

// Scan holds upload and extraction limits.
type Scan struct {
	MaxImages       int
	MaxImageBytes   int64
	MaxRequestBytes int64
	MaxPixels       int64
	SyncWait        time.Duration
	PollInterval    time.Duration
	MinConfidence   float64
}

// Problem is one invalid or missing configuration value. Values are never
// included: a bad secret must not end up in a log line.
type Problem struct {
	Field   string
	Message string
}

func (p Problem) String() string { return p.Field + ": " + p.Message }

// Error is returned when configuration is invalid. It reports every problem at
// once.
type Error struct {
	Problems []Problem
}

func (e *Error) Error() string {
	if len(e.Problems) == 1 {
		return "invalid configuration: " + e.Problems[0].String()
	}
	lines := make([]string, 0, len(e.Problems)+1)
	lines = append(lines, fmt.Sprintf("invalid configuration (%d problems):", len(e.Problems)))
	for _, p := range e.Problems {
		lines = append(lines, "  - "+p.String())
	}
	return strings.Join(lines, "\n")
}

// Load reads configuration from the process environment.
func Load() (Config, error) {
	return LoadFrom(os.Environ())
}

// LoadFrom parses configuration from a list of "KEY=VALUE" entries. It exists
// so tests never have to mutate the real environment.
func LoadFrom(environ []string) (Config, error) {
	env := make(map[string]string, len(environ))
	for _, kv := range environ {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		env[key] = value
	}

	l := &loader{env: env}
	appEnv := Environment(l.oneOf("APP_ENV", string(EnvDevelopment), string(EnvDevelopment), string(EnvTest), string(EnvProduction)))
	// The development key is a convenience default. Production must never
	// inherit it, or a misconfigured deploy would accept a well-known secret.
	legacyKeyDefault := "dev-api-key"
	if appEnv == EnvProduction {
		legacyKeyDefault = ""
	}
	cfg := Config{
		Env:      appEnv,
		LogLevel: l.oneOf("LOG_LEVEL", "info", "debug", "info", "warn", "error"),
		HTTP: HTTP{
			Addr:            l.str("HTTP_ADDR", ":8080"),
			ReadTimeout:     l.duration("HTTP_READ_TIMEOUT", 10*time.Second),
			WriteTimeout:    l.duration("HTTP_WRITE_TIMEOUT", 20*time.Second),
			IdleTimeout:     l.duration("HTTP_IDLE_TIMEOUT", 60*time.Second),
			RequestTimeout:  l.duration("HTTP_REQUEST_TIMEOUT", 15*time.Second),
			ShutdownTimeout: l.duration("HTTP_SHUTDOWN_TIMEOUT", 10*time.Second),
			MaxBodyBytes:    l.bytes("HTTP_MAX_BODY_BYTES", 1<<20),
		},
		Database: Database{
			URL:            l.required("DATABASE_URL"),
			MaxConns:       int32(l.positiveInt("DATABASE_MAX_CONNS", 10)),
			MinConns:       int32(l.nonNegativeInt("DATABASE_MIN_CONNS", 0)),
			ConnectTimeout: l.duration("DATABASE_CONNECT_TIMEOUT", 5*time.Second),
		},
		Storage: Storage{
			Endpoint:  l.required("STORAGE_ENDPOINT"),
			AccessKey: l.required("STORAGE_ACCESS_KEY"),
			SecretKey: l.required("STORAGE_SECRET_KEY"),
			Bucket:    l.required("STORAGE_BUCKET"),
			Region:    l.str("STORAGE_REGION", "us-east-1"),
			UseSSL:    l.boolean("STORAGE_USE_SSL", false),
		},
		Jobs: Jobs{
			Concurrency:     l.positiveInt("JOBS_CONCURRENCY", 4),
			ShutdownTimeout: l.duration("JOBS_SHUTDOWN_TIMEOUT", 15*time.Second),
		},
		Greenbook: Greenbook{
			Enabled:                       l.boolean("GREENBOOK_ENABLED", true),
			BaseURL:                       l.str("GREENBOOK_BASE_URL", "https://greenbook.nafdac.gov.ng"),
			UserAgent:                     l.str("GREENBOOK_USER_AGENT", "MedVerifyNG/0.1 (+https://github.com/benfaruna/medverify)"),
			RequestsPerSecond:             l.positiveFloat("GREENBOOK_RPS", 1),
			MaxConcurrency:                l.positiveInt("GREENBOOK_MAX_CONCURRENCY", 2),
			PageSize:                      l.positiveInt("GREENBOOK_PAGE_SIZE", 100),
			RequestTimeout:                l.duration("GREENBOOK_REQUEST_TIMEOUT", 30*time.Second),
			Retries:                       l.nonNegativeInt("GREENBOOK_RETRIES", 3),
			CacheDir:                      l.str("GREENBOOK_CACHE_DIR", filepath.Join(os.TempDir(), "medverify-greenbook-cache")),
			CacheTTL:                      l.duration("GREENBOOK_CACHE_TTL", time.Hour),
			MaxPDFBytes:                   l.bytes("GREENBOOK_MAX_PDF_BYTES", 50<<20),
			MaxPDFPages:                   l.positiveInt("GREENBOOK_MAX_PDF_PAGES", 100),
			AllowedHosts:                  l.csv("GREENBOOK_ALLOWED_HOSTS", "greenbook.nafdac.gov.ng,admin.greenbook.nafdac.gov.ng"),
			SyncInterval:                  l.duration("GREENBOOK_SYNC_INTERVAL", 24*time.Hour),
			AnomalyMaxDropPercent:         l.percent("GREENBOOK_ANOMALY_MAX_DROP_PERCENT", 30),
			AnomalyMaxParseFailurePercent: l.percent("GREENBOOK_ANOMALY_MAX_PARSE_FAILURE_PERCENT", 10),
		},
		API: API{
			LegacyKey:      l.str("API_LEGACY_KEY", legacyKeyDefault),
			CORSOrigins:    l.csv("API_CORS_ORIGINS", ""),
			RatePerMin:     l.positiveInt("API_RATE_PER_MIN", 60),
			IPRatePerMin:   l.positiveInt("API_IP_RATE_PER_MIN", 240),
			RateLimitBurst: l.positiveInt("API_RATE_LIMIT_BURST", 30),
		},
		Scan: Scan{
			MaxImages:       l.positiveInt("SCAN_MAX_IMAGES", 4),
			MaxImageBytes:   l.bytes("SCAN_MAX_IMAGE_BYTES", 10<<20),
			MaxRequestBytes: l.bytes("SCAN_MAX_REQUEST_BYTES", 45<<20),
			MaxPixels:       l.positiveInt64("SCAN_MAX_PIXELS", 40_000_000),
			SyncWait:        l.duration("SCAN_SYNC_WAIT", 5*time.Second),
			PollInterval:    l.duration("SCAN_POLL_INTERVAL", 100*time.Millisecond),
			MinConfidence:   l.fraction("SCAN_MIN_CONFIDENCE", 0.5),
		},
		Verify: Verify{
			ConfigFile: l.str("VERIFY_CONFIG_FILE", ""),
		},
		Safety: Safety{
			ConfigFile: l.str("SAFETY_CONFIG_FILE", ""),
		},
		Verdict: Verdict{
			ConfigFile: l.str("VERDICT_CONFIG_FILE", ""),
		},
		Embeddings: Embeddings{
			Provider:      l.oneOf("EMBEDDINGS_PROVIDER", "fake", "fake", "http"),
			BaseURL:       l.str("EMBEDDINGS_BASE_URL", ""),
			APIKey:        l.str("EMBEDDINGS_API_KEY", ""),
			Model:         l.str("EMBEDDINGS_MODEL", ""),
			Dimension:     l.positiveInt("EMBEDDINGS_DIMENSION", 1024),
			BatchSize:     l.positiveInt("EMBEDDINGS_BATCH_SIZE", 64),
			Timeout:       l.duration("EMBEDDINGS_TIMEOUT", 30*time.Second),
			BackfillLimit: l.positiveInt("EMBEDDINGS_BACKFILL_LIMIT", 50),
			Delay:         l.duration("EMBEDDINGS_DELAY", 0),
			IndexInterval: l.duration("EMBEDDINGS_INDEX_INTERVAL", 0),
		},
		Webhook: Webhook{
			Interval: l.duration("WEBHOOK_INTERVAL", 15*time.Second),
			Timeout:  l.duration("WEBHOOK_TIMEOUT", 10*time.Second),
		},
		Web: Web{
			Dir: l.str("WEB_DIR", "web"),
		},
		Assistant: Assistant{
			Provider:         l.oneOf("ASSISTANT_LLM_PROVIDER", "fake", "fake", "claude", "openai"),
			Model:            l.str("ASSISTANT_MODEL", ""),
			SmallModel:       l.str("ASSISTANT_SMALL_MODEL", ""),
			APIKey:           l.str("ASSISTANT_API_KEY", ""),
			BaseURL:          l.str("ASSISTANT_BASE_URL", ""),
			MaxTokens:        l.positiveInt("ASSISTANT_MAX_TOKENS", 1024),
			Timeout:          l.duration("ASSISTANT_TIMEOUT", 30*time.Second),
			MaxSteps:         l.positiveInt("ASSISTANT_MAX_STEPS", 4),
			MaxOutputTokens:  l.positiveInt("ASSISTANT_MAX_OUTPUT_TOKENS", 4096),
			MaxToolCalls:     l.positiveInt("ASSISTANT_MAX_TOOL_CALLS", 8),
			MaxTurns:         l.positiveInt("ASSISTANT_MAX_TURNS", 6),
			ConversationTTL:  l.duration("ASSISTANT_CONVERSATION_TTL", 24*time.Hour),
			MaxQuestionBytes: l.positiveInt("ASSISTANT_MAX_QUESTION_BYTES", 1000),
			Languages:        l.csv("ASSISTANT_LANGUAGES", "en"),
		},
		Vision: Vision{
			Provider:                l.oneOf("VISION_PROVIDER", "fake", "fake", "claude", "openai"),
			Model:                   l.str("VISION_MODEL", ""),
			FallbackModel:           l.str("VISION_FALLBACK_MODEL", ""),
			APIKey:                  l.str("VISION_API_KEY", ""),
			BaseURL:                 l.str("VISION_BASE_URL", ""),
			MaxTokens:               l.positiveInt("VISION_MAX_TOKENS", 4096),
			Timeout:                 l.duration("VISION_TIMEOUT", 45*time.Second),
			Retries:                 l.nonNegativeInt("VISION_RETRIES", 2),
			RetryBackoff:            l.duration("VISION_RETRY_BACKOFF", 500*time.Millisecond),
			CircuitFailureThreshold: l.positiveInt("VISION_CIRCUIT_FAILURE_THRESHOLD", 5),
			CircuitCooldown:         l.duration("VISION_CIRCUIT_COOLDOWN", 30*time.Second),
			MaxImageDimension:       l.positiveInt("VISION_MAX_IMAGE_DIMENSION", 1568),
			InputPricePerMToken:     l.nonNegativeFloat("VISION_INPUT_PRICE_PER_MTOKEN", 3),
			OutputPricePerMToken:    l.nonNegativeFloat("VISION_OUTPUT_PRICE_PER_MTOKEN", 15),
		},
	}

	l.validateURL("DATABASE_URL", cfg.Database.URL)
	l.validateHTTPURL("GREENBOOK_BASE_URL", cfg.Greenbook.BaseURL)
	if cfg.Database.MinConns > cfg.Database.MaxConns {
		l.add("DATABASE_MIN_CONNS", "must not be greater than DATABASE_MAX_CONNS")
	}
	if len(cfg.Greenbook.AllowedHosts) == 0 {
		l.add("GREENBOOK_ALLOWED_HOSTS", "must list at least one leaflet host")
	}
	if cfg.Env == EnvProduction && cfg.API.LegacyKey != "" {
		l.add("API_LEGACY_KEY", "must be empty in production; issue database-backed API keys instead")
	}
	for _, origin := range cfg.API.CORSOrigins {
		if origin == "*" {
			l.add("API_CORS_ORIGINS", "must list exact origins, not a wildcard")
			break
		}
	}
	if cfg.Env == EnvProduction && len(cfg.API.CORSOrigins) == 0 {
		l.add("API_CORS_ORIGINS", "must name at least one web client origin in production")
	}
	if cfg.Scan.MaxRequestBytes < cfg.Scan.MaxImageBytes*int64(cfg.Scan.MaxImages) {
		l.add("SCAN_MAX_REQUEST_BYTES", "must be at least SCAN_MAX_IMAGE_BYTES * SCAN_MAX_IMAGES")
	}
	if cfg.Vision.Provider != "fake" {
		if cfg.Vision.Model == "" {
			l.add("VISION_MODEL", "is required for a real provider; pin an exact model id")
		}
		if cfg.Vision.APIKey == "" {
			l.add("VISION_API_KEY", "is required for a real provider")
		}
	}
	if cfg.Env == EnvProduction && cfg.Vision.Provider == "fake" {
		l.add("VISION_PROVIDER", "must not be fake in production")
	}
	l.validateHTTPURL("VISION_BASE_URL", cfg.Vision.BaseURL)

	if cfg.Assistant.Provider != "fake" {
		if cfg.Assistant.Model == "" {
			l.add("ASSISTANT_MODEL", "is required for a real provider; pin an exact model id")
		}
		if cfg.Assistant.SmallModel == "" {
			l.add("ASSISTANT_SMALL_MODEL", "is required for a real provider; pin the router/verifier model id")
		}
		if cfg.Assistant.APIKey == "" {
			l.add("ASSISTANT_API_KEY", "is required for a real provider")
		}
	}
	if cfg.Env == EnvProduction && cfg.Assistant.Provider == "fake" {
		l.add("ASSISTANT_LLM_PROVIDER", "must not be fake in production")
	}
	if len(cfg.Assistant.Languages) == 0 {
		l.add("ASSISTANT_LANGUAGES", "must list at least one reviewed language")
	}
	l.validateHTTPURL("ASSISTANT_BASE_URL", cfg.Assistant.BaseURL)

	if len(l.problems) > 0 {
		return Config{}, &Error{Problems: l.problems}
	}
	return cfg, nil
}

// LogLevelValue maps the configured level name to a slog level.
func (c Config) LogLevelValue() slog.Level {
	switch c.LogLevel {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Secrets returns the configured secret values so they can be registered with
// the log redactor. The slice is never logged directly.
func (c Config) Secrets() []string {
	secrets := []string{
		c.Storage.SecretKey, c.Storage.AccessKey, c.Vision.APIKey,
		c.Embeddings.APIKey, c.Assistant.APIKey,
	}
	if password := databasePassword(c.Database.URL); password != "" {
		secrets = append(secrets, password)
	}
	return secrets
}

func databasePassword(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return ""
	}
	password, _ := u.User.Password()
	return password
}

type loader struct {
	env      map[string]string
	problems []Problem
}

func (l *loader) add(field, message string) {
	l.problems = append(l.problems, Problem{Field: field, Message: message})
}

func (l *loader) raw(key string) (string, bool) {
	value, ok := l.env[key]
	value = strings.TrimSpace(value)
	return value, ok && value != ""
}

func (l *loader) required(key string) string {
	value, ok := l.raw(key)
	if !ok {
		l.add(key, "is required")
	}
	return value
}

func (l *loader) str(key, fallback string) string {
	if value, ok := l.raw(key); ok {
		return value
	}
	return fallback
}

func (l *loader) oneOf(key, fallback string, allowed ...string) string {
	value, ok := l.raw(key)
	if !ok {
		return fallback
	}
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	l.add(key, "must be one of "+strings.Join(allowed, ", "))
	return fallback
}

func (l *loader) duration(key string, fallback time.Duration) time.Duration {
	value, ok := l.raw(key)
	if !ok {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		l.add(key, "must be a duration such as 15s or 1m")
		return fallback
	}
	if parsed <= 0 {
		l.add(key, "must be greater than zero")
		return fallback
	}
	return parsed
}

func (l *loader) positiveInt(key string, fallback int) int {
	parsed, ok := l.int(key)
	if !ok {
		return fallback
	}
	if parsed <= 0 {
		l.add(key, "must be greater than zero")
		return fallback
	}
	return parsed
}

func (l *loader) positiveFloat(key string, fallback float64) float64 {
	value, ok := l.raw(key)
	if !ok {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		l.add(key, "must be a number")
		return fallback
	}
	if parsed <= 0 {
		l.add(key, "must be greater than zero")
		return fallback
	}
	return parsed
}

func (l *loader) nonNegativeFloat(key string, fallback float64) float64 {
	value, ok := l.raw(key)
	if !ok {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		l.add(key, "must be a number")
		return fallback
	}
	if parsed < 0 {
		l.add(key, "must not be negative")
		return fallback
	}
	return parsed
}

func (l *loader) percent(key string, fallback float64) float64 {
	value, ok := l.raw(key)
	if !ok {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		l.add(key, "must be a percentage")
		return fallback
	}
	if parsed < 0 || parsed >= 100 {
		l.add(key, "must be between 0 and 100 (exclusive)")
		return fallback
	}
	return parsed
}

func (l *loader) fraction(key string, fallback float64) float64 {
	value, ok := l.raw(key)
	if !ok {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		l.add(key, "must be a number between 0 and 1")
		return fallback
	}
	if parsed < 0 || parsed > 1 {
		l.add(key, "must be between 0 and 1")
		return fallback
	}
	return parsed
}

func (l *loader) csv(key, fallback string) []string {
	value, ok := l.raw(key)
	if !ok {
		value = fallback
	}
	parts := strings.Split(value, ",")
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			items = append(items, part)
		}
	}
	return items
}

func (l *loader) nonNegativeInt(key string, fallback int) int {
	parsed, ok := l.int(key)
	if !ok {
		return fallback
	}
	if parsed < 0 {
		l.add(key, "must not be negative")
		return fallback
	}
	return parsed
}

func (l *loader) int(key string) (int, bool) {
	value, ok := l.raw(key)
	if !ok {
		return 0, false
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		l.add(key, "must be an integer")
		return 0, false
	}
	return parsed, true
}

func (l *loader) bytes(key string, fallback int64) int64 {
	value, ok := l.raw(key)
	if !ok {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		l.add(key, "must be a number of bytes")
		return fallback
	}
	if parsed <= 0 {
		l.add(key, "must be greater than zero")
		return fallback
	}
	return parsed
}

func (l *loader) positiveInt64(key string, fallback int64) int64 {
	value, ok := l.raw(key)
	if !ok {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		l.add(key, "must be a positive integer")
		return fallback
	}
	if parsed <= 0 {
		l.add(key, "must be greater than zero")
		return fallback
	}
	return parsed
}

func (l *loader) boolean(key string, fallback bool) bool {
	value, ok := l.raw(key)
	if !ok {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		l.add(key, "must be true or false")
		return fallback
	}
	return parsed
}

func (l *loader) validateURL(key, value string) {
	if value == "" {
		return
	}
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		l.add(key, "must be a postgres:// or postgresql:// URL with a host")
	}
}

func (l *loader) validateHTTPURL(key, value string) {
	if value == "" {
		return
	}
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		l.add(key, "must be an http:// or https:// URL with a host")
	}
}

// IsValidationError reports whether err is a configuration validation error and
// returns it.
func IsValidationError(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}
