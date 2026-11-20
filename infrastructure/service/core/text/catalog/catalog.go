// Package catalog translates provider model-list APIs into catalog entries.
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

type Catalog struct{ client *http.Client }

func New(client *http.Client) *Catalog {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Catalog{client: client}
}

// catalogEntry is the OpenRouter-shaped model payload — also what
// OpenRouter-compatible relays and Anthropic's /v1/models answer with a
// subset of. Every field is optional on the wire; undeclared metadata
// stays zero so the merge reads it as "the provider did not say".
type catalogEntry struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	DisplayName   string `json:"display_name"` // Anthropic-style name key
	CanonicalSlug string `json:"canonical_slug"`
	AliasTarget   *struct {
		Slug string `json:"slug"`
	} `json:"alias_target"`
	ContextLength int `json:"context_length"`
	TopProvider   *struct {
		MaxCompletionTokens int `json:"max_completion_tokens"`
	} `json:"top_provider"`
	Architecture *struct {
		InputModalities  []string `json:"input_modalities"`
		OutputModalities []string `json:"output_modalities"`
	} `json:"architecture"`
	Reasoning *struct {
		SupportedEfforts []string `json:"supported_efforts"`
	} `json:"reasoning"`
	Pricing *struct {
		Prompt          string `json:"prompt"`
		Completion      string `json:"completion"`
		InputCacheRead  string `json:"input_cache_read"`
		InputCacheWrite string `json:"input_cache_write"`
	} `json:"pricing"`

	// models.dev-shaped entries (opencode zen/go): a model kind, MIME-typed
	// modality lists, token limits, and reasoning efforts under capabilities.
	Type           string `json:"type"`
	CanonicalID    string `json:"canonical_id"`
	RecommendedAPI string `json:"recommended_api"`
	Modalities     *struct {
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"modalities"`
	Limit *struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	} `json:"limit"`
	Capabilities *struct {
		Reasoning *struct {
			Effort []string `json:"effort"`
		} `json:"reasoning"`
	} `json:"capabilities"`
}

// catalogModelTypes maps a catalog's model kind onto the domain type.
// "chat" is the models.dev word for a text model.
var catalogModelTypes = map[string]enum.ModelType{
	"chat":          enum.ModelTypeText,
	"text":          enum.ModelTypeText,
	"embedding":     enum.ModelTypeEmbedding,
	"embeddings":    enum.ModelTypeEmbedding,
	"image":         enum.ModelTypeImage,
	"video":         enum.ModelTypeVideo,
	"speech":        enum.ModelTypeSpeech,
	"tts":           enum.ModelTypeSpeech,
	"transcription": enum.ModelTypeTranscription,
	"stt":           enum.ModelTypeTranscription,
	"audio":         enum.ModelTypeAudio,
	"rerank":        enum.ModelTypeRerank,
	"decision":      enum.ModelTypeDecision,
	"realtime":      enum.ModelTypeRealtime,
}

// catalogStyles maps a models.dev recommended_api onto the wire style that
// runs it. Only APIs served under the gateway's own base URL map: opencode
// answers /chat/completions, /responses, and /messages there, while the
// Google surfaces (generate-content, interactions) live under a separate
// base and are left unpinned.
var catalogStyles = map[string]enum.Style{
	"chat-completions": enum.StyleCompletion,
	"responses":        enum.StyleResponses,
	"messages":         enum.StyleMessages,
}

// parseModalities normalizes a catalog modality list. MIME types collapse
// to their family ("image/png" → image, "application/pdf" → file); kinds
// the domain does not model ("embedding", "decision") drop. A list that
// was declared but kept nothing reports declared=true, so the caller can
// tell "no text here" from "the provider did not say".
func parseModalities(raw []string) (mods []enum.Modality, declared bool) {
	for _, v := range raw {
		v = strings.ToLower(strings.TrimSpace(v))
		family, _, _ := strings.Cut(v, "/")
		if family == "application" {
			family = string(enum.ModalityFile)
		}
		if mod, ok := enum.ParseModality(family); ok && !slices.Contains(mods, mod) {
			mods = append(mods, mod)
		}
	}
	return mods, len(raw) > 0
}

// toCatalogModel normalizes one wire entry. Provider vocabulary that the
// domain does not know (a new effort level, an unseen modality) drops
// silently — the next catalog pass re-derives it once the enum learns it.
func (e catalogEntry) toCatalogModel() dservice.CatalogModel {
	m := dservice.CatalogModel{
		ID:            e.ID,
		Name:          e.Name,
		Canonical:     e.CanonicalSlug,
		ContextWindow: e.ContextLength,
	}
	if m.Name == "" {
		m.Name = e.DisplayName
	}
	if e.AliasTarget != nil {
		m.AliasOf = e.AliasTarget.Slug
	}
	if e.TopProvider != nil {
		m.OutputLimit = e.TopProvider.MaxCompletionTokens
	}
	if m.Canonical == "" {
		m.Canonical = e.CanonicalID
	}
	if e.Limit != nil {
		if m.ContextWindow == 0 {
			m.ContextWindow = e.Limit.Context
		}
		if m.OutputLimit == 0 {
			m.OutputLimit = e.Limit.Output
		}
	}
	if t, ok := catalogModelTypes[strings.ToLower(e.Type)]; ok {
		m.ModelType = t
	}
	if st, ok := catalogStyles[strings.ToLower(e.RecommendedAPI)]; ok {
		m.Style = st
	}
	var in, out []string
	if e.Architecture != nil {
		in, out = e.Architecture.InputModalities, e.Architecture.OutputModalities
	} else if e.Modalities != nil {
		in, out = e.Modalities.Input, e.Modalities.Output
	}
	m.InputModalities, _ = parseModalities(in)
	var outDeclared bool
	m.OutputModalities, outDeclared = parseModalities(out)
	// An output list naming only kinds the domain does not model (e.g.
	// ["embedding"]) still says the model emits no text. Without a declared
	// type that would read as "unknown"; record it as a non-chat kind.
	if outDeclared && len(m.OutputModalities) == 0 && m.ModelType == "" {
		if t, ok := catalogModelTypes[strings.ToLower(out[0])]; ok {
			m.ModelType = t
		}
	}
	var efforts []string
	if e.Reasoning != nil {
		efforts = e.Reasoning.SupportedEfforts
	} else if e.Capabilities != nil && e.Capabilities.Reasoning != nil {
		efforts = e.Capabilities.Reasoning.Effort
	}
	for _, v := range efforts {
		if eff, ok := enum.ParseEffort(v); ok && !eff.IsUnset() {
			m.SupportedEfforts = append(m.SupportedEfforts, eff)
		}
	}
	if e.Pricing != nil {
		if p, ok := perTokenPricing(e.Pricing.Prompt, e.Pricing.Completion,
			e.Pricing.InputCacheRead, e.Pricing.InputCacheWrite); ok {
			m.Pricing = &p
		}
	}
	return m
}

// perTokenPricing converts a provider's USD-per-token strings into the
// per-1M rate card. A provided rate that fails to parse drops the whole
// pricing block — a zeroed-out card would read as "free", not "unknown".
func perTokenPricing(prompt, completion, cacheRead, cacheWrite string) (valueobject.Pricing, bool) {
	at := func(s string) float64 {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0
		}
		return f * 1_000_000
	}
	for _, s := range []string{prompt, completion, cacheRead, cacheWrite} {
		if s == "" {
			continue
		}
		if _, err := strconv.ParseFloat(s, 64); err != nil {
			return valueobject.Pricing{}, false
		}
	}
	p, err := valueobject.NewPricing(at(prompt), at(completion), at(cacheRead), at(cacheWrite))
	return p, err == nil
}

// ListModels reads every page of the provider catalog. Existing gateway
// model settings are deliberately outside this adapter's responsibility.
func (c *Catalog) ListModels(ctx context.Context, gateway *aggregate.Gateway) ([]dservice.CatalogModel, error) {
	base := strings.TrimRight(gateway.Endpoint(), "/") + "/models"
	models := make([]dservice.CatalogModel, 0)
	seenIDs := make(map[string]bool)
	seenPages := make(map[string]bool)
	page := ""
	for {
		u, err := url.Parse(base)
		if err != nil {
			return nil, apperror.New(400, "invalid gateway endpoint", err)
		}
		query := u.Query()
		switch gateway.Style() {
		case enum.StyleMessages:
			query.Set("limit", "1000")
			if page != "" {
				query.Set("after_id", page)
			}
		case enum.StyleGenerateContent:
			query.Set("pageSize", "1000")
			if page != "" {
				query.Set("pageToken", page)
			}
		}
		u.RawQuery = query.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, apperror.New(400, "invalid gateway endpoint", err)
		}
		if key := gateway.ApiKey(); key != "" {
			switch gateway.Style() {
			case enum.StyleMessages:
				req.Header.Set("x-api-key", key)
			case enum.StyleGenerateContent:
				req.Header.Set("x-goog-api-key", key)
			default:
				req.Header.Set("Authorization", "Bearer "+key)
			}
		}
		if gateway.Style() == enum.StyleMessages {
			req.Header.Set("anthropic-version", "2023-06-01")
		}
		resp, err := c.client.Do(req)
		if err != nil {
			return nil, apperror.New(502, "could not fetch provider models", err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, apperror.New(502, fmt.Sprintf("provider model list failed (HTTP %d)", resp.StatusCode), fmt.Errorf("upstream status %d", resp.StatusCode))
		}
		var body struct {
			Data   []catalogEntry `json:"data"`
			Models []struct {
				Name                       string   `json:"name"`
				DisplayName                string   `json:"displayName"`
				InputTokenLimit            int      `json:"inputTokenLimit"`
				OutputTokenLimit           int      `json:"outputTokenLimit"`
				SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
			} `json:"models"`
			HasMore       bool   `json:"has_more"`
			LastID        string `json:"last_id"`
			NextPageToken string `json:"nextPageToken"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if err != nil {
			return nil, apperror.New(502, "invalid provider model list", err)
		}
		if gateway.Style() == enum.StyleGenerateContent && body.Models == nil ||
			gateway.Style() != enum.StyleGenerateContent && body.Data == nil {
			return nil, apperror.New(502, "invalid provider model list", fmt.Errorf("model array missing"))
		}
		if gateway.Style() == enum.StyleGenerateContent {
			for _, entry := range body.Models {
				if len(entry.SupportedGenerationMethods) > 0 {
					chat := slices.Contains(entry.SupportedGenerationMethods, "generateContent")
					if !chat {
						continue
					}
				}
				id := strings.TrimPrefix(entry.Name, "models/")
				if id != "" && !seenIDs[id] {
					models = append(models, dservice.CatalogModel{
						ID:            id,
						Name:          entry.DisplayName,
						ContextWindow: entry.InputTokenLimit,
						OutputLimit:   entry.OutputTokenLimit,
					})
					seenIDs[id] = true
				}
			}
			page = body.NextPageToken
		} else {
			for _, entry := range body.Data {
				if entry.ID != "" && !seenIDs[entry.ID] {
					models = append(models, entry.toCatalogModel())
					seenIDs[entry.ID] = true
				}
			}
			if gateway.Style() == enum.StyleMessages && body.HasMore {
				if body.LastID == "" {
					return nil, apperror.New(502, "invalid provider model pagination", fmt.Errorf("cursor missing"))
				}
				page = body.LastID
			} else {
				page = ""
			}
		}
		if page == "" {
			break
		}
		if seenPages[page] {
			return nil, apperror.New(502, "invalid provider model pagination", fmt.Errorf("repeated cursor"))
		}
		seenPages[page] = true
	}
	return models, nil
}

