package entity

import (
	"errors"

	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// DefaultUnknownContextWindow and DefaultUnknownMaxOutputTokens are assumed
// catalog totals when contextWindow is zero — a 2026-era floor for budgeting
// and wire defaults until the gateway declares real limits.
const (
	DefaultUnknownContextWindow   = 256_000
	DefaultUnknownMaxOutputTokens = 64_000
)

// Model is a request-scoped route descriptor: which route (gateway surface,
// which owns the wire style), which model id, and which generation
// parameters to apply. Models are switchable — the service layer may
// substitute another Model on fallback without touching conversation state.
type Model struct {
	id    string
	route string // optional gateway selector resolved by the service layer, e.g. "openrouter" or "codex/work-account" — empty for catalog models, which resolve through their owning Gateway

	// Generation parameters. nil means unset — adapters must not send unset
	// parameters and must let the provider default apply, since parameter
	// support differs per provider and per model.
	temperature      *float64
	topP             *float64
	topK             *int
	maxOutputTokens  *int
	presencePenalty  *float64
	frequencyPenalty *float64
	seed             *int64
	stopSequences    []string

	// effort uses the enum's IsUnset sentinel instead of a pointer.
	effort          enum.Effort
	optionalContent map[string]string

	// Catalog metadata, hydrated from the gateway's model list — descriptive,
	// not request parameters. Empty means unknown.
	mtype            enum.ModelType
	pricing          valueobject.Pricing
	contextWindow    int // total context window in tokens, 0 = unknown
	inputModalities  []enum.Modality
	outputModalities []enum.Modality
}

// NewModel creates a model invoked through route — the gateway surface
// (including account selection when the gateway is multi-account) that owns
// the wire style for this call. route may be empty for catalog entries;
// not every gateway models named accounts.
func NewModel(id string, route string) (Model, error) {
	m := Model{id: id, route: route}
	return m, m.validate()
}

func (m Model) validate() error {
	if m.id == "" {
		return errors.New("id is required")
	}
	return nil
}

func (m Model) ID() string    { return m.id }
func (m Model) Route() string { return m.route }

func (m Model) Temperature() *float64      { return m.temperature }
func (m Model) TopP() *float64             { return m.topP }
func (m Model) TopK() *int                 { return m.topK }
func (m Model) MaxOutputTokens() *int      { return m.maxOutputTokens }
func (m Model) PresencePenalty() *float64  { return m.presencePenalty }
func (m Model) FrequencyPenalty() *float64 { return m.frequencyPenalty }
func (m Model) Seed() *int64               { return m.seed }
func (m Model) StopSequences() []string    { return m.stopSequences }
func (m Model) Effort() enum.Effort        { return m.effort }

// Generation-param setters take the pointer verbatim — nil leaves the
// param unset, and the pointed-to value is the caller's to own.
func (m *Model) SetTemperature(v *float64)      { m.temperature = v }
func (m *Model) SetTopP(v *float64)             { m.topP = v }
func (m *Model) SetTopK(v *int)                 { m.topK = v }
func (m *Model) SetMaxOutputTokens(v *int)      { m.maxOutputTokens = v }
func (m *Model) SetPresencePenalty(v *float64)  { m.presencePenalty = v }
func (m *Model) SetFrequencyPenalty(v *float64) { m.frequencyPenalty = v }
func (m *Model) SetSeed(v *int64)               { m.seed = v }
func (m *Model) SetStopSequences(v []string)    { m.stopSequences = v }

// SetEffort sets the reasoning effort for this request. EffortDefault and
// unset mean the field is omitted from the wire.
func (m *Model) SetEffort(v enum.Effort) { m.effort = v }

func (m Model) OptionalContent() map[string]string { return m.optionalContent }

// SetOptionalContent stores a provider-specific generation parameter under
// an adapter-namespaced key (e.g. "openai.reasoning_effort",
// "gemini.thinking_budget") for params without a common field.
func (m *Model) SetOptionalContent(key, value string) {
	if m.optionalContent == nil {
		m.optionalContent = map[string]string{}
	}
	m.optionalContent[key] = value
}

func (m Model) ModelType() enum.ModelType             { return m.mtype }
func (m *Model) SetModelType(v enum.ModelType)        { m.mtype = v }
func (m Model) Pricing() valueobject.Pricing          { return m.pricing }
func (m *Model) SetPricing(v valueobject.Pricing)     { m.pricing = v }
func (m Model) ContextWindow() int                    { return m.contextWindow }
func (m *Model) SetContextWindow(v int)               { m.contextWindow = v }

// EffectiveContextWindow is the total context size used for UI budgeting
// when the catalog left contextWindow at zero.
func (m Model) EffectiveContextWindow() int {
	if m.contextWindow > 0 {
		return m.contextWindow
	}
	return DefaultUnknownContextWindow
}

// ApplyUnknownContextDefaults returns a copy with assumed totals when the
// catalog left contextWindow at zero. Persisted gateway records stay as-is.
func (m Model) ApplyUnknownContextDefaults() Model {
	if m.contextWindow != 0 {
		return m
	}
	m.SetContextWindow(DefaultUnknownContextWindow)
	if m.maxOutputTokens == nil {
		v := DefaultUnknownMaxOutputTokens
		m.SetMaxOutputTokens(&v)
	}
	return m
}

// CompactionBudgetTokens is the estimated input size that triggers proactive
// compaction — a share of the input allowance after reserving output headroom.
// Catalog windows below 256K and unknown windows use total−64K input; wider
// declared windows keep the legacy share of the full context total.
func (m Model) CompactionBudgetTokens(budgetPct int) int {
	if budgetPct <= 0 {
		return 0
	}
	allowance := m.compactionInputAllowance()
	if allowance <= 0 {
		return 0
	}
	return allowance * budgetPct / 100
}

// CompactionInputAllowance is the input-side context budget before
// compaction reserves headroom for the reply (matches footer ~estimate/total).
func (m Model) CompactionInputAllowance() int {
	return m.compactionInputAllowance()
}

func (m Model) compactionInputAllowance() int {
	total := m.contextWindow
	if total <= 0 {
		total = DefaultUnknownContextWindow
		reserve := DefaultUnknownMaxOutputTokens
		if m.maxOutputTokens != nil && *m.maxOutputTokens > 0 {
			reserve = *m.maxOutputTokens
		}
		return total - reserve
	}
	if total < DefaultUnknownContextWindow {
		return total - DefaultUnknownMaxOutputTokens
	}
	return total
}

func (m Model) InputModalities() []enum.Modality      { return m.inputModalities }
func (m *Model) SetInputModalities(v []enum.Modality) { m.inputModalities = v }
func (m Model) OutputModalities() []enum.Modality     { return m.outputModalities }

func (m *Model) SetOutputModalities(v []enum.Modality) { m.outputModalities = v }

// AcceptsInput reports whether the model lists mod as an input modality.
// An empty modality list means the catalog did not declare capabilities,
// so predicates fail open — the provider remains the final validator.
func (m Model) AcceptsInput(mod enum.Modality) bool {
	return accepts(m.inputModalities, mod)
}

// AcceptsOutput reports whether the model lists mod as an output modality.
func (m Model) AcceptsOutput(mod enum.Modality) bool {
	return accepts(m.outputModalities, mod)
}

func accepts(mods []enum.Modality, mod enum.Modality) bool {
	if len(mods) == 0 {
		return true
	}
	for _, v := range mods {
		if v == mod {
			return true
		}
	}
	return false
}
