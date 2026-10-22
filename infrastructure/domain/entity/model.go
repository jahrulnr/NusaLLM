package entity

import (
	"errors"
	"slices"
	"strings"

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
// which declares the default wire style the model may override), which
// model id, and which generation parameters to apply. Models are
// switchable — the service layer may substitute another Model on fallback
// without touching conversation state.
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
	upstream        string     // upstream provider pin inside the route (e.g. an aggregator's backends); empty = the gateway decides
	style           enum.Style // wire-surface override — empty inherits the gateway's style
	optionalContent map[string]string

	// Catalog metadata, hydrated from the gateway's model list — descriptive,
	// not request parameters. Empty means unknown.
	name             string // display name the provider publishes
	canonical        string // stable base id the catalog declares (survives id churn); "" = undeclared
	aliasOf          string // target id when the entry is a pure alias; "" = not an alias
	outputLimit      int    // max completion tokens the provider declares, 0 = unknown
	supportedEfforts []enum.Effort
	mtype            enum.ModelType
	pricing          valueobject.Pricing
	contextWindow    int // total context window in tokens, 0 = unknown
	inputModalities  []enum.Modality
	outputModalities []enum.Modality
	maxImages        int // per-request image cap the serving enforces, 0 = none declared
}

// NewModel creates a model invoked through route — the gateway surface
// (including account selection when the gateway is multi-account) that
// declares the default wire style for this call. route may be empty for
// catalog entries; not every gateway models named accounts.
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

func (m Model) Upstream() string { return m.upstream }

// Style is the wire surface this model explicitly speaks — "" means it
// inherits the gateway's declared style. One endpoint may host models
// that only exist on their native surface (zen/go fronts claude-* on
// messages only, gpt-*-luna on responses only); the override lets a
// mixed catalog ride one gateway record.
func (m Model) Style() enum.Style { return m.style }

// SetStyle pins the adapter surface for this model; "" restores the
// gateway default.
func (m *Model) SetStyle(v enum.Style) { m.style = v }

// WireStyle is the adapter key — the model's own style when set, else
// the gateway's. Every caller resolving a TextModelService must use it
// so an override holds for send, edit, and compaction alike.
func (m Model) WireStyle(gwStyle enum.Style) enum.Style {
	if m.style != "" {
		return m.style
	}
	return gwStyle
}

// SetUpstream pins which upstream provider inside the route serves this
// request — aggregator gateways (OpenRouter-style) fan a model id out to
// several backends and the pin picks one; direct providers ignore it.
// Empty restores the gateway's own routing.
func (m *Model) SetUpstream(v string) { m.upstream = v }

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

func (m Model) Name() string                         { return m.name }
func (m *Model) SetName(v string)                    { m.name = v }
func (m Model) Canonical() string                    { return m.canonical }
func (m *Model) SetCanonical(v string)               { m.canonical = v }
func (m Model) AliasOf() string                      { return m.aliasOf }
func (m *Model) SetAliasOf(v string)                 { m.aliasOf = v }
func (m Model) OutputLimit() int                     { return m.outputLimit }
func (m *Model) SetOutputLimit(v int)                { m.outputLimit = v }
func (m Model) SupportedEfforts() []enum.Effort      { return m.supportedEfforts }
func (m *Model) SetSupportedEfforts(v []enum.Effort) { m.supportedEfforts = v }
func (m Model) ModelType() enum.ModelType            { return m.mtype }
func (m *Model) SetModelType(v enum.ModelType)       { m.mtype = v }
func (m Model) Pricing() valueobject.Pricing         { return m.pricing }
func (m *Model) SetPricing(v valueobject.Pricing)    { m.pricing = v }
func (m Model) ContextWindow() int                   { return m.contextWindow }
func (m *Model) SetContextWindow(v int)              { m.contextWindow = v }

// SupportsEffort reports whether the catalog lets the model take effort e.
// Unset always passes (nothing is sent); an undeclared list fails open —
// the provider remains the final validator, same rule as AcceptsInput.
func (m Model) SupportsEffort(e enum.Effort) bool {
	if e.IsUnset() || len(m.supportedEfforts) == 0 {
		return true
	}
	return slices.Contains(m.supportedEfforts, e)
}

// Variant returns the catalog-variant suffix the id carries
// ("z-ai/glm:free" → "free"), or "" for the base form. Only the colon
// convention reports one — dash-style variants are indistinguishable
// from ordinary id segments.
func (m Model) Variant() string {
	if i := strings.LastIndexByte(m.id, ':'); i >= 0 {
		return m.id[i+1:]
	}
	return ""
}

// Family is the grouping key for picker/family views: the alias target
// or declared canonical when present, otherwise the id — each minus the
// "~" alias marker, its :variant suffix, and a trailing -YYYYMMDD or
// -MMDD snapshot date (both conventions appear in real catalogs; short
// digit runs like "gpt-4" stay intact).
func (m Model) Family() string {
	base := m.id
	if m.aliasOf != "" {
		base = m.aliasOf
	} else if m.canonical != "" {
		base = m.canonical
	}
	base = strings.TrimPrefix(base, "~")
	if i := strings.LastIndexByte(base, ':'); i >= 0 {
		base = base[:i]
	}
	if i := strings.LastIndexByte(base, '-'); i >= 0 {
		if n := len(base) - i - 1; n == 8 || n == 4 {
			digits := true
			for _, r := range base[i+1:] {
				if r < '0' || r > '9' {
					digits = false
					break
				}
			}
			if digits {
				base = base[:i]
			}
		}
	}
	return base
}

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
		if m.outputLimit > 0 && m.outputLimit < v {
			v = m.outputLimit
		}
		m.SetMaxOutputTokens(&v)
	}
	return m
}

// CompactionBudgetTokens is the estimated input size that triggers proactive
// compaction — a share of the input allowance after reserving output headroom.
// Catalog windows below 256K and unknown windows use total−outputReserve;
// wider declared windows keep the legacy share of the full context total.
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
	if m.contextWindow >= DefaultUnknownContextWindow {
		return m.contextWindow // wide declared windows keep the full context total
	}
	total := m.contextWindow
	if total <= 0 {
		total = DefaultUnknownContextWindow
	}
	if a := total - m.outputReserve(); a > 0 {
		return a
	}
	return 0 // a reserve above the window is a misconfigured request — not a usable allowance
}

// outputReserve is the reply headroom the input allowance subtracts. A
// request-level maxOutputTokens is binding — clamped to a declared
// outputLimit, since the provider applies that ceiling anyway — then the
// declared catalog limit capped at the default so a provider's ceiling
// (often far above a real reply) cannot starve the input side.
func (m Model) outputReserve() int {
	if m.maxOutputTokens != nil && *m.maxOutputTokens > 0 {
		r := *m.maxOutputTokens
		if m.outputLimit > 0 && m.outputLimit < r {
			r = m.outputLimit
		}
		return r
	}
	if m.outputLimit > 0 && m.outputLimit < DefaultUnknownMaxOutputTokens {
		return m.outputLimit
	}
	return DefaultUnknownMaxOutputTokens
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

// MaxImages is the serving's per-request image cap — 0 means none was
// declared, so callers fail open (the provider validates).
func (m Model) MaxImages() int      { return m.maxImages }
func (m *Model) SetMaxImages(v int) { m.maxImages = v }

func accepts(mods []enum.Modality, mod enum.Modality) bool {
	if len(mods) == 0 {
		return true
	}
	return slices.Contains(mods, mod)
}
