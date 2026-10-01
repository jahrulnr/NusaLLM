package repoconversation

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
)

func TestRepairOrphanDuplicate(t *testing.T) {
	r := newRepo(t).(*ConversationRepository)
	if err := r.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, r, "c1", "one")
	if err := r.UpdateToolCallOutput(t.Context(), "c1", patchCall(t, "ghost", "ignored")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, r, "c1", "two")
	dir := r.convDir("c1")
	active, err := os.ReadFile(filepath.Join(dir, activeFile))
	if err != nil {
		t.Fatal(err)
	}
	var messages [][]byte
	for _, line := range bytes.Split(bytes.TrimSpace(active), []byte("\n")) {
		if bytes.Contains(line, []byte(`"type":"message"`)) {
			messages = append(messages, line)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "0.jsonl"), joinLines(messages), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.RepairOrphan(t.Context(), "c1", drepo.RepairCommit); err == nil {
		t.Fatal("must not commit a duplicate prefix")
	}
	if err := r.RepairOrphan(t.Context(), "c1", drepo.RepairDiscardDuplicate); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "0.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("duplicate segment remains: %v", err)
	}
	if got := len(msgIDs(t, r, "c1")); got != 2 {
		t.Fatalf("messages = %d", got)
	}
}

func TestRepairOrphanRejectsPartialMessageOverlap(t *testing.T) {
	r := newRepo(t).(*ConversationRepository)
	if err := r.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, r, "c1", "one")
	dir := r.convDir("c1")
	active, err := os.ReadFile(filepath.Join(dir, activeFile))
	if err != nil {
		t.Fatal(err)
	}
	ghost, _ := entity.NewMessage(enum.RoleUser, "ghost")
	ghostLine, err := json.Marshal(lineRecord{Type: lineMessage, Data: encodeMessage(ghost)})
	if err != nil {
		t.Fatal(err)
	}
	orphan := append(append([]byte(nil), active...), append(ghostLine, '\n')...)
	segment := filepath.Join(dir, "0.jsonl")
	if err := os.WriteFile(segment, orphan, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.RepairOrphan(t.Context(), "c1", drepo.RepairCommit); err == nil || !strings.Contains(err.Error(), "partial overlap requires manual reconciliation — stop the app, back up the room, then inspect its files or restore from backup") {
		t.Fatalf("partially overlapping orphan needs manual recovery guidance: %v", err)
	}
	if err := r.RepairOrphan(t.Context(), "c1", drepo.RepairDiscardDuplicate); err == nil || !strings.Contains(err.Error(), "partial overlap requires manual reconciliation — stop the app, back up the room, then inspect its files or restore from backup") {
		t.Fatalf("partially overlapping orphan must not be discarded: %v", err)
	}
	if got, err := os.ReadFile(segment); err != nil || !bytes.Equal(got, orphan) {
		t.Fatalf("failed repair changed orphan: %v", err)
	}
}

func TestRepairOrphanRejectsOverlapWithCommittedSegment(t *testing.T) {
	r := newRepo(t).(*ConversationRepository)
	if err := r.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, r, "c1", "one", "two")
	summary, _ := entity.NewMessage(enum.RoleUser, "summary")
	if err := r.Compact(t.Context(), "c1", summary, 1); err != nil {
		t.Fatal(err)
	}
	dir := r.convDir("c1")
	oldSegment, err := os.ReadFile(filepath.Join(dir, "0.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "1.jsonl"), oldSegment, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.RepairOrphan(t.Context(), "c1", drepo.RepairCommit); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("old-era duplicate must be rejected: %v", err)
	}
}

