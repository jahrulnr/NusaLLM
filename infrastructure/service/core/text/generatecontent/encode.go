package textgeneratecontent

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

// defaultMaxTokens fills generationConfig.maxOutputTokens when the model
// leaves it unset — this wire uses a low internal default that truncates
// agent turns early, so it must always be sent.
const defaultMaxTokens = 16384

// partsKey is the adapter-namespaced optionalContent key carrying the
// model turn's ordered part template: {"refill":"text"} and
// {"refill":"functionCall"} are placeholders rebuilt from domain state;
// signed and thought parts are stored verbatim — a thoughtSignature binds
// to the exact part bytes, so they must never be reserialized.
const partsKey = "generatecontent.parts"

// callIDKey marks a ToolCall whose provider issued a call id — echoed as
// functionResponse.id for correlation. Absent means legacy name-matching.
const callIDKey = "generatecontent.call_id"

// encodeRequest maps domain state onto the wire body. contents are
// role-alternated user/model turns — same-role neighbours merge (parallel
// tool results legally share one user turn, and mid-list system/developer
// notes fold into user text).
func encodeRequest(conv *aggregate.Conversation, model entity.Model, fileRoot wire.MediaRoot) (*RequestBody, error) {
	body := &RequestBody{}
	if sp := conv.SystemPrompt(); sp != "" {
		body.SystemInstruction = &ContentBody{Parts: []any{PartBody{Text: sp}}}
	}
	for _, m := range conv.Messages() {
		for _, c := range encodeMessage(m, model, fileRoot) {
			// Adjacent same-role contents are invalid — merge by folding
			// parts into the open turn (parallel functionResponses rely on it).
			if n := len(body.Contents); n > 0 && body.Contents[n-1].Role == c.Role {
				body.Contents[n-1].Parts = append(body.Contents[n-1].Parts, c.Parts...)
			} else {
				body.Contents = append(body.Contents, c)
			}
		}
	}
	if decls := toolDecls(conv.Tools()); len(decls) > 0 {
		body.Tools = []ToolBody{{FunctionDeclarations: decls}}
	}

	gen := &GenerationConfigBody{MaxOutput: defaultMaxTokens}
	if mt := model.MaxOutputTokens(); mt != nil {
		gen.MaxOutput = *mt
	}
	gen.Temperature = model.Temperature()
	gen.TopP = model.TopP()
	gen.TopK = model.TopK()
	gen.StopSequences = model.StopSequences()
	// Params without a wire home are dropped, not zeroed — presence/
	// frequency penalty and seed don't exist on this surface.
	if tc := thinkingConfig(model); tc != nil {
		gen.ThinkingConfig = tc
	}
	body.GenerationConfig = gen

	// Adapter-namespaced escape hatch: "generatecontent.<param>" lands
	// verbatim — safetySettings, toolConfig, cachedContent, responseMimeType…
	for k, v := range model.OptionalContent() {
		if name, ok := strings.CutPrefix(k, "generatecontent."); ok {
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

// thinkingConfig maps the domain effort onto the family-correct control —
// Gemini 3 takes thinkingLevel (clamped at high, the wire ceiling), Gemini
// 2.5 takes includeThoughts alone (invented budgets get rejected), Gemma
// and unset effort take nothing. Unknown families get includeThoughts
// only — the shape both families accept.
func thinkingConfig(model entity.Model) *ThinkingConfigBody {
	e := model.Effort()
	if e.IsUnset() {
		return nil
	}
	id := strings.ToLower(model.ID())
	if strings.Contains(id, "gemma") {
		return nil // rejects thinkingConfig outright
	}
	on := true
	if strings.Contains(id, "gemini-3") {
		level := e.String()
		switch e {
		case enum.EffortNone:
			level = "minimal"
		case enum.EffortXHigh, enum.EffortMax:
			level = "high" // no wire equivalent — clamp to the ceiling
		}
		return &ThinkingConfigBody{ThinkingLevel: level, IncludeThoughts: &on}
	}
	return &ThinkingConfigBody{IncludeThoughts: &on}
}

// encodeMessage emits one content turn for plain messages, or a model turn
// (replayed template + calls) followed by a user turn of functionResponse
// parts when the message carries executed calls. Roles with no wire home
// (system/developer inside the list) encode as user turns — mid-
// conversation system turns are model-conditional on this surface.
// Injection calls are application-originated hydration: on a system-role
// record they encode as the functionCall + functionResponse pair they
// record — observed evidence rides at tool-output trust, not inside an
// instruction turn. Anywhere else they hydrate as trailing text (legacy
// user-role records keep their shape). A call's output attachments ride
// inlineData parts in the same user turn — this wire carries media
// there natively.
func encodeMessage(m entity.Message, model entity.Model, fileRoot wire.MediaRoot) []ContentBody {
	var origin, injected []entity.ToolCall
	for _, tc := range m.ToolCalls() {
		if tc.Type() == enum.TooltypeInjection {
			injected = append(injected, tc)
		} else {
			origin = append(origin, tc)
		}
	}

	// A system-role hydration record with calls to show takes the
	// completed-call wire shape: a model turn of functionCall parts
	// immediately answered by a user turn of functionResponse parts.
	if m.Role() == enum.RoleSystem && len(injected) > 0 {
		var contents []ContentBody
		if m.Content() != "" {
			contents = append(contents, ContentBody{Role: "user",
				Parts: []any{PartBody{Text: m.Content()}}})
		}
		calls := append(append([]entity.ToolCall(nil), injected...), origin...)
		contents = append(contents,
			ContentBody{Role: "model", Parts: modelParts(m, "", "", calls)},
			toolResultContents(calls, model, fileRoot))
		return contents
	}

	text := m.Content()
	var injectedText string
	for _, tc := range injected {
		if tc.HasOutput() {
			injectedText += "\n" + tc.OutputWithUntrustedTag() // untrusted-evidence envelope
		}
	}

	if m.Role() == enum.RoleAssistant {
		parts := modelParts(m, text, injectedText, origin)
		contents := []ContentBody{{Role: "model", Parts: parts}}
		if len(origin) > 0 {
			contents = append(contents, toolResultContents(origin, model, fileRoot))
		}
		return contents
	}

	// user + homeless roles ride the user turn.
	text += injectedText
	var parts []any
	if text != "" {
		parts = append(parts, PartBody{Text: text})
	}
	for _, a := range m.Attachments() {
		parts = append(parts, attachmentPart(a, model, fileRoot))
	}
	if len(parts) == 0 {
		parts = append(parts, PartBody{Text: ""})
	}
	return []ContentBody{{Role: "user", Parts: parts}}
}

// modelParts rebuilds the model turn's parts in provider order. A stored
// template replays signed and thought parts verbatim with text/
// functionCall refilled at their marked positions; without one, text
// leads and calls follow — the shape a non-thinking turn takes. Injected
// output appends as a fresh unsigned text part — it must not touch the
// signed prefix it wasn't part of.
func modelParts(m entity.Message, text, injectedText string, origin []entity.ToolCall) []any {
	var parts []any
	var template []json.RawMessage
	if raw := m.OptionalContent()[partsKey]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &template) // malformed → default order
	}
	textDone, used := false, 0
	emitText := func() {
		parts = append(parts, PartBody{Text: text})
		textDone = true
	}
	emitCall := func() {
		if used >= len(origin) {
			return
		}
		tc := origin[used]
		in := json.RawMessage(tc.Input())
		if !json.Valid(in) || len(in) == 0 {
			in = json.RawMessage("{}")
		}
		fc := &FunctionCallBody{Name: tc.Name(), Args: in}
		if id := tc.OptionalContent()[callIDKey]; id != "" {
			fc.ID = id // echo the provider-issued id for correlation
		}
		parts = append(parts, PartBody{FunctionCall: fc})
		used++
	}
	for _, entry := range template {
		var head struct {
			Refill           string  `json:"refill"`
			Text             *string `json:"text"`
			Thought          bool    `json:"thought"`
			ThoughtSignature string  `json:"thoughtSignature"`
			FunctionCall     *struct {
				Name string `json:"name"`
			} `json:"functionCall"`
		}
		if err := json.Unmarshal(entry, &head); err != nil {
			continue
		}
		switch {
		case head.Refill == "text":
			emitText()
		case head.Refill == "functionCall":
			emitCall()
		case head.Thought:
			parts = append(parts, entry) // thought part — verbatim
		case head.FunctionCall != nil:
			if head.ThoughtSignature != "" {
				parts = append(parts, entry)
				used++ // a verbatim signed call still consumes its slot
			} else {
				emitCall()
			}
		case head.Text != nil:
			if head.ThoughtSignature != "" {
				parts = append(parts, entry)
				textDone = true // signed answer text occupies the slot
			} else {
				emitText()
			}
		default:
			parts = append(parts, entry) // executableCode etc. — verbatim
		}
	}
	if !textDone && (text != "" || (len(parts) == 0 && len(origin) == 0)) {
		// An empty text part is only the fallback when the turn carries
		// nothing else — never filler ahead of functionCall parts.
		emitText()
	}
	for used < len(origin) {
		emitCall()
	}
	if injectedText != "" {
		parts = append(parts, PartBody{Text: injectedText})
	}
	return parts
}

// toolResultContents emits one user turn carrying a functionResponse part
// per origin call plus any output-media parts after them — parallel calls
// legally share the turn by the same-role merge rule.
func toolResultContents(origin []entity.ToolCall, model entity.Model, fileRoot wire.MediaRoot) ContentBody {
	var parts []any
	for _, tc := range origin {
		content := tc.OutputWithUntrustedTagOrInterrupted()
		resp, _ := json.Marshal(map[string]string{"result": content})
		fr := &FunctionRespBody{Name: tc.Name(), Response: resp}
		if id := tc.OptionalContent()[callIDKey]; id != "" {
			fr.ID = id
		}
		parts = append(parts, PartBody{FunctionResponse: fr})
		for _, a := range tc.OutputAttachments() {
			parts = append(parts, attachmentPart(a, model, fileRoot))
		}
	}
	return ContentBody{Role: "user", Parts: parts}
}

// toolDecls converts schemas into the Gemini Schema subset — the wire
// rejects keywords outside it ($schema, $ref, additionalProperties, …)
// with a 400, so declarations are sanitized, never sent raw.
func toolDecls(tools []valueobject.ToolSchema) []FunctionDeclBody {
	var out []FunctionDeclBody
	for _, t := range tools {
		if !t.CallableByAgent() {
			continue // system-only and disabled tools never reach the wire
		}
		out = append(out, FunctionDeclBody{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  sanitizeSchema(t.Parameters()),
		})
	}
	return out
}

// schemaKeep is the Gemini Schema keyword subset — everything else is
// stripped recursively before send.
var schemaKeep = map[string]bool{
	"type": true, "description": true, "properties": true, "required": true,
	"items": true, "enum": true, "nullable": true, "format": true,
	"minItems": true, "maxItems": true, "minLength": true, "maxLength": true,
	"minimum": true, "maximum": true, "minProperties": true, "maxProperties": true,
	"anyOf": true, "propertyOrdering": true, "default": true, "example": true,
	"title": true, "pattern": true,
}

// sanitizeSchema rewrites a JSON Schema into the Gemini subset — keep
// listed keys, recurse into properties/items/anyOf, drop required entries
// that name no property, stringify non-string enum values.
func sanitizeSchema(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var schema any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil // malformed schema — drop rather than send a 400
	}
	out, _ := json.Marshal(sanitizeNode(schema))
	return out
}

