package repoconversation

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// metaRecord is meta.json — conversation-level state plus the counters
// Info serves, so inspection never scans message files.
type metaRecord struct {
	ID             string             `json:"id"`
	Title          string             `json:"title,omitempty"`
	Route          string             `json:"route,omitempty"`
	ModelID        string             `json:"modelId,omitempty"`
	Workspace      string             `json:"workspace,omitempty"`
	Environment    string             `json:"environment"`
	Tools          []toolSchemaRecord `json:"tools,omitempty"`
	Usage          usageRecord        `json:"usage"`
	MessageCount   int                `json:"messageCount"`
	ActiveCount    int                `json:"activeCount"`
	SealedSegments int                `json:"sealedSegments"`
	CreatedAt      time.Time          `json:"createdAt"`
	UpdatedAt      time.Time          `json:"updatedAt"`
}

type usageRecord struct {
	InputTokens      int `json:"inputTokens"`
	CacheReadTokens  int `json:"cacheReadTokens"`
	CacheWriteTokens int `json:"cacheWriteTokens"`
	OutputTokens     int `json:"outputTokens"`
}

type toolSchemaRecord struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Enabled     bool            `json:"enabled"`
	Permission  string          `json:"permission,omitempty"`
}

// jsonl line envelope: messages and tool-output patches share one stream
// per file so appends stay O(1) and ordering is literal.
type lineRecord struct {
	Type          string                  `json:"type"` // "message" | "tool_output"
	Data          messageRecord           `json:"data"`
	CallID        string                  `json:"callId,omitempty"`
	Output        string                  `json:"output,omitempty"`
	OutputLatency string                  `json:"outputLatency,omitempty"`
	Outcome       string                  `json:"outcome,omitempty"`
	Exit          *valueobject.ExitStatus `json:"exit,omitempty"`
	Attachments   []attachmentRecord      `json:"attachments,omitempty"`
}

type messageRecord struct {
	ID              string              `json:"id"`
	Role            string              `json:"role"`
	Content         string              `json:"content"`
	UserType        string              `json:"userType,omitempty"`
	InputID         string              `json:"inputId,omitempty"`
	FinishReason    string              `json:"finishReason,omitempty"`
	Parts           []messagePartRecord `json:"parts,omitempty"`
	CreatedAt       recordTime          `json:"createdAt"`
	ToolCalls       []toolCallRecord    `json:"toolCalls,omitempty"`
	Attachments     []attachmentRecord  `json:"attachments,omitempty"`
	OptionalContent map[string]string   `json:"optionalContent,omitempty"`
}

// recordTime decodes a timestamp leniently: a hand-edited or future record
// with a bad date keeps its message. The date is metadata, not identity, so
// dropping the whole line over it would lose readable dialogue.
type recordTime struct{ time.Time }

func (t *recordTime) UnmarshalJSON(b []byte) error {
	var raw string
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Time = time.Time{}
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t.Time = time.Time{}
		return nil
	}
	t.Time = parsed
	return nil
}

func (t recordTime) MarshalJSON() ([]byte, error) { return json.Marshal(t.Time) }

type messagePartRecord struct {
	Kind       string `json:"kind"`
	Text       string `json:"text,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
}

type toolCallRecord struct {
	ID                string                  `json:"id"`
	Name              string                  `json:"name"`
	Input             string                  `json:"input"`
	Output            string                  `json:"output,omitempty"`
	OutputSet         bool                    `json:"outputSet,omitempty"`
	OutputLatency     string                  `json:"outputLatency,omitempty"`
	Outcome           string                  `json:"outcome,omitempty"`
	Exit              *valueobject.ExitStatus `json:"exit,omitempty"`
	OutputBudget      int                     `json:"outputBudget,omitempty"`
	OutputAttachments []attachmentRecord      `json:"outputAttachments,omitempty"`
	Type              string                  `json:"type,omitempty"`
	CallAt            time.Time               `json:"callAt"`
	OptionalContent   map[string]string       `json:"optionalContent,omitempty"`
}

type attachmentRecord struct {
	URI       string `json:"uri"`
	MediaType string `json:"mediaType"`
	Name      string `json:"name,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
}

