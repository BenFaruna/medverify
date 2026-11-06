// Command api serves MedVerify NG's HTTP API.
//
// Process modes: cmd/api serves HTTP, cmd/worker runs background jobs, and
// cmd/admin holds one-off commands. They share the internal packages and the
// same database.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	medapi "github.com/benfaruna/medverify/api"
	"github.com/benfaruna/medverify/internal/account"
	"github.com/benfaruna/medverify/internal/assistant"
	"github.com/benfaruna/medverify/internal/llm"
	"github.com/benfaruna/medverify/internal/platform/auth"
	"github.com/benfaruna/medverify/internal/platform/config"
	"github.com/benfaruna/medverify/internal/platform/db"
	"github.com/benfaruna/medverify/internal/platform/httpx"
	"github.com/benfaruna/medverify/internal/platform/jobs"
	"github.com/benfaruna/medverify/internal/platform/logging"
	"github.com/benfaruna/medverify/internal/platform/metrics"
	"github.com/benfaruna/medverify/internal/platform/ratelimit"
	"github.com/benfaruna/medverify/internal/rag"
	"github.com/benfaruna/medverify/internal/scan"
	"github.com/benfaruna/medverify/internal/storage"
	"github.com/benfaruna/medverify/internal/vision"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "api: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger, _ := logging.New(logging.Options{
		Level:   cfg.LogLevelValue(),
		Service: "medverify-api",
		Secrets: cfg.Secrets(),
	})
	slog.SetDefault(logger)

	logger.Info("starting api", slog.String("env", string(cfg.Env)))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer pool.Close()

	store, err := storage.NewS3(storage.Options{
		Endpoint:  cfg.Storage.Endpoint,
		AccessKey: cfg.Storage.AccessKey,
		SecretKey: cfg.Storage.SecretKey,
		Bucket:    cfg.Storage.Bucket,
		Region:    cfg.Storage.Region,
		UseSSL:    cfg.Storage.UseSSL,
	})
	if err != nil {
		return err
	}

	// The API only enqueues work. An insert-only client (no workers, not
	// started) leaves execution to the worker process and can insert job kinds
	// registered there.
	jobClient, err := jobs.NewClient(pool, logger, nil, cfg.Jobs)
	if err != nil {
		return err
	}

	scanService, err := scan.NewService(scan.ServiceOptions{
		Store:     scan.NewStore(pool),
		Objects:   store,
		Provider:  vision.NewFake(),
		Enqueuer:  jobs.NewScanEnqueuer(jobClient),
		Logger:    logger,
		MaxImages: cfg.Scan.MaxImages,
		Limits: scan.ImageLimits{
			MaxBytes:  cfg.Scan.MaxImageBytes,
			MaxPixels: cfg.Scan.MaxPixels,
		},
		MinConfidence: cfg.Scan.MinConfidence,
		PollInterval:  cfg.Scan.PollInterval,
	})
	if err != nil {
		return err
	}

	assistantHandler, err := buildAssistant(cfg, pool, logger, scanService, jobs.NewLeafletEnqueuer(jobClient))
	if err != nil {
		return err
	}

	web, err := webClient(cfg.Web.Dir)
	if err != nil {
		return fmt.Errorf("serve web client: %w", err)
	}

	// Accounts share the API pool and the API's tenant model: a registered
	// person is an api_clients row, so the key issued at registration works on
	// /v1/scans without a second credential type.
	accountHandler := account.NewHTTPHandler(account.HTTPOptions{
		Service: account.NewService(account.ServiceOptions{
			Store:      account.NewDBStore(pool),
			SessionTTL: cfg.Auth.SessionTTL,
		}),
		Logger:       logger,
		SessionTTL:   cfg.Auth.SessionTTL,
		SecureCookie: cfg.Env == config.EnvProduction,
	})

	handler := NewHandler(Options{
		Logger: logger,
		Web:    web,
		Docs:   medapi.DocsHandler(),
		Dependencies: []Dependency{
			{Name: "database", Check: func(ctx context.Context) error { return db.Ping(ctx, pool) }},
			{Name: "storage", Check: store.Ping},
		},
		ReadyTimeout:     cfg.HTTP.RequestTimeout,
		RequestTimeout:   cfg.HTTP.RequestTimeout,
		MaxBodyBytes:     cfg.HTTP.MaxBodyBytes,
		Auth:             buildKeyStore(cfg, pool, logger),
		Usage:            auth.NewDBUsageStore(pool),
		Limiter:          ratelimit.New(),
		RatePerMin:       cfg.API.RatePerMin,
		IPRatePerMin:     cfg.API.IPRatePerMin,
		RateLimitBurst:   cfg.API.RateLimitBurst,
		CORSOrigins:      cfg.API.CORSOrigins,
		APIKey:           cfg.API.LegacyKey,
		Account:          accountHandler,
		Metrics:          metrics.New(),
		DisableAPI:       !cfg.Features.API,
		DisableScans:     !cfg.Features.Scans,
		DisableAssistant: !cfg.Features.Assistant,
		Scan: scan.NewHTTPHandler(scan.HTTPOptions{
			Service:         scanService,
			Logger:          logger,
			MaxImages:       cfg.Scan.MaxImages,
			MaxImageBytes:   cfg.Scan.MaxImageBytes,
			MaxRequestBytes: cfg.Scan.MaxRequestBytes,
			SyncWait:        cfg.Scan.SyncWait,
		}),
		Assistant: assistantHandler,
	})

	server := httpx.NewServer(httpx.ServerOptions{
		Addr:            cfg.HTTP.Addr,
		Handler:         handler,
		Logger:          logger,
		ReadTimeout:     cfg.HTTP.ReadTimeout,
		WriteTimeout:    cfg.HTTP.WriteTimeout,
		IdleTimeout:     cfg.HTTP.IdleTimeout,
		ShutdownTimeout: cfg.HTTP.ShutdownTimeout,
	})

	if err := server.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	logger.Info("api stopped")
	return nil
}

