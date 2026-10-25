package repoconversation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
)

// Compact seals the first dropBefore message lines verbatim into the next
// numbered segment, then rewrites active.jsonl as [summary] + retained
// messages + patch lines — patches stay global so late outputs still
// reach sealed calls. retained carries the caller-sanitized optionalContent
// for the surviving tail; every other field stays exactly as stored, so
// tool outputs and patches are never re-derived from the domain copy.
// Patches without a surviving call are ignored on read.
func (r *ConversationRepository) Compact(ctx context.Context, convID string, summary entity.Message, dropBefore int, retained []entity.Message) error {
	if err := validID(convID); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	unlock, err := r.lockStore()
	if err != nil {
		return err
	}
	defer unlock()

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
	if len(retained) != len(msgLines)-dropBefore {
		return fmt.Errorf("compact %s: retained tail has %d messages, active has %d after dropBefore %d",
			convID, len(retained), len(msgLines)-dropBefore, dropBefore)
	}
	tailLines, err := rewriteRetainedLines(msgLines[dropBefore:], retained)
	if err != nil {
		return fmt.Errorf("compact %s: %w", convID, err)
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
	for _, l := range append(tailLines, patchLines...) {
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

// rewriteRetainedLines overlays the caller-sanitized optionalContent onto
// the stored tail. Validation happens before any file write; unchanged
// lines pass through byte-exact so timestamp and unknown-field formatting
// survive.
func rewriteRetainedLines(lines [][]byte, retained []entity.Message) ([][]byte, error) {
	out := make([][]byte, 0, len(lines))
	for i, line := range lines {
		var lr lineRecord
		if err := json.Unmarshal(line, &lr); err != nil {
			return nil, fmt.Errorf("corrupt retained line: %w", err)
		}
		if lr.Type != lineMessage {
			return nil, fmt.Errorf("retained line %d is %q, not a message", i, lr.Type)
		}
		if lr.Data.ID != retained[i].ID() {
			return nil, fmt.Errorf("retained tail mismatch at %d: stored %q, supplied %q",
				i, lr.Data.ID, retained[i].ID())
		}
		if maps.Equal(lr.Data.OptionalContent, retained[i].OptionalContent()) {
			out = append(out, line)
			continue
		}
		lr.Data.OptionalContent = retained[i].OptionalContent()
		rewritten, err := json.Marshal(lr)
		if err != nil {
			return nil, fmt.Errorf("rewrite retained line %d: %w", i, err)
		}
		out = append(out, rewritten)
	}
	return out, nil
}
