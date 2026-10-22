package textresponses

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

// itemsKey is the adapter-namespaced optionalContent key carrying the
// turn's ordered output template: message and function_call entries are
// bare placeholders refilled from domain state; reasoning and server items
// are stored verbatim — encrypted reasoning blobs must replay byte-exact.
const itemsKey = "responses.output_items"

var errUnsupported = errPlain("modality has no carrier on this wire")

// encodeRequest maps domain state onto the wire body. The conversation is
// sent stateless — store:false, full item list each turn — because our
// repository is the record and edits would break previous_response_id
// chains anyway. include: reasoning.encrypted_content opts into replayable
// reasoning so stateless turns keep their chain of thought.
func encodeRequest(conv *aggregate.Conversation, model entity.Model, stream bool, fileRoot wire.MediaRoot) (*RequestBody, error) {
	body := &RequestBody{
		Model:   model.ID(),
		Store:   false,
		Include: []string{"reasoning.encrypted_content"},
		Stream:  stream,
	}
	body.PromptCacheKey = conv.ID()
	if sp := conv.SystemPrompt(); sp != "" {
		body.Instructions = sp
	}
	for _, m := range conv.Messages() {
		items, err := encodeMessage(m, model, fileRoot)
		if err != nil {
			return nil, err
		}
		body.Input = append(body.Input, items...)
	}
	for _, t := range conv.Tools() {
		if !t.CallableByAgent() {
			continue // system-only and disabled tools never reach the wire
		}
		body.Tools = append(body.Tools, ToolBody{
			Type:        "function",
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Parameters(),
		})
	}

	body.MaxOutputTokens = model.MaxOutputTokens()
	body.Temperature = model.Temperature()
	body.TopP = model.TopP()
	// Params without a wire home are dropped, not zeroed — top_k, presence/
	// frequency penalty, seed, and stop sequences don't exist on this
	// surface; reasoning models reject sampling params outright. The
	// provider validates what it receives.
	if e := model.Effort(); !e.IsUnset() && e != enum.EffortNone {
		effort := e.String()
		switch e {
		case enum.EffortXHigh, enum.EffortMax:
			effort = "high" // no wire equivalent — clamp to the ceiling
		}
		// Effort already implies a reasoning model — a non-reasoning
		// model rejects effort the same with or without summary. Asking
		// for the auto summary is what makes its reasoning visible;
		// encrypted_content alone keeps the replay chain but surfaces
		// nothing to the user.
		body.Reasoning = &ReasoningBody{Effort: effort, Summary: "auto"}
	}
	// Adapter-namespaced escape hatch: "responses.<param>" lands verbatim —
	// text.verbosity, service_tier, tool_choice, a store:true override…
	for k, v := range model.OptionalContent() {
		if name, ok := strings.CutPrefix(k, "responses."); ok {
			if name == "include" {
				// include is additive: the adapter always needs the
				// encrypted reasoning opt-in for stateless replay, and
				// an escape hatch must not silently replace it.
				body.Include = mergeInclude(body.Include, v)
				continue
			}
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

// encodeMessage emits one message item for plain turns, or — for an
// assistant turn with executed calls — the replayed output items followed
// by function_call_output items and any media follow-up user items. Items
// arrive pre-marshaled: template entries are raw bytes that must pass
// through untouched, so a typed item envelope can't carry them.
// Injection calls are application-originated hydration: on a system-role
// record they encode as the function_call + function_call_output pair
// they record — observed evidence rides at tool-output trust, not inside
// an instruction turn. Anywhere else they hydrate as inline text (legacy
// user-role records keep their shape). Injection output attachments are
// never encoded. A call's output attachments ride inside its
// function_call_output — this surface carries content parts there.
func encodeMessage(m entity.Message, model entity.Model, fileRoot wire.MediaRoot) ([]json.RawMessage, error) {
	var origin, injected []entity.ToolCall
	for _, tc := range m.ToolCalls() {
		if tc.Type() == enum.TooltypeInjection {
			injected = append(injected, tc)
		} else {
			origin = append(origin, tc)
		}
	}

	// A system-role hydration record with calls to show takes the
	// completed-call wire shape: function_call items immediately answered
	// by function_call_output items (output items — no assistant
	// message wrapper needed on this surface).
	if m.Role() == enum.RoleSystem && len(injected) > 0 {
		var items []json.RawMessage
		if m.Content() != "" {
			it, _ := marshalItem(ItemBody{Type: "message", Role: m.Role().String(), Content: m.Content()})
			items = append(items, it...)
		}
		calls := append(append([]entity.ToolCall(nil), injected...), origin...)
		items = append(items, assistantItems(m, "", calls)...)
		return append(items, toolResultItems(calls, model, fileRoot)...), nil
	}

	text := m.Content()
	for _, tc := range injected {
		if tc.HasOutput() {
			text += "\n" + tc.OutputWithUntrustedTag() // already an untrusted-evidence envelope
		}
	}

	if m.Role() == enum.RoleAssistant {
		items := assistantItems(m, text, origin)
		return append(items, toolResultItems(origin, model, fileRoot)...), nil
	}

	role := m.Role().String() // user/developer/system pass through as items
	if len(m.Attachments()) == 0 {
		return marshalItem(ItemBody{Type: "message", Role: role, Content: text})
	}
	parts := []any{}
	if text != "" {
		parts = append(parts, PartBody{Type: "input_text", Text: text})
	}
	for _, a := range m.Attachments() {
		parts = append(parts, attachmentPart(a, model, fileRoot))
	}
	return marshalItem(ItemBody{Type: "message", Role: role, Content: parts})
}

// assistantItems rebuilds the turn's output items in provider order. A
// stored template replays reasoning and server items verbatim with
// message/function_call refilled at their marked positions; without one,
// the message item leads and calls follow — the shape a non-reasoning
// turn takes.
func assistantItems(m entity.Message, text string, origin []entity.ToolCall) []json.RawMessage {
	var items []json.RawMessage
	var template []json.RawMessage
	if raw := m.OptionalContent()[itemsKey]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &template) // malformed → default order
	}
	msgDone, used := false, 0
	emit := func(it ItemBody) {
		raw, err := marshalItem(it)
		if err == nil {
			items = append(items, raw...)
		}
	}
	emitMsg := func(phase string, body *string) {
		msgText := text
		if body != nil {
			msgText = *body
		}
		emit(ItemBody{Type: "message", Role: "assistant", Phase: phase,
			Content: []any{PartBody{Type: "output_text", Text: msgText}}})
		msgDone = true
	}
	emitCall := func() {
		if used >= len(origin) {
			return
		}
		tc := origin[used]
		in := tc.Input()
		if !json.Valid(json.RawMessage(in)) || in == "" {
			in = "{}"
		}
		emit(ItemBody{Type: "function_call", CallID: tc.ID(), Name: tc.Name(), Arguments: in})
		used++
	}
	for _, entry := range template {
		var head struct {
			Type  string  `json:"type"`
			Phase string  `json:"phase"`
			Text  *string `json:"text"`
		}
		if err := json.Unmarshal(entry, &head); err != nil {
			continue
		}
		switch head.Type {
		case "message":
			emitMsg(head.Phase, head.Text)
		case "function_call":
			emitCall()
		case "reasoning":
			// Stateless replay needs the encrypted blob — a reasoning
			// item without it is unverifiable padding; drop it
			// (documented ZDR behavior).
			var probe struct {
				Encrypted string `json:"encrypted_content"`
			}
			if json.Unmarshal(entry, &probe) == nil && strings.TrimSpace(probe.Encrypted) != "" {
				items = append(items, entry)
			}
		default:
			// Server-tool items (web_search_call, …) replay verbatim.
			items = append(items, entry)
		}
	}
	if !msgDone && (text != "" || (len(items) == 0 && len(origin) == 0)) {
		// An empty output_text is only the fallback when the turn carries
		// nothing else — never filler ahead of function_call items.
		emitMsg("", nil)
	}
	for used < len(origin) {
		emitCall()
	}
	return items
}

// toolResultItems emits one function_call_output per origin call; media
// attachments ride inside its output alongside the text — this wire
// carries parts there, so nothing is synthesized as a user item.
func toolResultItems(origin []entity.ToolCall, model entity.Model, fileRoot wire.MediaRoot) []json.RawMessage {
	if len(origin) == 0 {
		return nil
	}
	var items []json.RawMessage
	for _, tc := range origin {
		content := tc.OutputWithUntrustedTagOrInterrupted()
		var out any = content
		if atts := tc.OutputAttachments(); len(atts) > 0 {
			parts := []any{PartBody{Type: "input_text", Text: content}}
			for _, a := range atts {
				parts = append(parts, attachmentPart(a, model, fileRoot))
			}
			out = parts
		}
		item, _ := marshalItem(ItemBody{Type: "function_call_output", CallID: tc.ID(), Output: out})
		items = append(items, item...)
	}
	return items
}

// marshalItem encodes one input item as a single JSON element — the slice
// stays flat, one message or call per element, raw entries untouched.
func marshalItem(it ItemBody) ([]json.RawMessage, error) {
	raw, err := json.Marshal(it)
	if err != nil {
		return nil, err
	}
	return []json.RawMessage{raw}, nil
}

// attachmentPart maps a stored reference to an input part, degrading in
// place — a missing file or an uncarriable modality must not wedge the
// whole request.
func attachmentPart(a valueobject.Attachment, model entity.Model, fileRoot wire.MediaRoot) any {
	if !model.AcceptsInput(a.Modality()) {
		return PartBody{Type: "input_text", Text: "(" + a.Modality().String() + " attachment omitted: model does not accept it)"}
	}
	p, err := encodeAttachment(a, fileRoot)
	if err != nil {
		label := "(media attachment unavailable)"
		if err == errUnsupported {
			label = "(" + a.Modality().String() + " attachment omitted: this wire cannot carry it)"
		} else if name := safeAttachmentName(a.Name()); name != "" {
			label = "(media attachment unavailable: " + name + ")"
		}
		return PartBody{Type: "input_text", Text: label}
	}
	return p
}

// encodeAttachment resolves a reference into an input_image or input_file
// part — URLs pass through (image_url/file_url), local and inline payloads
// materialize as data: URIs (image_url/file_data).
func encodeAttachment(a valueobject.Attachment, fileRoot wire.MediaRoot) (PartBody, error) {
	uri := a.URI()
	remote := strings.HasPrefix(uri, "https://") || strings.HasPrefix(uri, "http://")
	switch a.Modality() {
	case enum.ModalityImage:
		u, err := wire.ResolveURI(a, fileRoot)
		if err != nil {
			return PartBody{}, err
		}
		return PartBody{Type: "input_image", ImageURL: u}, nil
	case enum.ModalityFile, enum.ModalityText:
		if remote {
			if _, err := wire.ResolveURI(a, fileRoot); err != nil {
				return PartBody{}, err
			}
			return PartBody{Type: "input_file", FileURL: uri, Filename: safeAttachmentName(a.Name())}, nil
		}
		u, err := wire.ResolveURI(a, fileRoot) // data: URI form
		if err != nil {
			return PartBody{}, err
		}
		return PartBody{Type: "input_file", FileData: u, Filename: safeAttachmentName(a.Name())}, nil
	default:
		return PartBody{}, errUnsupported // audio/video — no carrier here
	}
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

// mergeInclude appends configured include values to the adapter defaults.
// Values may arrive as a JSON string array, a JSON string, or a comma/
// whitespace separated list; duplicates and empty entries are ignored.
func mergeInclude(base []string, raw string) []string {
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
	seen := make(map[string]bool, len(base)+len(values))
	out := make([]string, 0, len(base)+len(values))
	for _, v := range append(append([]string{}, base...), values...) {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
