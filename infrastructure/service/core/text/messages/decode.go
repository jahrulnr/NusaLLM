package textmessages

import (
	"encoding/json"
	"strings"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	"github.com/jahrulnr/nusallm/infrastructure/service/wire"
)

// decodeMessage maps wire content blocks onto a domain Message. Blocks
// arrive as raw JSON: text concatenates into content, tool_use becomes
// origin calls (input is a parsed object — stored as raw JSON), and the
// ordered block template lands in optionalContent for verbatim replay
// next request — thinking signatures are bound to the exact block bytes,
// so non-model blocks are never reserialized through our struct.
func decodeMessage(rawBlocks []json.RawMessage, stopReason string) (entity.Message, decodeMeta, error) {
	var text, reasoning strings.Builder
	var template []json.RawMessage
	keepTemplate := false

	msg, err := entity.NewMessage(enum.RoleAssistant, "")
	if err != nil {
		return entity.Message{}, decodeMeta{}, err
	}
	for _, raw := range rawBlocks {
		var b ContentBlock
		if err := json.Unmarshal(raw, &b); err != nil {
			continue // unparseable block — skip, don't fail the turn
		}
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
			template = append(template, json.RawMessage(`{"type":"text"}`))
		case "thinking":
			reasoning.WriteString(b.Thinking)
			template = append(template, raw) // signature rides verbatim
			keepTemplate = true
		case "tool_use":
			input := string(b.Input)
			if input == "" {
				input = "{}" // upstreams legitimately emit empty arguments
			}
			tc, err := entity.NewToolCall(b.ID, b.Name, input, enum.TooltypeOrigin)
			if err != nil {
				return entity.Message{}, decodeMeta{}, err
			}
			msg.AddToolCall(tc)
			template = append(template, json.RawMessage(`{"type":"tool_use"}`))
		default:
			// redacted_thinking and server-side blocks — keep the raw
			// block so replay round-trips what we never model.
			template = append(template, raw)
			keepTemplate = true
		}
	}
	msg.SetContent(text.String())
	if fr := mapStopReason(stopReason); fr != "" {
		msg.SetFinishReason(fr)
	}
	if keepTemplate {
		if raw, err := json.Marshal(template); err == nil {
			msg.SetOptionalContent(blocksKey, string(raw))
		}
	}
	// Thinking text itself lives inside the stored template — callers that
	// want it (live status) get it from the return value, not a second key.
	return msg, decodeMeta{reasoning: reasoning.String()}, nil
}

// decodeMeta is the by-product of a decode — data the caller emits as
// events but the domain stores elsewhere (reasoning rides the template).
type decodeMeta struct {
	reasoning string
}

// mapStopReason normalizes Anthropic stop_reason onto the domain set —
// context exhaustion maps to length (a truncation signal), pause_turn and
// friends stay verbatim so their cause is never rewritten.
func mapStopReason(s string) enum.FinishReason {
	switch s {
	case "end_turn", "stop_sequence":
		return enum.FinishReasonStop
	case "tool_use":
		return enum.FinishReasonToolCalls
	case "max_tokens", "model_context_window_exceeded":
		return enum.FinishReasonLength
	case "refusal":
		return enum.FinishReasonContentFilter
	case "":
		return ""
	default:
		return enum.FinishReason(s) // preserve unknown values verbatim
	}
}

// decodeUsage normalizes Anthropic usage — input_tokens is already the
// UNCACHED count (cache read/write are disjoint fields), so it goes
// through NewUsage directly.
func decodeUsage(u *UsageBody) (valueobject.Usage, error) {
	if u == nil {
		return valueobject.Usage{}, nil
	}
	return valueobject.NewUsage(u.InputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens, u.OutputTokens)
}

// statusError builds the routing-facing error for a non-200 response.
// Anthropic names context overflow in the message text rather than a code,
// so the compact-triggering 413 remap happens here on the untruncated
// message — wire.StatusError owns truncation and the fallback wording.
func statusError(status int, body []byte) error {
	var env struct {
		Error ErrorBody `json:"error"`
	}
	msg, code := "", ""
	if err := json.Unmarshal(body, &env); err == nil {
		msg = env.Error.Message
		code = env.Error.Type
	}
	if status == 400 && contextOverflow(msg) {
		return apperror.FromStatus(413, "context length exceeded", wire.UpstreamError(body))
	}
	return wire.StatusError(status, msg, code, body)
}

// contextOverflow spots Anthropic's context-window 400s — "prompt is too
// long: N tokens > M maximum" — by the stable message fragment.
func contextOverflow(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "prompt is too long") || strings.Contains(m, "context window")
}

// streamStatusHint maps a mid-stream error event to a status — the stream
// carries no HTTP status, so derive one from the error type.
func streamStatusHint(e *ErrorBody) int {
	switch e.Type {
	case "rate_limit_error":
		return 429
	case "authentication_error":
		return 401
	case "permission_error":
		return 403
	case "overloaded_error":
		return 529
	case "timeout_error":
		return 504
	case "invalid_request_error":
		if contextOverflow(e.Message) {
			return 413
		}
		return 400
	default:
		return 502
	}
}
