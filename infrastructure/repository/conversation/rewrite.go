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
//     segment is deleted, and the count resets so the next Compact starts
//     at 0.jsonl again.
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

	// Sealed segments — repromote the era holding the target.
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
		var buf bytes.Buffer
		for _, prev := range segs[:i] { // earlier eras come back verbatim
			lines, err := readLines(prev)
			if err != nil {
				return err
			}
			for _, l := range lines {
				buf.Write(l)
				buf.WriteByte('\n')
			}
		}
		for _, l := range cut {
			buf.Write(l)
			buf.WriteByte('\n')
		}
		// Patches live in the tail and resolve globally by call id — keep
		// the ones that still find a home; dead ones are harmless.
		for _, l := range activeLines {
			var lr lineRecord
			if json.Unmarshal(l, &lr) == nil && lr.Type != lineMessage {
				buf.Write(l)
				buf.WriteByte('\n')
			}
		}
		if err := writeFileAtomic(active, buf.Bytes(), 0o644); err != nil {
			return err
		}
		for _, f := range segs {
			if err := os.Remove(f); err != nil {
				return err
			}
		}
		return r.recountMeta(convID)
	}
	return apperror.NewNotFoundError("message "+msgID, fmt.Errorf("no message %s in %s", msgID, convID))
}

// rewriteCut finds msgID among message lines and returns the stream cut
// right after it, with the target's content patched in place. Non-message
// lines keep their position — a tool_output patch preceding the cut still
// applies. found=false means the id isn't in this stream at all.
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
		// Patch records resolve globally by call id — one sitting past
		// the cut may still answer a call in the retained prefix (e.g.
		// after Compact relocated every patch to the tail). Dropping it
		// would resurrect the call as unanswered on reload; dead ones
		// are harmless, so every patch survives the truncation.
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
	meta, err := r.readMeta(convID)
	if err != nil {
		return err
	}
	active, err := r.loadMessages(convID, false)
	if err != nil {
		return err
	}
	all, err := r.loadMessages(convID, true)
	if err != nil {
		return err
	}
	segs, err := sealedSegments(r.convDir(convID))
	if err != nil {
		return err
	}
	meta.ActiveCount = len(active)
	meta.MessageCount = len(all)
	meta.SealedSegments = len(segs)
	return r.writeMeta(convID, meta)
}
