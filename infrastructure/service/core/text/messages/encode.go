package textmessages

import (
	"encoding/base64"
	"encoding/json"
	"path"
	"strings"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	"github.com/jahrulnr/nusallm/infrastructure/service/wire"
)

// defaultMaxTokens fills the wire-required max_tokens when the model
// leaves it unset — Anthropic rejects the request without one. Generous
// enough to not clip thinking + answer on interactive turns; operators
// override per model when they want a tighter cap.
const defaultMaxTokens = 16384

// blocksKey is the adapter-namespaced optionalContent key carrying the
// assistant turn's ordered block template: text and tool_use entries are
// bare placeholders ({"type":"text"} / {"type":"tool_use"}) refilled from
// domain state; thinking, redacted_thinking, and server blocks are stored
// verbatim — signatures bind to the prompt prefix, so order and content
// must replay exactly as received.
const blocksKey = "messages.content_blocks"

// betaKey is the model optionalContent selector for the anthropic-beta
// header. Unlike messages.<param> body escape hatches it cannot ride the
// JSON body, so encodeRequest skips it and Send sets the header.
const betaKey = "messages.anthropic_beta"

var errUnsupported = errPlain("modality has no carrier on this wire")

// encodeRequest maps domain state onto the wire body. Attachments resolve
// to sendable form here (url/base64/text sources) — the domain stores
// references only.
func encodeRequest(conv *aggregate.Conversation, model entity.Model, stream bool, fileRoot wire.MediaRoot) (*RequestBody, error) {
	body := &RequestBody{
		Model:     model.ID(),
		MaxTokens: defaultMaxTokens,
		Stream:    stream,
		Metadata:  &MetadataBody{UserID: conv.ID()},
	}
	if mt := model.MaxOutputTokens(); mt != nil {
		body.MaxTokens = *mt
	}
	if sp := conv.SystemPrompt(); sp != "" {
		body.System = sp
	}
	for _, m := range conv.Messages() {
		msgs, err := encodeMessage(m, model, fileRoot)
		if err != nil {
			return nil, err
		}
		for _, msg := range msgs {
			// Roles must alternate — an assistant turn with calls
			// already emits a tool_result user turn, so a following
			// user message folds into it (the same merge rule the
			// generatecontent adapter applies). String content
			// normalizes to a text block so turns merge as one array.
			if n := len(body.Messages); n > 0 && body.Messages[n-1].Role == msg.Role {
				body.Messages[n-1].Content = append(
					contentBlocks(body.Messages[n-1].Content), contentBlocks(msg.Content)...)
			} else {
				body.Messages = append(body.Messages, msg)
			}
		}
	}
	for _, t := range conv.Tools() {
		if !t.CallableByAgent() {
			continue // system-only and disabled tools never reach the wire
		}
		body.Tools = append(body.Tools, ToolBody{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.Parameters(),
		})
	}

	body.Temperature = model.Temperature()
	body.TopP = model.TopP()
	body.TopK = model.TopK()
	body.StopSequences = model.StopSequences()
	// Params without a wire home are dropped, not zeroed — presence/
	// frequency penalty and seed don't exist on this surface; the provider
	// validates what it receives.
	if e := model.Effort(); !e.IsUnset() && e != enum.EffortNone {
		body.OutputConfig = &OutputConfig{Effort: e.String()}
	}
	// Adapter-namespaced escape hatch: "messages.<param>" lands verbatim —
	// this is how thinking config and cache_control ride through without
	// dedicated fields. Beta selectors are headers, not body fields.
	for k, v := range model.OptionalContent() {
		if k == betaKey {
			continue
		}
		if name, ok := strings.CutPrefix(k, "messages."); ok {
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

// contentBlocks normalizes a turn's content to a block array — bare
// strings become a single text block — so same-role turns merge into
// one content array.
func contentBlocks(content any) []any {
	switch c := content.(type) {
	case string:
		return []any{ContentBlock{Type: "text", Text: c}}
	case []any:
		return c
	case nil:
		return nil // no content to merge
	default:
		// A shape this switch predates still rides the merge as its own
		// block — dropping it would silently lose the turn's content.
		return []any{content}
	}
}

// encodeMessage emits one wire turn for plain messages, or an assistant
// turn followed by a user turn of tool_result blocks when the domain
// message carries executed calls — Anthropic has no tool role, results
// ride inside the next user turn. Injection calls hydrate as inline text
// (they are application-originated; their output attachments are never
// encoded). A call's own output attachments ride inside its tool_result
// block — this wire carries media there natively, no synthetic user
// message needed.
func encodeMessage(m entity.Message, model entity.Model, fileRoot wire.MediaRoot) ([]MessageBody, error) {
	role := "user"
	if m.Role() == enum.RoleAssistant {
		role = "assistant"
	}
	// Roles with no wire home (system/developer inside the message list)
	// encode as user turns — mid-conversation system turns are
	// model-conditional on this surface, a user turn is always legal.

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

	var blocks []any
	if role == "assistant" {
		blocks = encodeAssistantBlocks(m, text, origin)
	} else {
		if text != "" {
			blocks = append(blocks, ContentBlock{Type: "text", Text: text})
		}
		for _, a := range m.Attachments() {
			blocks = append(blocks, attachmentBlock(a, model, fileRoot))
		}
	}

	var content any = text
	if len(blocks) > 0 {
		content = blocks
	}
	msgs := []MessageBody{{Role: role, Content: content}}

	if len(origin) > 0 {
		var results []any
		for _, tc := range origin {
			content := tc.OutputWithUntrustedTagOrInterrupted()
			if atts := tc.OutputAttachments(); len(atts) > 0 {
				parts := []any{ContentBlock{Type: "text", Text: content}}
				for _, a := range atts {
					parts = append(parts, attachmentBlock(a, model, fileRoot))
				}
				results = append(results, ContentBlock{Type: "tool_result", ToolUseID: tc.ID(), Content: parts})
			} else {
				results = append(results, ContentBlock{Type: "tool_result", ToolUseID: tc.ID(), Content: content})
			}
		}
		msgs = append(msgs, MessageBody{Role: "user", Content: results})
	}
	return msgs, nil
}

// encodeAssistantBlocks rebuilds the assistant turn's content in the order
// the provider produced it. A stored template replays thinking and server
// blocks verbatim with text/tool_use refilled at their original positions;
// without one, text leads and tool_use blocks follow — the shape a
// non-thinking turn takes.
func encodeAssistantBlocks(m entity.Message, text string, origin []entity.ToolCall) []any {
	var blocks []any
	var template []json.RawMessage
	if raw := m.OptionalContent()[blocksKey]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &template) // malformed → default order below
	}
	textDone, used := false, 0
	emitText := func() {
		blocks = append(blocks, ContentBlock{Type: "text", Text: text})
		textDone = true
	}
	emitCall := func() {
		if used >= len(origin) {
			return
		}
		blocks = append(blocks, toolUseBlock(origin[used]))
		used++
	}
	for _, entry := range template {
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(entry, &head); err != nil {
			continue
		}
		switch head.Type {
		case "text":
			emitText()
		case "tool_use":
			emitCall()
		default:
			blocks = append(blocks, entry) // verbatim — signatures must not be reserialized
		}
	}
	if !textDone && (text != "" || len(blocks) == 0) {
		emitText()
	}
	for used < len(origin) {
		emitCall()
	}
	return blocks
}

// toolUseBlock encodes one domain call as a wire tool_use block — input is
// an object on this surface, so malformed stored input degrades to {}.
func toolUseBlock(tc entity.ToolCall) ContentBlock {
	in := json.RawMessage(tc.Input())
	if !json.Valid(in) || len(in) == 0 {
		in = json.RawMessage("{}")
	}
	return ContentBlock{Type: "tool_use", ID: tc.ID(), Name: tc.Name(), Input: in}
}

// attachmentBlock maps a stored reference to its wire block, degrading in
// place when the model can't take the modality or the wire can't carry it
// — a missing file must not wedge the whole request.
func attachmentBlock(a valueobject.Attachment, model entity.Model, fileRoot wire.MediaRoot) any {
	if !model.AcceptsInput(a.Modality()) {
		return ContentBlock{Type: "text", Text: "(" + a.Modality().String() + " attachment omitted: model does not accept it)"}
	}
	b, err := encodeAttachment(a, fileRoot)
	if err != nil {
		label := "(media attachment unavailable)"
		if err == errUnsupported {
			label = "(" + a.Modality().String() + " attachment omitted: this wire cannot carry it)"
		} else if name := safeAttachmentName(a.Name()); name != "" {
			label = "(media attachment unavailable: " + name + ")"
		}
		return ContentBlock{Type: "text", Text: label}
	}
	return b
}

// encodeAttachment resolves a reference into an image or document block.
// Remote URLs ride as url sources (the provider fetches them); local and
// inline payloads go base64 — or as a text document source when the media
// is already plain text.
func encodeAttachment(a valueobject.Attachment, fileRoot wire.MediaRoot) (ContentBlock, error) {
	switch a.Modality() {
	case enum.ModalityImage:
		src, err := source(a, fileRoot)
		if err != nil {
			return ContentBlock{}, err
		}
		return ContentBlock{Type: "image", Source: src}, nil
	case enum.ModalityFile, enum.ModalityText:
		if strings.HasPrefix(a.MediaType(), "text/") {
			src, err := textSource(a, fileRoot)
			if err != nil {
				return ContentBlock{}, err
			}
			return ContentBlock{Type: "document", Source: src}, nil
		}
		src, err := source(a, fileRoot)
		if err != nil {
			return ContentBlock{}, err
		}
		return ContentBlock{Type: "document", Source: src}, nil
	default:
		return ContentBlock{}, errUnsupported // audio/video — no carrier here
	}
}

// source picks url vs base64 by scheme — remote references pass through,
// local/inline materialize.
func source(a valueobject.Attachment, fileRoot wire.MediaRoot) (*SourceBody, error) {
	uri := a.URI()
	if strings.HasPrefix(uri, "https://") || strings.HasPrefix(uri, "http://") {
		if _, err := wire.ResolveURI(a, fileRoot); err != nil {
			return nil, err
		}
		return &SourceBody{Type: "url", URL: uri}, nil
	}
	data, _, err := wire.ResolveBase64(a, fileRoot)
	if err != nil {
		return nil, err
	}
	return &SourceBody{Type: "base64", MediaType: a.MediaType(), Data: data}, nil
}

// textSource materializes a text/* attachment as a document text source —
// the wire wants the content itself, not an encoding of it.
func textSource(a valueobject.Attachment, fileRoot wire.MediaRoot) (*SourceBody, error) {
	uri := a.URI()
	if strings.HasPrefix(uri, "https://") || strings.HasPrefix(uri, "http://") {
		if _, err := wire.ResolveURI(a, fileRoot); err != nil {
			return nil, err
		}
		return &SourceBody{Type: "url", URL: uri}, nil
	}
	data, _, err := wire.ResolveBase64(a, fileRoot)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, err
	}
	return &SourceBody{Type: "text", MediaType: a.MediaType(), Data: string(raw)}, nil
}

func safeAttachmentName(name string) string {
	name = path.Base(strings.ReplaceAll(name, `\`, "/"))
	if name == "." || name == "/" {
		return ""
	}
	return name
}

type plainError string

func (e plainError) Error() string { return string(e) }

func errPlain(s string) error { return plainError(s) }

// betaHeaderValue flattens the model's beta selector into the header form
// Anthropic accepts: JSON string array, JSON string, or a comma/whitespace
// separated list. Empty entries and duplicates are removed.
func betaHeaderValue(model entity.Model) string {
	raw := strings.TrimSpace(model.OptionalContent()[betaKey])
	if raw == "" {
		return ""
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		var one string
		if err := json.Unmarshal([]byte(raw), &one); err == nil {
			values = []string{one}
		} else {
			values = strings.FieldsFunc(raw, func(r rune) bool {
				return r == ',' || r == '\n' || r == '\r' || r == ' ' || r == '\t'
			})
		}
	}
	seen := make(map[string]bool, len(values))
	out := values[:0]
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return strings.Join(out, ", ")
}
