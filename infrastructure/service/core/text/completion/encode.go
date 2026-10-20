package textcompletion

import (
	"encoding/json"
	"path"
	"strings"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	"github.com/jahrulnr/nusallm/infrastructure/service/wire"
)

// encodeRequest maps domain state onto the wire body. Attachments are
// resolved to sendable form here (data:/https passthrough, file:// read +
// base64) — the domain stores references only.
func encodeRequest(conv *aggregate.Conversation, model entity.Model, stream bool, fileRoot wire.MediaRoot, endpoint string) (*RequestBody, error) {
	body := &RequestBody{Model: model.ID(), Stream: stream}
	body.PromptCacheKey = conv.ID()
	body.PromptCacheOption = RequestPromptCacheOption{TTL: "30m"}
	if stream {
		body.StreamOptions = &StreamOptions{IncludeUsage: true} // else usage never arrives
	}

	if sp := conv.SystemPrompt(); sp != "" {
		body.Messages = append(body.Messages, MessageBody{Role: enum.RoleSystem.String(), Content: sp})
	}
	for _, m := range conv.Messages() {
		msgs, err := encodeMessage(m, model, fileRoot)
		if err != nil {
			return nil, err
		}
		body.Messages = append(body.Messages, msgs...)
	}
	for _, t := range conv.Tools() {
		if !t.CallableByAgent() {
			continue // system-only and disabled tools never reach the wire
		}
		var tb ToolBody
		tb.Type = "function"
		tb.Function.Name = t.Name()
		tb.Function.Description = t.Description()
		tb.Function.Parameters = t.Parameters()
		body.Tools = append(body.Tools, tb)
	}

	body.Temperature = model.Temperature()
	body.TopP = model.TopP()
	body.TopK = model.TopK()
	body.MaxTokens = model.MaxOutputTokens()
	body.PresencePenalty = model.PresencePenalty()
	body.FrequencyPenalty = model.FrequencyPenalty()
	body.Seed = model.Seed()
	body.Stop = model.StopSequences()
	if e := model.Effort(); !e.IsUnset() {
		body.ReasoningEffort = e.String() // OpenAI native; OpenRouter translates
	}
	// An upstream pin routes an aggregator request to one named backend —
	// no silent fallback to a provider the user did not pick.
	if up := model.Upstream(); up != "" && strings.Contains(endpoint, string(enum.OpenRouter)) {
		pref, err := json.Marshal(map[string]any{
			"order":           []string{up},
			"allow_fallbacks": false,
		})
		if err == nil {
			if body.Extra == nil {
				body.Extra = map[string]json.RawMessage{}
			}
			body.Extra["provider"] = pref
		}
	}
	// Adapter-namespaced escape hatch: "completion.<param>" lands verbatim.
	for k, v := range model.OptionalContent() {
		if name, ok := strings.CutPrefix(k, "completion."); ok {
			if body.Extra == nil {
				body.Extra = map[string]json.RawMessage{}
			}
			var raw json.RawMessage
			if err := json.Unmarshal([]byte(v), &raw); err != nil {
				raw, _ = json.Marshal(v) // not JSON → plain string value
			}
			body.Extra[name] = raw
		}
	}
	return body, nil
}

