package catalog

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
)

func newGateway(t *testing.T, endpoint string, style enum.Style) *aggregate.Gateway {
	t.Helper()
	g, err := aggregate.NewGateway("g", "G", endpoint, style)
	if err != nil {
		t.Fatal(err)
	}
	return &g
}

// The OpenRouter-shaped payload carries the metadata the record imports:
// limits, modalities, effort levels, identity (canonical/alias), and
// per-token USD pricing normalized to the per-1M rate card.
func TestListModelsCompletionMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Error("api key must ride as a bearer token")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"id":             "z-ai/glm-5.2",
					"name":           "Z.ai: GLM 5.2",
					"canonical_slug": "z-ai/glm-5.2-20260301",
					"context_length": 1000000,
					"architecture": map[string]any{
						"input_modalities":  []string{"text", "image"},
						"output_modalities": []string{"text"},
					},
					"top_provider": map[string]any{"context_length": 1000000, "max_completion_tokens": 131072},
					"reasoning":    map[string]any{"supported_efforts": []string{"max", "high", "low"}},
					"pricing":      map[string]any{"prompt": "0.0000028", "completion": "0.0000088", "input_cache_read": "0.00000056"},
				},
				{
					"id":             "z-ai/glm-5.2:free",
					"context_length": 200_000, // variant entries carry their own limits
				},
				{
					"id":           "~z-ai/glm-latest",
					"alias_target": map[string]any{"name": "Z.ai: GLM", "slug": "z-ai/glm-5.2"},
				},
				{
					"id":      "bad/pricing",
					"pricing": map[string]any{"prompt": "not-a-number"},
				},
			},
		})
	}))
	defer srv.Close()

	g := newGateway(t, srv.URL, enum.StyleCompletion)
	g.SetApiKey("k")
	models, err := New(nil).ListModels(t.Context(), g)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 4 {
		t.Fatalf("models = %+v", models)
	}
	m := models[0]
	if m.Name != "Z.ai: GLM 5.2" || m.Canonical != "z-ai/glm-5.2-20260301" ||
		m.ContextWindow != 1_000_000 || m.OutputLimit != 131_072 {
		t.Fatalf("identity/limits mismatch: %+v", m)
	}
	if len(m.InputModalities) != 2 || m.InputModalities[1] != enum.ModalityImage ||
		len(m.OutputModalities) != 1 || m.OutputModalities[0] != enum.ModalityText {
		t.Fatalf("modalities mismatch: %+v", m)
	}
	if len(m.SupportedEfforts) != 3 || m.SupportedEfforts[0] != enum.EffortMax {
		t.Fatalf("efforts mismatch: %+v", m)
	}
	if m.Pricing == nil || m.Pricing.InputPerM() != 2.8 || m.Pricing.OutputPerM() != 8.8 ||
		m.Pricing.CacheReadPerM() != 0.56 || m.Pricing.CacheWritePerM() != 0 {
		t.Fatalf("pricing must convert per-token to per-1M: %+v", m.Pricing)
	}
	free := models[1]
	if free.ContextWindow != 200_000 || free.OutputLimit != 0 {
		t.Fatalf("variant entry keeps its own limits: %+v", free)
	}
	alias := models[2]
	if alias.AliasOf != "z-ai/glm-5.2" {
		t.Fatalf("alias target lost: %+v", alias)
	}
	if models[3].Pricing != nil {
		t.Fatal("malformed pricing must drop, not zero-fill")
	}
}

// Anthropic's /v1/models declares only id + display_name — everything
// else stays zero so the merge reads "the provider did not say".
func TestListModelsMessagesThinMetadata(t *testing.T) {
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "k" || r.Header.Get("anthropic-version") == "" {
			t.Error("messages-style headers missing")
		}
		pages = append(pages, r.URL.Query().Get("after_id"))
		if len(pages) == 1 {
			json.NewEncoder(w).Encode(map[string]any{
				"data":     []map[string]any{{"id": "claude-a", "display_name": "Claude A"}},
				"has_more": true, "last_id": "claude-a",
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"id": "claude-b", "display_name": "Claude B"}},
		})
	}))
	defer srv.Close()

	g := newGateway(t, srv.URL, enum.StyleMessages)
	g.SetApiKey("k")
	models, err := New(nil).ListModels(t.Context(), g)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "claude-a" || models[0].Name != "Claude A" ||
		models[1].ID != "claude-b" {
		t.Fatalf("models = %+v", models)
	}
	if models[0].ContextWindow != 0 || len(models[0].SupportedEfforts) != 0 {
		t.Fatalf("thin catalog must stay zero: %+v", models[0])
	}
}

// Gemini's models[] carries token limits under its own field names and
// filters out entries without generateContent.
func TestListModelsGenerateContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "k" {
			t.Error("generatecontent-style key header missing")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{
					"name": "models/gemini-3-pro", "displayName": "Gemini 3 Pro",
					"inputTokenLimit": 1_048_576, "outputTokenLimit": 65_536,
					"supportedGenerationMethods": []string{"generateContent"},
				},
				{
					"name":                       "models/embedding-1",
					"supportedGenerationMethods": []string{"embedContent"},
				},
			},
		})
	}))
	defer srv.Close()

	g := newGateway(t, srv.URL, enum.StyleGenerateContent)
	g.SetApiKey("k")
	models, err := New(nil).ListModels(t.Context(), g)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "gemini-3-pro" {
		t.Fatalf("models = %+v", models)
	}
	if models[0].Name != "Gemini 3 Pro" || models[0].ContextWindow != 1_048_576 ||
		models[0].OutputLimit != 65_536 {
		t.Fatalf("gemini limits mismatch: %+v", models[0])
	}
}
