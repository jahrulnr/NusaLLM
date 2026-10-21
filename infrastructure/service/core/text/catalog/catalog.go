// Package catalog translates provider model-list APIs into catalog entries.
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
	if e.Architecture != nil {
		for _, v := range e.Architecture.InputModalities {
			if mod, ok := enum.ParseModality(v); ok {
				m.InputModalities = append(m.InputModalities, mod)
			}
		}
		for _, v := range e.Architecture.OutputModalities {
			if mod, ok := enum.ParseModality(v); ok {
				m.OutputModalities = append(m.OutputModalities, mod)
			}
		}
	}
	if e.Reasoning != nil {
		for _, v := range e.Reasoning.SupportedEfforts {
			if eff, ok := enum.ParseEffort(v); ok && !eff.IsUnset() {
				m.SupportedEfforts = append(m.SupportedEfforts, eff)
			}
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
					chat := false
					for _, method := range entry.SupportedGenerationMethods {
						if method == "generateContent" {
							chat = true
							break
						}
					}
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
	if !strings.Contains(gateway.Endpoint(), string(enum.OpenRouter)) || !strings.Contains(modelID, "/") {
		return nil, nil
	}
	u := strings.TrimRight(gateway.Endpoint(), "/") + "/models/" + modelID + "/endpoints"
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
