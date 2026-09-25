package repoconversation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
)

// Compact seals the first dropBefore message lines verbatim into the next
// numbered segment, then rewrites active.jsonl as [summary] + surviving
// messages + every patch line — patches stay global so late outputs still
// reach sealed calls.
func (r *ConversationRepository) Compact(ctx context.Context, convID string, summary entity.Message, dropBefore int) error {
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
		if json.Unmarshal(l, &lr) != nil {
			continue
		}
		if lr.Type == lineMessage {
			msgLines = append(msgLines, l)
		} else {
			patchLines = append(patchLines, l)
		}
	}
	if dropBefore < 0 {
		dropBefore = 0
	}
	if dropBefore > len(msgLines) {
		dropBefore = len(msgLines)
	}

	// Seal the dropped prefix verbatim into the next numbered segment.
	sealed := filepath.Join(r.convDir(convID), fmt.Sprintf("%d.jsonl", meta.SealedSegments))
	var buf bytes.Buffer
	for _, l := range msgLines[:dropBefore] {
		buf.Write(l)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(sealed, buf.Bytes(), 0o644); err != nil {
		return err
	}

	// Rewrite active: summary + surviving messages + every patch line.
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
	if err := os.WriteFile(active, buf.Bytes(), 0o644); err != nil {
		return err
	}

	meta.SealedSegments++
	meta.MessageCount++
	meta.ActiveCount = meta.ActiveCount - dropBefore + 1
	return r.writeMeta(convID, meta)
}