func sanitizeNode(node any) any {
	m, ok := node.(map[string]any)
	if !ok {
		return node
	}
	out := map[string]any{}
	for k, v := range m {
		if !schemaKeep[k] {
			continue
		}
		switch k {
		case "properties":
			if props, ok := v.(map[string]any); ok {
				cleaned := map[string]any{}
				for name, sub := range props {
					cleaned[name] = sanitizeNode(sub)
				}
				out[k] = cleaned
			}
		case "items":
			out[k] = sanitizeNode(v)
		case "anyOf":
			if list, ok := v.([]any); ok {
				var cleaned []any
				for _, sub := range list {
					cleaned = append(cleaned, sanitizeNode(sub))
				}
				out[k] = cleaned
			}
		case "enum":
			if list, ok := v.([]any); ok {
				var cleaned []any
				for _, e := range list {
					if s, ok := e.(string); ok {
						cleaned = append(cleaned, s)
					} else if raw, err := json.Marshal(e); err == nil {
						cleaned = append(cleaned, string(raw)) // non-string → its JSON spelling
					}
				}
				out[k] = cleaned
			}
		default:
			out[k] = v
		}
	}
	// required entries must name a declared property — prune strays.
	if req, ok := out["required"].([]any); ok {
		props, _ := out["properties"].(map[string]any)
		var kept []any
		for _, r := range req {
			if s, ok := r.(string); ok {
				if _, exists := props[s]; len(props) == 0 || exists {
					kept = append(kept, s)
				}
			}
		}
		if len(kept) == 0 {
			delete(out, "required")
		} else {
			out["required"] = kept
		}
	}
	return out
}

