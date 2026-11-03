package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/benfaruna/medverify/internal/llm"
	"github.com/benfaruna/medverify/internal/rag"
	"github.com/benfaruna/medverify/internal/safety"
	"github.com/benfaruna/medverify/internal/verify"
)

// Session is the server-side scope for one conversation. Tools receive it from
// the service, never from model arguments. That single rule blocks cross-pack
// answers even when the model invents an NRN or product ID.
type Session struct {
	ScanID    uuid.UUID
	ProductID int64
	Language  string
	Match     verify.MatchResult
	Flags     []safety.Flag
}

// Citation is one source the model may cite. ID is stable within a turn and is
// what the verifier checks.
type Citation struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`
	Text  string `json:"text"`
}

// ToolResult is one tool answer. Content is JSON data for the model; Citations
// are the verifier's trusted view of the same facts.
type ToolResult struct {
	Name      string     `json:"name"`
	Content   string     `json:"content"`
	Citations []Citation `json:"citations,omitempty"`
	IsError   bool       `json:"is_error,omitempty"`
}

// LeafletSearcher is the phase 08 retrieval seam. It is an interface so the
// tools never need a database in unit tests.
type LeafletSearcher interface {
	Search(ctx context.Context, request rag.SearchRequest) ([]rag.Hit, error)
}

// Toolbox holds the read-only tools and their retrieval dependency.
type Toolbox struct {
	Searcher   LeafletSearcher
	MaxResults int
	// MaxTextBytes bounds one leaflet passage placed in the prompt.
	MaxTextBytes int
}

// NewToolbox builds a toolbox with safe defaults.
func NewToolbox(searcher LeafletSearcher) *Toolbox {
	return &Toolbox{Searcher: searcher, MaxResults: rag.DefaultSearchLimit, MaxTextBytes: 4000}
}

// Definitions returns the tool schemas allowed by a policy row.
func (t *Toolbox) Definitions(row PolicyRow) []llm.Tool {
	tools := make([]llm.Tool, 0, len(row.Tools))
	for _, name := range row.Tools {
		switch name {
		case ToolRegistration:
			tools = append(tools, llm.Tool{
				Name:        ToolRegistration,
				Description: "Read the NAFDAC registration record for the scanned pack. It takes no arguments.",
				Schema:      json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			})
		case ToolIngredients:
			tools = append(tools, llm.Tool{
				Name:        ToolIngredients,
				Description: "Read the registered ingredients and strengths for the scanned pack. It takes no arguments.",
				Schema:      json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			})
		case ToolSafetyFlags:
			tools = append(tools, llm.Tool{
				Name:        ToolSafetyFlags,
				Description: "Read the sourced safety flags stored for the scanned pack. It takes no arguments.",
				Schema:      json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			})
		case ToolSearchLeaflet:
			tools = append(tools, llm.Tool{
				Name:        ToolSearchLeaflet,
				Description: "Search the leaflet on file for this scanned pack. Returns passages with a source ID.",
				Schema: json.RawMessage(`{
					"type":"object",
					"properties":{
						"query":{"type":"string","description":"Words to find in the leaflet."},
						"section":{"type":"string","description":"Optional section code such as indications, warnings, or storage."}
					},
					"required":["query"],
					"additionalProperties":false
				}`),
			})
		}
	}
	return tools
}

// Call runs one allowed tool. The product and scan always come from the
// session; any product_id, nrn, or scan_id in args is ignored.
func (t *Toolbox) Call(ctx context.Context, row PolicyRow, session Session, name string, args json.RawMessage) (ToolResult, error) {
	if !row.AllowsTool(name) {
		return ToolResult{Name: name, IsError: true, Content: `{"error":"tool_not_allowed"}`},
			fmt.Errorf("assistant: tool %q is not allowed for intent %s", name, row.Intent)
	}
	switch name {
	case ToolRegistration:
		return t.registration(session), nil
	case ToolIngredients:
		return t.ingredients(session), nil
	case ToolSafetyFlags:
		return t.safetyFlags(session), nil
	case ToolSearchLeaflet:
		return t.searchLeaflet(ctx, row, session, args)
	default:
		return ToolResult{Name: name, IsError: true, Content: `{"error":"unknown_tool"}`},
			fmt.Errorf("assistant: unknown tool %q", name)
	}
}

func (t *Toolbox) registration(session Session) ToolResult {
	result := ToolResult{Name: ToolRegistration}
	product := session.Match.Product
	if product == nil {
		result.Content = mustJSON(map[string]any{
			"found":   false,
			"outcome": session.Match.Outcome,
			"note":    "no confirmed registry record for this scan",
		})
		return result
	}
	sourceID := registryCitationID(product, "")
	citation := Citation{
		ID:    sourceID,
		Kind:  "registry_record",
		Title: "NAFDAC registry record",
		Text: fmt.Sprintf("NAFDAC registry record: NRN %s; name %s; manufacturer %s; applicant %s; status %s; expires %s.",
			product.NRN, product.Name, product.Manufacturer, product.Applicant, product.Status, formatDate(product.ExpiresAt)),
	}
	expires := ""
	if product.ExpiresAt != nil {
		expires = product.ExpiresAt.Format("2006-01-02")
	}
	result.Citations = []Citation{citation}
	result.Content = mustJSON(map[string]any{
		"found":      true,
		"source_ids": []string{sourceID},
		"registration": map[string]any{
			"nrn":          product.NRN,
			"name":         product.Name,
			"manufacturer": product.Manufacturer,
			"applicant":    product.Applicant,
			"status":       product.Status,
			"expires_at":   expires,
		},
	})
	return result
}

func (t *Toolbox) ingredients(session Session) ToolResult {
	result := ToolResult{Name: ToolIngredients}
	product := session.Match.Product
	if product == nil || len(product.Ingredients) == 0 {
		result.Content = mustJSON(map[string]any{
			"found":   false,
			"outcome": session.Match.Outcome,
			"note":    "no registered ingredient list for this scan",
		})
		return result
	}
	sourceID := registryCitationID(product, "#ingredients")
	parts := make([]string, 0, len(product.Ingredients))
	items := make([]map[string]string, 0, len(product.Ingredients))
	for _, ingredient := range product.Ingredients {
		strength := strings.TrimSpace(ingredient.Strength)
		parts = append(parts, strings.TrimSpace(ingredient.Name+" "+strength))
		items = append(items, map[string]string{"name": ingredient.Name, "strength": strength})
	}
	result.Citations = []Citation{{
		ID:    sourceID,
		Kind:  "registry_record",
		Title: "NAFDAC registry ingredients",
		Text:  fmt.Sprintf("NAFDAC registry record for %s lists: %s.", product.Name, strings.Join(parts, "; ")),
	}}
	result.Content = mustJSON(map[string]any{
		"found":        true,
		"source_ids":   []string{sourceID},
		"product_name": product.Name,
		"ingredients":  items,
	})
	return result
}

func (t *Toolbox) safetyFlags(session Session) ToolResult {
	result := ToolResult{Name: ToolSafetyFlags}
	if len(session.Flags) == 0 {
		// Absence of a stored flag is not a sourced statement that the pack is
		// free of problems. The model is told to fall back rather than claim it.
		result.Content = mustJSON(map[string]any{
			"found": false,
			"note":  "no sourced safety flag is stored for this scan",
		})
		return result
	}
	items := make([]map[string]any, 0, len(session.Flags))
	seen := make(map[string]struct{}, len(session.Flags))
	for _, flag := range session.Flags {
		sourceID := flagCitationID(flag)
		citation := Citation{
			ID:    sourceID,
			Kind:  "safety_flag",
			Title: flag.Source.Title,
			URL:   flag.Source.URL,
			Text:  flagText(flag),
		}
		if _, exists := seen[sourceID]; !exists {
			seen[sourceID] = struct{}{}
			result.Citations = append(result.Citations, citation)
		}
		items = append(items, map[string]any{
			"source_id":    sourceID,
			"type":         flag.Type,
			"severity":     flag.Severity,
			"reason_code":  flag.ReasonCode,
			"evidence":     flag.Evidence,
			"issuing_body": flag.Source.IssuingBody,
			"title":        flag.Source.Title,
			"url":          flag.Source.URL,
		})
	}
	result.Content = mustJSON(map[string]any{"found": true, "flags": items})
	return result
}

func (t *Toolbox) searchLeaflet(ctx context.Context, row PolicyRow, session Session, args json.RawMessage) (ToolResult, error) {
	result := ToolResult{Name: ToolSearchLeaflet}
	if t.Searcher == nil || session.ProductID <= 0 {
		result.Content = mustJSON(map[string]any{
			"found":  false,
			"reason": "no_leaflet",
			"note":   "no readable leaflet is indexed for this scan",
		})
		return result, nil
	}
	var request struct {
		Query   string `json:"query"`
		Section string `json:"section"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &request); err != nil {
			return ToolResult{Name: ToolSearchLeaflet, IsError: true, Content: `{"error":"invalid_arguments"}`},
				fmt.Errorf("assistant: search_leaflet arguments: %w", err)
		}
	}
	query := strings.TrimSpace(request.Query)
	if query == "" || len(query) > 500 {
		return ToolResult{Name: ToolSearchLeaflet, IsError: true, Content: `{"error":"invalid_query"}`},
			fmt.Errorf("assistant: search_leaflet needs a query of at most 500 bytes")
	}
	section := strings.TrimSpace(request.Section)
	// The policy table owns the section for topic intents. A model-supplied
	// section can only be used when the policy row has no hint.
	if row.SectionCode != "" {
		section = row.SectionCode
	}
	if len(section) > 40 {
		section = ""
	}

	hits, err := t.Searcher.Search(ctx, rag.SearchRequest{
		ProductID:   session.ProductID,
		Query:       query,
		SectionCode: section,
		Limit:       t.MaxResults,
	})
	if err != nil {
		return ToolResult{Name: ToolSearchLeaflet, IsError: true, Content: `{"error":"leaflet_search_failed"}`}, err
	}
	if len(hits) == 0 {
		result.Content = mustJSON(map[string]any{
			"found":  false,
			"reason": "no_match",
			"note":   "the leaflet on file has no matching passage",
		})
		return result, nil
	}
	passages := make([]map[string]any, 0, len(hits))
	for _, hit := range hits {
		id := fmt.Sprintf("leaflet:%d", hit.ChunkID)
		text := hit.Text
		if t.MaxTextBytes > 0 && len(text) > t.MaxTextBytes {
			text = text[:t.MaxTextBytes]
		}
		result.Citations = append(result.Citations, Citation{
			ID:    id,
			Kind:  "leaflet",
			Title: leafletTitle(hit),
			URL:   hit.SourceURL,
			Text:  text,
		})
		passages = append(passages, map[string]any{
			"source_id":  id,
			"section":    hit.SectionCode,
			"heading":    hit.Heading,
			"page":       hit.PageStart,
			"source_url": hit.SourceURL,
			"passage":    text,
		})
	}
	result.Content = mustJSON(map[string]any{"found": true, "passages": passages})
	return result, nil
}