// buildKeyStore accepts database-backed keys and, when configured, the legacy
// single key. Database keys are tried first, so a key issued with
// `admin apikey create` always works; the legacy key is an additional
// development convenience, not a mode that disables the database.
//
// Configuration rejects a legacy key in production, so the fallback cannot
// reach a production deployment.
func buildKeyStore(cfg config.Config, pool *pgxpool.Pool, logger *slog.Logger) auth.Store {
	stores := []auth.Store{auth.NewDBStore(pool)}
	if cfg.API.LegacyKey != "" {
		logger.Warn("also accepting the legacy single API key; issue database-backed keys instead")
		stores = append(stores, auth.NewSingleKeyStore(auth.SingleKeyOptions{Key: cfg.API.LegacyKey}))
	}
	return auth.NewChainStore(stores...)
}

func buildAssistant(cfg config.Config, pool *pgxpool.Pool, logger *slog.Logger, scans assistant.ScanSource, fetches assistant.LeafletFetcher) (*assistant.HTTPHandler, error) {
	prompts, err := assistant.LoadPrompts(assistant.PromptVersion)
	if err != nil {
		return nil, err
	}
	catalogs := make(map[string]assistant.Catalog, len(cfg.Assistant.Languages))
	for _, language := range cfg.Assistant.Languages {
		catalog, err := assistant.LoadTemplates(language)
		if err != nil {
			return nil, fmt.Errorf("load assistant templates for %q: %w", language, err)
		}
		if cfg.Env == config.EnvProduction {
			if err := catalog.ProductionReady(); err != nil {
				return nil, err
			}
		} else if err := catalog.ProductionReady(); err != nil {
			logger.Warn("assistant templates are not reviewed", slog.String("language", language), slog.Any("err", err))
		}
		catalogs[strings.ToLower(language)] = catalog
	}

	// The fake provider returns an empty response for every call, which the
	// router can only read as a question it did not understand. Say so at
	// startup, otherwise a missing provider setting looks like a user asking
	// badly rather than a stack with no model behind it.
	if cfg.Assistant.Provider == "fake" {
		logger.Warn("assistant is using the fake model: every question gets a fixed fallback, never an answer",
			slog.String("fix", "set ASSISTANT_LLM_PROVIDER, ASSISTANT_MODEL, ASSISTANT_SMALL_MODEL, and ASSISTANT_API_KEY"))
	}

	strongModel := cfg.Assistant.Model
	smallModel := cfg.Assistant.SmallModel
	if smallModel == "" {
		smallModel = strongModel
	}
	if strongModel == "" {
		strongModel = "fake-chat-v1"
	}
	if smallModel == "" {
		smallModel = "fake-chat-v1"
	}

	strong, err := newChatClient(cfg, strongModel)
	if err != nil {
		return nil, err
	}
	small, err := newChatClient(cfg, smallModel)
	if err != nil {
		return nil, err
	}
	recorder := llm.NewRecorder(pool)
	strong = llm.NewRecordingClient(strong, recorder, "", cfg.Assistant.Provider, strongModel)
	small = llm.NewRecordingClient(small, recorder, "", cfg.Assistant.Provider, smallModel)

	embedder, err := rag.NewEmbedder(cfg.Embeddings)
	if err != nil {
		return nil, err
	}
	ragStore := rag.NewStore(pool)
	searcher := ragSearcher{store: ragStore, embedder: embedder}

	router := assistant.NewRouter(small, prompts.Router, cfg.Assistant.Languages)
	verifier := assistant.NewVerifier(small, prompts.Verifier)
	limits := assistant.Limits{
		MaxSteps: cfg.Assistant.MaxSteps, MaxOutputTokens: cfg.Assistant.MaxOutputTokens,
		MaxToolCalls: cfg.Assistant.MaxToolCalls,
	}
	orchestrator := &assistant.Orchestrator{
		Client: strong, Tools: assistant.NewToolbox(searcher), Verifier: verifier,
		Prompts: prompts, Limits: limits,
	}

	service, err := assistant.NewService(assistant.ServiceOptions{
		Store:            assistant.NewStore(pool),
		Scans:            scans,
		Resolver:         ragStore,
		Searcher:         searcher,
		Leaflets:         ragStore,
		Fetches:          fetches,
		Prompts:          prompts,
		Router:           router,
		Verifier:         verifier,
		Orchestra:        orchestrator,
		Catalogs:         catalogs,
		Languages:        cfg.Assistant.Languages,
		Limits:           limits,
		ConversationTTL:  cfg.Assistant.ConversationTTL,
		MaxTurns:         cfg.Assistant.MaxTurns,
		MaxQuestionBytes: cfg.Assistant.MaxQuestionBytes,
		Logger:           logger,
	})
	if err != nil {
		return nil, err
	}
	logger.Info("assistant configured",
		slog.String("provider", cfg.Assistant.Provider),
		slog.String("model", strongModel),
		slog.String("small_model", smallModel),
		slog.Any("languages", cfg.Assistant.Languages),
		slog.String("embeddings_model", embedder.Model()),
	)
	return assistant.NewHTTPHandler(assistant.HTTPOptions{
		Service: service, Logger: logger, MaxBodyBytes: int64(cfg.Assistant.MaxQuestionBytes) + 512,
	}), nil
}

func newChatClient(cfg config.Config, model string) (llm.Client, error) {
	switch cfg.Assistant.Provider {
	case "fake":
		return llm.NewFake(), nil
	case "claude":
		return llm.NewClaude(llm.ClaudeOptions{
			APIKey: cfg.Assistant.APIKey, BaseURL: cfg.Assistant.BaseURL, Model: model,
			MaxTokens: cfg.Assistant.MaxTokens, Timeout: cfg.Assistant.Timeout,
		})
	case "openai":
		return llm.NewOpenAI(llm.OpenAIOptions{
			APIKey: cfg.Assistant.APIKey, BaseURL: cfg.Assistant.BaseURL, Model: model,
			MaxTokens: cfg.Assistant.MaxTokens, Timeout: cfg.Assistant.Timeout,
		})
	default:
		return nil, fmt.Errorf("assistant: unknown provider %q", cfg.Assistant.Provider)
	}
}

// ragSearcher binds the phase 08 embedder to assistant's searcher interface.
type ragSearcher struct {
	store    *rag.Store
	embedder rag.Embedder
}

func (s ragSearcher) Search(ctx context.Context, request rag.SearchRequest) ([]rag.Hit, error) {
	return s.store.Search(ctx, s.embedder, request)
}
