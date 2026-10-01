package repoconversation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
)

// RepairOrphan recovers the one extra segment left by an interrupted Compact.
// The operator must stop other processes using the store before calling it.
// A duplicate prefix can only be discarded; a unique prefix can only be
// committed after validating the full history and recalculating counters.
func (r *ConversationRepository) RepairOrphan(ctx context.Context, convID string, action drepo.RepairAction) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validID(convID); err != nil {
		return err
	}
	if action != drepo.RepairDiscardDuplicate && action != drepo.RepairCommit {
		return fmt.Errorf("repair %s: choose discard-duplicate or commit", convID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	meta, err := r.readMetaUnvalidated(convID)
	if err != nil {
		return err
	}
	dir := r.convDir(convID)
	segs, err := sealedSegments(dir)
	if err != nil {
		return err
	}
	if meta.SealedSegments < 0 || len(segs) != meta.SealedSegments+1 {
		return fmt.Errorf("repair %s: expected exactly one extra sealed segment, found %d with metadata count %d", convID, len(segs), meta.SealedSegments)
	}
	for i, path := range segs {
		if filepath.Base(path) != fmt.Sprintf("%d.jsonl", i) {
			return fmt.Errorf("repair %s: sealed segment %d missing; restore it from backup", convID, i)
		}
	}
	orphan := segs[meta.SealedSegments]
	orphanRaw, err := os.ReadFile(orphan)
	if err != nil {
		return err
	}
	if len(orphanRaw) == 0 || orphanRaw[len(orphanRaw)-1] != '\n' {
		return fmt.Errorf("repair %s: orphan segment is empty or incomplete", convID)
	}
	orphanLines, err := readLines(orphan)
	if err != nil {
		return err
	}
	if len(orphanLines) == 0 {
		return fmt.Errorf("repair %s: orphan segment has no valid message lines", convID)
	}
	if !bytes.Equal(joinLines(orphanLines), orphanRaw) {
		return fmt.Errorf("repair %s: orphan segment has incomplete lines", convID)
	}
	for _, line := range orphanLines {
		var record lineRecord
		if err := json.Unmarshal(line, &record); err != nil || record.Type != lineMessage {
			return fmt.Errorf("repair %s: orphan segment contains a non-message line", convID)
		}
	}
	// Compact copies a prefix of active message lines verbatim. If those
	// lines still lead active, the orphan is a duplicate from window W1.
	activePath := filepath.Join(dir, activeFile)
	if _, err := os.Stat(activePath); err != nil {
		return fmt.Errorf("repair %s: active history unavailable: %w", convID, err)
	}
	activeLines, err := readLines(activePath)
	if err != nil {
		return err
	}
	if len(activeLines) == 0 {
		return fmt.Errorf("repair %s: active history is empty", convID)
	}
	matched := 0
	for _, line := range activeLines {
		var record lineRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return fmt.Errorf("repair %s: corrupt active line: %w", convID, err)
		}
		if record.Type != lineMessage {
			continue // Compact moves interleaved patch lines to the new tail
		}
		if !bytes.Equal(line, orphanLines[matched]) {
			break
		}
		matched++
		if matched == len(orphanLines) {
			break
		}
	}
	duplicate := matched == len(orphanLines)
	if duplicate {
		if action != drepo.RepairDiscardDuplicate {
			return fmt.Errorf("repair %s: orphan duplicates active prefix; use discard-duplicate", convID)
		}
		if err := os.Remove(orphan); err != nil {
			return fmt.Errorf("repair %s: remove duplicate segment: %w", convID, err)
		}
		if d, err := os.Open(dir); err == nil {
			_ = d.Sync()
			_ = d.Close()
		}
		return nil
	}
	// Before suggesting commit, check that the orphan does not overlap any
	// history already known to metadata. A partial overlap cannot be repaired
	// by either action without choosing which copy of a message to keep.
	existing, err := r.loadMessages(convID, meta.SealedSegments)
	if err != nil {
		return fmt.Errorf("repair %s: read existing history: %w", convID, err)
	}
	seen := make(map[string]struct{}, len(existing)+len(orphanLines))
	for _, message := range existing {
		seen[message.ID()] = struct{}{}
	}
	for _, line := range orphanLines {
		var record lineRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return fmt.Errorf("repair %s: decode orphan message: %w", convID, err)
		}
		message, err := decodeMessage(record.Data)
		if err != nil {
			return fmt.Errorf("repair %s: decode orphan message: %w", convID, err)
		}
		if _, exists := seen[message.ID()]; exists {
			return fmt.Errorf("repair %s: orphan message %s already exists in history; partial overlap requires manual reconciliation — stop the app, back up the room, then inspect its files or restore from backup", convID, message.ID())
		}
		seen[message.ID()] = struct{}{}
	}
	if action != drepo.RepairCommit {
		return fmt.Errorf("repair %s: orphan is not an active prefix; inspect it, then use commit if it contains the missing history", convID)
	}
	// Validate both streams before publishing the segment. No archive byte is
	// rewritten; only metadata's derived counters and segment count change.
	meta.SealedSegments++
	active, err := r.loadMessages(convID, 0)
	if err != nil {
		return fmt.Errorf("repair %s: read active history: %w", convID, err)
	}
	all, err := r.loadMessages(convID, meta.SealedSegments)
	if err != nil {
		return fmt.Errorf("repair %s: read repaired history: %w", convID, err)
	}
	meta.ActiveCount = len(active)
	meta.MessageCount = len(all)
	if err := r.writeMeta(convID, meta); err != nil {
		return fmt.Errorf("repair %s: write metadata: %w", convID, err)
	}
	return nil
}
