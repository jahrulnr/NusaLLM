package valueobject

import (
	"math"
	"testing"
)

func TestNewPricing(t *testing.T) {
	if _, err := NewPricing(2, 12, 0.2, 2.5); err != nil {
		t.Fatalf("NewPricing: %v", err)
	}
	if _, err := NewPricing(-1, 0, 0, 0); err == nil {
		t.Fatal("expected error for negative rate")
	}
}

// Cost maps the four rate card entries onto the four Usage counters.
func TestPricingCost(t *testing.T) {
	p, err := NewPricing(2, 12, 0.2, 2.5) // USD per 1M tokens
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewUsage(1_000_000, 1_000_000, 1_000_000, 500_000)
	if err != nil {
		t.Fatal(err)
	}
	// 1M*2 + 1M*0.2 + 1M*2.5 + 0.5M*12 = 2 + 0.2 + 2.5 + 6 = 10.7
	got := p.Cost(u)
	if got < 10.69 || got > 10.71 {
		t.Fatalf("Cost = %v, want ~10.7", got)
	}
}

// Rates are JSON-persisted catalog metadata. NaN and ±Inf are not valid
// JSON numbers: json.Marshal rejects them, so a provider catalog that
// spells a rate "NaN" or "Inf" would make the gateway record
// unsaveable. Validate finiteness at the value object rather than
// discovering it at the store.
func TestNewPricingRejectsNonFiniteRates(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   float64
	}{
		{"NaN", math.NaN()},
		{"+Inf", math.Inf(1)},
		{"-Inf", math.Inf(-1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewPricing(tc.in, 0, 0, 0); err == nil {
				t.Fatalf("NewPricing(%v) accepted a non-finite rate", tc.in)
			}
		})
	}
}
