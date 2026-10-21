package entity

import (
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
)

func TestNewModel(t *testing.T) {
	if _, err := NewModel("", "openrouter"); err == nil {
		t.Fatal("expected error for empty id")
	}
	m, err := NewModel("claude-opus-4", "openrouter")
	if err != nil {
		t.Fatalf("NewModel: %v", err)
	}
	if m.ID() != "claude-opus-4" || m.Route() != "openrouter" {
		t.Fatalf("unexpected model: %+v", m)
	}
}

// Unset generation params must stay nil so adapters omit them from the wire.
func TestModelParamsPointerSemantics(t *testing.T) {
	m, _ := NewModel("m", "r")
	if m.Temperature() != nil || m.MaxOutputTokens() != nil {
		t.Fatal("unset params must be nil")
	}
	zero := 0.0
	m.SetTemperature(&zero)
	if m.Temperature() == nil || *m.Temperature() != 0 {
		t.Fatal("explicit zero must be distinguishable from unset")
	}
}

func TestModelModalityPredicates(t *testing.T) {
	m, _ := NewModel("m", "r")

	// Empty list = unknown catalog → fail open.
	if !m.AcceptsInput(enum.ModalityImage) {
		t.Fatal("empty modality list must fail open")
	}

	m.SetInputModalities([]enum.Modality{enum.ModalityText})
	if m.AcceptsInput(enum.ModalityImage) {
		t.Fatal("declared text-only model must reject image input")
	}
	if !m.AcceptsInput(enum.ModalityText) {
		t.Fatal("declared text model must accept text")
	}
}

func TestModelUnknownContextDefaults(t *testing.T) {
	m, _ := NewModel("m", "r")
	if m.CompactionBudgetTokens(90) != 172_800 {
		t.Fatalf("unknown window budget = %d, want 192000*90%%", m.CompactionBudgetTokens(90))
	}
	if m.CompactionBudgetTokens(100) != 192_000 {
		t.Fatalf("unknown window cap = %d, want 192000", m.CompactionBudgetTokens(100))
	}
	custom, _ := NewModel("c", "r")
	out := 32_000
	custom.SetMaxOutputTokens(&out)
	if custom.CompactionBudgetTokens(100) != 224_000 {
		t.Fatalf("custom max output budget = %d", custom.CompactionBudgetTokens(100))
	}
	d := m.ApplyUnknownContextDefaults()
	if d.ContextWindow() != DefaultUnknownContextWindow {
		t.Fatalf("context window = %d", d.ContextWindow())
	}
	if d.MaxOutputTokens() == nil || *d.MaxOutputTokens() != DefaultUnknownMaxOutputTokens {
		t.Fatalf("max output = %v", d.MaxOutputTokens())
	}
	if m.ContextWindow() != 0 {
		t.Fatal("defaults must not mutate the original catalog copy")
	}

	known, _ := NewModel("k", "r")
	known.SetContextWindow(200_000)
	if known.CompactionBudgetTokens(100) != 136_000 {
		t.Fatalf("200k window input cap = %d, want 136000", known.CompactionBudgetTokens(100))
	}
	if known.CompactionBudgetTokens(90) != 122_400 {
		t.Fatalf("200k window budget = %d, want 136000*90%%", known.CompactionBudgetTokens(90))
	}
	wide, _ := NewModel("w", "r")
	wide.SetContextWindow(400_000)
	if wide.CompactionBudgetTokens(90) != 360_000 {
		t.Fatalf("wide window budget = %d", wide.CompactionBudgetTokens(90))
	}
	edge, _ := NewModel("e", "r")
	edge.SetContextWindow(DefaultUnknownContextWindow)
	if edge.CompactionBudgetTokens(100) != DefaultUnknownContextWindow {
		t.Fatalf("256K boundary keeps the full total: %d", edge.CompactionBudgetTokens(100))
	}
}

