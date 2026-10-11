package gateway

import (
	"strings"

	appgateway "github.com/jahrulnr/nusallm/application/gateway"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

type pricingDTO struct {
	InputPerMillionUsd      float64 `json:"inputPerMillionUsd"`
	OutputPerMillionUsd     float64 `json:"outputPerMillionUsd"`
	CacheReadPerMillionUsd  float64 `json:"cacheReadPerMillionUsd"`
	CacheWritePerMillionUsd float64 `json:"cacheWritePerMillionUsd"`
}

// modelDTO is the readable catalog entry — optionalContent is
// server-owned and never projected.
type modelDTO struct {
	ID               string     `json:"id"`
	Route            string     `json:"route,omitempty"`
	Temperature      *float64   `json:"temperature,omitempty"`
	TopP             *float64   `json:"topP,omitempty"`
	TopK             *int       `json:"topK,omitempty"`
	MaxOutputTokens  *int       `json:"maxOutputTokens,omitempty"`
	PresencePenalty  *float64   `json:"presencePenalty,omitempty"`
	FrequencyPenalty *float64   `json:"frequencyPenalty,omitempty"`
	Seed             *int64     `json:"seed,omitempty"`
	StopSequences    []string   `json:"stopSequences,omitempty"`
	Effort           string     `json:"effort,omitempty"`
	ModelType        string     `json:"modelType,omitempty"`
	ContextWindow    int        `json:"contextWindow"`
	Pricing          pricingDTO `json:"pricing"`
	InputModalities  []string   `json:"inputModalities,omitempty"`
	OutputModalities []string   `json:"outputModalities,omitempty"`
}

func toModel(m entity.Model) modelDTO {
	p := m.Pricing()
	out := modelDTO{
		ID:               m.ID(),
		Route:            m.Route(),
		Temperature:      m.Temperature(),
		TopP:             m.TopP(),
		TopK:             m.TopK(),
		MaxOutputTokens:  m.MaxOutputTokens(),
		PresencePenalty:  m.PresencePenalty(),
		FrequencyPenalty: m.FrequencyPenalty(),
		Seed:             m.Seed(),
		ModelType:        m.ModelType().String(),
		ContextWindow:    m.ContextWindow(),
		Pricing: pricingDTO{
			InputPerMillionUsd:      p.InputPerM(),
			OutputPerMillionUsd:     p.OutputPerM(),
			CacheReadPerMillionUsd:  p.CacheReadPerM(),
			CacheWritePerMillionUsd: p.CacheWritePerM(),
		},
	}
	// "Unset ≠ zero": absent effort never serializes as a value.
	if e := m.Effort(); !e.IsUnset() {
		out.Effort = e.String()
	}
	for _, mod := range m.InputModalities() {
		out.InputModalities = append(out.InputModalities, mod.String())
	}
	for _, mod := range m.OutputModalities() {
		out.OutputModalities = append(out.OutputModalities, mod.String())
	}
	return out
}

type gatewayDTO struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Style    string     `json:"style"`
	Endpoint string     `json:"endpoint"`
	KeySet   bool       `json:"keySet"`
	KeyState string     `json:"keyState"`
	Broken   bool       `json:"broken"`
	Stream   bool       `json:"stream"`
	Models   []modelDTO `json:"models"`
}

// keyState flattens the credential projection: the raw KeyRef string
// ("env:NAME"/"secret:<key>") never crosses the wire, only its class.
// Broken dominates — a record that can't decode may be hiding anything.
func toGateway(i appgateway.GatewayInfo) gatewayDTO {
	state := "unset"
	switch {
	case i.Broken:
		state = "broken"
	case strings.HasPrefix(i.KeyRef, valueobject.KeyRefEnv):
		state = "env"
	case i.KeySet:
		state = "vaulted"
	}
	out := gatewayDTO{
		ID:       i.ID,
		Name:     i.Name,
		Style:    i.Style.String(),
		Endpoint: i.Endpoint,
		KeySet:   i.KeySet,
		KeyState: state,
		Broken:   i.Broken,
		Stream:   i.Stream,
		Models:   make([]modelDTO, 0, len(i.Models)),
	}
	for _, m := range i.Models {
		out.Models = append(out.Models, toModel(m))
	}
	return out
}

// gatewayWriteDTO is the mutation response — the sanitized projection
// plus the import outcome of the write that just ran. `added` always
// serializes (an empty array when nothing merged); `catalogError`
// appears only when the post-write catalog merge failed — the record
// itself saved, so the status stays 200.
type gatewayWriteDTO struct {
	gatewayDTO
	Added        []string `json:"added"`
	CatalogError string   `json:"catalogError,omitempty"`
}

func toGatewayWrite(i appgateway.GatewayInfo, out appgateway.UpsertOutcome) gatewayWriteDTO {
	d := gatewayWriteDTO{
		gatewayDTO:   toGateway(i),
		Added:        out.Added,
		CatalogError: out.CatalogError,
	}
	if d.Added == nil {
		d.Added = []string{}
	}
	return d
}