// encodeMessage emits one wire message for plain turns, or an assistant
// message followed by tool-role messages when it carries executed calls.
// Injection calls hydrate the wire as inline text — they are
// application-originated and must not pose as provider tool calls; their
// output attachments are likewise never encoded (app-produced context is
// text by contract).
// A call's output attachments ride a synthetic user message: the
// chat-completions tool role is text-only, so the openclaw-style
// follow-up is the portable shape — Responses-style wires would put the
// parts inside function_call_output instead. Those follow-ups land after
// the whole tool block: strict providers require an uninterrupted run of
// tool messages behind tool_calls, so a media-bearing earlier call must
// not push its user message between two tool results.
func encodeMessage(m entity.Message, model entity.Model, fileRoot wire.MediaRoot) ([]MessageBody, error) {
	out := MessageBody{Role: m.Role().String()}

	var origin, injected []entity.ToolCall
	for _, tc := range m.ToolCalls() {
		if tc.Type() == enum.TooltypeInjection {
			injected = append(injected, tc)
		} else {
			origin = append(origin, tc)
		}
	}

	text := m.Content()
	for _, tc := range injected {
		if tc.HasOutput() {
			text += "\n" + tc.OutputWithUntrustedTag() // already an untrusted-evidence envelope
		}
	}

	switch {
	case len(m.Attachments()) > 0:
		parts := []any{}
		if text != "" {
			parts = append(parts, map[string]any{"type": "text", "text": text})
		}
		for _, a := range m.Attachments() {
			if !model.AcceptsInput(a.Modality()) {
				parts = append(parts, map[string]any{"type": "text",
					"text": "(" + a.Modality().String() + " attachment omitted: model does not accept it)"})
				continue
			}
			p, err := encodeAttachment(a, fileRoot)
			if err != nil {
				label := "(media attachment unavailable)"
				if name := safeAttachmentName(a.Name()); name != "" {
					label = "(media attachment unavailable: " + name + ")"
				}
				parts = append(parts, map[string]any{"type": "text", "text": label})
				continue
			}
			parts = append(parts, p)
		}
		out.Content = parts
	default:
		out.Content = text
	}

	for _, tc := range origin {
		var wb ToolCallBody
		wb.ID = tc.ID()
		wb.Type = "function"
		wb.Function.Name = tc.Name()
		wb.Function.Arguments = tc.Input()
		out.ToolCalls = append(out.ToolCalls, wb)
	}
	// Replay artifacts: "completion.<field>" optionalContent → wire field.
	// reasoning_details is the structured payload (signatures and
	// encrypted blobs); reasoning is OpenRouter's plaintext alias and
	// reasoning_content is DeepSeek's. Prefer the structured details when
	// present — sending both forms duplicates the reasoning text.
	var plainReasoning, plainContent string
	for k, v := range m.OptionalContent() {
		switch k {
		case reasoningKey:
			plainReasoning = v
		case reasoningContentKey:
			plainContent = v
		case reasoningDetailsKey:
			if json.Valid([]byte(v)) {
				out.ReasoningDetails = json.RawMessage(v)
			}
		}
	}
	if len(out.ReasoningDetails) == 0 {
		if plainReasoning != "" {
			out.Reasoning = plainReasoning
		} else {
			out.ReasoningContent = plainContent
		}
	}

	msgs := []MessageBody{out}
	var media []MessageBody
	for _, tc := range origin {
		content := tc.OutputWithUntrustedTagOrInterrupted()
		msgs = append(msgs, MessageBody{
			Role:       "tool",
			ToolCallID: tc.ID(),
			Content:    content,
		})
		if atts := tc.OutputAttachments(); len(atts) > 0 {
			// Per-attachment degrade at send time, not record time: the
			// bound model can change between turns, and a missing file
			// must not wedge the whole request. The call id labels the
			// pairing now that the block is detached from its tool message.
			parts := []any{map[string]any{"type": "text",
				"text": "Attached output from tool " + tc.Name() + " (call " + tc.ID() + "):"}}
			for _, a := range atts {
				if !model.AcceptsInput(a.Modality()) {
					parts = append(parts, map[string]any{"type": "text",
						"text": "(" + a.Modality().String() + " output omitted: model does not accept it)"})
					continue
				}
				p, err := encodeAttachment(a, fileRoot)
				if err != nil {
					label := "(media output unavailable)"
					if name := safeAttachmentName(a.Name()); name != "" {
						label = "(media output unavailable: " + name + ")"
					}
					parts = append(parts, map[string]any{"type": "text", "text": label})
					continue
				}
				parts = append(parts, p)
			}
			media = append(media, MessageBody{Role: enum.RoleUser.String(), Content: parts})
		}
	}
	return append(msgs, media...), nil
}

func safeAttachmentName(name string) string {
	name = path.Base(strings.ReplaceAll(name, `\`, "/"))
	if name == "." || name == "/" {
		return ""
	}
	return name
}

// encodeAttachment maps a stored URI to the wire content part for its
// modality. Audio must arrive as base64 — URL audio is rejected upstream.
// Pinned attachments verify here: a digest mismatch surfaces as a resolve
// error, which callers degrade or propagate as their contract dictates.
func encodeAttachment(a valueobject.Attachment, fileRoot wire.MediaRoot) (any, error) {
	switch a.Modality() {
	case enum.ModalityImage:
		u, err := wire.ResolveURI(a, fileRoot)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "image_url", "image_url": map[string]any{"url": u}}, nil
	case enum.ModalityAudio:
		data, format, err := wire.ResolveBase64(a, fileRoot)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": data, "format": format}}, nil
	case enum.ModalityVideo:
		u, err := wire.ResolveURI(a, fileRoot)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "video_url", "video_url": map[string]any{"url": u}}, nil // OpenRouter extension
	default: // file, text, anything else
		u, err := wire.ResolveURI(a, fileRoot)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "file", "file": map[string]any{
			"filename": safeAttachmentName(a.Name()), "file_data": u,
		}}, nil
	}
}