func encodeMessage(m entity.Message) messageRecord {
	r := messageRecord{
		ID:              m.ID(),
		Role:            m.Role().String(),
		Content:         m.Content(),
		UserType:        m.UserType().String(),
		InputID:         m.InputID(),
		FinishReason:    m.FinishReason().String(),
		CreatedAt:       recordTime{m.CreatedAt()},
		OptionalContent: m.OptionalContent(),
	}
	for _, part := range m.Parts() {
		r.Parts = append(r.Parts, messagePartRecord{
			Kind:       part.Kind,
			Text:       part.Text,
			ToolCallID: part.ToolCallID,
		})
	}
	for _, tc := range m.ToolCalls() {
		rec := toolCallRecord{
			ID:              tc.ID(),
			Name:            tc.Name(),
			Input:           tc.Input(),
			Output:          tc.Output(),
			OutputSet:       tc.HasOutput(),
			OutputLatency:   tc.OutputLatency(),
			Outcome:         tc.Outcome().String(),
			Exit:            tc.Exit(),
			OutputBudget:    tc.OutputBudget(),
			Type:            tc.Type().String(),
			CallAt:          tc.CallAt(),
			OptionalContent: tc.OptionalContent(),
		}
		for _, a := range tc.OutputAttachments() {
			rec.OutputAttachments = append(rec.OutputAttachments, attachmentRecord{
				URI:       a.URI(),
				MediaType: a.MediaType(),
				Name:      a.Name(),
				SHA256:    a.SHA256(),
			})
		}
		r.ToolCalls = append(r.ToolCalls, rec)
	}
	for _, a := range m.Attachments() {
		r.Attachments = append(r.Attachments, attachmentRecord{
			URI:       a.URI(),
			MediaType: a.MediaType(),
			Name:      a.Name(),
			SHA256:    a.SHA256(),
		})
	}
	return r
}

// decodeMessage hydrates an entity.Message via the public constructor +
// setters — no reconstitute seam. Stored enum strings are validated strictly:
// unknown values mean corrupt or forward-incompatible data.
// decodeOptions decides how a corrupt tool call is treated on the way in.
// A read tolerates one bad call — a hand-edited record must not make a whole
// conversation unreadable — while a repair stays strict, because it rewrites
// the file and must not launder a bad record into a placeholder.
type decodeOptions struct {
	tolerate bool
	report   func(id, reason string)
}

// strictDecode is the write-side and repair default.
var strictDecode = decodeOptions{}

// tolerantRead degrades unreadable records and reports each one.
func tolerantRead(report func(id, reason string)) decodeOptions {
	return decodeOptions{tolerate: true, report: report}
}

// note reports one dropped field; a read without a sink stays silent.
func (o decodeOptions) note(id, reason string) {
	if o.report != nil {
		o.report(id, reason)
	}
}

func decodeMessage(r messageRecord, opts decodeOptions) (entity.Message, error) {
	role, ok := enum.ParseRole(r.Role)
	if !ok {
		return entity.Message{}, fmt.Errorf("unknown role %q", r.Role)
	}
	if r.ID == "" {
		return entity.Message{}, fmt.Errorf("message record missing id")
	}
	m, err := entity.NewMessage(role, r.Content)
	if err != nil {
		return entity.Message{}, err
	}
	m.SetID(r.ID)
	if r.UserType != "" {
		ut, ok := enum.ParseLLMUserType(r.UserType)
		if !ok {
			if !opts.tolerate {
				return entity.Message{}, fmt.Errorf("unknown userType %q", r.UserType)
			}
			// The line still says something a reader can use: keep the
			// message, drop the field we cannot name.
			opts.note(r.ID, fmt.Sprintf("field dropped: userType %q", r.UserType))
		} else {
			m.SetUserType(ut)
		}
	}
	if r.InputID != "" {
		m.SetInputID(r.InputID)
	}
	if r.FinishReason != "" {
		fr, ok := enum.ParseFinishReason(r.FinishReason)
		if !ok {
			if !opts.tolerate {
				return entity.Message{}, fmt.Errorf("unknown finishReason %q", r.FinishReason)
			}
			opts.note(r.ID, fmt.Sprintf("field dropped: finishReason %q", r.FinishReason))
		} else {
			m.SetFinishReason(fr)
		}
	}
	m.SetCreatedAt(r.CreatedAt.Time)
	for _, a := range r.Attachments {
		att, err := valueobject.NewAttachment(a.URI, a.MediaType)
		if err != nil {
			if !opts.tolerate {
				return entity.Message{}, fmt.Errorf("attachment: %w", err)
			}
			opts.note(r.ID, fmt.Sprintf("field dropped: attachment %q: %v", a.URI, err))
			continue
		}
		att.SetName(a.Name)
		att.SetSHA256(a.SHA256)
		m.AddAttachment(att)
	}
	parts := make([]entity.MessagePart, 0, len(r.Parts))
	for _, part := range r.Parts {
		parts = append(parts, entity.MessagePart{
			Kind:       part.Kind,
			Text:       part.Text,
			ToolCallID: part.ToolCallID,
		})
	}
	m.SetParts(parts)
	for _, t := range r.ToolCalls {
		tc, err := decodeToolCall(t)
		if err != nil {
			if !opts.tolerate {
				return entity.Message{}, err
			}
			opts.note(t.ID, "tool call unreadable: "+err.Error())
			// Keep what the record still says (id, name, input, output) and
			// drop the verdict: it is unknown, so it projects as unknown.
			tc = entity.NewUnreadableToolCall(t.ID, t.Name, t.Input)
			if t.OutputSet {
				tc.SetStoredOutput(t.Output, t.OutputLatency)
			}
			tc.SetCallAt(t.CallAt)
			if err := m.AddToolCall(tc); err != nil {
				// A duplicate id in a corrupt record is still a readable
				// message; keep the first call.
				opts.note(t.ID, "tool call dropped: "+err.Error())
				continue
			}
			continue
		}
		if err := m.AddToolCall(tc); err != nil {
			return entity.Message{}, err
		}
	}
	for k, v := range r.OptionalContent {
		m.SetOptionalContent(k, v)
	}
	return m, nil
}

