package entity

import (
	"testing"

	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
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
	m.SetTemperature(0)
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
