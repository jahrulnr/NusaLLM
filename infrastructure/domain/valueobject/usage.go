package valueobject

import "errors"

// Usage captures the token accounting a provider reports for a request.
// Convention: inputTokens is the UNCACHED input count — providers report
// cache counters in two different layouts and adapters must normalize into
// this shape:
//   - disjoint (Anthropic): input_tokens, cache_read_input_tokens and
//     cache_creation_input_tokens are separate counters → NewUsage.
//   - inclusive (OpenAI, Gemini): prompt_tokens / prompt_token_count already
//     contains the cached subset → NewUsageFromTotal.
//
// Adapters must never map an inclusive total straight into inputTokens —
// that double-counts cached tokens in TotalInput().
type Usage struct {
	inputTokens      int // uncached input only
	cacheReadTokens  int
	cacheWriteTokens int
	outputTokens     int
}

// NewUsage builds a Usage from disjoint counters — inputTokens must already
// be the uncached portion.
func NewUsage(inputTokens, cacheReadTokens, cacheWriteTokens, outputTokens int) (Usage, error) {
	u := Usage{
		inputTokens:      inputTokens,
		cacheReadTokens:  cacheReadTokens,
		cacheWriteTokens: cacheWriteTokens,
		outputTokens:     outputTokens,
	}
	return u, u.validate()
}

// NewUsageFromTotal builds a Usage for providers reporting an inclusive
// input total plus the cached subset (OpenAI input_tokens +
// input_tokens_details.cached_tokens, Gemini prompt_token_count +
// cached_content_token_count). The uncached portion is derived.
func NewUsageFromTotal(totalInput, cacheReadTokens, cacheWriteTokens, outputTokens int) (Usage, error) {
	uncached := totalInput - cacheReadTokens - cacheWriteTokens
	if uncached < 0 {
		return Usage{}, errors.New("cache counters exceed total input")
	}
	return NewUsage(uncached, cacheReadTokens, cacheWriteTokens, outputTokens)
}

func (u Usage) validate() error {
	if u.inputTokens < 0 || u.cacheReadTokens < 0 || u.cacheWriteTokens < 0 || u.outputTokens < 0 {
		return errors.New("token counts cannot be negative")
	}
	return nil
}

func (u Usage) InputTokens() int      { return u.inputTokens }
func (u Usage) CacheReadTokens() int  { return u.cacheReadTokens }
func (u Usage) CacheWriteTokens() int { return u.cacheWriteTokens }
func (u Usage) OutputTokens() int     { return u.outputTokens }

// TotalInput is the full billed input: uncached + cache reads + cache writes.
func (u Usage) TotalInput() int {
	return u.inputTokens + u.cacheReadTokens + u.cacheWriteTokens
}

func (u Usage) Total() int {
	return u.TotalInput() + u.outputTokens
}

// Add returns the sum of two usage reports. Providers report per request,
// so conversation-level totals accumulate across turns.
func (u Usage) Add(o Usage) Usage {
	return Usage{
		inputTokens:      u.inputTokens + o.inputTokens,
		cacheReadTokens:  u.cacheReadTokens + o.cacheReadTokens,
		cacheWriteTokens: u.cacheWriteTokens + o.cacheWriteTokens,
		outputTokens:     u.outputTokens + o.outputTokens,
	}
}
