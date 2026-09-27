package transport

import (
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// UsagePayload is the shared Usage wire shape — used by HTTP
// conversation projections and by WebSocket step_finished frames so
// both render the same accounting fields.
type UsagePayload struct {
	InputTokens      int `json:"inputTokens"`
	CacheReadTokens  int `json:"cacheReadTokens"`
	CacheWriteTokens int `json:"cacheWriteTokens"`
	OutputTokens     int `json:"outputTokens"`
	TotalInputTokens int `json:"totalInputTokens"`
	TotalTokens      int `json:"totalTokens"`
}

// MarshalUsage projects a domain Usage onto the wire shape.
func MarshalUsage(u valueobject.Usage) UsagePayload {
	return UsagePayload{
		InputTokens:      u.InputTokens(),
		CacheReadTokens:  u.CacheReadTokens(),
		CacheWriteTokens: u.CacheWriteTokens(),
		OutputTokens:     u.OutputTokens(),
		TotalInputTokens: u.TotalInput(),
		TotalTokens:      u.Total(),
	}
}
