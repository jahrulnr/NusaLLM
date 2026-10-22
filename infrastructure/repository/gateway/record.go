package repogateway

import (
	"fmt"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

type gatewayRecord struct {
	ID       string        `json:"id"`
	Name     string        `json:"name,omitempty"`
	Style    string        `json:"style"`
	Endpoint string        `json:"endpoint"`
	ApiKey   string        `json:"apiKey,omitempty"` // literal, "env:NAME", or "secret:<key>" ref
	Stream   bool          `json:"stream"`
	Models   []modelRecord `json:"models,omitempty"`
}

// modelRecord mirrors every Model field — request params and catalog
// metadata — so a catalog entry round-trips losslessly.
type modelRecord struct {
	ID               string            `json:"id"`
	Route            string            `json:"route,omitempty"`
	Temperature      *float64          `json:"temperature,omitempty"`
	TopP             *float64          `json:"topP,omitempty"`
	TopK             *int              `json:"topK,omitempty"`
	MaxOutputTokens  *int              `json:"maxOutputTokens,omitempty"`
	PresencePenalty  *float64          `json:"presencePenalty,omitempty"`
	FrequencyPenalty *float64          `json:"frequencyPenalty,omitempty"`
	Seed             *int64            `json:"seed,omitempty"`
	StopSequences    []string          `json:"stopSequences,omitempty"`
	Effort           string            `json:"effort,omitempty"`
	ModelType        string            `json:"modelType,omitempty"`
	Style            string            `json:"style,omitempty"`
	ContextWindow    int               `json:"contextWindow,omitempty"`
	Pricing          *pricingRecord    `json:"pricing,omitempty"`
	InputModalities  []string          `json:"inputModalities,omitempty"`
	OutputModalities []string          `json:"outputModalities,omitempty"`
	OptionalContent  map[string]string `json:"optionalContent,omitempty"`

	// Catalog-declared metadata — what the provider's model list last
	// advertised; zero values mean the gateway stayed silent.
	Name             string   `json:"name,omitempty"`
	Canonical        string   `json:"canonical,omitempty"`
	AliasOf          string   `json:"aliasOf,omitempty"`
	OutputLimit      int      `json:"outputLimit,omitempty"`
	MaxImages        int      `json:"maxImages,omitempty"`
	SupportedEfforts []string `json:"supportedEfforts,omitempty"`
}

type pricingRecord struct {
	InputPerM      float64 `json:"inputPerM"`
	OutputPerM     float64 `json:"outputPerM"`
	CacheReadPerM  float64 `json:"cacheReadPerM"`
	CacheWritePerM float64 `json:"cacheWritePerM"`
}

func encodeGateway(g *aggregate.Gateway) gatewayRecord {
	rec := gatewayRecord{
		ID:       g.ID(),
		Name:     g.Name(),
		Style:    g.Style().String(),
		Endpoint: g.Endpoint(),
		ApiKey:   g.ApiKey(),
		Stream:   g.StreamMode(),
	}
	for _, m := range g.Models() {
		mr := modelRecord{
			ID:               m.ID(),
			Route:            m.Route(),
			Temperature:      m.Temperature(),
			TopP:             m.TopP(),
			TopK:             m.TopK(),
			MaxOutputTokens:  m.MaxOutputTokens(),
			PresencePenalty:  m.PresencePenalty(),
			FrequencyPenalty: m.FrequencyPenalty(),
			Seed:             m.Seed(),
			StopSequences:    m.StopSequences(),
			Effort:           m.Effort().String(),
			ModelType:        m.ModelType().String(),
			Style:            m.Style().String(),
			ContextWindow:    m.ContextWindow(),
			OptionalContent:  m.OptionalContent(),
			Name:             m.Name(),
			Canonical:        m.Canonical(),
			AliasOf:          m.AliasOf(),
			OutputLimit:      m.OutputLimit(),
			MaxImages:        m.MaxImages(),
		}
		for _, e := range m.SupportedEfforts() {
			mr.SupportedEfforts = append(mr.SupportedEfforts, e.String())
		}
		if p := m.Pricing(); p != (valueobject.Pricing{}) {
			mr.Pricing = &pricingRecord{
				InputPerM:      p.InputPerM(),
				OutputPerM:     p.OutputPerM(),
				CacheReadPerM:  p.CacheReadPerM(),
				CacheWritePerM: p.CacheWritePerM(),
			}
		}
		for _, mod := range m.InputModalities() {
			mr.InputModalities = append(mr.InputModalities, mod.String())
		}
		for _, mod := range m.OutputModalities() {
			mr.OutputModalities = append(mr.OutputModalities, mod.String())
		}
		rec.Models = append(rec.Models, mr)
	}
	return rec
}

// decodeGateway hydrates the record; apiKey arrives already resolved —
// dereferencing env/secret refs is the repository's job, not mapping's.
func decodeGateway(rec gatewayRecord, apiKey string) (*aggregate.Gateway, error) {
	style, ok := enum.ParseStyle(rec.Style)
	if !ok {
		return nil, fmt.Errorf("gateway %q: unknown style %q", rec.ID, rec.Style)
	}
	g, err := aggregate.NewGateway(rec.ID, rec.Name, rec.Endpoint, style)
	if err != nil {
		return nil, err
	}
	g.SetApiKey(apiKey)
	if rec.Stream {
		g.EnableStream()
	} else {
		g.DisableStream()
	}
	var models []entity.Model
	for _, mr := range rec.Models {
		m, err := entity.NewModel(mr.ID, mr.Route)
		if err != nil {
			return nil, fmt.Errorf("gateway %q: %w", rec.ID, err)
		}
		m.SetTemperature(mr.Temperature)
		m.SetTopP(mr.TopP)
		m.SetTopK(mr.TopK)
		m.SetMaxOutputTokens(mr.MaxOutputTokens)
		m.SetPresencePenalty(mr.PresencePenalty)
		m.SetFrequencyPenalty(mr.FrequencyPenalty)
		m.SetSeed(mr.Seed)
		m.SetStopSequences(mr.StopSequences)
		if mr.Effort != "" {
			e, ok := enum.ParseEffort(mr.Effort)
			if !ok {
				return nil, fmt.Errorf("gateway %q: model %q: unknown effort %q", rec.ID, mr.ID, mr.Effort)
			}
			m.SetEffort(e)
		}
		if mr.ModelType != "" {
			t, ok := enum.ParseModelType(mr.ModelType)
			if !ok {
				return nil, fmt.Errorf("gateway %q: model %q: unknown type %q", rec.ID, mr.ID, mr.ModelType)
			}
			m.SetModelType(t)
		}
		if mr.Style != "" {
			s, ok := enum.ParseStyle(mr.Style)
			if !ok {
				return nil, fmt.Errorf("gateway %q: model %q: unknown style %q", rec.ID, mr.ID, mr.Style)
			}
			m.SetStyle(s)
		}
		m.SetContextWindow(mr.ContextWindow)
		m.SetName(mr.Name)
		m.SetCanonical(mr.Canonical)
		m.SetAliasOf(mr.AliasOf)
		m.SetOutputLimit(mr.OutputLimit)
		m.SetMaxImages(mr.MaxImages)
		var efforts []enum.Effort
		for _, v := range mr.SupportedEfforts {
			e, ok := enum.ParseEffort(v)
			if !ok {
				return nil, fmt.Errorf("gateway %q: model %q: unknown supported effort %q", rec.ID, mr.ID, v)
			}
			efforts = append(efforts, e)
		}
		m.SetSupportedEfforts(efforts)
		if mr.Pricing != nil {
			p, err := valueobject.NewPricing(mr.Pricing.InputPerM, mr.Pricing.OutputPerM,
				mr.Pricing.CacheReadPerM, mr.Pricing.CacheWritePerM)
			if err != nil {
				return nil, fmt.Errorf("gateway %q: model %q pricing: %w", rec.ID, mr.ID, err)
			}
			m.SetPricing(p)
		}
		var in, out []enum.Modality
		for _, v := range mr.InputModalities {
			mod, ok := enum.ParseModality(v)
			if !ok {
				return nil, fmt.Errorf("gateway %q: model %q: unknown modality %q", rec.ID, mr.ID, v)
			}
			in = append(in, mod)
		}
		for _, v := range mr.OutputModalities {
			mod, ok := enum.ParseModality(v)
			if !ok {
				return nil, fmt.Errorf("gateway %q: model %q: unknown modality %q", rec.ID, mr.ID, v)
			}
			out = append(out, mod)
		}
		m.SetInputModalities(in)
		m.SetOutputModalities(out)
		for k, v := range mr.OptionalContent {
			m.SetOptionalContent(k, v)
		}
		models = append(models, m)
	}
	g.SetModels(models)
	return &g, nil
}
