package textcompletion

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	"github.com/jahrulnr/nusallm/infrastructure/service/wire"
)

// decodeMessage maps a wire assistant message onto a domain Message:
// content, origin tool calls, finish reason, and replay artifacts
// (reasoning_content rides optionalContent for the next request).
func decodeMessage(w MessageBody, finishReason string) (entity.Message, error) {
	msg, err := entity.NewMessage(enum.RoleAssistant, contentText(w.Content))
	if err != nil {
		return entity.Message{}, err
	}
	if fr, ok := enum.ParseFinishReason(finishReason); ok {
		msg.SetFinishReason(fr)
	} else if finishReason != "" {
		msg.SetFinishReason(enum.FinishReason(finishReason)) // preserve unknown values verbatim
	}
	for _, c := range w.ToolCalls {
		args := c.Function.Arguments
		if args == "" {
			args = "{}" // upstreams legitimately emit empty arguments
		}
		tc, err := entity.NewToolCall(c.ID, c.Function.Name, args, enum.TooltypeOrigin)
		if err != nil {
			return entity.Message{}, err
		}
		msg.AddToolCall(tc)
	}
	if w.ReasoningContent != "" {
		msg.SetOptionalContent("completion.reasoning_content", w.ReasoningContent)
	}
	return msg, nil
}

// contentText normalizes wire content — string or parts array — into text.
func contentText(c any) string {
	switch v := c.(type) {
	case nil:
		return ""
	case string:
		return v
	case []any: // some providers return parts on output too
		var b strings.Builder
		for _, p := range v {
			if pm, ok := p.(map[string]any); ok && pm["type"] == "text" {
				if s, ok := pm["text"].(string); ok {
					b.WriteString(s)
				}
			}
		}
		return b.String()
	default:
		return ""
	}
}

// decodeUsage normalizes provider usage: prompt_tokens is the INCLUSIVE
// total on this surface — cached tokens are a subset, so uncached input is
// derived via NewUsageFromTotal.
func decodeUsage(u *UsageBody) (valueobject.Usage, error) {
	if u == nil {
		return valueobject.Usage{}, nil
	}
	cacheWrite := u.CacheWrite
	if cacheWrite == 0 {
		cacheWrite = u.CacheCreationInput
	}
	return valueobject.NewUsageFromTotal(u.PromptTokens, u.PromptDetails.CachedTokens, cacheWrite, u.CompletionTokens)
}

// statusError builds the routing-facing error for a non-200 response via
// the shared convention — context_length 400 → 413 remap and the
// message-less fallback both live in wire.StatusError. The upstream
// message is safe to surface; the raw body stays in Cause.
func statusError(status int, body []byte) error {
	return wire.StatusError(status, upstreamMessage(body), upstreamCode(body), body)
}

// upstreamMessage extracts the provider's message when it is plain text —
// it describes request/parameter problems and is safe to surface. A body
// without one yields ""; wire owns the fallback wording.
func upstreamMessage(body []byte) string {
	var env struct {
		Error ErrorBody `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return ""
	}
	return env.Error.Message
}

func upstreamCode(body []byte) string {
	var env struct {
		Error ErrorBody `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return ""
	}
	switch c := env.Error.Code.(type) {
	case string:
		return c
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", c)
	}
}
