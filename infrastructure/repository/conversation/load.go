package repoconversation

import (
	"context"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// Load rebuilds meta + the active segment only — the live context the
// model sees. Sealed archives are reached via LoadAt.
func (r *ConversationRepository) Load(ctx context.Context, convID string) (*aggregate.Conversation, error) {
	if err := validID(convID); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	meta, err := r.readMeta(convID)
	if err != nil {
		return nil, err
	}
	c, err := aggregate.NewConversation(meta.ID)
	if err != nil {
		return nil, err
	}
	c.SetTitle(meta.Title)
	c.SetRoute(meta.Route)
	c.SetModelID(meta.ModelID)
	// systemPrompt is not restored: it is code (embedded templates), and
	// older metas carrying a stamped copy are ignored so every room gets
	// the current template.
	u, err := decodeUsage(meta.Usage)
	if err != nil {
		return nil, err
	}
	c.AddUsage(u)
	var schemas []valueobject.ToolSchema
	for _, t := range meta.Tools {
		s, err := decodeToolSchema(t)
		if err != nil {
			return nil, err
		}
		schemas = append(schemas, s)
	}
	c.SetTools(schemas)

	msgs, err := r.loadMessages(convID, false)
	if err != nil {
		return nil, err
	}
	for _, m := range msgs {
		c.Append(m)
	}
	return c, nil
}

// LoadAt slices the global logical history — sealed segments first, then
// active — with clamped indexes and negative-from-end semantics.
func (r *ConversationRepository) LoadAt(ctx context.Context, convID string, start, end int) ([]entity.Message, error) {
	if err := validID(convID); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, err := r.readMeta(convID); err != nil {
		return nil, err
	}
	msgs, err := r.loadMessages(convID, true)
	if err != nil {
		return nil, err
	}
	n := len(msgs)
	if start < 0 {
		start += n
	}
	if end <= 0 {
		end = n
	}
	if start < 0 {
		start = 0
	}
	if end > n {
		end = n
	}
	if start >= end {
		return nil, nil
	}
	return msgs[start:end], nil
}
