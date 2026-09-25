// Package repoconversation implements the Conversation repository port on
// the local filesystem: one directory per conversation holding meta.json,
// numbered sealed archive segments (0.jsonl, 1.jsonl, …) written by Compact,
// and active.jsonl — the live tail. Messages and tool-output patches share
// the same append-only line stream so writes never rewrite history.
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
	tmp := filepath.Join(r.convDir(id), metaFile+".tmp")
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(r.convDir(id), metaFile))
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
	type patch struct{ callID, output string }
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
			patches = append(patches, patch{lr.CallID, lr.Output})
		}
	}
	for _, p := range patches {
		for i := range msgs {
			if msgs[i].SetStoredToolOutput(p.callID, p.output) == nil {
				break
			}
		}
	}
	return msgs, nil
}

// loadMessages reads the live tail, optionally prefixed by every sealed
// segment in numeric order — the global logical history.
func (r *ConversationRepository) loadMessages(convID string, includeSealed bool) ([]entity.Message, error) {
	dir := r.convDir(convID)
	var files []string
	if includeSealed {
		segs, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
		if err != nil {
			return nil, err
		}
		for _, f := range segs {
			name := strings.TrimSuffix(filepath.Base(f), ".jsonl")
			if _, err := strconv.Atoi(name); err == nil { // numbered segment only
				files = append(files, f)
			}
		}
		sort.Slice(files, func(i, j int) bool {
			ai, _ := strconv.Atoi(strings.TrimSuffix(filepath.Base(files[i]), ".jsonl"))
			aj, _ := strconv.Atoi(strings.TrimSuffix(filepath.Base(files[j]), ".jsonl"))
			return ai < aj
		})
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
