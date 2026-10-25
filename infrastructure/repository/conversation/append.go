package repoconversation

import (
	"context"
	"path/filepath"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
)

// AppendMessage appends one message line to active.jsonl without rewriting
// history and bumps the meta counters Info serves. The bool is
// true once the line itself has been appended and synced, even when writing
// the later metadata projection fails.
func (r *ConversationRepository) AppendMessage(ctx context.Context, convID string, msg entity.Message) (bool, error) {
	if err := validID(convID); err != nil {
		return false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	unlock, err := r.lockStore()
	if err != nil {
		return false, err
	}
	defer unlock()

	meta, err := r.readMeta(convID)
	if err != nil {
		return false, err
	}
	line := lineRecord{Type: lineMessage, Data: encodeMessage(msg)}
	if err := appendLine(filepath.Join(r.convDir(convID), activeFile), line); err != nil {
		return false, err
	}
	meta.MessageCount++
	meta.ActiveCount++
	if err := r.writeMeta(convID, meta); err != nil {
		return true, err
	}
	return true, nil
}
