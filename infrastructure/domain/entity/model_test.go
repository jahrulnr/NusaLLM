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
