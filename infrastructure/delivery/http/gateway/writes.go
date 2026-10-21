package gateway

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/gin-gonic/gin"

	appgateway "github.com/jahrulnr/nusallm/application/gateway"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/http/internal/transport"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

// upsertBody is the PUT request document — the readable fields only. No
// id (path is authoritative), no apiKey material echo, no
// optionalContent; unknown fields are rejected by DecodeJSON.
type upsertBody struct {
	Name     string           `json:"name"`
	Style    string           `json:"style"`
	Endpoint string           `json:"endpoint"`
	Stream   *bool            `json:"stream"`
	ApiKey   json.RawMessage  `json:"apiKey"`
	Models   []modelInputBody `json:"models"`
}

func (b *upsertBody) toInput(id string) (appgateway.GatewayInput, error) {
	in := appgateway.GatewayInput{
		ID:       id,
		Name:     b.Name,
		Style:    enum.Style(b.Style),
		Endpoint: b.Endpoint,
		Stream:   b.Stream,
	}
	if b.ApiKey != nil {
		// Tri-state: absent keeps the stored credential, "" clears it,
		// a value sets it. null is not a state — reject it explicitly,
		// since decoding it into a string would masquerade as clear.
		raw := bytes.TrimSpace(b.ApiKey)
		var key string
		if bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, &key) != nil {
			return in, apperror.New(400, "invalid gateway",
				errors.New("apiKey must be a string — omit to keep, \"\" to clear"))
		}
		in.ApiKey = &key
	}
	for _, mb := range b.Models {
		mi := appgateway.ModelInput{
			ID:               mb.ID,
			Route:            mb.Route,
			Temperature:      mb.Temperature,
			TopP:             mb.TopP,
			TopK:             mb.TopK,
			MaxOutputTokens:  mb.MaxOutputTokens,
			PresencePenalty:  mb.PresencePenalty,
			FrequencyPenalty: mb.FrequencyPenalty,
			Seed:             mb.Seed,
			StopSequences:    mb.StopSequences,
			Effort:           enum.Effort(mb.Effort),
			ModelType:        enum.ModelType(mb.ModelType),
			ContextWindow:    mb.ContextWindow,
			Name:             mb.Name,
			Canonical:        mb.Canonical,
			AliasOf:          mb.AliasOf,
			OutputLimit:      mb.OutputLimit,
		}
		for _, e := range mb.SupportedEfforts {
			mi.SupportedEfforts = append(mi.SupportedEfforts, enum.Effort(e))
		}
		for _, mod := range mb.InputModalities {
			mi.InputModalities = append(mi.InputModalities, enum.Modality(mod))
		}
		for _, mod := range mb.OutputModalities {
			mi.OutputModalities = append(mi.OutputModalities, enum.Modality(mod))
		}
		if mb.Pricing != nil {
			p, err := valueobject.NewPricing(
				mb.Pricing.InputPerMillionUsd, mb.Pricing.OutputPerMillionUsd,
				mb.Pricing.CacheReadPerMillionUsd, mb.Pricing.CacheWritePerMillionUsd)
			if err != nil {
				return in, apperror.New(400, "invalid model",
					errors.New("model "+mb.ID+": "+err.Error()))
			}
			mi.Pricing = &p
		}
		in.Models = append(in.Models, mi)
	}
	return in, nil
}

type modelInputBody struct {
	ID               string      `json:"id"`
	Route            string      `json:"route"`
	Temperature      *float64    `json:"temperature"`
	TopP             *float64    `json:"topP"`
	TopK             *int        `json:"topK"`
	MaxOutputTokens  *int        `json:"maxOutputTokens"`
	PresencePenalty  *float64    `json:"presencePenalty"`
	FrequencyPenalty *float64    `json:"frequencyPenalty"`
	Seed             *int64      `json:"seed"`
	StopSequences    []string    `json:"stopSequences"`
	Effort           string      `json:"effort"`
	ModelType        string      `json:"modelType"`
	ContextWindow    int         `json:"contextWindow"`
	Pricing          *pricingDTO `json:"pricing"`
	InputModalities  []string    `json:"inputModalities"`
	OutputModalities []string    `json:"outputModalities"`
	Name             string      `json:"name"`
	Canonical        string      `json:"canonical"`
	AliasOf          string      `json:"aliasOf"`
	OutputLimit      int         `json:"outputLimit"`
	SupportedEfforts []string    `json:"supportedEfforts"`
}

// put replaces the whole readable document at :id — Upsert owns apiKey
// tri-state, model validation, and the server-owned hidden
// optionalContent preservation; the transport only shapes the input.
// The response carries the import outcome alongside the projection:
// provider ids the merge added, or the catalog failure that left the
// saved record as written — either way the write succeeded, so the
// status is 200 in both cases.
func (s *server) put(c *gin.Context) {
	id, ok := transport.PathID(c)
	if !ok {
		return
	}
	var body upsertBody
	if !transport.DecodeJSON(c, &body) {
		return
	}
	in, err := body.toInput(id)
	if err != nil {
		transport.RespondError(c, err)
		return
	}
	ctx := c.Request.Context()
	outcome, err := s.writes.Upsert(ctx, in)
	if err != nil {
		transport.RespondError(c, err)
		return
	}
	info, err := s.reads.Get(ctx, id)
	if err != nil {
		transport.RespondError(c, err)
		return
	}
	c.JSON(200, toGatewayWrite(info, outcome))
}

// importModels reruns the provider-catalog merge against the stored
// record — the same import a save performs — for the Refresh button:
// no document to resend, the stored endpoint and key do the fetch. A
// catalog failure still answers 200 with catalogError because the
// record is intact; a missing id is 404.
func (s *server) importModels(c *gin.Context) {
	id, ok := transport.PathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	outcome, err := s.writes.ImportModels(ctx, id)
	if err != nil {
		transport.RespondError(c, err)
		return
	}
	info, err := s.reads.Get(ctx, id)
	if err != nil {
		transport.RespondError(c, err)
		return
	}
	c.JSON(200, toGatewayWrite(info, outcome))
}

func (s *server) delete(c *gin.Context) {
	id, ok := transport.PathID(c)
	if !ok {
		return
	}
	if err := s.writes.Delete(c.Request.Context(), id); err != nil {
		transport.RespondError(c, err)
		return
	}
	c.Status(204)
}