func decodeToolCall(r toolCallRecord) (entity.ToolCall, error) {
	var tt enum.Tooltype
	if r.Type != "" {
		parsed, ok := enum.ParseTooltype(r.Type)
		if !ok {
			return entity.ToolCall{}, fmt.Errorf("unknown tooltype %q", r.Type)
		}
		tt = parsed
	}
	tc, err := entity.NewToolCall(r.ID, r.Name, r.Input, tt)
	if err != nil {
		return entity.ToolCall{}, fmt.Errorf("toolCall %q: %w", r.ID, err)
	}
	tc.SetCallAt(r.CallAt)
	if r.OutputBudget > 0 {
		tc.SetOutputBudget(r.OutputBudget)
	}
	if r.OutputSet {
		tc.SetStoredOutput(r.Output, r.OutputLatency)
	}
	// A record written before the field existed decodes to the zero
	// outcome — "no verdict recorded" — and keeps it: no heuristic here.
	outcome, ok := enum.ParseToolOutcome(r.Outcome)
	if !ok {
		return entity.ToolCall{}, fmt.Errorf("toolCall %q: unknown outcome %q", r.ID, r.Outcome)
	}
	tc.SetOutcome(outcome, r.Exit)
	var atts []valueobject.Attachment
	for _, a := range r.OutputAttachments {
		att, err := valueobject.NewAttachment(a.URI, a.MediaType)
		if err != nil {
			return entity.ToolCall{}, fmt.Errorf("toolCall %q attachment: %w", r.ID, err)
		}
		att.SetName(a.Name)
		att.SetSHA256(a.SHA256)
		atts = append(atts, att)
	}
	tc.SetOutputAttachments(atts)
	for k, v := range r.OptionalContent {
		tc.SetOptionalContent(k, v)
	}
	return tc, nil
}

func decodeUsage(r usageRecord) (valueobject.Usage, error) {
	return valueobject.NewUsage(r.InputTokens, r.CacheReadTokens, r.CacheWriteTokens, r.OutputTokens)
}

// mustUsage is decodeUsage for records we wrote ourselves — counters were
// validated at write time.
func mustUsage(r usageRecord) valueobject.Usage {
	u, _ := decodeUsage(r)
	return u
}

func encodeUsage(u valueobject.Usage) usageRecord {
	return usageRecord{
		InputTokens:      u.InputTokens(),
		CacheReadTokens:  u.CacheReadTokens(),
		CacheWriteTokens: u.CacheWriteTokens(),
		OutputTokens:     u.OutputTokens(),
	}
}

func encodeToolSchema(s valueobject.ToolSchema) toolSchemaRecord {
	return toolSchemaRecord{
		Name:        s.Name(),
		Description: s.Description(),
		Parameters:  s.Parameters(),
		Enabled:     s.Enabled(),
		Permission:  s.Permission().String(),
	}
}

func decodeToolSchema(r toolSchemaRecord) (valueobject.ToolSchema, error) {
	var perm enum.ToolPermission
	if r.Permission != "" {
		parsed, ok := enum.ParseToolPermission(r.Permission)
		if !ok {
			return valueobject.ToolSchema{}, fmt.Errorf("unknown permission %q", r.Permission)
		}
		perm = parsed
	}
	return valueobject.NewToolSchema(r.Name, r.Description, r.Parameters, r.Enabled, perm)
}