// A declared output limit sizes the headroom the input allowance
// reserves — capped at the default so a provider ceiling cannot starve
// the input side. A request-level maxOutputTokens always wins: it is the
// ceiling actually sent.
func TestModelOutputReserve(t *testing.T) {
	declared, _ := NewModel("d", "r")
	declared.SetContextWindow(65_536)
	declared.SetOutputLimit(16_384)
	if got := declared.CompactionBudgetTokens(100); got != 49_152 {
		t.Fatalf("declared output reserve: cap = %d, want 65536-16384", got)
	}

	over, _ := NewModel("o", "r")
	over.SetContextWindow(200_000)
	over.SetOutputLimit(131_072) // provider ceiling above the default reserve
	if got := over.CompactionBudgetTokens(100); got != 136_000 {
		t.Fatalf("output limit must cap at the default reserve: %d, want 136000", got)
	}

	bound, _ := NewModel("b", "r")
	bound.SetContextWindow(200_000)
	bound.SetOutputLimit(131_072)
	out := 8_000
	bound.SetMaxOutputTokens(&out)
	if got := bound.CompactionBudgetTokens(100); got != 192_000 {
		t.Fatalf("request param must win the reserve: %d, want 192000", got)
	}

	clamped, _ := NewModel("cl", "r")
	clamped.SetContextWindow(200_000)
	clamped.SetOutputLimit(131_072)
	big := 200_000
	clamped.SetMaxOutputTokens(&big)
	if got := clamped.CompactionBudgetTokens(100); got != 68_928 {
		t.Fatalf("request must clamp to the declared ceiling: %d, want 68928", got)
	}

	starved, _ := NewModel("s", "r")
	starved.SetContextWindow(100_000)
	starved.SetMaxOutputTokens(&big)
	if got := starved.CompactionBudgetTokens(100); got != 0 {
		t.Fatalf("reserve above the window clamps to zero: %d", got)
	}
}

func TestModelSupportsEffort(t *testing.T) {
	m, _ := NewModel("m", "r")
	if !m.SupportsEffort(enum.EffortMax) {
		t.Fatal("undeclared list must fail open")
	}
	m.SetSupportedEfforts([]enum.Effort{enum.EffortLow, enum.EffortHigh})
	if m.SupportsEffort(enum.EffortMax) || !m.SupportsEffort(enum.EffortLow) {
		t.Fatal("declared list must gate effort choice")
	}
	if !m.SupportsEffort(enum.EffortDefault) {
		t.Fatal("unset effort always passes — nothing is sent")
	}
}

func TestModelFamilyVariant(t *testing.T) {
	m, _ := NewModel("z-ai/glm-5.2:free", "r")
	m.SetCanonical("z-ai/glm-5.2-20260301")
	if m.Variant() != "free" {
		t.Fatalf("variant = %q, want free", m.Variant())
	}
	if m.Family() != "z-ai/glm-5.2" {
		t.Fatalf("family = %q, want z-ai/glm-5.2", m.Family())
	}
	alias, _ := NewModel("~z-ai/glm-flash-latest", "r")
	alias.SetAliasOf("z-ai/glm-5.3-flash")
	if alias.Family() != "z-ai/glm-5.3-flash" {
		t.Fatalf("alias family = %q", alias.Family())
	}
	// Real catalogs pair "~" aliases with -MMDD targets while the target's
	// canonical carries -YYYYMMDD — both date conventions must collapse.
	dated, _ := NewModel("~deepseek/deepseek-flash-latest", "r")
	dated.SetAliasOf("deepseek/deepseek-v4.1-flash-1017")
	if dated.Family() != "deepseek/deepseek-v4.1-flash" {
		t.Fatalf("-MMDD alias target must join its family: %q", dated.Family())
	}
	bare, _ := NewModel("deepseek-v4.1-flash", "r")
	if bare.Variant() != "" || bare.Family() != "deepseek-v4.1-flash" {
		t.Fatalf("bare id: variant=%q family=%q", bare.Variant(), bare.Family())
	}
}

func TestModelEffort(t *testing.T) {
	m, _ := NewModel("m", "r")
	if !m.Effort().IsUnset() {
		t.Fatal("default effort must be unset")
	}
	m.SetEffort(enum.EffortHigh)
	if m.Effort() != enum.EffortHigh || m.Effort().IsUnset() {
		t.Fatal("effort not recorded")
	}
}
