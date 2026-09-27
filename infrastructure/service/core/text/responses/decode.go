package textresponses

import (
	"encoding/json"
	"strings"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	"github.com/jahrulnr/nusallm/infrastructure/service/wire"
)

// decodeResponse maps the output item list onto a domain Message. Items
// arrive as raw JSON: output_text concatenates into content, function_call
// becomes an origin call (arguments stay a JSON string), reasoning summary
// text surfaces as reasoning, and the ordered output template lands in
// optionalContent — reasoning blobs and server items must replay
// byte-exact, so non-model items are never reserialized through our shape.
func decodeResponse(rb *ResponseBody) (entity.Message, decodeMeta, error) {
	var text, reasoning strings.Builder
	var template []json.RawMessage
	keepTemplate, refused := false, false

	msg, err := entity.NewMessage(enum.RoleAssistant, "")
	if err != nil {
		return entity.Message{}, decodeMeta{}, err
	}
	for _, raw := range rb.Output {
		var it ItemShape
		if err := json.Unmarshal(raw, &it); err != nil {
			continue // unparseable item — skip, don't fail the turn
		}
		switch it.Type {
		case "message":
			for _, p := range it.Content {
				switch p.Type {
				case "output_text":
					text.WriteString(p.Text)
				case "refusal":
					refused = true
					text.WriteString(p.Refusal)
				}
			}
			template = append(template, json.RawMessage(`{"type":"message"}`))
		case "function_call":
			input := it.Arguments
			if input == "" || !json.Valid(json.RawMessage(input)) {
				input = "{}" // upstreams legitimately emit empty arguments
			}
			tc, err := entity.NewToolCall(it.CallID, it.Name, input, enum.TooltypeOrigin)
			if err != nil {
				return entity.Message{}, decodeMeta{}, err
			}
			msg.AddToolCall(tc)
			template = append(template, json.RawMessage(`{"type":"function_call"}`))
		case "reasoning":
			for _, s := range it.Summary {
				if s.Type == "summary_text" {
					reasoning.WriteString(s.Text)
				}
			}
			template = append(template, raw) // encrypted blob rides verbatim
			keepTemplate = true
		default:
			// web_search_call, file_search_call, mcp_*, … — keep raw so
			// replay round-trips what we never model.
			template = append(template, raw)
			keepTemplate = true
		}
	}
	msg.SetContent(text.String())
	msg.SetFinishReason(mapStatus(rb, refused, len(msg.ToolCalls()) > 0))
	if keepTemplate {
		if raw, err := json.Marshal(template); err == nil {
			msg.SetOptionalContent(itemsKey, string(raw))
		}
	}
	if rb.ID != "" {
		msg.SetOptionalContent("responses.id", rb.ID) // support/debug handle
	}
	return msg, decodeMeta{reasoning: reasoning.String()}, nil
}

// decodeMeta is the by-product of a decode — data the caller emits as
// events but the domain stores elsewhere (reasoning rides the template).
type decodeMeta struct {
	reasoning string
}

// mapStatus normalizes the response lifecycle status onto the domain set —
// incomplete carries its cause in incomplete_details; a refusal part wins
// over a generic completed.
func mapStatus(rb *ResponseBody, refused, hasCalls bool) enum.FinishReason {
	if refused {
		return enum.FinishReasonContentFilter
	}
	if hasCalls {
		return enum.FinishReasonToolCalls
	}
	switch rb.Status {
	case "completed":
		return enum.FinishReasonStop
	case "incomplete":
		if rb.IncompleteDetails != nil && rb.IncompleteDetails.Reason == "max_output_tokens" {
			return enum.FinishReasonLength
		}
		return enum.FinishReasonLength // incomplete without detail is still truncation-shaped
	case "failed", "":
		return ""
	default:
		return enum.FinishReason(rb.Status) // preserve unknown values verbatim
	}
}

// decodeUsage normalizes Responses usage — input_tokens is the INCLUSIVE
// total (cached tokens are a subset), so it goes through
// NewUsageFromTotal.
func decodeUsage(u *UsageBody) (valueobject.Usage, error) {
	if u == nil {
		return valueobject.Usage{}, nil
	}
	return valueobject.NewUsageFromTotal(u.InputTokens, u.InputDetails.CachedTokens, u.CacheWrite, u.OutputTokens)
}

// statusError builds the routing-facing error for a non-200 response. The
// envelope carries {error:{message,type,code,param}}; context overflow on
// this surface arrives as a 400 whose code/param names the window.
func statusError(status int, body []byte) error {
	var env struct {
		Error ErrorBody `json:"error"`
	}
	msg, code := "upstream request failed", ""
	if err := json.Unmarshal(body, &env); err == nil {
		if env.Error.Message != "" {
			msg = env.Error.Message
		}
		code = errorCode(env.Error)
	}
	if status == 400 && contextOverflow(msg, code) {
		return apperror.New(413, "context length exceeded", wire.UpstreamError(body))
	}
	return wire.StatusError(status, msg, code, body)
}

// errorCode flattens the code field — the wire emits it as either a string
// or, on some responses, a nested object with its own code.
func errorCode(e ErrorBody) string {
	switch c := e.Code.(type) {
	case string:
		return c
	case map[string]any:
		if s, ok := c["code"].(string); ok {
			return s
		}
	}
	return ""
}

// contextOverflow spots context-window rejections by their stable code or
// message fragments — this surface names them context_length_exceeded.
func contextOverflow(msg, code string) bool {
	if code == "context_length_exceeded" {
		return true
	}
	m := strings.ToLower(msg)
	return strings.Contains(m, "context length") || strings.Contains(m, "context window") ||
		strings.Contains(m, "maximum context")
}

// streamStatusHint maps a mid-stream error event to a status — the stream
// carries no HTTP status, so derive one from the error type/code.
func streamStatusHint(e *ErrorBody) int {
	code := errorCode(*e)
	switch {
	case code == "rate_limit_exceeded" || e.Type == "rate_limit_error":
		return 429
	case code == "context_length_exceeded" || contextOverflow(e.Message, code):
		return 413
	case e.Type == "authentication_error" || code == "invalid_api_key":
		return 401
	case e.Type == "permission_error" || code == "insufficient_quota":
		return 403
	case e.Type == "server_error" || code == "internal_error":
		return 502
	default:
		return 502
	}
}