func registryCitationID(product *verify.ProductRef, suffix string) string {
	base := strings.TrimSpace(product.SourceID)
	if base == "" {
		base = strings.TrimSpace(product.NRN)
	}
	if base == "" {
		base = "unknown"
	}
	return "registry:" + base + suffix
}

func flagCitationID(flag safety.Flag) string {
	if flag.Source.WatchlistEntryID > 0 {
		return fmt.Sprintf("watchlist:%d", flag.Source.WatchlistEntryID)
	}
	if flag.Source.RegistrySourceID != "" {
		return "registry:" + flag.Source.RegistrySourceID
	}
	return "flag:" + string(flag.Type)
}

func flagText(flag safety.Flag) string {
	parts := []string{fmt.Sprintf("Safety flag %s (%s).", flag.Type, flag.Severity)}
	if flag.Source.IssuingBody != "" || flag.Source.Title != "" {
		parts = append(parts, strings.TrimSpace("Source: "+flag.Source.IssuingBody+" "+flag.Source.Title+"."))
	}
	for key, value := range flag.Evidence {
		parts = append(parts, key+"="+value)
	}
	return strings.Join(parts, " ")
}

func leafletTitle(hit rag.Hit) string {
	if hit.Heading != "" {
		return hit.Heading
	}
	if hit.SectionCode != "" {
		return hit.SectionCode
	}
	return "Leaflet passage"
}

func formatDate(value *time.Time) string {
	if value == nil {
		return "not stated"
	}
	return value.Format("2006-01-02")
}

func mustJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `{"error":"encode_failed"}`
	}
	return string(encoded)
}
