// Package repoconversation implements the Conversation repository port on
// the local filesystem: one directory per conversation holding meta.json,
// numbered sealed archive segments (0.jsonl, 1.jsonl, …) written by Compact,
// and active.jsonl — the live tail. Messages and tool-output patches share
// the same append-only line stream so writes never rewrite history. The
// exceptions are the surgery operations — Compact seals a prefix,
// RewriteFrom truncates/promotes for an edit — which rewrite segment
// files atomically rather than appending.
package repoconversation

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	apperror "github.com/jahrulnr/llm-base/infrastructure/error"

	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	drepo "github.com/jahrulnr/llm-base/infrastructure/domain/repository"
	"github.com/jahrulnr/llm-base/infrastructure/domain/valueobject"
)

const (
	metaFile       = "meta.json"
	activeFile     = "active.jsonl"
	lineMessage    = "message"
	lineToolOutput = "tool_output"
)

// ConversationRepository is a JSONL-backed Conversation store. A single
// process owns the directory; the mutex serializes writes.
type ConversationRepository struct {
	path string
	mu   sync.Mutex
}

func NewConversationRepository(path string) drepo.Conversation {
	return &ConversationRepository{path: path}
}

func (r *ConversationRepository) convDir(id string) string {
	return filepath.Join(r.path, id)
}

// validID is the boundary guard: the id becomes a path segment, so the
// domain's own rule (valueobject.ValidID) is re-asserted against this root.
func validID(id string) error {
	if err := valueobject.ValidID(id); err != nil {
		return apperror.New(400, "invalid id", fmt.Errorf("id %q: %w", id, err))
	}
	return nil
}

// writeFileAtomic writes via tmp+rename — a crash mid-write leaves the
// old file or nothing, never a torn target. fsync before and after the
// rename matters as much as the rename: without it a crash can resurrect
// the name with no contents (a sealed segment that reads empty is silent
// history loss, not an error). The dir sync is best-effort — the bytes
// are already durable once the file syncs.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// --- meta -----------------------------------------------------------------

func (r *ConversationRepository) readMeta(id string) (metaRecord, error) {
	raw, err := os.ReadFile(filepath.Join(r.convDir(id), metaFile))
	if errors.Is(err, os.ErrNotExist) {
		return metaRecord{}, apperror.NewNotFoundError("conversation "+id, err)
	}
	if err != nil {
		return metaRecord{}, err
	}
	var m metaRecord
	if err := json.Unmarshal(raw, &m); err != nil {
		return metaRecord{}, fmt.Errorf("meta corrupt: %w", err)
	}
	return m, nil
}

func (r *ConversationRepository) writeMeta(id string, m metaRecord) error {
	m.UpdatedAt = time.Now()
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(r.convDir(id), metaFile), raw, 0o644)
}

// --- line stream ------------------------------------------------------------

// readLines slurps every line of a jsonl file. A truncated final line is
// tolerated — it is the signature of a crash mid-append; earlier corruption
// is reported.
func readLines(path string) ([][]byte, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var lines [][]byte
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) > 0 {
			lines = append(lines, append([]byte(nil), line...))
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(lines) > 0 {
		last := lines[len(lines)-1]
		var probe struct{ Type string }
		if json.Unmarshal(last, &probe) != nil || probe.Type == "" {
			lines = lines[:len(lines)-1] // truncated tail — drop
		}
	}
	return lines, nil
}

func appendLine(path string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(raw, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

// messagesFromLines decodes message lines and applies tool_output patches
// globally by callID — a patch may target a call sealed in an earlier
// segment.
func messagesFromLines(lines [][]byte) ([]entity.Message, error) {
	var msgs []entity.Message
	type patch struct {
		callID, output string
		atts           []valueobject.Attachment
	}
	var patches []patch
	for _, l := range lines {
		var lr lineRecord
		if err := json.Unmarshal(l, &lr); err != nil {
			return nil, fmt.Errorf("corrupt line: %w", err)
		}
		switch lr.Type {
		case lineMessage:
			m, err := decodeMessage(lr.Data)
			if err != nil {
				return nil, err
			}
			msgs = append(msgs, m)
		case lineToolOutput:
			var atts []valueobject.Attachment
			for _, a := range lr.Attachments {
				att, err := valueobject.NewAttachment(a.URI, a.MediaType)
				if err != nil {
					return nil, fmt.Errorf("tool_output attachment: %w", err)
				}
				att.SetName(a.Name)
				att.SetSHA256(a.SHA256)
				atts = append(atts, att)
			}
			patches = append(patches, patch{lr.CallID, lr.Output, atts})
		}
	}
	for _, p := range patches {
		for i := range msgs {
			if msgs[i].SetStoredToolOutput(p.callID, p.output) == nil {
				if p.atts != nil {
					// Cannot fail — SetStoredToolOutput just matched this
					// callID, which is the only lookup the setter repeats.
					_ = msgs[i].SetToolOutputAttachments(p.callID, p.atts)
				}
				break
			}
		}
	}
	return msgs, nil
}

// sealedSegments lists the conversation's numbered archive files in
// order — the eras Compact wrote, oldest first.
func sealedSegments(dir string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	var segs []string
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".jsonl")
		if _, err := strconv.Atoi(name); err == nil { // numbered segment only
			segs = append(segs, f)
		}
	}
	sort.Slice(segs, func(i, j int) bool {
		ai, _ := strconv.Atoi(strings.TrimSuffix(filepath.Base(segs[i]), ".jsonl"))
		aj, _ := strconv.Atoi(strings.TrimSuffix(filepath.Base(segs[j]), ".jsonl"))
		return ai < aj
	})
	return segs, nil
}

// loadMessages reads the live tail, optionally prefixed by the sealed
// segments in numeric order — the global logical history. Only segments
// meta counts as sealed qualify: Compact writes the file before bumping
// the count, so a crash mid-write leaves an orphan glob would count —
// replaying a prefix the intact active copy still holds.
func (r *ConversationRepository) loadMessages(convID string, includeSealed bool) ([]entity.Message, error) {
	dir := r.convDir(convID)
	var files []string
	if includeSealed {
		meta, err := r.readMeta(convID)
		if err != nil {
			return nil, err
		}
		segs, err := sealedSegments(dir)
		if err != nil {
			return nil, err
		}
		for _, f := range segs {
			n, _ := strconv.Atoi(strings.TrimSuffix(filepath.Base(f), ".jsonl"))
			if n < meta.SealedSegments { // committed segments only
				files = append(files, f)
			}
		}
	}
	files = append(files, filepath.Join(dir, activeFile))

	var lines [][]byte
	for _, f := range files {
		l, err := readLines(f)
		if err != nil {
			return nil, err
		}
		lines = append(lines, l...)
	}
	return messagesFromLines(lines)
}
