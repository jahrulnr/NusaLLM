package repoconversation

import (
	"context"
	"path/filepath"
)

// UpdateToolCallOutput appends a patch record to the line stream — the
// enveloped output lands verbatim and resolves by callID at read time.
func (r *ConversationRepository) UpdateToolCallOutput(ctx context.Context, convID, callID, output string) error {
	if err := validID(convID); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, err := r.readMeta(convID); err != nil {
		return err
	}
	// Append-only patch — no read-modify-write, so parallel tool results
	// racing to persist stay safe.
	return appendLine(filepath.Join(r.convDir(convID), activeFile),
		lineRecord{Type: lineToolOutput, CallID: callID, Output: output})
}
