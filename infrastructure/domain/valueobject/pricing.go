package valueobject

import (
	"errors"
	"math"
)

// Pricing is the per-1M-token USD rate card a gateway publishes for a
// model. Rates are catalog metadata — informational, never authoritative
// for billing; the provider's invoice is.
type Pricing struct {
	inputPerM      float64 // USD per 1M uncached input tokens
	outputPerM     float64 // USD per 1M output tokens
	cacheReadPerM  float64 // USD per 1M cache-read tokens
	cacheWritePerM float64 // USD per 1M cache-write tokens
}

func NewPricing(inputPerM, outputPerM, cacheReadPerM, cacheWritePerM float64) (Pricing, error) {
	p := Pricing{
		inputPerM:      inputPerM,
		outputPerM:     outputPerM,
		cacheReadPerM:  cacheReadPerM,
		cacheWritePerM: cacheWritePerM,
	}
	return p, p.validate()
}

func (p Pricing) validate() error {
	// Rates persist as JSON — NaN and ±Inf have no JSON spelling, so a
	// non-finite value would make the whole record unmarshalable. NaN
	// also fails every ordered comparison, including the < 0 check.
	for _, r := range []float64{p.inputPerM, p.outputPerM, p.cacheReadPerM, p.cacheWritePerM} {
		if r < 0 || math.IsNaN(r) || math.IsInf(r, 0) {
			return errors.New("rates must be finite and non-negative")
		}
	}
	return nil
}

func (p Pricing) InputPerM() float64      { return p.inputPerM }
func (p Pricing) OutputPerM() float64     { return p.outputPerM }
func (p Pricing) CacheReadPerM() float64  { return p.cacheReadPerM }
func (p Pricing) CacheWritePerM() float64 { return p.cacheWritePerM }

// Cost estimates the USD cost of usage against this rate card.
func (p Pricing) Cost(u Usage) float64 {
	const perM = 1_000_000
	return (float64(u.InputTokens())*p.inputPerM +
		float64(u.CacheReadTokens())*p.cacheReadPerM +
		float64(u.CacheWriteTokens())*p.cacheWritePerM +
		float64(u.OutputTokens())*p.outputPerM) / perM
}
