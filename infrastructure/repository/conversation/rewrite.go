package repoconversation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

// RewriteFrom performs segment surgery in the same class as Compact —
// the stream is rewritten atomically rather than patched with a replay
// record. Two shapes:
//
//   - target in active.jsonl: the file is truncated right after the
//     edited line, whose content field is patched in place.
//   - target in sealed segment N: segments 0..N-1 and N's surviving prefix
//     are merged into the new active.jsonl (the model regains the full
//     pre-edit era verbatim — compaction summaries covering later eras
//     would lie, so they are dropped with the old tail), every numbered
//     segment is parked then removed, and the count resets so the next
//     Compact starts at 0.jsonl again.
//
// Late tool-output patches can never resurrect a dropped suffix: they are
// appended records, and the calls they targeted are simply gone.
func (r *ConversationRepository) RewriteFrom(ctx context.Context, convID, msgID, content string) error {
	if err := validID(convID); err != nil {
		return err
	}
	if err := valueobject.ValidID(msgID); err != nil {
		return apperror.New(400, "invalid id", fmt.Errorf("id %q: %w", msgID, err))
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, err := r.readMeta(convID); err != nil {
		return err
	}
	dir := r.convDir(convID)
	active := filepath.Join(dir, activeFile)
	activeLines, err := readLines(active)
	if err != nil {
		return err
	}

	// The common case: the target still lives in the tail.
	if cut, found, err := rewriteCut(activeLines, msgID, content); err != nil {
		return err
	} else if found {
		if err := writeFileAtomic(active, joinLines(cut), 0o644); err != nil {
			return err
		}
		return r.recountMeta(convID)
	}

	// Sealed segments — repromote the era holding the target. The archive
	// just validated at readMeta, so rewrite leftovers here are debris —
	// a payload whose edit never committed, or post-commit cleanup a crash
	// cut short. Sweep them first, the payload's staged temp sibling
	// included: a stale payload must never be published.
	for _, leftover := range []string{rewritePayload, rewritePayload + ".tmp"} {
		_ = os.Remove(filepath.Join(dir, leftover))
	}
	if stale, err := filepath.Glob(filepath.Join(dir, "*.jsonl"+rewriteParkSuffix)); err == nil {
		for _, p := range stale {
			_ = os.Remove(p)
		}
	}
	segs, err := sealedSegments(dir)
	if err != nil {
		return err
	}
	for i, seg := range segs {
		segLines, err := readLines(seg)
		if err != nil {
			return err
		}
		cut, found, err := rewriteCut(segLines, msgID, content)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		var merged [][]byte
		for _, prev := range segs[:i] { // earlier eras come back verbatim
			lines, err := readLines(prev)
			if err != nil {
				return err
			}
			merged = append(merged, lines...)
		}
		merged = append(merged, cut...)
		var tailPatches [][]byte
		for _, l := range activeLines {
			var lr lineRecord
			if json.Unmarshal(l, &lr) == nil && lr.Type != lineMessage {
				tailPatches = append(tailPatches, l)
			}
		}
		merged = append(merged, tailPatches...)

		// The merged stream and the retired eras live in different files,
		// so no single rename can commit this edit. The protocol: stage
		// the merged bytes durable under a non-archive name, park every
		// numbered segment under a parked name — renamed, never deleted —
		// then publish the same bytes over active.jsonl; that one rename
		// is the commit point. The invariants this buys:
		//
		//   - payload present + an edit-shaped archive mismatch = an
		//     interrupted edit, finished by `conversation repair <id>
		//     --finish-rewrite`. Edit-shaped is exact: the edit removes
		//     numbered files only and parks them in ascending order
		//     without a gap, so the archive shows fewer segments than
		//     committed, a parked set that is the contiguous prefix
		//     0.jsonl.rewrite-old … j.jsonl.rewrite-old, and every
		//     surviving numbered segment an index above that prefix — or
		//     the payload already equal to the live tail (the publish
		//     landed, so republishing is a no-op whatever the leftovers);
		//   - payload present + consistent archive = post-commit debris,
		//     invisible to readers, swept by the next sealed edit, and
		//     never republished;
		//   - the payload is removed only once the archive validates, so
		//     "inconsistent and no payload" is a state this code never
		//     produces. A debris payload beside a foreign mismatch (a
		//     Compact orphan, manual damage, a gapped parked set) is not
		//     this window and is never republished either.
		stream := joinLines(merged)
		payload := filepath.Join(dir, rewritePayload)
		if err := writeFileAtomic(payload, stream, 0o644); err != nil {
			return err // nothing canonical moved yet
		}
		parked := make([]string, 0, len(segs))
		rollback := func() {
			restored := true
			for _, p := range parked {
				if err := os.Rename(p+rewriteParkSuffix, p); err != nil {
					restored = false
				}
			}
			// Drop the payload only when the archive was fully restored —
			// while any segment stays parked the payload is the only
			// roll-forward source for the era it holds.
			if restored {
				_ = os.Remove(payload)
			}
		}
		var parkErr error
		for _, seg := range segs {
			if parkErr = os.Rename(seg, seg+rewriteParkSuffix); parkErr != nil {
				break
			}
			parked = append(parked, seg)
		}
		if parkErr == nil {
			// The parked names must be durable before the publish: after
			// it they are the only copies of their eras until the payload
			// wins.
			if d, err := os.Open(dir); err == nil {
				parkErr = d.Sync()
				_ = d.Close()
			} else {
				parkErr = err
			}
		}
		if parkErr != nil {
			rollback()
			return parkErr
		}
		// Publish — the commit point. Before this rename the canonical
		// stream is intact and the payload is debris; after it the merged
		// history is the truth and the parked files are.
		if err := writeFileAtomic(active, stream, 0o644); err != nil {
			rollback()
			return err
		}
		if err := r.recountMeta(convID); err != nil {
			return err
		}
		// Cleanup is best effort, payload first: once it is gone, leftover
		// parked files still read as a consistent archive (they are not
		// `*.jsonl`) and are swept by the next sealed edit.
		_ = os.Remove(payload)
		for _, p := range parked {
			_ = os.Remove(p + rewriteParkSuffix)
		}
		return nil
	}
	return apperror.NewNotFoundError("message "+msgID, fmt.Errorf("no message %s in %s", msgID, convID))
}

// rewriteCut finds msgID among message lines and returns the stream cut
// right after it, with the target's content patched in place. Non-message
// lines keep their position — a tool_output patch preceding the cut still
// applies. found=false means the id isn't in this stream at all. A patch
// without a retained call is ignored when the stream is read.
func rewriteCut(lines [][]byte, msgID, content string) (cut [][]byte, found bool, err error) {
	for i, l := range lines {
		var lr lineRecord
		if err := json.Unmarshal(l, &lr); err != nil {
			return nil, false, fmt.Errorf("corrupt line: %w", err)
		}
		if lr.Type != lineMessage || lr.Data.ID != msgID {
			continue
		}
		if !entity.EditableMessage(enum.Role(lr.Data.Role), enum.LLMUserType(lr.Data.UserType)) {
			return nil, false, apperror.New(400, "only user messages are editable",
				fmt.Errorf("message %s has role %s userType %s", msgID, lr.Data.Role, lr.Data.UserType))
		}
		lr.Data.Content = content
		patched, err := json.Marshal(lr)
		if err != nil {
			return nil, false, err
		}
		cut = append(cut, lines[:i]...)
		cut = append(cut, patched)
		for _, l := range lines[i+1:] {
			var tail lineRecord
			if json.Unmarshal(l, &tail) == nil && tail.Type != lineMessage {
				cut = append(cut, l)
			}
		}
		return cut, true, nil
	}
	return nil, false, nil
}

func joinLines(lines [][]byte) []byte {
	var buf bytes.Buffer
	for _, l := range lines {
		buf.Write(l)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

// recountMeta resyncs the derived counters with the stream after surgery
// — sealed count is re-globbed so it doubles as the next segment index.
func (r *ConversationRepository) recountMeta(convID string) error {
	meta, err := r.readMetaUnvalidated(convID)
	if err != nil {
		return err
	}
	segs, err := sealedSegments(r.convDir(convID))
	if err != nil {
		return err
	}
	meta.SealedSegments = len(segs)
	active, err := r.loadMessages(convID, 0)
	if err != nil {
		return err
	}
	all, err := r.loadMessages(convID, meta.SealedSegments)
	if err != nil {
		return err
	}
	meta.ActiveCount = len(active)
	meta.MessageCount = len(all)
	return r.writeMeta(convID, meta)
}
