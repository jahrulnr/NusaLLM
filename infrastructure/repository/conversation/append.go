package repoconversation

import (
	"context"
	"path/filepath"

	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
)

// AppendMessage appends one message line to active.jsonl — O(1), never
// rewrites history — and bumps the meta counters Info serves.
func (r *ConversationRepository) AppendMessage(ctx context.Context, convID string, msg entity.Message) error {
	if err := validID(convID); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	meta, err := r.readMeta(convID)
	if err != nil {
		return err
	}
	line := lineRecord{Type: lineMessage, Data: encodeMessage(msg)}
	if err := appendLine(filepath.Join(r.convDir(convID), activeFile), line); err != nil {
		return err
	}
	meta.MessageCount++
	meta.ActiveCount++
	return r.writeMeta(convID, meta)
}
