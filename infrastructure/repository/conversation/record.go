package repoconversation

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	"github.com/jahrulnr/llm-base/infrastructure/domain/valueobject"
)

// metaRecord is meta.json — conversation-level state plus the counters
// Info serves, so inspection never scans message files.
type metaRecord struct {
	ID             string             `json:"id"`
	Title          string             `json:"title,omitempty"`
	Route          string             `json:"route,omitempty"`
	ModelID        string             `json:"modelId,omitempty"`
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
	Type        string             `json:"type"` // "message" | "tool_output"
	Data        messageRecord      `json:"data,omitempty"`
	CallID      string             `json:"callId,omitempty"`
	Output      string             `json:"output,omitempty"`
	Attachments []attachmentRecord `json:"attachments,omitempty"`
}

type messageRecord struct {
	ID              string             `json:"id"`
	Role            string             `json:"role"`
	Content         string             `json:"content"`
	UserType        string             `json:"userType,omitempty"`
	FinishReason    string             `json:"finishReason,omitempty"`
	CreatedAt       time.Time          `json:"createdAt"`
	ToolCalls       []toolCallRecord   `json:"toolCalls,omitempty"`
	Attachments     []attachmentRecord `json:"attachments,omitempty"`
	OptionalContent map[string]string  `json:"optionalContent,omitempty"`
}

type toolCallRecord struct {
	ID                string             `json:"id"`
	Name              string             `json:"name"`
	Input             string             `json:"input"`
	Output            string             `json:"output,omitempty"`
	OutputBudget      int                `json:"outputBudget,omitempty"`
	OutputAttachments []attachmentRecord `json:"outputAttachments,omitempty"`
	Type              string             `json:"type,omitempty"`
	CallAt            time.Time          `json:"callAt,omitempty"`
	OptionalContent   map[string]string  `json:"optionalContent,omitempty"`
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
		FinishReason:    m.FinishReason().String(),
		CreatedAt:       m.CreatedAt(),
		OptionalContent: m.OptionalContent(),
	}
	for _, tc := range m.ToolCalls() {
		rec := toolCallRecord{
			ID:              tc.ID(),
			Name:            tc.Name(),
			Input:           tc.Input(),
			Output:          tc.Output(),
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
func decodeMessage(r messageRecord) (entity.Message, error) {
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
			return entity.Message{}, fmt.Errorf("unknown userType %q", r.UserType)
		}
		m.SetUserType(ut)
	}
	if r.FinishReason != "" {
		fr, ok := enum.ParseFinishReason(r.FinishReason)
		if !ok {
			return entity.Message{}, fmt.Errorf("unknown finishReason %q", r.FinishReason)
		}
		m.SetFinishReason(fr)
	}
	m.SetCreatedAt(r.CreatedAt)
	for _, a := range r.Attachments {
		att, err := valueobject.NewAttachment(a.URI, a.MediaType)
		if err != nil {
			return entity.Message{}, fmt.Errorf("attachment: %w", err)
		}
		att.SetName(a.Name)
		att.SetSHA256(a.SHA256)
		m.AddAttachment(att)
	}
	for _, t := range r.ToolCalls {
		tc, err := decodeToolCall(t)
		if err != nil {
			return entity.Message{}, err
		}
		m.AddToolCall(tc)
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
	if r.Output != "" {
		tc.SetStoredOutput(r.Output)
	}
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