// attachmentPart maps a stored reference to an inlineData/fileData part,
// degrading in place — a missing file must not wedge the whole request.
// This wire carries every modality inline, so nothing is dropped.
func attachmentPart(a valueobject.Attachment, model entity.Model, fileRoot wire.MediaRoot) any {
	if !model.AcceptsInput(a.Modality()) {
		return PartBody{Text: "(" + a.Modality().String() + " attachment omitted: model does not accept it)"}
	}
	p, err := encodeAttachment(a, fileRoot)
	if err != nil {
		label := "(media attachment unavailable)"
		if name := safeAttachmentName(a.Name()); name != "" {
			label = "(media attachment unavailable: " + name + ")"
		}
		return PartBody{Text: label}
	}
	return p
}

// encodeAttachment resolves a reference — remote URLs ride as fileData
// (the provider fetches them); local and inline payloads go inlineData
// base64.
func encodeAttachment(a valueobject.Attachment, fileRoot wire.MediaRoot) (PartBody, error) {
	uri := a.URI()
	if strings.HasPrefix(uri, "https://") || strings.HasPrefix(uri, "http://") {
		if _, err := wire.ResolveURI(a, fileRoot); err != nil {
			return PartBody{}, err
		}
		return PartBody{FileData: &FileDataBody{MIMEType: a.MediaType(), FileURI: uri}}, nil
	}
	data, _, err := wire.ResolveBase64(a, fileRoot)
	if err != nil {
		return PartBody{}, err
	}
	return PartBody{InlineData: &BlobBody{MIMEType: a.MediaType(), Data: data}}, nil
}

func safeAttachmentName(name string) string {
	name = path.Base(strings.ReplaceAll(name, `\`, "/"))
	if name == "." || name == "/" {
		return ""
	}
	return name
}
