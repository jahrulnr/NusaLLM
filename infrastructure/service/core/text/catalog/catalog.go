// Package catalog translates provider model-list APIs into model IDs.
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

type Catalog struct{ client *http.Client }

func New(client *http.Client) *Catalog {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Catalog{client: client}
}

// ListModels reads every page of the provider catalog. Existing gateway
// model settings are deliberately outside this adapter's responsibility.
func (c *Catalog) ListModels(ctx context.Context, gateway *aggregate.Gateway) ([]string, error) {
	base := strings.TrimRight(gateway.Endpoint(), "/") + "/models"
	ids := make([]string, 0)
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
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			Models []struct {
				Name                       string   `json:"name"`
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
			for _, model := range body.Models {
				if len(model.SupportedGenerationMethods) > 0 {
					chat := false
					for _, method := range model.SupportedGenerationMethods {
						if method == "generateContent" {
							chat = true
							break
						}
					}
					if !chat {
						continue
					}
				}
				id := strings.TrimPrefix(model.Name, "models/")
				if id != "" && !seenIDs[id] {
					ids = append(ids, id)
					seenIDs[id] = true
				}
			}
			page = body.NextPageToken
		} else {
			for _, model := range body.Data {
				if model.ID != "" && !seenIDs[model.ID] {
					ids = append(ids, model.ID)
					seenIDs[model.ID] = true
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
	return ids, nil
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
