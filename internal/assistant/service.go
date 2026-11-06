package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/benfaruna/medverify/internal/llm"
	"github.com/benfaruna/medverify/internal/rag"
	"github.com/benfaruna/medverify/internal/scan"
)

// Service errors.
var (
	ErrQuestionEmpty    = errors.New("question is required")
	ErrQuestionTooLong  = errors.New("question is too long")
	ErrScanNotReady     = errors.New("scan is not ready for questions")
	ErrCatalogMissing   = errors.New("no fixed-text catalog for this language")
	ErrConversationGone = errors.New("conversation is not available")
)

// ScanSource loads a scan with its stored result and flags.
type ScanSource interface {
	Get(ctx context.Context, id uuid.UUID) (scan.Scan, error)
}

// ProductResolver maps a confirmed registry record to the local product id used
// by retrieval. A missing product is not an error.
type ProductResolver interface {
	FindProductID(ctx context.Context, sourceID, nrn string) (int64, bool, error)
}

// LeafletSource reads the leaflet state for a product. rag.Store implements it.
type LeafletSource interface {
	LeafletStatus(ctx context.Context, productID int64) (rag.LeafletStatus, bool, error)
}

// LeafletFetcher asks for a background fetch of one product's leaflet. The
// service never downloads in the request path: a person's question must be
// answered immediately, and the download plus indexing is slow work.
type LeafletFetcher interface {
	EnsureLeaflet(ctx context.Context, productID int64) error
}

// TranscriptStore is the persistence seam for unit tests and the real store.
type TranscriptStore interface {
	GetConversation(ctx context.Context, scanID uuid.UUID) (Conversation, bool, error)
	GetOrCreate(ctx context.Context, scanID uuid.UUID, language, rulesVersion, promptVersion string, ttl time.Duration) (Conversation, error)
	CountUserMessages(ctx context.Context, conversationID uuid.UUID) (int, error)
	Messages(ctx context.Context, conversationID uuid.UUID) ([]Message, error)
	RecordTurn(ctx context.Context, record TurnRecord) error
}

// ServiceOptions configures the grounded assistant.
type ServiceOptions struct {
	Store    TranscriptStore
	Scans    ScanSource
	Resolver ProductResolver
	Searcher LeafletSearcher
	// Leaflets reads whether the scanned pack has a leaflet on file.
	Leaflets LeafletSource
	// Fetches requests a background leaflet download when none is stored.
	Fetches   LeafletFetcher
	Client    llm.Client
	Prompts   Prompts
	Router    *Router
	Verifier  *Verifier
	Orchestra *Orchestrator
	Catalogs  map[string]Catalog
	Languages []string
	Limits    Limits

	ConversationTTL    time.Duration
	MaxTurns           int
	MaxQuestionBytes   int
	MaxHistoryMessages int
	Logger             *slog.Logger
}

// Service applies the router, policy gate, orchestrator, verifier, and audit
// recording for one scan at a time.
type Service struct {
	store        TranscriptStore
	scans        ScanSource
	resolver     ProductResolver
	leaflets     LeafletSource
	fetches      LeafletFetcher
	router       *Router
	orchestrator *Orchestrator
	catalogs     map[string]Catalog
	limits       Limits
	ttl          time.Duration
	maxTurns     int
	maxQuestion  int
	maxHistory   int
	logger       *slog.Logger
}

