package repoconversation

import (
	"context"
	"os"
	"time"

	"github.com/jahrulnr/llm-base/infrastructure/domain/aggregate"
)

// Save upserts meta.json — creates the conversation directory on first
// write and preserves record-level fields (counters, CreatedAt) on update.
func (r *ConversationRepository) Save(ctx context.Context, conv *aggregate.Conversation) error {
	if err := validID(conv.ID()); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	dir := r.convDir(conv.ID())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	m := metaRecord{
		ID:      conv.ID(),
		Title:   conv.Title(),
		Route:   conv.Route(),
		ModelID: conv.ModelID(),
		Usage:   encodeUsage(conv.Usage()),
		// SystemPrompt is deliberately absent: the prompt ships embedded
		// in the binary, so it is applied in memory, never persisted.
	}
	for _, t := range conv.Tools() {
		m.Tools = append(m.Tools, encodeToolSchema(t))
	}
	// Preserve record-level fields on update.
	if prev, err := r.readMeta(conv.ID()); err == nil {
		m.CreatedAt = prev.CreatedAt
		m.MessageCount = prev.MessageCount
		m.ActiveCount = prev.ActiveCount
		m.SealedSegments = prev.SealedSegments
	} else {
		m.CreatedAt = time.Now()
	}
	return r.writeMeta(conv.ID(), m)
}