// ListProviders reads OpenRouter's per-model endpoints and returns the
// routable upstream backends — the names a provider.order pin accepts.
// Non-aggregator surfaces have no upstream layer and return empty.
func (c *Catalog) ListProviders(ctx context.Context, gateway *aggregate.Gateway, modelID string) ([]dservice.UpstreamProvider, error) {
	if !enum.OpenRouter.MatchesEndpoint(gateway.Endpoint()) || !strings.Contains(modelID, "/") {
		return nil, nil
	}
	// The id is path data, not URL syntax — escape each segment so a
	// delimiter inside one can't push /endpoints into the query, while
	// the vendor/name slash keeps the separator it routes on.
	// PathEscape leaves "." and ".." untouched, so those segments are
	// rejected before the request: they would climb out of /models/.
	segs := strings.Split(modelID, "/")
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return nil, apperror.New(400, "invalid model id", fmt.Errorf("model id %q", modelID))
		}
	}
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	u := strings.TrimRight(gateway.Endpoint(), "/") + "/models/" + strings.Join(segs, "/") + "/endpoints"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, apperror.New(400, "invalid gateway endpoint", err)
	}
	if key := gateway.ApiKey(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, apperror.New(502, "could not fetch upstream providers", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil // model with no routable endpoints
	}
	if resp.StatusCode != http.StatusOK {
		return nil, apperror.New(502, fmt.Sprintf("provider endpoints failed (HTTP %d)", resp.StatusCode), fmt.Errorf("upstream status %d", resp.StatusCode))
	}
	var body struct {
		Data struct {
			Endpoints []struct {
				ProviderName string `json:"provider_name"`
			} `json:"endpoints"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, apperror.New(502, "invalid provider endpoints", err)
	}
	out := make([]dservice.UpstreamProvider, 0, len(body.Data.Endpoints))
	seen := make(map[string]bool)
	for _, ep := range body.Data.Endpoints {
		if ep.ProviderName != "" && !seen[ep.ProviderName] {
			out = append(out, dservice.UpstreamProvider{Name: ep.ProviderName, Slug: ep.ProviderName})
			seen[ep.ProviderName] = true
		}
	}
	return out, nil
}
