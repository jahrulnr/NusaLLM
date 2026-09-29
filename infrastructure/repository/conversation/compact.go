package repoconversation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
)

// Compact seals the first dropBefore message lines verbatim into the next
// numbered segment, then rewrites active.jsonl as [summary] + surviving
// messages + patch lines — patches stay global so late outputs still
// reach sealed calls. Patches without a surviving call are ignored on read.
func (r *ConversationRepository) Compact(ctx context.Context, convID string, summary entity.Message, dropBefore int) error {
	if err := validID(convID); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	meta, err := r.readMeta(convID)
	if err != nil {
		return err
	}
	active := filepath.Join(r.convDir(convID), activeFile)
	lines, err := readLines(active)
	if err != nil {
		return err
	}

	var msgLines, patchLines [][]byte
	for _, l := range lines {
		var lr lineRecord
		if err := json.Unmarshal(l, &lr); err != nil {
			return fmt.Errorf("corrupt line: %w", err)
		}
		if lr.Type == lineMessage {
			msgLines = append(msgLines, l)
		} else {
			patchLines = append(patchLines, l)
		}
	}
	if dropBefore <= 0 || dropBefore > len(msgLines) {
		// Nothing to fold, or a boundary beyond the stream — an empty or
		// oversized segment would corrupt the archive layout.
		return fmt.Errorf("compact %s: invalid dropBefore %d over %d message lines", convID, dropBefore, len(msgLines))
	}

	// Seal the dropped prefix verbatim into the next numbered segment.
	sealed := filepath.Join(r.convDir(convID), fmt.Sprintf("%d.jsonl", meta.SealedSegments))
	var buf bytes.Buffer
	for _, l := range msgLines[:dropBefore] {
		buf.Write(l)
		buf.WriteByte('\n')
	}
	if err := writeFileAtomic(sealed, buf.Bytes(), 0o644); err != nil {
		return err
	}
	// The sealed file is committed only once meta counts it. A crash before
	// that leaves an orphan that normal reads reject until offline repair.

	// Rewrite active: summary + surviving messages + all patch lines.
	// The reader matches patches to retained calls across every segment.
	buf.Reset()
	sum, err := json.Marshal(lineRecord{Type: lineMessage, Data: encodeMessage(summary)})
	if err != nil {
		return err
	}
	buf.Write(sum)
	buf.WriteByte('\n')
	for _, l := range append(msgLines[dropBefore:], patchLines...) {
		buf.Write(l)
		buf.WriteByte('\n')
	}
	if err := writeFileAtomic(active, buf.Bytes(), 0o644); err != nil {
		// Active never changed — it still holds the prefix, so the
		// sealed copy is a pure duplicate: safe to remove.
		_ = os.Remove(sealed)
		return err
	}

	meta.SealedSegments++
	meta.MessageCount++
	meta.ActiveCount = meta.ActiveCount - dropBefore + 1
	if err := r.writeMeta(convID, meta); err != nil {
		// Do NOT remove the sealed file here — the active rewrite
		// already dropped the prefix, so the uncounted segment is now
		// its only copy. Readers reject the mismatched archive until repair
		// commits the segment and recalculates the counters.
		return err
	}
	return nil
}