// NewService validates dependencies and applies safe defaults.
func NewService(opts ServiceOptions) (*Service, error) {
	if opts.Store == nil {
		return nil, fmt.Errorf("assistant: a store is required")
	}
	if opts.Scans == nil {
		return nil, fmt.Errorf("assistant: a scan source is required")
	}
	prompts := opts.Prompts
	if prompts.Version == "" {
		loaded, err := LoadPrompts(PromptVersion)
		if err != nil {
			return nil, err
		}
		prompts = loaded
	}
	catalogs := opts.Catalogs
	if len(catalogs) == 0 {
		catalog, err := LoadTemplates(DefaultLanguage)
		if err != nil {
			return nil, err
		}
		catalogs = map[string]Catalog{DefaultLanguage: catalog}
	}
	languages := opts.Languages
	if len(languages) == 0 {
		languages = []string{DefaultLanguage}
	}

	router := opts.Router
	if router == nil && opts.Client != nil {
		router = NewRouter(opts.Client, prompts.Router, languages)
	}
	verifier := opts.Verifier
	if verifier == nil && opts.Client != nil {
		verifier = NewVerifier(opts.Client, prompts.Verifier)
	}
	orchestrator := opts.Orchestra
	if orchestrator == nil && opts.Client != nil {
		orchestrator = &Orchestrator{
			Client: opts.Client, Tools: NewToolbox(opts.Searcher), Verifier: verifier,
			Prompts: prompts, Limits: opts.Limits,
		}
	}
	if router == nil || orchestrator == nil {
		return nil, fmt.Errorf("assistant: a router and orchestrator are required")
	}
	if opts.ConversationTTL <= 0 {
		opts.ConversationTTL = 24 * time.Hour
	}
	if opts.MaxTurns <= 0 {
		opts.MaxTurns = 6
	}
	if opts.MaxQuestionBytes <= 0 {
		opts.MaxQuestionBytes = 1000
	}
	if opts.MaxHistoryMessages <= 0 {
		opts.MaxHistoryMessages = 12
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Service{
		store: opts.Store, scans: opts.Scans, resolver: opts.Resolver,
		leaflets: opts.Leaflets, fetches: opts.Fetches,
		router: router, orchestrator: orchestrator, catalogs: catalogs,
		limits: opts.Limits, ttl: opts.ConversationTTL, maxTurns: opts.MaxTurns,
		maxQuestion: opts.MaxQuestionBytes, maxHistory: opts.MaxHistoryMessages,
		logger: logger,
	}, nil
}

// AskRequest is one question about a scan.
type AskRequest struct {
	ScanID    uuid.UUID
	ClientKey string
	Question  string
}

// SourceView is one citation in an answer.
type SourceView struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`
	Text  string `json:"text,omitempty"`
}

// AnswerView is the API response for one question.
type AnswerView struct {
	ConversationID string       `json:"conversation_id"`
	Turn           int          `json:"turn"`
	Answer         string       `json:"answer"`
	Sources        []SourceView `json:"sources"`
	Disclaimer     string       `json:"disclaimer,omitempty"`
	Intent         Intent       `json:"intent"`
	Language       string       `json:"language"`
	Verified       bool         `json:"verified"`
	Fallback       bool         `json:"fallback,omitempty"`
}

// MessageView is one transcript row.
type MessageView struct {
	Turn      int       `json:"turn"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Intent    string    `json:"intent,omitempty"`
	Language  string    `json:"language,omitempty"`
	Sources   []string  `json:"sources,omitempty"`
	Verifier  string    `json:"verifier,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ConversationView is the transcript response.
type ConversationView struct {
	ConversationID string        `json:"conversation_id"`
	ScanID         string        `json:"scan_id"`
	Language       string        `json:"language"`
	ExpiresAt      time.Time     `json:"expires_at"`
	Messages       []MessageView `json:"messages"`
}

// Ask answers one question and records the turn before returning it.
func (s *Service) Ask(ctx context.Context, request AskRequest) (AnswerView, error) {
	question := strings.TrimSpace(request.Question)
	if question == "" {
		return AnswerView{}, ErrQuestionEmpty
	}
	if len(question) > s.maxQuestion {
		return AnswerView{}, ErrQuestionTooLong
	}
	scanned, err := s.loadScan(ctx, request.ScanID, request.ClientKey)
	if err != nil {
		return AnswerView{}, err
	}
	if scanned.Status != scan.StatusCompleted && scanned.Status != scan.StatusUnreadable {
		return AnswerView{}, ErrScanNotReady
	}

	ctx = llm.WithScanID(ctx, scanned.ID.String())
	language := DefaultLanguage
	if supported := s.router.SupportedLanguage(scanned.Language); supported {
		language = strings.ToLower(scanned.Language)
	}
	rulesVersion := "unknown"
	if scanned.Result != nil && scanned.Result.Verdict != nil && scanned.Result.Verdict.RulesVersion != "" {
		rulesVersion = scanned.Result.Verdict.RulesVersion
	}
	conversation, err := s.store.GetOrCreate(ctx, scanned.ID, language, rulesVersion, PromptVersion, s.ttl)
	if err != nil {
		return AnswerView{}, err
	}
	if !conversation.ExpiresAt.After(time.Now()) {
		return AnswerView{}, ErrConversationExpired
	}
	turnCount, err := s.store.CountUserMessages(ctx, conversation.ID)
	if err != nil {
		return AnswerView{}, err
	}
	if turnCount >= s.maxTurns {
		return AnswerView{}, ErrTurnLimit
	}

	route, err := s.router.Route(ctx, question, language)
	if err != nil {
		return AnswerView{}, err
	}
	// A malformed route can only use the unknown row, even if the model also
	// returned an intent string. It is logged because an unusable route nearly
	// always means the router model returned nothing — a fake provider, a
	// missing key, a model that ignores the forced tool — and the person would
	// otherwise be told their question was not understood.
	if route.Malformed {
		route.Intent = IntentUnknown
		s.logger.WarnContext(ctx, "assistant router returned no usable route; answering with the unknown fallback",
			slog.String("scan_id", scanned.ID.String()),
			slog.String("language", route.Language),
			slog.String("prompt_version", PromptVersion),
		)
	}
	row := DefaultPolicy().Decide(route.Intent)
	catalog, err := s.catalog(route.Language)
	if err != nil {
		return AnswerView{}, err
	}

	turn := turnCount + 1
	session, err := s.session(ctx, scanned)
	if err != nil {
		return AnswerView{}, err
	}

	// A leaflet question with no readable leaflet has nothing to quote and
	// nothing to cite, so the model cannot answer it: every claim it produced
	// would fail verification. Answer from the fixed text instead, keep the link
	// that is on file, and ask for a download in the background so the next ask
	// can quote the leaflet itself.
	if row.Intent == IntentLeaflet && !session.Leaflet.Readable() {
		return s.answerWithoutLeaflet(ctx, answerContext{
			conversation: conversation, turn: turn, question: question,
			route: route, catalog: catalog, session: session,
		})
	}

	if !row.Answerable() {
		text, err := catalog.Text(row.Template)
		if err != nil {
			return AnswerView{}, err
		}
		record := TurnRecord{
			Conversation: conversation, Turn: turn, User: question, Answer: text,
			Intent: route.Intent, Language: route.Language, Verifier: "policy_refusal",
			RefusalKey: row.Template,
		}
		if err := s.store.RecordTurn(ctx, record); err != nil {
			return AnswerView{}, err
		}
		return AnswerView{
			ConversationID: conversation.ID.String(), Turn: turn, Answer: text,
			Sources: []SourceView{}, Intent: route.Intent, Language: route.Language,
			Verified: false,
		}, nil
	}

	history, err := s.history(ctx, conversation.ID)
	if err != nil {
		return AnswerView{}, err
	}
	result, err := s.orchestrator.Answer(ctx, AnswerRequest{
		Session: session, Row: row, Question: question, History: history, Catalog: catalog,
	})
	if err != nil {
		return AnswerView{}, err
	}
	if strings.TrimSpace(result.Text) == "" {
		return AnswerView{}, fmt.Errorf("assistant: turn produced no fixed or verified answer")
	}

	sources := make([]SourceView, 0, len(result.Sources)+1)
	sourceIDs := make([]string, 0, len(result.Sources)+1)
	for _, source := range result.Sources {
		sources = append(sources, SourceView(source))
		sourceIDs = append(sourceIDs, source.ID)
	}
	// A leaflet question that ended in a fixed fallback still gets the link, so
	// "the leaflet has no such passage" arrives with a way to read the leaflet
	// rather than as a dead end. The link is a stored fact, not model text, and
	// no claim is made about what the leaflet says.
	if len(sources) == 0 && row.Intent == IntentLeaflet {
		if citation, ok := leafletDocumentCitation(session.Leaflet); ok {
			sources = append(sources, SourceView{
				ID: citation.ID, Label: "1", Kind: citation.Kind,
				Title: citation.Title, URL: citation.URL, Text: citation.Text,
			})
			sourceIDs = append(sourceIDs, citation.ID)
		}
	}
	record := TurnRecord{
		Conversation: conversation, Turn: turn, User: question, Answer: result.Text,
		Intent: route.Intent, Language: route.Language, ToolLogs: result.ToolLogs,
		SourceIDs: sourceIDs, Verifier: result.Verification.Reason,
		RefusalKey: result.FallbackKey,
	}
	if err := s.store.RecordTurn(ctx, record); err != nil {
		return AnswerView{}, err
	}
	s.logger.InfoContext(ctx, "assistant turn answered",
		slog.String("scan_id", scanned.ID.String()),
		slog.Int("turn", turn),
		slog.String("intent", string(route.Intent)),
		slog.Int("tool_calls", result.ToolCalls),
		slog.Bool("verified", result.Verification.Allowed),
		slog.String("fallback", result.FallbackKey),
	)

	return AnswerView{
		ConversationID: conversation.ID.String(), Turn: turn,
		Answer: result.Text, Sources: sources, Disclaimer: result.Disclaimer,
		Intent: route.Intent, Language: route.Language,
		Verified: result.Verification.Allowed, Fallback: result.FallbackKey != "",
	}, nil
}

// Conversation returns the stored transcript for a scan.
func (s *Service) Conversation(ctx context.Context, scanID uuid.UUID, clientKey string) (ConversationView, error) {
	scanned, err := s.loadScan(ctx, scanID, clientKey)
	if err != nil {
		return ConversationView{}, err
	}
	conversation, found, err := s.store.GetConversation(ctx, scanned.ID)
	if err != nil {
		return ConversationView{}, err
	}
	if !found {
		return ConversationView{}, ErrConversationMissing
	}
	if !conversation.ExpiresAt.After(time.Now()) {
		return ConversationView{}, ErrConversationExpired
	}
	messages, err := s.store.Messages(ctx, conversation.ID)
	if err != nil {
		return ConversationView{}, err
	}
	view := ConversationView{
		ConversationID: conversation.ID.String(), ScanID: conversation.ScanID.String(),
		Language: conversation.Language, ExpiresAt: conversation.ExpiresAt,
		Messages: make([]MessageView, 0, len(messages)),
	}
	for _, message := range messages {
		view.Messages = append(view.Messages, MessageView{
			Turn: message.Turn, Role: message.Role, Content: message.Content,
			Intent: message.Intent, Language: message.Language,
			Sources: message.SourceIDs, Verifier: message.Verifier,
			CreatedAt: message.CreatedAt,
		})
	}
	return view, nil
}

func (s *Service) loadScan(ctx context.Context, id uuid.UUID, clientKey string) (scan.Scan, error) {
	scanned, err := s.scans.Get(ctx, id)
	if errors.Is(err, scan.ErrScanNotFound) {
		return scan.Scan{}, scan.ErrScanNotFound
	}
	if err != nil {
		return scan.Scan{}, err
	}
	// Tenant isolation: a scan is only visible to the client that created it.
	// scans.client_key is NOT NULL and always holds the owning tenant key, so a
	// mismatch is treated as "not found" rather than revealing that the scan
	// exists under another client.
	if scanned.ClientKey != clientKey {
		return scan.Scan{}, scan.ErrScanNotFound
	}
	return scanned, nil
}

// answerContext is the state the fixed leaflet answers need. It is a struct so
// the function signature cannot drift as fields are added.
type answerContext struct {
	conversation Conversation
	turn         int
	question     string
	route        Route
	catalog      Catalog
	session      Session
}

// answerWithoutLeaflet answers a leaflet question from fixed text when no
// readable leaflet is stored. It admits the gap, points at the leaflet link when
// one is known, and queues a fetch. The turn is recorded like any other, so the
// transcript shows what the person asked and what they were told.
func (s *Service) answerWithoutLeaflet(ctx context.Context, state answerContext) (AnswerView, error) {
	key, verifier := TemplateFallbackNoLeaflet, "leaflet_unavailable"
	if state.session.ProductID <= 0 {
		// No confirmed registry record means no leaflet can be located at all,
		// which is a different answer from "the leaflet has not been read yet".
		key, verifier = TemplateFallbackNotFound, "leaflet_no_record"
	}
	text, err := state.catalog.Text(key)
	if err != nil {
		return AnswerView{}, err
	}

	sources := make([]SourceView, 0, 1)
	if citation, ok := leafletDocumentCitation(state.session.Leaflet); ok {
		sources = append(sources, SourceView{
			ID: citation.ID, Label: "1", Kind: citation.Kind,
			Title: citation.Title, URL: citation.URL, Text: citation.Text,
		})
	}

	s.ensureLeaflet(ctx, state.session)

	record := TurnRecord{
		Conversation: state.conversation, Turn: state.turn, User: state.question,
		Answer: text, Intent: state.route.Intent, Language: state.route.Language,
		Verifier: verifier, RefusalKey: key,
	}
	if err := s.store.RecordTurn(ctx, record); err != nil {
		return AnswerView{}, err
	}
	s.logger.InfoContext(ctx, "assistant answered without a leaflet",
		slog.String("scan_id", state.session.ScanID.String()),
		slog.Int("turn", state.turn),
		slog.String("reason", verifier),
		slog.Bool("link_known", len(sources) > 0),
	)
	return AnswerView{
		ConversationID: state.conversation.ID.String(), Turn: state.turn,
		Answer: text, Sources: sources, Intent: state.route.Intent,
		Language: state.route.Language, Verified: false, Fallback: true,
	}, nil
}

// ensureLeaflet queues a background fetch of the pack's leaflet. A failure is
// logged, never shown: the answer already said the leaflet is not on file, and a
// queue outage must not turn that into an error page.
func (s *Service) ensureLeaflet(ctx context.Context, session Session) {
	if s.fetches == nil || session.ProductID <= 0 {
		return
	}
	if err := s.fetches.EnsureLeaflet(ctx, session.ProductID); err != nil {
		s.logger.WarnContext(ctx, "could not queue a leaflet fetch",
			slog.Int64("product_id", session.ProductID),
			slog.Any("err", err),
		)
	}
}

func (s *Service) session(ctx context.Context, scanned scan.Scan) (Session, error) {
	session := Session{ScanID: scanned.ID, Language: scanned.Language, Flags: scanned.Flags}
	if scanned.Result == nil {
		return session, nil
	}
	if len(scanned.Result.Features) > 0 {
		if err := json.Unmarshal(scanned.Result.Features, &session.Match); err != nil {
			return Session{}, fmt.Errorf("assistant: decode stored registry match: %w", err)
		}
	}
	if product := session.Match.Product; product != nil && s.resolver != nil {
		id, found, err := s.resolver.FindProductID(ctx, product.SourceID, product.NRN)
		if err != nil {
			return Session{}, fmt.Errorf("assistant: resolve product for retrieval: %w", err)
		}
		if found {
			session.ProductID = id
		}
	}
	if product := session.Match.Product; product != nil && session.ProductID > 0 && s.leaflets != nil {
		status, found, err := s.leaflets.LeafletStatus(ctx, session.ProductID)
		if err != nil {
			return Session{}, fmt.Errorf("assistant: resolve the leaflet on file: %w", err)
		}
		if found {
			session.Leaflet = status
		}
	}
	return session, nil
}

func (s *Service) history(ctx context.Context, conversationID uuid.UUID) ([]llm.Message, error) {
	messages, err := s.store.Messages(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	start := 0
	if len(messages) > s.maxHistory {
		start = len(messages) - s.maxHistory
	}
	history := make([]llm.Message, 0, len(messages)-start)
	for _, message := range messages[start:] {
		role := llm.RoleUser
		if message.Role == "assistant" {
			role = llm.RoleAssistant
		}
		history = append(history, llm.Message{Role: role, Text: message.Content})
	}
	return history, nil
}

func (s *Service) catalog(language string) (Catalog, error) {
	if catalog, ok := s.catalogs[strings.ToLower(language)]; ok {
		return catalog, nil
	}
	if catalog, ok := s.catalogs[DefaultLanguage]; ok {
		return catalog, nil
	}
	return Catalog{}, fmt.Errorf("%w: %s", ErrCatalogMissing, language)
}

// HasVerifiedSources is a small helper for evals and tests.
func HasVerifiedSources(turn Turn) bool {
	return turn.Verification.Allowed && len(turn.Sources) > 0
}