func TestRepairOrphanRejectsMalformedSegments(t *testing.T) {
	tests := []struct {
		name, want string
		mutate     func(t *testing.T, dir string, active []byte)
	}{
		{"two extras", "exactly one extra", func(t *testing.T, dir string, active []byte) {
			for _, name := range []string{"0.jsonl", "1.jsonl"} {
				if err := os.WriteFile(filepath.Join(dir, name), active, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{"gap", "segment 0 missing", func(t *testing.T, dir string, active []byte) {
			if err := os.WriteFile(filepath.Join(dir, "1.jsonl"), active, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"empty", "empty or incomplete", func(t *testing.T, dir string, _ []byte) {
			if err := os.WriteFile(filepath.Join(dir, "0.jsonl"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"no newline", "empty or incomplete", func(t *testing.T, dir string, active []byte) {
			if err := os.WriteFile(filepath.Join(dir, "0.jsonl"), bytes.TrimSuffix(active, []byte("\n")), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"non-json", "no valid message", func(t *testing.T, dir string, _ []byte) {
			if err := os.WriteFile(filepath.Join(dir, "0.jsonl"), []byte("corrupt\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"non-message", "non-message line", func(t *testing.T, dir string, _ []byte) {
			if err := os.WriteFile(filepath.Join(dir, "0.jsonl"), []byte("{\"type\":\"tool_output\"}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"noncanonical bytes", "incomplete lines", func(t *testing.T, dir string, active []byte) {
			if err := os.WriteFile(filepath.Join(dir, "0.jsonl"), append([]byte(" "), active...), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRepo(t).(*ConversationRepository)
			if err := r.Save(t.Context(), newConv(t, "c1")); err != nil {
				t.Fatal(err)
			}
			appendMsgs(t, r, "c1", "one")
			dir := r.convDir("c1")
			active, err := os.ReadFile(filepath.Join(dir, activeFile))
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(t, dir, active)
			if err := r.RepairOrphan(t.Context(), "c1", drepo.RepairCommit); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("repair error = %v; want %q", err, tc.want)
			}
		})
	}
}

func TestArchiveMismatchGuidance(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "c1")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateSealedArchive(dir, -1); err == nil || !strings.Contains(err.Error(), "conversation c1") || !strings.Contains(err.Error(), "metadata") || strings.Contains(err.Error(), "repair <id>") {
		t.Fatalf("negative metadata count guidance = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "0.jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateSealedArchive(dir, 0); err == nil || !strings.Contains(err.Error(), "conversation c1") || !strings.Contains(err.Error(), "offline repair") {
		t.Fatalf("single orphan guidance = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "1.jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateSealedArchive(dir, 0); err == nil || !strings.Contains(err.Error(), "restore") || strings.Contains(err.Error(), "offline repair") {
		t.Fatalf("multiple orphan guidance = %v", err)
	}
}

func TestArchiveGapDoesNotSuggestAutomaticRepair(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "c1")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "1.jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateSealedArchive(dir, 0); err == nil || !strings.Contains(err.Error(), "restore") || strings.Contains(err.Error(), "offline repair") {
		t.Fatalf("gap guidance = %v", err)
	}
}

func TestRepairOrphanCommittedActiveRewrite(t *testing.T) {
	r := newRepo(t).(*ConversationRepository)
	if err := r.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, r, "c1", "one", "two", "three")
	dir := r.convDir("c1")
	block := filepath.Join(dir, "meta.json.tmp")
	if err := os.Mkdir(block, 0o700); err != nil {
		t.Fatal(err)
	}
	summary, _ := entity.NewMessage(enum.RoleUser, "summary")
	if err := r.Compact(t.Context(), "c1", summary, 2); err == nil {
		t.Fatal("expected metadata failure")
	}
	if err := os.Remove(block); err != nil {
		t.Fatal(err)
	}
	segment := filepath.Join(dir, "0.jsonl")
	before, err := os.ReadFile(segment)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RepairOrphan(t.Context(), "c1", drepo.RepairDiscardDuplicate); err == nil {
		t.Fatal("must not discard unique history")
	}
	if err := r.RepairOrphan(t.Context(), "c1", drepo.RepairCommit); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(segment)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("sealed history changed: %v", err)
	}
	info, err := r.Info(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if info.SealedSegments != 1 || info.MessageCount != 4 || info.ActiveCount != 2 {
		t.Fatalf("recovered counters: %+v", info)
	}
	if got := len(msgIDs(t, r, "c1")); got != 4 {
		t.Fatalf("messages = %d", got)
	}
}
