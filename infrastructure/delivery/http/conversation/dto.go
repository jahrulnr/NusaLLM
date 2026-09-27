package conversation

import (
	"time"

	"github.com/jahrulnr/nusallm/infrastructure/delivery/http/internal/transport"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// The DTOs below are the contract's wire projections — every field is
// explicitly chosen; no domain entity is marshaled. content fields pass
// through raw (they are untrusted text the client renders escaped); the
// guarantee covers server-held fields: no tool input/output, no
// attachment URI or name, no optionalContent, no reasoning content.

type conversationDTO struct {
	ID             string                 `json:"id"`
	Title          string                 `json:"title,omitempty"`
	Route          string                 `json:"route"`
	ModelID        string                 `json:"modelId"`
	Usage          transport.UsagePayload `json:"usage"`
	CreatedAt      time.Time              `json:"createdAt"`
	UpdatedAt      time.Time              `json:"updatedAt"`
	MessageCount   int                    `json:"messageCount"`
	ActiveCount    int                    `json:"activeCount"`
	SealedSegments int                    `json:"sealedSegments"`
}

func toConversation(i drepo.ConversationInfo) conversationDTO {
	return conversationDTO{
		ID:             i.ID,
		Title:          i.Title,
		Route:          i.Route,
		ModelID:        i.ModelID,
		Usage:          transport.MarshalUsage(i.Usage),
		CreatedAt:      i.CreatedAt,
		UpdatedAt:      i.UpdatedAt,
		MessageCount:   i.MessageCount,
		ActiveCount:    i.ActiveCount,
		SealedSegments: i.SealedSegments,
	}
}

// toolCallDTO is the sanitized call reference — id, name, and whether
// output has been recorded. Inputs, outputs, and optionalContent never
// serialize.
type toolCallDTO struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"` // "pending" | "completed"
}

func toToolCall(t entity.ToolCall) toolCallDTO {
	status := "completed"
	if t.Output() == "" {
		status = "pending"
	}
	return toolCallDTO{ID: t.ID(), Name: t.Name(), Status: status}
}

// attachmentDTO carries metadata only — never the URI (a local path or
// data blob), never the name (stored names can carry path-shaped data),
// never the sha256 pin.
type attachmentDTO struct {
	MediaType string `json:"mediaType"`
	Modality  string `json:"modality"`
}

func toAttachment(a valueobject.Attachment) attachmentDTO {
	return attachmentDTO{MediaType: a.MediaType(), Modality: a.Modality().String()}
}

type messageDTO struct {
	ID          string          `json:"id"`
	Role        string          `json:"role"`
	Content     string          `json:"content"`
	UserType    string          `json:"userType,omitempty"`
	Finish      string          `json:"finishReason,omitempty"`
	ToolCalls   []toolCallDTO   `json:"toolCalls,omitempty"`
	Attachments []attachmentDTO `json:"attachments,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
}

func toMessage(m entity.Message) messageDTO {
	out := messageDTO{
		ID:        m.ID(),
		Role:      m.Role().String(),
		Content:   m.Content(),
		Finish:    m.FinishReason().String(),
		CreatedAt: m.CreatedAt(),
	}
	// userType is only meaningful on user-role messages — it marks real
	// input ("origin") vs synthetic artifacts like compaction checkpoints.
	if m.Role() == enum.RoleUser {
		out.UserType = m.UserType().String()
	}
	for _, t := range m.ToolCalls() {
		out.ToolCalls = append(out.ToolCalls, toToolCall(t))
	}
	for _, a := range m.Attachments() {
		out.Attachments = append(out.Attachments, toAttachment(a))
	}
	return out
}
