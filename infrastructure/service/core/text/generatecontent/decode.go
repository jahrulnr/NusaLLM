package textgeneratecontent

import (
	"encoding/json"
	"strings"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	"github.com/jahrulnr/nusallm/infrastructure/service/wire"
)

// decodeParts maps wire parts onto a domain Message. Parts arrive as raw
// JSON: plain text concatenates into content, thought parts become
// reasoning, functionCall becomes an origin call (args is a JSON object —
// stored as raw JSON), and the ordered part template lands in
// optionalContent for verbatim replay — a thoughtSignature binds to the
// exact part bytes, so signed parts are never reserialized.
func decodeParts(rawParts []json.RawMessage, finishReason string) (entity.Message, decodeMeta, error) {
	var text, reasoning strings.Builder
	var template []json.RawMessage
	keepTemplate := false

	msg, err := entity.NewMessage(enum.RoleAssistant, "")
	if err != nil {
		return entity.Message{}, decodeMeta{}, err
	}
	for _, raw := range rawParts {
		var p PartBody
		if err := json.Unmarshal(raw, &p); err != nil {
			continue // unparseable part — skip, don't fail the turn
		}
		signed := p.ThoughtSignature != ""
		switch {
		case p.FunctionCall != nil:
			id := p.FunctionCall.ID
			if id == "" {
				id = p.FunctionCall.Name // legacy name-matching when no id issued
			}
			input := string(p.FunctionCall.Args)
			if input == "" || !json.Valid(json.RawMessage(input)) {
				input = "{}" // upstreams legitimately emit empty arguments
			}
			tc, err := entity.NewToolCall(id, p.FunctionCall.Name, input, enum.TooltypeOrigin)
			if err != nil {
				return entity.Message{}, decodeMeta{}, err
			}
			if p.FunctionCall.ID != "" {
				// Marker for replay: functionResponse.id echoes this.
				tc.SetOptionalContent(callIDKey, p.FunctionCall.ID)
			}
			msg.AddToolCall(tc)
			if signed {
				template = append(template, raw) // signature binds the part bytes
				keepTemplate = true
			} else {
				template = append(template, json.RawMessage(`{"refill":"functionCall"}`))
			}
		case p.Thought:
			reasoning.WriteString(p.Text)
			template = append(template, raw) // thought parts replay verbatim
			keepTemplate = true
		case p.Text != "" || textPart(raw):
			text.WriteString(p.Text)
			if signed {
				template = append(template, raw) // signed answer — verbatim
				keepTemplate = true
			} else {
				template = append(template, json.RawMessage(`{"refill":"text"}`))
			}
		default:
			// executableCode, codeExecutionResult, … — keep raw so replay
			// round-trips what we never model.
			template = append(template, raw)
			keepTemplate = true
		}
	}
	msg.SetContent(text.String())
	if fr := mapFinishReason(finishReason, len(msg.ToolCalls()) > 0); fr != "" {
		msg.SetFinishReason(fr)
	}
	if keepTemplate {
		if raw, err := json.Marshal(template); err == nil {
			msg.SetOptionalContent(partsKey, string(raw))
		}
	}
	return msg, decodeMeta{reasoning: reasoning.String()}, nil
}

// textPart reports whether a raw part is a text part that decoded empty —
// the wire legitimately emits {"text":""} as a placeholder.
func textPart(raw json.RawMessage) bool {
	var probe struct {
		Text *string `json:"text"`
	}
	return json.Unmarshal(raw, &probe) == nil && probe.Text != nil
}

// decodeMeta is the by-product of a decode — reasoning rides the template
// for replay and surfaces to callers via the return value only.
type decodeMeta struct {
	reasoning string
}

// mapFinishReason normalizes the wire finishReason onto the domain set —
// a turn carrying calls reports tool_calls regardless of the emitted
// reason (Gemini says STOP alongside functionCall parts).
func mapFinishReason(fr string, hasCalls bool) enum.FinishReason {
	if hasCalls {
		return enum.FinishReasonToolCalls
	}
	switch fr {
	case "STOP":
		return enum.FinishReasonStop
	case "MAX_TOKENS":
		return enum.FinishReasonLength
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY":
		return enum.FinishReasonContentFilter
	case "":
		return ""
	default:
		return enum.FinishReason(strings.ToLower(fr)) // OTHER etc. — preserve verbatim-ish
	}
}

// decodeUsage normalizes usageMetadata — promptTokenCount is the INCLUSIVE
// total (cachedContentTokenCount a subset), and thoughts bill as output so
// they fold into the generated-token count.
func decodeUsage(u *UsageBody) (valueobject.Usage, error) {
	if u == nil {
		return valueobject.Usage{}, nil
	}
	return valueobject.NewUsageFromTotal(u.PromptTokens, u.CachedTokens, 0, u.CandidatesTokens+u.ThoughtsTokens)
}

// statusError builds the routing-facing error for a non-200 response —
// the envelope is {error:{code,message,status}} with gRPC-style names.
func statusError(status int, body []byte) error {
	var env struct {
		Error ErrorBody `json:"error"`
	}
	msg, status_ := "upstream request failed", ""
	if err := json.Unmarshal(body, &env); err == nil {
		if env.Error.Message != "" {
			msg = env.Error.Message
		}
		status_ = env.Error.Status
	}
	if contextOverflow(msg, status_) {
		return apperror.New(413, "context length exceeded", wire.UpstreamError(body))
	}
	return wire.StatusError(status, msg, status_, body)
}

// contextOverflow spots context-window rejections — this surface names
// them INVALID_ARGUMENT/400 with a token-count message.
func contextOverflow(msg, status string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "maximum number of tokens") ||
		strings.Contains(m, "context window") ||
		strings.Contains(m, "input is too long") ||
		(status == "INVALID_ARGUMENT" && strings.Contains(m, "token"))
}

// streamError maps a mid-stream error payload — the stream carries the
// same envelope; status name drives the routing code.
func streamError(e *ErrorBody, raw []byte) error {
	code := e.Code
	if code == 0 {
		code = 502
	}
	switch e.Status {
	case "RESOURCE_EXHAUSTED":
		code = 429
	case "UNAUTHENTICATED":
		code = 401
	case "PERMISSION_DENIED":
		code = 403
	}
	if contextOverflow(e.Message, e.Status) {
		code = 413
	}
	msg := e.Message
	if msg == "" {
		msg = "upstream stream failed"
	}
	return apperror.New(code, msg, wire.UpstreamError(raw))
}
