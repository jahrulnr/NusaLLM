package conversation

import (
	"context"
	"time"

	"github.com/jahrulnr/nusallm/infrastructure/delivery/http/internal/transport"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// The DTOs below are the contract's wire projections — every field is
// explicitly chosen; no domain entity is marshaled. Message parts preserve
// the assistant's text, reasoning, and tool-call order for the owner UI.
// Provider replay artifacts, attachment sha256 pins, and error causes stay
// internal. Tool input/output and attachment uri/name cross as inert text.

type conversationDTO struct {
	ID             string                 `json:"id"`
	Title          string                 `json:"title,omitempty"`
	Route          string                 `json:"route"`
	ModelID        string                 `json:"modelId"`
	Workspace      string                 `json:"workspace,omitempty"`
	Environment    enum.Environment       `json:"environment"`
	Usage          transport.UsagePayload `json:"usage"`
	CreatedAt      time.Time              `json:"createdAt"`
	UpdatedAt      time.Time              `json:"updatedAt"`
	MessageCount   int                    `json:"messageCount"`
	ActiveCount    int                    `json:"activeCount"`
	SealedSegments int                    `json:"sealedSegments"`
	CanCompact     *bool                  `json:"canCompact,omitempty"`
	// ActiveTurnID is the hub's in-progress transport turn for this room.
	// Detail and list items set it while a non-terminal turn exists;
	// writes leave it empty so it stays off the wire. It is not
	// persisted conversation state.
	ActiveTurnID   string `json:"activeTurnId,omitempty"`
	ActiveTurnKind string `json:"activeTurnKind,omitempty"`
}

func toConversation(i drepo.ConversationInfo) conversationDTO {
	return conversationDTO{
		ID:             i.ID,
		Title:          i.Title,
		Route:          i.Route,
		ModelID:        i.ModelID,
		Workspace:      i.Workspace,
		Environment:    i.Environment,
		Usage:          transport.MarshalUsage(i.Usage),
		CreatedAt:      i.CreatedAt,
		UpdatedAt:      i.UpdatedAt,
		MessageCount:   i.MessageCount,
		ActiveCount:    i.ActiveCount,
		SealedSegments: i.SealedSegments,
	}
}

// toolCallDTO is the owner-visible call record — id, name, provenance
// (model-originated vs application-injected), lifecycle status, the exact
// input string the model produced, when the call was
// recorded, the plain result text when one exists, and any
// media the tool produced. Tool I/O intentionally crosses to the local
// owner UI; optionalContent and key material never do. Output and
// calledAt are pointers so absent facts omit the key rather than
// inventing a result or the zero time.
type toolCallDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"` // "origin" | "injection"
	// Status is the call's verdict plus the one state that is not a
	// verdict: "pending" (no output yet), "ok" | "failed" | "canceled"
	// (recorded), or "unknown" (a record from before the verdict existed).
	// It never comes from reading Output.
	Status      string                  `json:"status"`
	Input       string                  `json:"input"`
	CalledAt    *time.Time              `json:"calledAt,omitempty"`
	Output      *string                 `json:"output,omitempty"`
	Exit        *valueobject.ExitStatus `json:"exit,omitempty"`
	Attachments []attachmentDTO         `json:"attachments,omitempty"`
}

func toToolCall(t entity.ToolCall) toolCallDTO {
	kind := t.Type().String()
	if kind == "" {
		// An unreadable record has no provenance; saying "unknown" is
		// honest, and it also keeps the call out of the pending bucket.
		kind = "unknown"
	}
	d := toolCallDTO{
		ID:     t.ID(),
		Name:   t.Name(),
		Type:   kind,
		Status: "unknown",
		Input:  t.Input(),
	}
	if outcome := t.Outcome(); outcome != "" {
		d.Status = outcome.String()
	}
	d.Exit = t.Exit()
	if at := t.CallAt(); !at.IsZero() {
		d.CalledAt = &at
	}
	if t.HasOutput() {
		plain := t.Output()
		d.Output = &plain
	} else if t.Type() != enum.TooltypeInjection && t.Type().String() != "" {
		// Only a real execution can be pending; an injection is never run,
		// so it has no verdict to wait for.
		d.Status = "pending"
	}
	for _, a := range t.OutputAttachments() {
		d.Attachments = append(d.Attachments, toAttachment(a))
	}
	return d
}

// attachmentDTO projects the stored reference — uri (a `file://` local
// path or remote URL is owner-visible by design on this loopback UI)
// and the optional display name (stored names can carry path-shaped
// data; the client renders them as inert text). The sha256 content pin
// stays internal — it is an adapter integrity check, not content.
type attachmentDTO struct {
	MediaType string `json:"mediaType"`
	Modality  string `json:"modality"`
	URI       string `json:"uri"`
	Name      string `json:"name,omitempty"`
}

func toAttachment(a valueobject.Attachment) attachmentDTO {
	return attachmentDTO{
		MediaType: a.MediaType(),
		Modality:  a.Modality().String(),
		URI:       a.URI(),
		Name:      a.Name(),
	}
}

type messageDTO struct {
	ID          string           `json:"id"`
	Role        string           `json:"role"`
	Content     string           `json:"content"`
	InputID     string           `json:"inputId,omitempty"`
	Parts       []messagePartDTO `json:"parts,omitempty"`
	UserType    string           `json:"userType,omitempty"`
	Finish      string           `json:"finishReason,omitempty"`
	ToolCalls   []toolCallDTO    `json:"toolCalls,omitempty"`
	Attachments []attachmentDTO  `json:"attachments,omitempty"`
	CreatedAt   time.Time        `json:"createdAt"`
}

type messagePartDTO struct {
	Kind       string `json:"kind"`
	Text       string `json:"text,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
}

func toMessage(m entity.Message) messageDTO {
	out := messageDTO{
		ID:        m.ID(),
		Role:      m.Role().String(),
		Content:   m.Content(),
		InputID:   m.InputID(),
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
	for _, part := range m.Parts() {
		out.Parts = append(out.Parts, messagePartDTO{
			Kind:       part.Kind,
			Text:       part.Text,
			ToolCallID: part.ToolCallID,
		})
	}
	for _, a := range m.Attachments() {
		out.Attachments = append(out.Attachments, toAttachment(a))
	}
	return out
}

// project answers the room's accepted projection: the stored info, the
// compaction verdict, and the hub's live turn. Every response that returns a
// room — detail, list item, and each write's reply — uses it, so a client can
// treat a write response as the room's new state instead of merging a partial
// one into what it had.
func (s *server) project(ctx context.Context, info drepo.ConversationInfo) (conversationDTO, error) {
	available, err := s.reads.CanCompact(ctx, info.ID)
	if err != nil {
		return conversationDTO{}, err
	}
	out := toConversation(info)
	out.CanCompact = &available
	if s.live != nil {
		if turnID, kind, ok := s.live.ActiveTurn(info.ID); ok {
			out.ActiveTurnID = turnID
			out.ActiveTurnKind = kind
		}
	}
	return out, nil
}
