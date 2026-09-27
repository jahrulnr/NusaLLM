package repoconversation

import (
	"context"
	"path/filepath"

	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// UpdateToolCallOutput appends a patch record to the line stream — the
// enveloped output and any media references land verbatim and resolve by
// callID at read time.
func (r *ConversationRepository) UpdateToolCallOutput(ctx context.Context, convID, callID, output string, atts []valueobject.Attachment) error {
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
	line := lineRecord{Type: lineToolOutput, CallID: callID, Output: output}
	for _, a := range atts {
		line.Attachments = append(line.Attachments, attachmentRecord{
			URI:       a.URI(),
			MediaType: a.MediaType(),
			Name:      a.Name(),
			SHA256:    a.SHA256(),
		})
	}
	return appendLine(filepath.Join(r.convDir(convID), activeFile), line)
}
