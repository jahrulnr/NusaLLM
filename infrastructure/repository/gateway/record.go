package repogateway

import (
	"fmt"

	"github.com/jahrulnr/llm-base/infrastructure/domain/aggregate"
	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	"github.com/jahrulnr/llm-base/infrastructure/domain/valueobject"
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
	ContextWindow    int               `json:"contextWindow,omitempty"`
	Pricing          *pricingRecord    `json:"pricing,omitempty"`
	InputModalities  []string          `json:"inputModalities,omitempty"`
	OutputModalities []string          `json:"outputModalities,omitempty"`
	OptionalContent  map[string]string `json:"optionalContent,omitempty"`
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
		rec.Models = append(rec.Models, encodeModel(m))
	}
	return rec
}

func encodeModel(m entity.Model) modelRecord {
	rec := modelRecord{
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
		ContextWindow:    m.ContextWindow(),
		OptionalContent:  m.OptionalContent(),
	}
	if p := m.Pricing(); p != (valueobject.Pricing{}) {
		rec.Pricing = &pricingRecord{
			InputPerM:      p.InputPerM(),
			OutputPerM:     p.OutputPerM(),
			CacheReadPerM:  p.CacheReadPerM(),
			CacheWritePerM: p.CacheWritePerM(),
		}
	}
	for _, mod := range m.InputModalities() {
		rec.InputModalities = append(rec.InputModalities, mod.String())
	}
	for _, mod := range m.OutputModalities() {
		rec.OutputModalities = append(rec.OutputModalities, mod.String())
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
		m, err := decodeModel(mr)
		if err != nil {
			return nil, fmt.Errorf("gateway %q: %w", rec.ID, err)
		}
		models = append(models, m)
	}
	g.SetModels(models)
	return &g, nil
}

func decodeModel(r modelRecord) (entity.Model, error) {
	m, err := entity.NewModel(r.ID, r.Route)
	if err != nil {
		return entity.Model{}, err
	}
	if r.Temperature != nil {
		m.SetTemperature(*r.Temperature)
	}
	if r.TopP != nil {
		m.SetTopP(*r.TopP)
	}
	if r.TopK != nil {
		m.SetTopK(*r.TopK)
	}
	if r.MaxOutputTokens != nil {
		m.SetMaxOutputTokens(*r.MaxOutputTokens)
	}
	if r.PresencePenalty != nil {
		m.SetPresencePenalty(*r.PresencePenalty)
	}
	if r.FrequencyPenalty != nil {
		m.SetFrequencyPenalty(*r.FrequencyPenalty)
	}
	if r.Seed != nil {
		m.SetSeed(*r.Seed)
	}
	m.SetStopSequences(r.StopSequences)
	if r.Effort != "" {
		e, ok := enum.ParseEffort(r.Effort)
		if !ok {
			return entity.Model{}, fmt.Errorf("model %q: unknown effort %q", r.ID, r.Effort)
		}
		m.SetEffort(e)
	}
	if r.ModelType != "" {
		t, ok := enum.ParseModelType(r.ModelType)
		if !ok {
			return entity.Model{}, fmt.Errorf("model %q: unknown type %q", r.ID, r.ModelType)
		}
		m.SetModelType(t)
	}
	m.SetContextWindow(r.ContextWindow)
	if r.Pricing != nil {
		p, err := valueobject.NewPricing(r.Pricing.InputPerM, r.Pricing.OutputPerM,
			r.Pricing.CacheReadPerM, r.Pricing.CacheWritePerM)
		if err != nil {
			return entity.Model{}, fmt.Errorf("model %q pricing: %w", r.ID, err)
		}
		m.SetPricing(p)
	}
	in, err := decodeModalities(r.InputModalities, r.ID)
	if err != nil {
		return entity.Model{}, err
	}
	m.SetInputModalities(in)
	out, err := decodeModalities(r.OutputModalities, r.ID)
	if err != nil {
		return entity.Model{}, err
	}
	m.SetOutputModalities(out)
	for k, v := range r.OptionalContent {
		m.SetOptionalContent(k, v)
	}
	return m, nil
}

func decodeModalities(vs []string, modelID string) ([]enum.Modality, error) {
	var out []enum.Modality
	for _, v := range vs {
		mod, ok := enum.ParseModality(v)
		if !ok {
			return nil, fmt.Errorf("model %q: unknown modality %q", modelID, v)
		}
		out = append(out, mod)
	}
	return out, nil
}
